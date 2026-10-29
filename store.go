package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
)

// jsonStore is a tiny mutex-guarded value persisted as a json file.
type jsonStore[T any] struct {
	path string
	mu   sync.Mutex
	val  T
}

func openStore[T any](path string) (*jsonStore[T], error) {
	s := &jsonStore[T]{path: path}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("store: read %s: %w", path, err)
	default:
		if err := json.Unmarshal(b, &s.val); err != nil {
			return nil, fmt.Errorf("store: parse %s: %w", path, err)
		}
	}
	return s, nil
}

// Get returns a deep copy, so callers can't race with Update.
func (s *jsonStore[T]) Get() T {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out T
	b, _ := json.Marshal(s.val)
	_ = json.Unmarshal(b, &out)
	return out
}

func (s *jsonStore[T]) Update(fn func(v T) T) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.val = fn(s.val)
	b, err := json.MarshalIndent(s.val, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("store: write: %w", err)
	}
	return os.Rename(tmp, s.path)
}
