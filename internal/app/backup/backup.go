package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ordo-systems/wrench/internal/backupstatus"
)

// config is the backup container's environment.
type config struct {
	// pgEnv carries the database connection from WRENCH_BACKUP_POSTGRES_DSN to pg_dump and pg_restore as libpq
	// variables, so the password never appears on a command line.
	pgEnv        []string
	schedule     *schedule
	retention    string
	repos        map[string]string // name -> restic repository
	statusFile   string
	configDir    string
	stageDir     string
	heartbeatURL string
	// resticEnv carries the repository password and S3 credentials to restic only.
	resticEnv []string
}

var retentionPattern = regexp.MustCompile(`^[0-9]+[ymdh]$`)

// copyPolicy copies the regular files of src (config.yaml and the files it references, such as the managed
// instructions) into dst. Subdirectories are skipped; secrets are not in this directory and are recovered separately.
// It fails without config.yaml, so a wrongly mounted directory never yields a backup without policy.
func copyPolicy(src, dst string) error {
	if _, err := os.Stat(filepath.Join(src, "config.yaml")); err != nil {
		return fmt.Errorf("policy: %w", err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// loadConfig reads WRENCH_BACKUP_* variables and reports every problem at once, naming variables only.
func loadConfig(lookup func(string) (string, bool)) (config, error) {
	var errs []error
	get := func(key string) string {
		v, _ := lookup(key)
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is required", key))
		}
		return v
	}
	optional := func(key, fallback string) string {
		if v, _ := lookup(key); v != "" {
			return v
		}
		return fallback
	}
	c := config{
		retention:    get("WRENCH_BACKUP_RETENTION"),
		statusFile:   optional("WRENCH_BACKUP_STATUS_FILE", "/var/lib/wrench-backup/status/backup.json"),
		configDir:    optional("WRENCH_BACKUP_CONFIG_DIR", "/etc/wrench"),
		stageDir:     optional("WRENCH_BACKUP_STAGE_DIR", "/var/lib/wrench-backup/stage"),
		heartbeatURL: optional("WRENCH_BACKUP_HEARTBEAT_URL", ""),
		repos:        map[string]string{"local": optional("WRENCH_BACKUP_LOCAL_REPO", "/var/lib/wrench-backup/restic")},
	}
	if dsn := get("WRENCH_BACKUP_POSTGRES_DSN"); dsn != "" {
		env, err := pgEnv(dsn)
		if err != nil {
			errs = append(errs, fmt.Errorf("WRENCH_BACKUP_POSTGRES_DSN: %w", err))
		}
		c.pgEnv = env
	}
	if text := get("WRENCH_BACKUP_SCHEDULE"); text != "" {
		s, err := parseSchedule(text)
		if err != nil {
			errs = append(errs, fmt.Errorf("WRENCH_BACKUP_SCHEDULE: %w", err))
		}
		c.schedule = s
	}
	if c.retention != "" && !retentionPattern.MatchString(c.retention) {
		errs = append(errs, errors.New("WRENCH_BACKUP_RETENTION must look like 30d, 12m or 1y"))
	}
	endpoint, bucket := get("WRENCH_BACKUP_S3_ENDPOINT"), get("WRENCH_BACKUP_S3_BUCKET")
	path := strings.Trim(optional("WRENCH_BACKUP_S3_PATH", ""), "/")
	c.repos["s3"] = strings.TrimSuffix("s3:"+strings.TrimRight(endpoint, "/")+"/"+bucket+"/"+path, "/")
	c.resticEnv = []string{
		"RESTIC_PASSWORD=" + get("WRENCH_BACKUP_RESTIC_PASSWORD"),
		"AWS_ACCESS_KEY_ID=" + get("WRENCH_BACKUP_S3_ACCESS_KEY"),
		"AWS_SECRET_ACCESS_KEY=" + get("WRENCH_BACKUP_S3_SECRET_KEY"),
		"AWS_DEFAULT_REGION=" + get("WRENCH_BACKUP_S3_REGION"),
	}
	return c, errors.Join(errs...)
}

// commander runs external tools; tests replace it.
type commander interface {
	run(ctx context.Context, env []string, name string, args ...string) ([]byte, error)
}

type execCommander struct{}

func (execCommander) run(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // name is one of the fixed backup tools (pg_dump, pg_restore, restic)
	cmd.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, args[0], err, strings.TrimSpace(lastLine(stderr.String())))
	}
	return out, nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

type Backuper struct {
	cfg    config
	cmd    commander
	now    func() time.Time
	client *http.Client
	log    *slog.Logger
}

func (b *Backuper) restic(ctx context.Context, repo string, args ...string) ([]byte, error) {
	return b.cmd.run(ctx, b.cfg.resticEnv, "restic", append([]string{"-r", repo}, args...)...)
}

// stage writes a verified dump and the policy files into an empty stage directory.
func (b *Backuper) stage(ctx context.Context) error {
	if err := os.RemoveAll(b.cfg.stageDir); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(b.cfg.stageDir, "config"), 0o700); err != nil {
		return err
	}
	dump := filepath.Join(b.cfg.stageDir, "wrench.dump")
	if _, err := b.cmd.run(ctx, b.cfg.pgEnv, "pg_dump", "--format=custom", "--file="+dump); err != nil {
		return err
	}
	// A dump that pg_restore cannot list would not restore; never upload it.
	if _, err := b.cmd.run(ctx, b.cfg.pgEnv, "pg_restore", "--list", dump); err != nil {
		return fmt.Errorf("dump verification: %w", err)
	}
	return copyPolicy(b.cfg.configDir, filepath.Join(b.cfg.stageDir, "config"))
}

// toRepo backs up the stage to one repository, then applies retention and checks the repository.
// Retention runs only after a successful backup, so a failed run never removes the last good snapshot.
func (b *Backuper) toRepo(ctx context.Context, repo string) (string, error) {
	if _, err := b.restic(ctx, repo, "cat", "config"); err != nil {
		if _, err := b.restic(ctx, repo, "init"); err != nil {
			return "", err
		}
	}
	out, err := b.restic(ctx, repo, "backup", "--json", "--host", "wrench", "--tag", "wrench", b.cfg.stageDir)
	if err != nil {
		return "", err
	}
	snapshot := snapshotID(out)
	if snapshot == "" {
		return "", errors.New("restic backup reported no snapshot")
	}
	if _, err := b.restic(ctx, repo, "forget", "--host", "wrench", "--keep-within", b.cfg.retention, "--prune"); err != nil {
		return snapshot, err
	}
	_, err = b.restic(ctx, repo, "check")
	return snapshot, err
}

// snapshotID finds the summary message of `restic backup --json`.
func snapshotID(out []byte) string {
	for _, line := range bytes.Split(out, []byte("\n")) {
		var m struct {
			MessageType string `json:"message_type"`
			SnapshotID  string `json:"snapshot_id"`
		}
		if json.Unmarshal(line, &m) == nil && m.MessageType == "summary" {
			return m.SnapshotID
		}
	}
	return ""
}

// run performs one backup and returns the new status, keeping the previous success on failure.
func (b *Backuper) run(ctx context.Context, prev backupstatus.Status) backupstatus.Status {
	started := b.now()
	s := prev
	s.LastRunAt = &started
	s.LastError = ""
	s.Repositories = map[string]backupstatus.Repository{}
	var errs []string
	if err := b.stage(ctx); err != nil {
		errs = append(errs, "dump: "+err.Error())
	} else {
		names := make([]string, 0, len(b.cfg.repos))
		for name := range b.cfg.repos {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			snap, err := b.toRepo(ctx, b.cfg.repos[name])
			r := backupstatus.Repository{OK: err == nil, Snapshot: snap}
			if err != nil {
				r.Error = err.Error()
				errs = append(errs, name+": "+err.Error())
			}
			s.Repositories[name] = r
		}
	}
	_ = os.RemoveAll(b.cfg.stageDir)
	if len(errs) == 0 {
		s.LastSuccessAt = &started
	}
	s.LastError = strings.Join(errs, "; ")
	b.schedule(&s, b.now())
	b.heartbeat(ctx, s.LastError == "")
	return s
}

// schedule sets the next run and the time after which a missing success is an alert.
func (b *Backuper) schedule(s *backupstatus.Status, now time.Time) {
	s.NextRunAt = b.cfg.schedule.next(now)
	s.StaleAfter = s.NextRunAt.Add(backupstatus.Grace)
}

// heartbeat pings an external dead-man switch: the URL on success, URL/fail on failure. Because the
// external service also notices missing pings, it covers a node that stopped entirely.
func (b *Backuper) heartbeat(ctx context.Context, ok bool) {
	if b.cfg.heartbeatURL == "" {
		return
	}
	u := b.cfg.heartbeatURL
	if !ok {
		u = strings.TrimRight(u, "/") + "/fail"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		b.log.Warn("backup heartbeat URL is invalid")
		return
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.log.Warn("backup heartbeat failed")
		return
	}
	resp.Body.Close()
}

// Restore restores a snapshot from one repository: the dump into the database named by
// WRENCH_BACKUP_POSTGRES_DSN, which must be empty, and the policy files into configOut.
func (b *Backuper) Restore(ctx context.Context, from, snapshot, configOut string) error {
	repo, ok := b.cfg.repos[from]
	if !ok {
		return fmt.Errorf("unknown repository %q; use local or s3", from)
	}
	target, err := os.MkdirTemp("", "wrench-restore-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(target) }()
	if _, err := b.restic(ctx, repo, "restore", snapshot, "--host", "wrench", "--target", target); err != nil {
		return err
	}
	staged := filepath.Join(target, b.cfg.stageDir)
	if _, err := b.cmd.run(ctx, b.cfg.pgEnv, "pg_restore", "--exit-on-error", "--no-owner", "--single-transaction",
		"--dbname="+b.cfg.database(), filepath.Join(staged, "wrench.dump")); err != nil {
		return err
	}
	if err := os.MkdirAll(configOut, 0o700); err != nil {
		return err
	}
	return copyPolicy(filepath.Join(staged, "config"), configOut)
}

// pgEnv turns a postgres:// DSN into libpq variables. Errors never include the DSN, which carries a password.
func pgEnv(dsn string) ([]string, error) {
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" || u.User == nil {
		return nil, errors.New("must be postgres://user:password@host[:port]/database")
	}
	db := strings.TrimPrefix(u.Path, "/")
	if db == "" {
		return nil, errors.New("must name a database")
	}
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	password, _ := u.User.Password()
	env := []string{"PGHOST=" + u.Hostname(), "PGPORT=" + port, "PGUSER=" + u.User.Username(), "PGPASSWORD=" + password,
		"PGDATABASE=" + db}
	if mode := u.Query().Get("sslmode"); mode != "" {
		env = append(env, "PGSSLMODE="+mode)
	}
	return env, nil
}

// database is the database name from the DSN.
func (c config) database() string {
	for _, kv := range c.pgEnv {
		if v, ok := strings.CutPrefix(kv, "PGDATABASE="); ok {
			return v
		}
	}
	return ""
}
