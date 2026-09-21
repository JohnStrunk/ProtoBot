package specvalidation

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/redhat-et/protobot/ears-manager/internal/records"
	"github.com/redhat-et/protobot/ears-manager/internal/schema"
	"github.com/redhat-et/protobot/ears-manager/internal/storage"
)

type loadCause string

const (
	loadCauseYAML             loadCause = "YAML decoding failed"
	loadCauseUnexpectedFile   loadCause = "unexpected file in record store"
	loadCauseFilenameMismatch loadCause = "record filename does not match its ID"
	loadCauseStorePath        loadCause = "record store path is invalid"
	loadCauseSchema           loadCause = "schema version is unsupported"
	loadCauseFilesystem       loadCause = "filesystem or project data access failed"
)

type loadError struct {
	Path       string
	Field      string
	Code       string
	Cause      loadCause
	ExposePath bool
	Err        error
}

func (e loadError) Error() string {
	if e.Cause == loadCauseSchema {
		var versionErr *schema.VersionError
		if errors.As(e.Err, &versionErr) {
			return fmt.Sprintf("Unsupported %s schema version %d; supported version is %d.", versionErr.Store, versionErr.Found, versionErr.Supported)
		}
	}
	if e.Cause != "" {
		return string(e.Cause)
	}
	return string(loadCauseFilesystem)
}

func (e loadError) Unwrap() error {
	return e.Err
}

type loadFailures struct {
	Items []loadError
}

func (e *loadFailures) Error() string {
	return "project data load failed"
}

func (e *loadFailures) Unwrap() []error {
	items := make([]error, len(e.Items))
	for index := range e.Items {
		items[index] = e.Items[index]
	}
	return items
}

// Load reads a complete project snapshot using the storage layer's safe YAML
// decoder. It performs no semantic validation and never writes files.
func Load(root string) (Snapshot, error) {
	return LoadWithContext(root, ValidationContext{})
}

// LoadWithContext reads a snapshot and records which change-set manifests are
// proposed so impact validation can use the correct specification state.
func LoadWithContext(root string, context ValidationContext) (Snapshot, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return Snapshot{}, loadError{Field: "project", Cause: loadCauseFilesystem, Err: err}
	}
	rootHandle, err := os.OpenRoot(absoluteRoot)
	if err != nil {
		return Snapshot{}, loadError{Field: "project", Cause: loadCauseFilesystem, Err: err}
	}
	defer func() { _ = rootHandle.Close() }()
	controlInfo, err := rootHandle.Lstat(".protobot")
	if err != nil {
		return Snapshot{}, loadError{Field: "project", Cause: loadCauseFilesystem, Err: err}
	}
	if controlInfo.Mode()&os.ModeSymlink != 0 || !controlInfo.IsDir() {
		return Snapshot{}, loadError{Field: "project", Cause: loadCauseStorePath, Err: fmt.Errorf("control namespace must be a directory")}
	}
	configRelative := filepath.FromSlash(".protobot/project.yaml")
	configInfo, err := rootHandle.Lstat(configRelative)
	if err != nil {
		return Snapshot{}, loadError{Field: "project", Cause: loadCauseFilesystem, Err: err}
	}
	if configInfo.Mode()&os.ModeSymlink != 0 || !configInfo.Mode().IsRegular() {
		return Snapshot{}, loadError{Field: "project", Cause: loadCauseFilesystem, Err: fmt.Errorf("project configuration must be a regular file")}
	}
	data, err := rootHandle.ReadFile(configRelative)
	if err != nil {
		return Snapshot{}, loadError{Field: "project", Cause: loadCauseFilesystem, Err: err}
	}
	versions, _, err := storage.DecodeSchemaVersions(data)
	if err != nil {
		return Snapshot{}, loadError{Field: "schema_versions", Cause: loadCauseYAML, Err: err}
	}
	if err := schema.Validate(versions); err != nil {
		versionErr := &schema.VersionError{}
		if errors.As(err, &versionErr) {
			return Snapshot{}, loadError{Field: "schema_versions." + versionErr.Store, Code: "schema.unsupported_version", Cause: loadCauseSchema, Err: err}
		}
		return Snapshot{}, loadError{Field: "schema_versions", Code: "schema.unsupported_version", Cause: loadCauseSchema, Err: err}
	}
	var config records.ProjectConfig
	fields, err := storage.DecodeFields(data, &config)
	if err != nil {
		return Snapshot{}, loadError{Field: "project", Cause: loadCauseYAML, Err: err}
	}
	snapshot := Snapshot{
		Root:         absoluteRoot,
		Config:       config,
		ConfigPath:   ".protobot/project.yaml",
		ConfigFields: fields,
		Context:      context,
	}
	failures := loadStoreDocuments(&snapshot, absoluteRoot, rootHandle, config.Stores.WithDefaults())
	if len(failures) > 0 {
		return snapshot, &loadFailures{Items: failures}
	}
	return snapshot, nil
}

// ValidateProject loads and validates a project in read-only mode.
func ValidateProject(root string) Result {
	snapshot, err := Load(root)
	return validateLoadedProject(snapshot, err)
}

// ValidateProjectWithContext loads and validates a project while preserving
// independent load failures and the caller's proposed-change-set context.
func ValidateProjectWithContext(root string, context ValidationContext) Result {
	snapshot, err := LoadWithContext(root, context)
	return validateLoadedProject(snapshot, err)
}

func validateLoadedProject(snapshot Snapshot, err error) Result {
	result := Result{}
	if err != nil {
		for _, failure := range loadFailureList(err) {
			result.add(loadDiagnostic(failure))
		}
	}
	if snapshot.ConfigPath != "" {
		semantic := Validate(snapshot)
		result.Diagnostics = append(result.Diagnostics, semantic.Diagnostics...)
	}
	result.finish()
	return result
}

func loadStoreDocuments(snapshot *Snapshot, root string, rootHandle *os.Root, paths records.StorePaths) []loadError {
	var failures []loadError
	var storeFailures []loadError
	snapshot.Requirements, storeFailures = loadDocuments(root, rootHandle, paths.Requirements, records.RequirementStore, func(data []byte) (records.Requirement, map[string]bool, error) {
		var value records.Requirement
		fields, err := storage.DecodeFields(data, &value)
		return value, fields, err
	})
	failures = append(failures, storeFailures...)
	snapshot.Interfaces, storeFailures = loadDocuments(root, rootHandle, paths.Interfaces, records.InterfaceStore, func(data []byte) (records.InterfaceRecord, map[string]bool, error) {
		var value records.InterfaceRecord
		fields, err := storage.DecodeFields(data, &value)
		return value, fields, err
	})
	failures = append(failures, storeFailures...)
	snapshot.ChangeSets, storeFailures = loadDocuments(root, rootHandle, paths.ChangeSets, records.ChangeSetStore, func(data []byte) (records.ChangeSet, map[string]bool, error) {
		var value records.ChangeSet
		fields, err := storage.DecodeFields(data, &value)
		return value, fields, err
	})
	return append(failures, storeFailures...)
}

func loadFailureList(err error) []loadError {
	var failures *loadFailures
	if errors.As(err, &failures) {
		return failures.Items
	}
	var failure loadError
	if errors.As(err, &failure) {
		return []loadError{failure}
	}
	return []loadError{{Field: "project", Cause: loadCauseFilesystem}}
}

func loadDiagnostic(failure loadError) Diagnostic {
	code := failure.Code
	if code == "" {
		code = loadDiagnosticCode(failure.Cause)
	}
	field := failure.Field
	if field == "" {
		field = "project"
	}
	path := ".protobot/project.yaml"
	if failure.ExposePath {
		if safe := safeLoadPath(failure.Path); safe != "" {
			path = safe
		}
	}
	return diagnostic(code, path, "", field, failure.Error(), "Fix the reported project data through ears-manager without modifying it through another route.")
}

func safeLoadPath(path string) string {
	canonical, err := canonicalProjectPath(path)
	if err != nil {
		return ""
	}
	return canonical
}

func loadDiagnosticCode(cause loadCause) string {
	switch cause {
	case loadCauseYAML:
		return "storage.decode_failed"
	case loadCauseUnexpectedFile:
		return "storage.unexpected_file"
	case loadCauseFilenameMismatch:
		return "record.filename_mismatch"
	case loadCauseStorePath:
		return "project.invalid_path"
	case loadCauseSchema:
		return "schema.unsupported_version"
	default:
		return "storage.read_failed"
	}
}

func loadDocuments[T any](root string, rootHandle *os.Root, relativeDirectory string, kind records.StoreKind, decode func([]byte) (T, map[string]bool, error)) ([]Document[T], []loadError) {
	canonical, err := canonicalStorePath(relativeDirectory)
	if err != nil {
		return nil, []loadError{{Path: relativeDirectory, Field: storeField(kind), Cause: loadCauseStorePath, Err: err}}
	}
	if _, err := storage.ValidatePathWithin(root, filepath.FromSlash(canonical)); err != nil {
		return nil, []loadError{{Path: canonical, Field: storeField(kind), Cause: loadCauseStorePath, Err: err}}
	}
	storeInfo, err := rootHandle.Lstat(filepath.FromSlash(canonical))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []Document[T]{}, nil
		}
		return nil, []loadError{{Path: canonical, Field: storeField(kind), Cause: loadCauseFilesystem, Err: err}}
	}
	if storeInfo.Mode()&os.ModeSymlink != 0 || !storeInfo.IsDir() {
		return nil, []loadError{{Path: canonical, Field: storeField(kind), Cause: loadCauseStorePath, Err: fmt.Errorf("record store path must be a directory")}}
	}
	directory, err := rootHandle.Open(filepath.FromSlash(canonical))
	if err != nil {
		return nil, []loadError{{Path: canonical, Field: storeField(kind), Cause: loadCauseFilesystem, Err: err}}
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, []loadError{{Path: canonical, Field: storeField(kind), Cause: loadCauseFilesystem, Err: err}}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	result := make([]Document[T], 0, len(entries))
	var failures []loadError
	for _, entry := range entries {
		document, include, failure := loadDocumentEntry(rootHandle, canonical, entry.Name(), kind, decode)
		if failure != nil {
			failures = append(failures, *failure)
			continue
		}
		if include {
			result = append(result, document)
		}
	}
	return result, failures
}

func loadDocumentEntry[T any](rootHandle *os.Root, relativeDirectory, name string, kind records.StoreKind, decode func([]byte) (T, map[string]bool, error)) (Document[T], bool, *loadError) {
	if strings.HasPrefix(name, ".") {
		return Document[T]{}, false, nil
	}
	relativePath := filepath.ToSlash(filepath.Join(relativeDirectory, name))
	entryPath := filepath.FromSlash(relativePath)
	info, err := rootHandle.Lstat(entryPath)
	if err != nil {
		return Document[T]{}, false, &loadError{Path: relativePath, Field: storeField(kind), Cause: loadCauseFilesystem, ExposePath: true, Err: err}
	}
	if info.IsDir() {
		return Document[T]{}, false, &loadError{Path: relativePath, Field: storeField(kind), Cause: loadCauseUnexpectedFile, ExposePath: true, Err: fmt.Errorf("entry is a directory")}
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Document[T]{}, false, &loadError{Path: relativePath, Field: storeField(kind), Cause: loadCauseUnexpectedFile, ExposePath: true, Err: fmt.Errorf("entry is not a regular file")}
	}
	if filepath.Ext(name) != ".yaml" {
		return Document[T]{}, false, &loadError{Path: relativePath, Field: storeField(kind), Cause: loadCauseUnexpectedFile, ExposePath: true, Err: fmt.Errorf("entry is not a YAML record")}
	}
	data, err := rootHandle.ReadFile(entryPath)
	if err != nil {
		return Document[T]{}, false, &loadError{Path: relativePath, Field: storeField(kind), Cause: loadCauseFilesystem, ExposePath: true, Err: err}
	}
	value, fields, err := decode(data)
	if err != nil {
		return Document[T]{}, false, &loadError{Path: relativePath, Field: storeField(kind), Cause: loadCauseYAML, ExposePath: true, Err: err}
	}
	id := documentID(value)
	if expected, mapErr := records.FilenameFor(kind, id); mapErr == nil && expected != name {
		return Document[T]{}, false, &loadError{Path: relativePath, Field: storeField(kind), Cause: loadCauseFilenameMismatch, ExposePath: true, Err: fmt.Errorf("record ID does not match filename")}
	}
	return Document[T]{Path: relativePath, Value: value, Fields: fields}, true, nil
}

func storeField(kind records.StoreKind) string {
	switch kind {
	case records.RequirementStore:
		return "stores.requirements"
	case records.InterfaceStore:
		return "stores.interfaces"
	case records.ChangeSetStore:
		return "stores.change_sets"
	default:
		return "stores"
	}
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
