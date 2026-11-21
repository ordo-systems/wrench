package mocks

import (
	"context"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

var gitlabEnv = map[string]string{
	"WRENCH_GITLAB_CLIENT_ID": "cid", "WRENCH_GITLAB_CLIENT_SECRET": "secret", "WRENCH_GITLAB_SYNC_TOKEN": "sync",
}

func TestRunGitLabNeedsEnv(t *testing.T) {
	for _, missing := range []string{"WRENCH_GITLAB_CLIENT_ID", "WRENCH_GITLAB_CLIENT_SECRET", "WRENCH_GITLAB_SYNC_TOKEN"} {
		t.Run(missing, func(t *testing.T) {
			e := map[string]string{}
			for k, v := range gitlabEnv {
				if k != missing {
					e[k] = v
				}
			}
			err := RunGitLab(context.Background(), GitLabOptions{Listen: "127.0.0.1:0", ConfigPath: "../../../deploy/shared/config/config.yaml"}, env(e))
			if err == nil || !strings.Contains(err.Error(), missing) {
				t.Fatalf("err = %v, want it to name %s", err, missing)
			}
		})
	}
}

func TestRunGitLabBadConfig(t *testing.T) {
	err := RunGitLab(context.Background(), GitLabOptions{Listen: "127.0.0.1:0", ConfigPath: "missing.yaml"}, env(gitlabEnv))
	if err == nil || !strings.Contains(err.Error(), "missing.yaml") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	tests := map[string]func(context.Context) error{
		"llm": func(ctx context.Context) error { return RunLLM(ctx, "127.0.0.1:0") },
		"gitlab": func(ctx context.Context) error {
			return RunGitLab(ctx, GitLabOptions{Listen: "127.0.0.1:0", Issuer: "http://127.0.0.1:9000",
				ConfigPath: "../../../deploy/shared/config/config.yaml"}, env(gitlabEnv))
		},
	}
	for name, run := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			if err := run(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}
