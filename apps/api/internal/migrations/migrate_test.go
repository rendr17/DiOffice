package migrations

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestMigrationBundleContainsVersionedUpAndDownFiles(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate migration test source")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", ".."))
	migrationDir := filepath.Join(repoRoot, "db", "migrations")
	files, err := filepath.Glob(filepath.Join(migrationDir, "*.sql"))
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("no versioned SQL migrations found in %s", migrationDir)
	}

	filenamePattern := regexp.MustCompile(`^[0-9]+_[a-z0-9_]+\.sql$`)
	for _, file := range files {
		if !filenamePattern.MatchString(filepath.Base(file)) {
			t.Errorf("migration filename %q must start with a numeric version", filepath.Base(file))
			continue
		}
		contents, err := os.ReadFile(file)
		if err != nil {
			t.Errorf("read migration %q: %v", filepath.Base(file), err)
			continue
		}
		text := string(contents)
		if !strings.Contains(text, "-- +goose Up") {
			t.Errorf("migration %q is missing a goose Up section", filepath.Base(file))
		}
		if !strings.Contains(text, "-- +goose Down") {
			t.Errorf("migration %q is missing a goose Down section", filepath.Base(file))
		}
	}
}
