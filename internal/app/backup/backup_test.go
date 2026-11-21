package backup

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/internal/backupstatus"
)

// fakeTools records commands. fail maps "tool subcommand repo" prefixes to errors; restic repositories
// count as initialized after init.
type fakeTools struct {
	mu    sync.Mutex
	calls []string
	// pgEnv is the environment the last pg_dump or pg_restore ran with.
	pgEnv   []string
	fail    map[string]error
	inited  map[string]bool
	restore func(target string)
	// backup, when set, runs as restic backs up the stage, before the stage is cleaned up.
	backup func()
}

func (f *fakeTools) run(_ context.Context, env []string, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	line := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, line)
	if strings.HasPrefix(name, "pg_") {
		f.pgEnv = env
	}
	for prefix, err := range f.fail {
		if strings.HasPrefix(line, prefix) {
			return nil, err
		}
	}
	if name == "restic" {
		if !strings.Contains(strings.Join(env, " "), "RESTIC_PASSWORD=pw") {
			return nil, errors.New("restic without password")
		}
		repo, sub := args[1], args[2]
		switch sub {
		case "cat":
			if !f.inited[repo] {
				return nil, errors.New("repository does not exist")
			}
		case "init":
			f.inited[repo] = true
		case "backup":
			if f.backup != nil {
				f.backup()
			}
			return []byte(`{"message_type":"status"}` + "\n" + `{"message_type":"summary","snapshot_id":"snap-` + filepath.Base(repo) + `"}`), nil
		case "restore":
			f.restore(args[len(args)-1])
		}
	}
	return nil, nil
}

func (f *fakeTools) ran(prefix string) bool {
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

var vars = map[string]string{
	"WRENCH_BACKUP_SCHEDULE":        "0 2 * * *",
	"WRENCH_BACKUP_RETENTION":       "30d",
	"WRENCH_BACKUP_S3_ENDPOINT":     "https://s3.example.com/",
	"WRENCH_BACKUP_S3_BUCKET":       "wrench-backups",
	"WRENCH_BACKUP_S3_PATH":         "/prod/",
	"WRENCH_BACKUP_S3_REGION":       "eu-1",
	"WRENCH_BACKUP_S3_ACCESS_KEY":   "ak",
	"WRENCH_BACKUP_S3_SECRET_KEY":   "sk-secret",
	"WRENCH_BACKUP_RESTIC_PASSWORD": "pw",
	"WRENCH_BACKUP_POSTGRES_DSN":    "postgres://wrench_backup:db-secret@postgres:5432/wrench?sslmode=disable",
}

func lookup(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

// testPolicy is what the configuration directory holds besides a nested directory, which is not backed up.
var testPolicy = []string{"config.yaml", "notes.md"}

var t0 = time.Date(2026, 11, 10, 2, 0, 0, 0, time.UTC)

func newBackuper(t *testing.T, tools *fakeTools) *Backuper {
	t.Helper()
	dir := t.TempDir()
	v := map[string]string{"WRENCH_BACKUP_CONFIG_DIR": filepath.Join(dir, "etc"), "WRENCH_BACKUP_STAGE_DIR": filepath.Join(dir, "stage"),
		"WRENCH_BACKUP_LOCAL_REPO": filepath.Join(dir, "restic"), "WRENCH_BACKUP_STATUS_FILE": filepath.Join(dir, "status.json")}
	for k, val := range vars {
		v[k] = val
	}
	cfg, err := loadConfig(lookup(v))
	if err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(cfg.configDir, "nested"), 0o755)
	_ = os.WriteFile(filepath.Join(cfg.configDir, "nested", "skipped.yaml"), []byte("no"), 0o644)
	for _, name := range testPolicy {
		_ = os.WriteFile(filepath.Join(cfg.configDir, name), []byte("# "+name+"\n"), 0o644)
	}
	if tools.inited == nil {
		tools.inited = map[string]bool{}
	}
	return &Backuper{cfg: cfg, cmd: tools, now: func() time.Time { return t0 }, client: http.DefaultClient,
		log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestConfigBuildsRepositories(t *testing.T) {
	cfg, err := loadConfig(lookup(vars))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.repos["s3"] != "s3:https://s3.example.com/wrench-backups/prod" || cfg.repos["local"] != "/var/lib/wrench-backup/restic" {
		t.Fatalf("repos = %v", cfg.repos)
	}
	bad := map[string]string{"WRENCH_BACKUP_SCHEDULE": "daily", "WRENCH_BACKUP_RETENTION": "forever", "WRENCH_BACKUP_S3_SECRET_KEY": "sk-secret"}
	_, err = loadConfig(lookup(bad))
	for _, want := range []string{"WRENCH_BACKUP_SCHEDULE", "WRENCH_BACKUP_RETENTION", "WRENCH_BACKUP_RESTIC_PASSWORD", "WRENCH_BACKUP_S3_BUCKET",
		"WRENCH_BACKUP_POSTGRES_DSN"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("missing %s in %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "sk-secret") {
		t.Fatal("secret value in error")
	}
}

func TestPostgresDSN(t *testing.T) {
	tests := []struct {
		dsn  string
		want []string
		err  bool
	}{
		{"postgres://u:p%40ss@db:6543/wrench?sslmode=require",
			[]string{"PGHOST=db", "PGPORT=6543", "PGUSER=u", "PGPASSWORD=p@ss", "PGDATABASE=wrench", "PGSSLMODE=require"}, false},
		{"postgresql://u:p@db/wrench", []string{"PGHOST=db", "PGPORT=5432", "PGUSER=u", "PGPASSWORD=p", "PGDATABASE=wrench"}, false},
		{"mysql://u:p@db/wrench", nil, true},
		{"postgres://u:p@db", nil, true},
		{"not a url %zz", nil, true},
	}
	for _, tt := range tests {
		v := map[string]string{}
		for k, val := range vars {
			v[k] = val
		}
		v["WRENCH_BACKUP_POSTGRES_DSN"] = tt.dsn
		cfg, err := loadConfig(lookup(v))
		if tt.err {
			if err == nil || !strings.Contains(err.Error(), "WRENCH_BACKUP_POSTGRES_DSN") || strings.Contains(err.Error(), "p@") {
				t.Errorf("%s: err = %v", tt.dsn, err)
			}
			continue
		}
		if err != nil || strings.Join(cfg.pgEnv, " ") != strings.Join(tt.want, " ") {
			t.Errorf("%s: pgEnv = %v, %v", tt.dsn, cfg.pgEnv, err)
		}
	}
}

func TestPgToolsGetTheDSNThroughTheEnvironment(t *testing.T) {
	tools := &fakeTools{}
	b := newBackuper(t, tools)
	if err := b.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range tools.calls {
		if strings.Contains(c, "postgres://") || strings.Contains(c, "db-secret") {
			t.Fatalf("DSN on a command line: %s", c)
		}
	}
	if !slices.Contains(tools.pgEnv, "PGPASSWORD=db-secret") || !slices.Contains(tools.pgEnv, "PGDATABASE=wrench") {
		t.Fatalf("pg env = %v", tools.pgEnv)
	}
}

func TestBackupToBothRepositories(t *testing.T) {
	tools := &fakeTools{}
	b := newBackuper(t, tools)
	s := b.run(context.Background(), backupstatus.Status{})
	if s.LastError != "" || s.LastSuccessAt == nil || !s.LastSuccessAt.Equal(t0) {
		t.Fatalf("status = %+v", s)
	}
	if s.Repositories["local"].Snapshot != "snap-restic" || s.Repositories["s3"].Snapshot != "snap-prod" {
		t.Fatalf("repositories = %+v", s.Repositories)
	}
	if !s.NextRunAt.Equal(t0.Add(24*time.Hour)) || !s.StaleAfter.Equal(t0.Add(26*time.Hour)) {
		t.Fatalf("schedule = %s / %s", s.NextRunAt, s.StaleAfter)
	}
	for _, prefix := range []string{"pg_dump --format=custom", "pg_restore --list", "restic -r s3:https://s3.example.com/wrench-backups/prod init",
		"restic -r s3:https://s3.example.com/wrench-backups/prod forget --host wrench --keep-within 30d --prune", "restic -r " + b.cfg.repos["local"] + " check"} {
		if !tools.ran(prefix) {
			t.Errorf("did not run %q; calls: %v", prefix, tools.calls)
		}
	}
	if _, err := os.Stat(b.cfg.stageDir); !os.IsNotExist(err) {
		t.Fatal("stage directory left behind")
	}
}

func TestFailedRepositoryKeepsPreviousSuccessAndSnapshots(t *testing.T) {
	tools := &fakeTools{fail: map[string]error{"restic -r s3:https://s3.example.com/wrench-backups/prod backup": errors.New("connection refused")}}
	b := newBackuper(t, tools)
	earlier := t0.Add(-24 * time.Hour)
	s := b.run(context.Background(), backupstatus.Status{LastSuccessAt: &earlier})
	if !strings.Contains(s.LastError, "s3: connection refused") || !s.LastSuccessAt.Equal(earlier) || !s.Repositories["local"].OK || s.Repositories["s3"].OK {
		t.Fatalf("status = %+v", s)
	}
	// No retention without a new snapshot: the previous good backup survives.
	if tools.ran("restic -r s3:https://s3.example.com/wrench-backups/prod forget") {
		t.Fatal("pruned a repository whose backup failed")
	}
}

func TestUnreadableDumpIsNeverUploaded(t *testing.T) {
	tools := &fakeTools{fail: map[string]error{"pg_restore --list": errors.New("corrupt")}}
	b := newBackuper(t, tools)
	s := b.run(context.Background(), backupstatus.Status{})
	if !strings.HasPrefix(s.LastError, "dump: dump verification") || tools.ran("restic") {
		t.Fatalf("status = %+v, calls %v", s, tools.calls)
	}
}

func TestHeartbeatReportsOutcome(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { paths = append(paths, r.URL.Path) }))
	defer srv.Close()
	tools := &fakeTools{}
	b := newBackuper(t, tools)
	b.cfg.heartbeatURL = srv.URL + "/ping/abc"
	b.run(context.Background(), backupstatus.Status{})
	tools.fail = map[string]error{"pg_dump": errors.New("no database")}
	b.run(context.Background(), backupstatus.Status{})
	if strings.Join(paths, " ") != "/ping/abc /ping/abc/fail" {
		t.Fatalf("heartbeats = %v", paths)
	}
}

func TestRunOnceWritesStatus(t *testing.T) {
	b := newBackuper(t, &fakeTools{})
	if err := b.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p, err := backupstatus.Problem(b.cfg.statusFile, t0.Add(time.Hour)); err != nil || p != "" {
		t.Fatalf("problem = %q, %v", p, err)
	}
}

func TestRestoreFromS3(t *testing.T) {
	tools := &fakeTools{}
	b := newBackuper(t, tools)
	tools.restore = func(target string) {
		staged := filepath.Join(target, b.cfg.stageDir)
		_ = os.MkdirAll(filepath.Join(staged, "config"), 0o755)
		_ = os.WriteFile(filepath.Join(staged, "wrench.dump"), []byte("dump"), 0o644)
		for _, name := range testPolicy {
			_ = os.WriteFile(filepath.Join(staged, "config", name), []byte("restored "+name), 0o644)
		}
	}
	t.Setenv("PGDATABASE", "wrench")
	out := filepath.Join(t.TempDir(), "config")
	if err := b.Restore(context.Background(), "s3", "latest", out); err != nil {
		t.Fatal(err)
	}
	if !tools.ran("restic -r s3:https://s3.example.com/wrench-backups/prod restore latest --host wrench") ||
		!tools.ran("pg_restore --exit-on-error --no-owner --single-transaction --dbname=wrench") {
		t.Fatalf("calls = %v", tools.calls)
	}
	for _, name := range testPolicy {
		if data, _ := os.ReadFile(filepath.Join(out, name)); string(data) != "restored "+name {
			t.Fatalf("%s = %q", name, data)
		}
	}
	if err := b.Restore(context.Background(), "tape", "latest", out); err == nil {
		t.Fatal("unknown repository accepted")
	}
}

func TestPolicyFilesAreConfigDirFiles(t *testing.T) {
	tools := &fakeTools{}
	b := newBackuper(t, tools)
	var staged []string
	tools.backup = func() {
		entries, _ := os.ReadDir(filepath.Join(b.cfg.stageDir, "config"))
		staged = staged[:0]
		for _, e := range entries {
			staged = append(staged, e.Name())
		}
	}
	if err := b.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(staged, ",") != strings.Join(testPolicy, ",") {
		t.Fatalf("staged %v, want %v", staged, testPolicy)
	}
}

// A wrongly mounted configuration directory must fail the backup instead of storing no policy.
func TestBackupFailsWithoutPolicy(t *testing.T) {
	tools := &fakeTools{}
	b := newBackuper(t, tools)
	if err := os.Remove(filepath.Join(b.cfg.configDir, "config.yaml")); err != nil {
		t.Fatal(err)
	}
	err := b.Once(context.Background())
	if err == nil || !strings.Contains(err.Error(), "config.yaml") {
		t.Fatalf("err = %v, want a missing config.yaml failure", err)
	}
	if tools.ran("restic") {
		t.Fatalf("restic ran without a policy: %v", tools.calls)
	}
}
