package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/rendr17/dioffice/apps/api/internal/auth"
	"github.com/rendr17/dioffice/apps/api/internal/httpapi"
	"github.com/rendr17/dioffice/apps/api/internal/tasks"
)

func main() {
	if err := run(); err != nil {
		slog.Error("API stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required; apply migrations before starting the API")
	}
	db, err := openDatabase(databaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	secureCookies := true
	if raw := strings.TrimSpace(os.Getenv("COOKIE_SECURE")); raw != "" {
		secureCookies, err = strconv.ParseBool(raw)
		if err != nil {
			return errors.New("COOKIE_SECURE must be true or false")
		}
	}
	webOrigin := strings.TrimSpace(os.Getenv("WEB_ORIGIN"))
	if webOrigin == "" {
		webOrigin = "http://localhost:5173"
	}
	address := os.Getenv("API_ADDRESS")
	if address == "" {
		address = "127.0.0.1:8080"
	}

	server := &http.Server{
		Addr: address,
		Handler: httpapi.NewRouter(httpapi.Dependencies{
			DB: db, Auth: auth.NewService(db), Tasks: tasks.NewService(db),
			SecureCookies: secureCookies, WebOrigin: webOrigin,
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		slog.Info("api listening", "address", address)
		serverErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("API server failed: %w", err)
		}
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("API shutdown failed: %w", err)
		}
	}
	return nil
}

func openDatabase(databaseURL string) (*sql.DB, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, errors.New("could not open database connection; verify DATABASE_URL")
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxIdleTime(5 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, errors.New("database is unavailable; verify DATABASE_URL and PostgreSQL health")
	}
	return db, nil
}
