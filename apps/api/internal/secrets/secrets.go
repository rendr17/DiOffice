// Package secrets resolves credentials for outbound integrations. The
// resolver interface keeps callers agnostic about where a secret lives —
// v0.1 ships an environment-variable implementation for local development;
// a production deployment swaps in a secrets-manager-backed resolver
// (Vault, AWS/GCP secret managers, Doppler, …) without touching the
// runners. Resolution results are never logged, never put into canonical
// event payloads, and never persisted to the database.
package secrets

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
)

// ErrNotConfigured is returned when a named secret has no resolvable value.
// Callers treat it as a hard configuration error, not a retryable failure.
var ErrNotConfigured = errors.New("secret is not configured")

// Resolver resolves a named secret to its value. Names are opaque to the
// resolver — today they are environment variable names; a secrets-manager
// implementation would treat them as secret identifiers.
type Resolver interface {
	// Resolve returns the secret's value or ErrNotConfigured when unset.
	// Implementations must not log or record the returned value.
	Resolve(ctx context.Context, name string) (string, error)
}

// Env resolves secrets from process environment variables.
type Env struct{}

// Resolve reads name from the environment, trimming surrounding whitespace.
// An empty or whitespace-only value is treated as unconfigured.
func (Env) Resolve(_ context.Context, name string) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return "", fmt.Errorf("%w: %s", ErrNotConfigured, name)
	}
	return value, nil
}

// Cached wraps a resolver and memoizes each name's resolution for the
// lifetime of the process — credentials rarely rotate mid-run, and callers
// (e.g. a reconciler polling every minute) should not hit a backing store
// on every use. A secrets manager that rotates mid-process should provide
// its own TTL-aware implementation instead.
func Cached(inner Resolver) Resolver {
	return &cached{inner: inner, values: map[string]cachedValue{}}
}

type cachedValue struct {
	value string
	err   error
}

type cached struct {
	mu     sync.Mutex
	inner  Resolver
	values map[string]cachedValue
}

func (c *cached) Resolve(ctx context.Context, name string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if hit, ok := c.values[name]; ok {
		return hit.value, hit.err
	}
	value, err := c.inner.Resolve(ctx, name)
	c.values[name] = cachedValue{value: value, err: err}
	return value, err
}

// FromEnv is a convenience helper: resolve a secret with Env or return a
// contextual error. It exists so command entrypoints stay one line.
func FromEnv(ctx context.Context, name, purpose string) (string, error) {
	value, err := Env{}.Resolve(ctx, name)
	if err != nil {
		return "", fmt.Errorf("%s is required %s", name, purpose)
	}
	return value, nil
}
