package auth

import (
	"crypto/sha256"
	"testing"
)

func TestVerifyCSRFRequiresBothMatchingTokenAndPersistedHash(t *testing.T) {
	token := "csrf-token"
	hash := sha256.Sum256([]byte(token))
	service := &Service{}
	identity := Identity{csrfTokenHash: hash[:]}
	if !service.VerifyCSRF(identity, token, token) {
		t.Fatal("VerifyCSRF() rejected valid token")
	}
	if service.VerifyCSRF(identity, token, "other") {
		t.Fatal("VerifyCSRF() accepted mismatched header")
	}
	identity.csrfTokenHash = make([]byte, sha256.Size)
	if service.VerifyCSRF(identity, token, token) {
		t.Fatal("VerifyCSRF() accepted token that does not match stored hash")
	}
}
