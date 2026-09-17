package specvalidation

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/redhat-et/protobot/ears-manager/internal/records"
	"github.com/redhat-et/protobot/ears-manager/internal/storage"
)

type loadError struct {
	Path string
	Err  error
}

func (e loadError) Error() string {
	return fmt.Sprintf("unable to load %s", e.Path)
}

// Load reads a complete project snapshot using the storage layer's safe YAML
// decoder. It performs no semantic validation and never writes files.
func Load(root string) (Snapshot, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return Snapshot{}, loadError{Path: ".protobot/project.yaml", Err: err}
	}
	configPath := filepath.Join(absoluteRoot, ".protobot", "project.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return Snapshot{}, loadError{Path: ".protobot/project.yaml", Err: err}
	}
	var config records.ProjectConfig
	fields, err := storage.DecodeFields(data, &config)
	if err != nil {
		return Snapshot{}, loadError{Path: ".protobot/project.yaml", Err: err}
	}
	snapshot := Snapshot{
		Root:         absoluteRoot,
		Config:       config,
		ConfigPath:   ".protobot/project.yaml",
		ConfigFields: fields,
	}
	paths := config.Stores.WithDefaults()
	snapshot.Requirements, err = loadDocuments(absoluteRoot, paths.Requirements, records.RequirementStore, func(data []byte) (records.Requirement, map[string]bool, error) {
		var value records.Requirement
		fields, err := storage.DecodeFields(data, &value)
		return value, fields, err
	})
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.Interfaces, err = loadDocuments(absoluteRoot, paths.Interfaces, records.InterfaceStore, func(data []byte) (records.InterfaceRecord, map[string]bool, error) {
		var value records.InterfaceRecord
		fields, err := storage.DecodeFields(data, &value)
		return value, fields, err
	})
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.ChangeSets, err = loadDocuments(absoluteRoot, paths.ChangeSets, records.ChangeSetStore, func(data []byte) (records.ChangeSet, map[string]bool, error) {
		var value records.ChangeSet
		fields, err := storage.DecodeFields(data, &value)
		return value, fields, err
	})
	if err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

// ValidateProject loads and validates a project in read-only mode.
func ValidateProject(root string) Result {
	snapshot, err := Load(root)
	if err != nil {
		var loadErr loadError
		path := ""
		if ok := asLoadError(err, &loadErr); ok {
			path = loadErr.Path
		}
		result := Result{}
		result.add(diagnostic("storage.decode_failed", path, "", "", err.Error(), "Fix the reported file without modifying it through another route."))
		result.finish()
		return result
	}
	return Validate(snapshot)
}

func loadDocuments[T any](root, relativeDirectory string, kind records.StoreKind, decode func([]byte) (T, map[string]bool, error)) ([]Document[T], error) {
	canonical, err := canonicalProjectPath(relativeDirectory)
	if err != nil {
		return nil, loadError{Path: relativeDirectory, Err: err}
	}
	resolved, err := storage.ValidatePathWithin(root, filepath.FromSlash(canonical))
	if err != nil {
		return nil, loadError{Path: canonical, Err: err}
	}
	entries, err := os.ReadDir(resolved)
	if os.IsNotExist(err) {
		return []Document[T]{}, nil
	}
	if err != nil {
		return nil, loadError{Path: canonical, Err: err}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	result := make([]Document[T], 0, len(entries))
	for _, entry := range entries {
		document, include, err := loadDocumentEntry(root, resolved, canonical, entry.Name(), kind, decode)
		if err != nil {
			return nil, err
		}
		if include {
			result = append(result, document)
		}
	}
	return result, nil
}

func loadDocumentEntry[T any](root, resolved, relativeDirectory, name string, kind records.StoreKind, decode func([]byte) (T, map[string]bool, error)) (Document[T], bool, error) {
	if strings.HasPrefix(name, ".") {
		return Document[T]{}, false, nil
	}
	entryPath := filepath.Join(resolved, name)
	relativePath := filepath.ToSlash(filepath.Join(relativeDirectory, name))
	info, err := os.Lstat(entryPath)
	if err != nil {
		return Document[T]{}, false, loadError{Path: relativePath, Err: err}
	}
	if info.IsDir() {
		return Document[T]{}, false, nil
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Document[T]{}, false, loadError{Path: relativePath, Err: fmt.Errorf("record path must be a regular file")}
	}
	if filepath.Ext(name) != ".yaml" {
		return Document[T]{}, false, loadError{Path: relativePath, Err: fmt.Errorf("unexpected file in record store")}
	}
	data, err := os.ReadFile(entryPath)
	if err != nil {
		return Document[T]{}, false, loadError{Path: relativePath, Err: err}
	}
	value, fields, err := decode(data)
	if err != nil {
		return Document[T]{}, false, loadError{Path: relativePath, Err: err}
	}
	id := documentID(value)
	if expected, mapErr := records.FilenameFor(kind, id); mapErr == nil && expected != name {
		return Document[T]{}, false, loadError{Path: relativePath, Err: fmt.Errorf("record ID %q does not match filename %q", id, name)}
	}
	rootRelative, err := filepath.Rel(root, entryPath)
	if err != nil {
		return Document[T]{}, false, loadError{Path: relativePath, Err: err}
	}
	return Document[T]{Path: filepath.ToSlash(rootRelative), Value: value, Fields: fields}, true, nil
}

func documentID[T any](value T) string {
	switch typed := any(value).(type) {
	case records.Requirement:
		return typed.ID
	case records.InterfaceRecord:
		return typed.ID
	case records.ChangeSet:
		return typed.ID
	default:
		return ""
	}
}

func asLoadError(err error, target *loadError) bool {
	if typed, ok := err.(loadError); ok {
		*target = typed
		return true
	}
	return false
}
