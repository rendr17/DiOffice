// Command checks-runner is the verification stage of the execution layer.
// It claims RUNNING attempts whose agent session completed, pins the
// candidate commit SHA in the task worktree, executes the manifest's checks
// as argv processes with bounded output capture, and persists canonical
// check.* facts plus log artifacts. All required checks passing marks the
// attempt SUCCEEDED; failures mark it FAILED and move the task to FAILED for
// an explicit Owner Retry. Optional object storage (OBJECTSTORE_*) stores
// per-check log artifacts; without it the check.* facts still stand.
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
	"github.com/rendr17/dioffice/apps/api/internal/checksrunner"
	"github.com/rendr17/dioffice/apps/api/internal/objectstore"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("checks runner stopped with error", "error", err)
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
	var store checksrunner.ArtifactStore
	if endpoint := strings.TrimSpace(os.Getenv("OBJECTSTORE_ENDPOINT")); endpoint != "" {
		s3, err := objectstore.NewS3Store(ctx, objectstore.Config{
			Endpoint:  endpoint,
			Region:    strings.TrimSpace(os.Getenv("OBJECTSTORE_REGION")),
			Bucket:    strings.TrimSpace(os.Getenv("OBJECTSTORE_BUCKET")),
			AccessKey: strings.TrimSpace(os.Getenv("OBJECTSTORE_ACCESS_KEY")),
			SecretKey: strings.TrimSpace(os.Getenv("OBJECTSTORE_SECRET_KEY")),
			ForcePathStyle: strings.EqualFold(strings.TrimSpace(
				os.Getenv("OBJECTSTORE_FORCE_PATH_STYLE")), "true"),
		})
		if err != nil {
			return fmt.Errorf("object storage is configured but unavailable: %w", err)
		}
		store = s3
	} else {
		slog.Warn("OBJECTSTORE_ENDPOINT unset: check log artifacts will not be stored")
	}
	cfg := checksrunner.Config{
		WorkRoot:        workRoot,
		GitBin:          strings.TrimSpace(os.Getenv("GIT_BIN")),
		PollInterval:    envDuration("CHECKS_RUNNER_POLL_INTERVAL", 2*time.Second),
		StaleClaimAfter: envDuration("CHECKS_RUNNER_CLAIM_STALE_AFTER", 30*time.Minute),
		OpTimeout:       envDuration("CHECKS_RUNNER_OP_TIMEOUT", 30*time.Minute),
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
	runner, err := checksrunner.New(db, cfg, nil, store)
	if err != nil {
		return err
	}
	slog.Info("checks runner started",
		"work_root", cfg.WorkRoot, "poll_interval", cfg.PollInterval,
		"claim_stale_after", cfg.StaleClaimAfter)
	return runner.Run(ctx)
}

func envDuration(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
