// Command provisioner is the local workspace reconciler for the execution
// layer. It polls for committed execution attempts, materializes isolated git
// worktrees from connected repositories, verifies recorded manifest digests,
// and advances workspaces to READY with durable events. It does not start
// containers or agent sessions — that boundary belongs to the runtime session
// starter. Temporal remains available for later orchestration; this process is
// deliberately restart-safe because claims are storage-level.
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
	"github.com/rendr17/dioffice/apps/api/internal/provisioning"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("provisioner stopped with error", "error", err)
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
		return errors.New("PROVISIONER_WORK_ROOT is required (absolute directory for repository caches and task worktrees)")
	}
	cfg := provisioning.Config{
		WorkRoot:        workRoot,
		GitBin:          strings.TrimSpace(os.Getenv("GIT_BIN")),
		RemoteBase:      strings.TrimSpace(os.Getenv("GIT_REMOTE_BASE")),
		OpTimeout:       envDuration("PROVISIONER_OP_TIMEOUT", 5*time.Minute),
		StaleClaimAfter: envDuration("PROVISIONER_CLAIM_STALE_AFTER", 10*time.Minute),
		PollInterval:    envDuration("PROVISIONER_POLL_INTERVAL", 2*time.Second),
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
	provisioner, err := provisioning.New(db, cfg, nil)
	if err != nil {
		return err
	}
	slog.Info("provisioner started",
		"work_root", cfg.WorkRoot, "poll_interval", cfg.PollInterval,
		"claim_stale_after", cfg.StaleClaimAfter)
	return provisioner.Run(ctx)
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
