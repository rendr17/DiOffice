// Command session-runner is the runtime session starter for the execution
// layer. It polls for PROVISIONING attempts whose workspace is READY, asks
// the local OpenCode server for a directory-scoped session, sends the task
// instruction, and advances attempt/workspace/task state with durable
// events. Claims are storage-level agent_sessions rows, so the process is
// restart-safe and safe to run alongside the provisioner.
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
	"github.com/rendr17/dioffice/apps/api/internal/sessionrunner"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("session runner stopped with error", "error", err)
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
	cfg := sessionrunner.Config{
		WorkRoot:        workRoot,
		OpTimeout:       envDuration("SESSION_RUNNER_OP_TIMEOUT", 2*time.Minute),
		StaleClaimAfter: envDuration("SESSION_RUNNER_CLAIM_STALE_AFTER", 10*time.Minute),
		PollInterval:    envDuration("SESSION_RUNNER_POLL_INTERVAL", 2*time.Second),
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
	// The resolver reads each org's provider_configs row for the attempt's
	// runtime key; OPENCODE_SERVER_URL remains the dev fallback for
	// unconfigured orgs, and codex/claude resolve to provider_not_implemented.
	resolver := sessionrunner.Resolver{
		DB:             db,
		OpenCodeEnvURL: strings.TrimSpace(os.Getenv("OPENCODE_SERVER_URL")),
	}
	runner, err := sessionrunner.NewWithResolver(db, cfg, resolver.Resolve)
	if err != nil {
		return err
	}
	slog.Info("session runner started",
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
