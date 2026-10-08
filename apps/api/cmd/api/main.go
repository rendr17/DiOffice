package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/rendr17/dioffice/apps/api/internal/auth"
	"github.com/rendr17/dioffice/apps/api/internal/directory"
	"github.com/rendr17/dioffice/apps/api/internal/events"
	"github.com/rendr17/dioffice/apps/api/internal/folderbridge"
	"github.com/rendr17/dioffice/apps/api/internal/httpapi"
	"github.com/rendr17/dioffice/apps/api/internal/objectstore"
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
		webOrigin = "http://127.0.0.1:5173"
	}
	address := os.Getenv("API_ADDRESS")
	if address == "" {
		address = "127.0.0.1:8080"
	}
	devAuthBypass := false
	if raw := strings.TrimSpace(os.Getenv("DEV_AUTH_BYPASS")); raw != "" {
		devAuthBypass, err = strconv.ParseBool(raw)
		if err != nil {
			return errors.New("DEV_AUTH_BYPASS must be true or false")
		}
	}
	if devAuthBypass {
		if err := validateDevelopmentAuthBypass(address, webOrigin, secureCookies); err != nil {
			return err
		}
	}
	referenceImageStore, err := newReferenceImageStore(context.Background())
	if err != nil {
		return err
	}
	folderBridge, err := folderbridge.NewClient(os.Getenv("AGENT_GATEWAY_URL"), os.Getenv("AGENT_GATEWAY_INTERNAL_TOKEN"))
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr: address,
		Handler: httpapi.NewRouter(httpapi.Dependencies{
			DB: db, Auth: auth.NewService(db), Directory: directory.NewService(db), Tasks: tasks.NewService(db),
			Events:          events.NewService(db),
			ReferenceImages: referenceImageStore,
			FolderBridge:    folderBridge,
			SecureCookies:   secureCookies, WebOrigin: webOrigin, DevAuthBypass: devAuthBypass,
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

func newReferenceImageStore(ctx context.Context) (httpapi.ReferenceImageStore, error) {
	endpoint := strings.TrimSpace(os.Getenv("S3_ENDPOINT"))
	bucket := strings.TrimSpace(os.Getenv("S3_BUCKET"))
	accessKey := strings.TrimSpace(os.Getenv("S3_ACCESS_KEY"))
	secretKey := strings.TrimSpace(os.Getenv("S3_SECRET_KEY"))
	if endpoint == "" && bucket == "" && accessKey == "" && secretKey == "" {
		return nil, nil
	}
	if endpoint == "" || bucket == "" || accessKey == "" || secretKey == "" {
		return nil, errors.New("S3_ENDPOINT, S3_BUCKET, S3_ACCESS_KEY, and S3_SECRET_KEY must be configured together")
	}
	forcePathStyle := true
	if raw := strings.TrimSpace(os.Getenv("S3_FORCE_PATH_STYLE")); raw != "" {
		var err error
		forcePathStyle, err = strconv.ParseBool(raw)
		if err != nil {
			return nil, errors.New("S3_FORCE_PATH_STYLE must be true or false")
		}
	}
	storageCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return objectstore.NewS3Store(storageCtx, objectstore.Config{
		Endpoint: endpoint, Region: os.Getenv("S3_REGION"), Bucket: bucket,
		AccessKey: accessKey, SecretKey: secretKey, ForcePathStyle: forcePathStyle,
	})
}

func validateDevelopmentAuthBypass(address, webOrigin string, secureCookies bool) error {
	apiHost, _, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("DEV_AUTH_BYPASS requires API_ADDRESS to bind an explicit loopback IP and port")
	}
	apiIP := net.ParseIP(apiHost)
	if apiIP == nil || !apiIP.IsLoopback() {
		return errors.New("DEV_AUTH_BYPASS is only allowed on a loopback API bind address")
	}

	origin, err := url.Parse(webOrigin)
	if err != nil || origin.Scheme != "http" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" ||
		(origin.Path != "" && origin.Path != "/") {
		return errors.New("DEV_AUTH_BYPASS requires a local HTTP WEB_ORIGIN")
	}
	originIP := net.ParseIP(origin.Hostname())
	if originIP == nil || !originIP.IsLoopback() {
		return errors.New("DEV_AUTH_BYPASS is only allowed for a loopback WEB_ORIGIN")
	}
	if secureCookies {
		return errors.New("DEV_AUTH_BYPASS requires COOKIE_SECURE=false for local HTTP")
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
