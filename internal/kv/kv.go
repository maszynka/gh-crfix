// Package kv provides a tiny filesystem key-value store.
package kv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Store is a filesystem-backed key-value store rooted at Dir.
type Store struct {
	Dir string
}

// NewStore returns a new Store rooted at dir.
func NewStore(dir string) *Store {
	return &Store{Dir: dir}
}

func (s *Store) kvDir() string {
	return filepath.Join(s.Dir, "kv")
}

// isSafeName reports whether s is safe to use as a filename component.
// It rejects empty strings, path separators, and dot-dot sequences.
func isSafeName(s string) bool {
	return s != "" && !strings.ContainsAny(s, "/\\") && !strings.Contains(s, "..")
}

// Set stores value under namespace/key.
func (s *Store) Set(namespace, key, value string) error {
	if !isSafeName(namespace) {
		return fmt.Errorf("kv: unsafe namespace %q", namespace)
	}
	if !isSafeName(key) {
		return fmt.Errorf("kv: unsafe key %q", key)
	}
	dir := s.kvDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	path := filepath.Join(dir, namespace+"__"+key)
	return os.WriteFile(path, []byte(value), 0644)
}

// Get retrieves the value for namespace/key. Returns ("", nil) if not found.
func (s *Store) Get(namespace, key string) (string, error) {
	if !isSafeName(namespace) {
		return "", fmt.Errorf("kv: unsafe namespace %q", namespace)
	}
	if !isSafeName(key) {
		return "", fmt.Errorf("kv: unsafe key %q", key)
	}
	path := filepath.Join(s.kvDir(), namespace+"__"+key)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

// Append appends value to the named list.
func (s *Store) Append(list, value string) error {
	if !isSafeName(list) {
		return fmt.Errorf("kv: unsafe list name %q", list)
	}
	dir := s.kvDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	path := filepath.Join(dir, "list__"+list)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(value + "\n")
	return err
}

// List returns all values in the named list, in order.
func (s *Store) List(list string) ([]string, error) {
	if !isSafeName(list) {
		return nil, fmt.Errorf("kv: unsafe list name %q", list)
	}
	path := filepath.Join(s.kvDir(), "list__"+list)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	var result []string
	for _, line := range lines {
		if line != "" {
			result = append(result, line)
		}
	}
	if result == nil {
		return []string{}, nil
	}
	return result, nil
}
