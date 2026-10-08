package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
)

type memoryReferenceImageStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newMemoryReferenceImageStore() *memoryReferenceImageStore {
	return &memoryReferenceImageStore{objects: make(map[string][]byte)}
}

func (s *memoryReferenceImageStore) Put(ctx context.Context, key, _ string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = append([]byte(nil), data...)
	return nil
}

func (s *memoryReferenceImageStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, exists := s.objects[key]
	if !exists {
		return nil, errors.New("object not found")
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), data...))), nil
}

func (s *memoryReferenceImageStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key)
	return nil
}

func (s *memoryReferenceImageStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.objects)
}
