package auth

import (
	"strings"
	"testing"
)

func TestNormalizeEmail(t *testing.T) {
	got, err := normalizeEmail("  OWNER@Example.Invalid ")
	if err != nil {
		t.Fatalf("normalizeEmail() error = %v", err)
	}
	if got != "owner@example.invalid" {
		t.Fatalf("normalizeEmail() = %q, want lower-case trimmed email", got)
	}
	for _, invalid := range []string{"", "owner", "Owner <owner@example.invalid>", strings.Repeat("a", 260) + "@example.invalid"} {
		if _, err := normalizeEmail(invalid); err == nil {
			t.Errorf("normalizeEmail(%q) accepted invalid email", invalid)
		}
	}
}
