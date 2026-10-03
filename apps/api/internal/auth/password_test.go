package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestHashPasswordProducesOneWayHashAndVerifiesPassword(t *testing.T) {
	password := "correct-horse-battery-staple"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if hash == password || len(hash) < 50 {
		t.Fatalf("HashPassword() returned an invalid password hash %q", hash)
	}
	if !VerifyPassword(hash, password) {
		t.Fatal("VerifyPassword() rejected the correct password")
	}
	if VerifyPassword(hash, "different-password") {
		t.Fatal("VerifyPassword() accepted an incorrect password")
	}
}

func TestHashPasswordRejectsUnsupportedLengths(t *testing.T) {
	for _, password := range []string{"short", strings.Repeat("x", bcryptMaxPasswordBytes+1)} {
		if _, err := HashPassword(password); !errors.Is(err, ErrInvalidPassword) {
			t.Errorf("HashPassword(%d bytes) error = %v, want ErrInvalidPassword", len(password), err)
		}
	}
}
