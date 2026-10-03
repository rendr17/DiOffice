package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/rendr17/dioffice/apps/api/internal/auth"
	"golang.org/x/term"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Owner bootstrap failed:", err)
		os.Exit(1)
	}
}

func run() error {
	flags := flag.NewFlagSet("bootstrap-owner", flag.ContinueOnError)
	organizationName := flags.String("organization", "", "organization name")
	projectName := flags.String("project", "", "initial project name")
	email := flags.String("email", "", "Owner email address")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return errors.New("invalid command options")
	}
	if strings.TrimSpace(*organizationName) == "" || strings.TrimSpace(*projectName) == "" || strings.TrimSpace(*email) == "" {
		return errors.New("--organization, --project, and --email are required")
	}
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required; apply migrations first")
	}

	password, err := readPassword("Owner password (12-72 bytes): ")
	if err != nil {
		return errors.New("could not read password from the terminal")
	}
	confirmation, err := readPassword("Confirm Owner password: ")
	if err != nil {
		return errors.New("could not read password confirmation from the terminal")
	}
	if password != confirmation {
		return errors.New("passwords do not match")
	}

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return errors.New("could not open database connection; verify DATABASE_URL")
	}
	defer db.Close()
	db.SetMaxOpenConns(4)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return errors.New("database is unavailable; verify DATABASE_URL, PostgreSQL health, and migrations")
	}

	owner, err := auth.NewService(db).BootstrapOwner(ctx, auth.BootstrapOwnerInput{
		OrganizationName: *organizationName,
		ProjectName:      *projectName,
		Email:            *email,
		Password:         password,
	})
	if err != nil {
		if errors.Is(err, auth.ErrInvalidBootstrapInput) {
			return err
		}
		return errors.New("could not create Owner; verify the database migration state")
	}
	fmt.Printf("Owner bootstrap complete. Organization ID: %s\nProject ID: %s\nDeni employee ID: %s\n",
		owner.OrganizationID, owner.ProjectID, owner.EmployeeID)
	return nil
}

func readPassword(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	password, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return string(password), nil
}
