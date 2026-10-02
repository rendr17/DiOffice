package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/rendr17/dioffice/apps/api/internal/migrations"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := run(ctx); err != nil {
		slog.Error("database migration failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}

	migrationsDir := strings.TrimSpace(os.Getenv("MIGRATIONS_DIR"))
	if migrationsDir == "" {
		migrationsDir = filepath.Clean(filepath.Join("..", "..", "db", "migrations"))
	}
	info, err := os.Stat(migrationsDir)
	if err != nil {
		return fmt.Errorf("inspect migrations directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("migrations path %q is not a directory", migrationsDir)
	}

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open database connection: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	if err := migrations.Up(ctx, db, migrationsDir); err != nil {
		return err
	}

	slog.Info("database migrations applied", "directory", migrationsDir)
	return nil
}
