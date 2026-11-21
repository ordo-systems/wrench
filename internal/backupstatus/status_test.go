package backupstatus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 10, 2, 0, 0, 0, time.UTC)

func TestProblem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup.json")
	if p, err := Problem(path, t0); err != nil || !strings.Contains(p, "has not reported") {
		t.Fatalf("missing = %q, %v", p, err)
	}
	ok := Status{LastRunAt: &t0, LastSuccessAt: &t0, NextRunAt: t0.Add(24 * time.Hour), StaleAfter: t0.Add(26 * time.Hour),
		Repositories: map[string]Repository{"s3": {OK: true, Snapshot: "abc"}}}
	if err := Write(path, ok); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		status Status
		at     time.Time
		want   string
	}{
		{"healthy", ok, t0.Add(25 * time.Hour), ""},
		{"stale", ok, t0.Add(26*time.Hour + time.Second), "no successful backup since 2026-11-10T02:00:00Z"},
		{"failed", func() Status { s := ok; s.LastError = "s3: connection refused"; return s }(), t0, "the last backup failed: s3: connection refused"},
		{"never succeeded", func() Status { s := ok; s.LastSuccessAt = nil; return s }(), t0.Add(27 * time.Hour), "since never"},
	}
	for _, c := range cases {
		if err := Write(path, c.status); err != nil {
			t.Fatal(err)
		}
		p, err := Problem(path, c.at)
		if err != nil || (c.want == "" && p != "") || !strings.Contains(p, c.want) {
			t.Errorf("%s: %q, %v", c.name, p, err)
		}
	}
	if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Problem(path, t0); err == nil {
		t.Fatal("corrupt status accepted")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temporary files left: %v", entries)
	}
}
