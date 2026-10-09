package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/rendr17/dioffice/apps/api/internal/prreconciler"
	"github.com/rendr17/dioffice/apps/api/internal/prrunner"
	"github.com/rendr17/dioffice/apps/api/internal/secrets"
)

// pr-reconciler re-polls recorded pull requests against the GitHub API and
// persists drift (head/base/state) as canonical facts — including the
// DONE → IN_REVIEW approval-staleness transition on post-approval head
// changes. The GitHub credential resolves through internal/secrets —
// env-var backed locally, a secrets-manager resolver for production.
func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		slog.Error("DATABASE_URL is required")
		os.Exit(1)
	}
	token, err := secrets.FromEnv(context.Background(), "GITHUB_TOKEN", "to read pull request state")
	if err != nil {
		slog.Error("resolve github credential", "error", err)
		os.Exit(1)
	}

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		slog.Error("open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	db.SetMaxOpenConns(4)
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := db.PingContext(pingCtx); err != nil {
		pingCancel()
		slog.Error("ping database", "error", err)
		os.Exit(1)
	}
	pingCancel()

	reconciler, err := prreconciler.New(db, &prrunner.HTTPGitHub{
		APIURL: os.Getenv("GITHUB_API_URL"), Token: token,
	}, prreconciler.Config{
		PollInterval: envDuration("PR_RECONCILER_POLL_INTERVAL", 60*time.Second),
		BatchSize:    envInt("PR_RECONCILER_BATCH_SIZE", 50),
		OpTimeout:    envDuration("PR_RECONCILER_OP_TIMEOUT", 30*time.Second),
	})
	if err != nil {
		slog.Error("configure reconciler", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	slog.Info("pull request reconciler started")
	if err := reconciler.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("reconciler stopped", "error", err)
		os.Exit(1)
	}
}

func envDuration(key string, fallback time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		slog.Warn("invalid duration env; using fallback", "key", key, "value", raw)
		return fallback
	}
	return value
}

func envInt(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	var value int
	if _, err := fmt.Sscanf(raw, "%d", &value); err != nil || value <= 0 {
		slog.Warn("invalid int env; using fallback", "key", key, "value", raw)
		return fallback
	}
	return value
}
