package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Codec[T any] struct {
	ID     func(T) string
	Decode func([]byte) (T, error)
	Encode func(T) ([]byte, error)
}

type Store[T any] struct {
	root              string
	relativeDirectory string
	directory         string
	filename          func(string) (string, error)
	codec             Codec[T]
}

func New[T any](root, relativeDirectory string, filename func(string) (string, error), codec Codec[T]) (*Store[T], error) {
	if codec.ID == nil || codec.Decode == nil || codec.Encode == nil {
		return nil, fmt.Errorf("storage codec is incomplete")
	}
	if filename == nil {
		return nil, fmt.Errorf("storage filename mapper is nil")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve store root: %w", err)
	}
	if _, err := os.Stat(root); err != nil {
		return nil, fmt.Errorf("store root %s: %w", root, err)
	}
	directory, err := ValidatePathWithin(root, relativeDirectory)
	if err != nil {
		return nil, fmt.Errorf("store directory: %w", err)
	}
	if info, err := os.Stat(directory); err == nil && !info.IsDir() {
		return nil, fmt.Errorf("store path %s is not a directory", relativeDirectory)
	}
	return &Store[T]{root: root, relativeDirectory: relativeDirectory, directory: directory, filename: filename, codec: codec}, nil
}

func ValidatePathWithin(root, relativePath string) (string, error) {
	if relativePath == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	if filepath.IsAbs(relativePath) {
		return "", fmt.Errorf("path %q must be relative to the project root", relativePath)
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve root symlinks: %w", err)
	}
	candidate := filepath.Clean(filepath.Join(root, relativePath))
	if !isWithin(root, candidate) {
		return "", fmt.Errorf("path %q escapes the project root", relativePath)
	}

	check := candidate
	for {
		info, statErr := os.Lstat(check)
		if statErr == nil {
			realPath, evalErr := filepath.EvalSymlinks(check)
			if evalErr != nil {
				return "", fmt.Errorf("resolve path symlinks: %w", evalErr)
			}
			if !isWithin(root, realPath) {
				return "", fmt.Errorf("path %q resolves outside the project root", relativePath)
			}
			if info.Mode()&os.ModeSymlink == 0 || check == candidate {
				break
			}
		}
		parent := filepath.Dir(check)
		if parent == check {
			break
		}
		check = parent
	}
	return candidate, nil
}

func (s *Store[T]) PathForID(id string) (string, error) {
	filename, err := s.filename(id)
	if err != nil {
		return "", err
	}
	if filepath.Base(filename) != filename || filepath.Ext(filename) != ".yaml" {
		return "", fmt.Errorf("filename mapper returned unsafe filename %q", filename)
	}
	return ValidatePathWithin(s.root, filepath.Join(s.relativeDirectory, filename))
}

func (s *Store[T]) Load(id string) (T, error) {
	var zero T
	path, err := s.PathForID(id)
	if err != nil {
		return zero, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return zero, fmt.Errorf("read record %s: %w", id, err)
	}
	value, err := s.codec.Decode(data)
	if err != nil {
		return zero, fmt.Errorf("decode record %s: %w", id, err)
	}
	if actualID := s.codec.ID(value); actualID != id {
		return zero, fmt.Errorf("record %s contains id %q", id, actualID)
	}
	return value, nil
}

func (s *Store[T]) Save(value T) error {
	id := s.codec.ID(value)
	if id == "" {
		return fmt.Errorf("record id must not be empty")
	}
	path, err := s.PathForID(id)
	if err != nil {
		return err
	}
	data, err := s.codec.Encode(value)
	if err != nil {
		return fmt.Errorf("encode record %s: %w", id, err)
	}
	if err := os.MkdirAll(s.directory, 0o755); err != nil {
		return fmt.Errorf("create store directory: %w", err)
	}
	if err := atomicWrite(path, data); err != nil {
		return fmt.Errorf("save record %s: %w", id, err)
	}
	return nil
}

func (s *Store[T]) List() ([]T, error) {
	entries, err := os.ReadDir(s.directory)
	if os.IsNotExist(err) {
		return []T{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read store directory: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	result := make([]T, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if filepath.Ext(entry.Name()) != ".yaml" {
			return nil, fmt.Errorf("unexpected file %q in store", entry.Name())
		}
		recordPath, err := ValidatePathWithin(s.root, filepath.Join(s.relativeDirectory, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("record file %s: %w", entry.Name(), err)
		}
		data, err := os.ReadFile(recordPath)
		if err != nil {
			return nil, fmt.Errorf("read record file %s: %w", entry.Name(), err)
		}
		value, err := s.codec.Decode(data)
		if err != nil {
			return nil, fmt.Errorf("decode record file %s: %w", entry.Name(), err)
		}
		id := s.codec.ID(value)
		expected, err := s.PathForID(id)
		if err != nil {
			return nil, fmt.Errorf("record file %s: %w", entry.Name(), err)
		}
		if filepath.Clean(expected) != filepath.Clean(filepath.Join(s.directory, entry.Name())) {
			return nil, fmt.Errorf("record id %q does not match filename %q", id, entry.Name())
		}
		result = append(result, value)
	}
	return result, nil
}

func isWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)))
}
