// Command event-ingester consumes the local OpenCode server's event stream
// for every RUNNING agent session and normalizes provider activity into
// canonical durable facts (agent.message, file.activity, command.*,
// session.completed/failed). Provider ids become deterministic dedupe keys
// so restarts and replays never double-record a fact.
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
	"github.com/rendr17/dioffice/apps/api/internal/runtimeevents"
	"github.com/rendr17/dioffice/apps/api/internal/sessionrunner"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("event ingester stopped with error", "error", err)
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
	runtime, err := sessionrunner.NewOpenCodeRuntime(sessionrunner.OpenCodeConfig{
		ServerURL: strings.TrimSpace(os.Getenv("OPENCODE_SERVER_URL")),
	})
	if err != nil {
		return err
	}
	cfg := runtimeevents.Config{
		WorkRoot:       workRoot,
		PollInterval:   envDuration("EVENT_INGESTER_POLL_INTERVAL", 2*time.Second),
		ReconnectDelay: envDuration("EVENT_INGESTER_RECONNECT_DELAY", 3*time.Second),
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
	ingester, err := runtimeevents.New(db, cfg, runtime)
	if err != nil {
		return err
	}
	slog.Info("event ingester started",
		"work_root", cfg.WorkRoot, "poll_interval", cfg.PollInterval)
	return ingester.Run(ctx)
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
