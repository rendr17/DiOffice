// Command pr-runner is the publication stage of the execution layer. It
// claims SUCCEEDED attempts whose candidate_sha is recorded, pushes that
// exact SHA to the task branch on GitHub (token via http.extraheader, never
// in URLs or logs), opens or adopts the pull request, persists the
// pull_requests row, and moves the task to IN_REVIEW — the last step before
// Owner approval decides DONE. GITHUB_TOKEN supplies both push and API
// credentials; GITHUB_API_URL overrides the REST base for tests.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/rendr17/dioffice/apps/api/internal/prrunner"
	"github.com/rendr17/dioffice/apps/api/internal/secrets"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("pr runner stopped with error", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	workRoot := strings.TrimSpace(os.Getenv("PROVISIONER_WORK_ROOT"))
	if workRoot == "" {
		return errors.New("PROVISIONER_WORK_ROOT is required (must match the provisioner's work root)")
	}
	// The credential resolves through the secrets abstraction: env-var
	// backed locally; a secrets-manager resolver swaps in for production
	// without changing the runner.
	token, err := secrets.FromEnv(ctx, "GITHUB_TOKEN", "for pull request publication")
	if err != nil {
		return err
	}
	// Git must never block on a credential prompt in a background runner.
	_ = os.Setenv("GIT_TERMINAL_PROMPT", "0")

	cfg := prrunner.Config{
		WorkRoot:        workRoot,
		GitBin:          strings.TrimSpace(os.Getenv("GIT_BIN")),
		PollInterval:    envDuration("PR_RUNNER_POLL_INTERVAL", 2*time.Second),
		StaleClaimAfter: envDuration("PR_RUNNER_CLAIM_STALE_AFTER", 10*time.Minute),
		RetryBackoff:    envDuration("PR_RUNNER_RETRY_BACKOFF", 30*time.Second),
		OpTimeout:       envDuration("PR_RUNNER_OP_TIMEOUT", 10*time.Minute),
		Token:           token,
	}
	if raw := strings.TrimSpace(os.Getenv("PR_RUNNER_MAX_FAILURES")); raw != "" {
		var n int
		if _, err := fmt.Sscanf(raw, "%d", &n); err == nil && n > 0 {
			cfg.MaxFailures = n
		}
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open database connection: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(4)
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	github := &prrunner.HTTPGitHub{
		APIURL: strings.TrimSpace(os.Getenv("GITHUB_API_URL")),
		Token:  token,
	}
	runner, err := prrunner.New(db, cfg, nil, github)
	if err != nil {
		return err
	}
	slog.Info("pr runner started",
		"work_root", cfg.WorkRoot, "poll_interval", cfg.PollInterval,
		"api_url", github.APIURL)
	return runner.Run(ctx)
}

func envDuration(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value < 0 {
		return fallback
	}
	return value
}
