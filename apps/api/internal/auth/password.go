package auth

import (
	"errors"

	"golang.org/x/crypto/bcrypt"
)

const (
	passwordMinBytes       = 12
	bcryptMaxPasswordBytes = 72
	bcryptCost             = 12
)

var ErrInvalidPassword = errors.New("password must contain 12 to 72 bytes")

func HashPassword(password string) (string, error) {
	if len(password) < passwordMinBytes || len(password) > bcryptMaxPasswordBytes {
		return "", ErrInvalidPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func VerifyPassword(encodedHash, password string) bool {
	if encodedHash == "" || len(password) > bcryptMaxPasswordBytes {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(encodedHash), []byte(password)) == nil
}
