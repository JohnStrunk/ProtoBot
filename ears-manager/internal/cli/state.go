package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/redhat-et/protobot/ears-manager/internal/project"
	"github.com/redhat-et/protobot/ears-manager/internal/records"
	"github.com/redhat-et/protobot/ears-manager/internal/specvalidation"
	"github.com/redhat-et/protobot/ears-manager/internal/storage"
)

type projectState struct {
	root        string
	snapshot    specvalidation.Snapshot
	observed    map[string]fileExpectation
	head        string
	diagnostics []specvalidation.Diagnostic
}

type fileWrite struct {
	path string
	data []byte
}

type fileExpectation struct {
	present bool
	data    []byte
}

func resolveRoot() (string, *commandFailure) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", projectFailure("project.not_git_root", "Unable to determine the current working directory.")
	}
	root, err := project.GitRoot(cwd)
	if err != nil {
		return "", projectFailure("project.not_git_root", "The current directory is not inside a Git working tree.")
	}
	if root == "" {
		return "", projectFailure("project.not_git_root", "Git did not return a working-tree root.")
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", projectFailure("project.not_git_root", "The Git working-tree root could not be resolved.")
	}
	return filepath.Clean(root), nil
}

func loadState() (projectState, *commandFailure) {
	root, failure := resolveRoot()
	if failure != nil {
		return projectState{}, failure
	}
	if _, err := os.Stat(filepath.Join(root, ".protobot", "project.yaml")); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return projectState{}, projectFailure("project.not_initialized", "No .protobot/project.yaml was found in the Git working tree.")
		}
		return projectState{}, ioFailure("project.configuration_unreadable", "The project configuration could not be inspected.")
	}
	snapshot, err := specvalidation.Load(root)
	if err == nil {
		snapshot.Context = specvalidation.ValidationContext{}
		head, failure := currentCommit(root)
		if failure != nil {
			return projectState{}, failure
		}
		return projectState{root: root, snapshot: snapshot, observed: observeSnapshot(root, snapshot), head: head}, nil
	}
	result := specvalidation.ValidateProjectWithContext(root, specvalidation.ValidationContext{})
	if !result.Valid || len(result.Diagnostics) > 0 {
		return projectState{}, failureFromValidation(result, false)
	}
	return projectState{}, projectFailure("project.load_failed", "The project specification could not be loaded.")
}

func applyStateTransaction(state projectState, writes []fileWrite, postValidate func() *commandFailure) (Mutation, *commandFailure) {
	current, failure := currentCommit(state.root)
	if failure != nil {
		return Mutation{}, failure
	}
	if !strings.EqualFold(current, state.head) {
		return Mutation{}, conflictFailure("change_set.base_mismatch", "The repository advanced while the command was preparing its write.", nil)
	}
	expected := cloneExpectations(state.observed)
	for _, write := range writes {
		path := filepath.ToSlash(write.path)
		if _, exists := expected[path]; !exists {
			expected[path] = fileExpectation{}
		}
	}
	return applyTransaction(state.root, writes, expected, postValidate)
}

func loadReadState() (projectState, *commandFailure) {
	state, failure := loadState()
	if failure != nil {
		return projectState{}, failure
	}
	if failure := validateCandidate(state.snapshot, true); failure != nil {
		return projectState{}, failure
	}
	return state, nil
}

func checkState() (projectState, *commandFailure) {
	root, failure := resolveRoot()
	if failure != nil {
		return projectState{}, failure
	}
	projectPath := filepath.Join(root, ".protobot", "project.yaml")
	if _, err := os.Stat(projectPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return projectState{}, projectFailure("project.not_initialized", "No .protobot/project.yaml was found in the Git working tree.")
		}
		return projectState{}, ioFailure("project.configuration_unreadable", "The project configuration could not be inspected.")
	}
	context := specvalidation.ValidationContext{}
	result := specvalidation.ValidateProjectWithContext(root, context)
	if !result.Valid {
		return projectState{root: root}, failureFromValidation(result, false)
	}
	snapshot, err := specvalidation.LoadWithContext(root, context)
	if err != nil {
		return projectState{root: root}, ioFailure("storage.read_failed", "The validated project could not be reloaded.")
	}
	head, failure := currentCommit(root)
	if failure != nil {
		return projectState{root: root}, failure
	}
	return projectState{root: root, snapshot: snapshot, observed: observeSnapshot(root, snapshot), head: head, diagnostics: result.Diagnostics}, nil
}

func cloneExpectations(value map[string]fileExpectation) map[string]fileExpectation {
	result := make(map[string]fileExpectation, len(value))
	for path, expectation := range value {
		result[filepath.ToSlash(path)] = fileExpectation{
			present: expectation.present,
			data:    append([]byte(nil), expectation.data...),
		}
	}
	return result
}

func observeSnapshot(root string, snapshot specvalidation.Snapshot) map[string]fileExpectation {
	paths := []string{configPath(snapshot)}
	for _, document := range snapshot.Requirements {
		paths = append(paths, document.Path)
	}
	for _, document := range snapshot.Interfaces {
		paths = append(paths, document.Path)
	}
	for _, document := range snapshot.ChangeSets {
		paths = append(paths, document.Path)
	}
	for _, artifact := range snapshot.Config.Artifacts {
		paths = append(paths, artifact.Path)
	}
	observed := make(map[string]fileExpectation, len(paths))
	for _, path := range paths {
		if _, exists := observed[path]; exists {
			continue
		}
		observed[path] = readExpectation(root, path)
	}
	return observed
}

func readExpectation(root, relative string) fileExpectation {
	if _, err := storage.ValidatePathWithinNoSymlinks(root, filepath.FromSlash(relative)); err != nil {
		return fileExpectation{}
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return fileExpectation{}
	}
	defer func() { _ = rootHandle.Close() }()
	file, err := rootHandle.OpenFile(filepath.FromSlash(relative), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fileExpectation{}
	}
	defer func() { _ = file.Close() }()
	data, err := readOpenRegularFile(file)
	if err != nil {
		return fileExpectation{}
	}
	return fileExpectation{present: true, data: data}
}

func failureFromValidation(result specvalidation.Result, allowDraft bool) *commandFailure {
	diagnostics := make([]specvalidation.Diagnostic, 0, len(result.Diagnostics))
	for _, diagnostic := range result.Diagnostics {
		if allowDraft && draftIncompleteDiagnostic(diagnostic) {
			continue
		}
		diagnostics = append(diagnostics, diagnostic)
	}
	return failureFromDiagnostics(diagnostics)
}

func failureFromDiagnostics(diagnostics []specvalidation.Diagnostic) *commandFailure {
	if len(diagnostics) == 0 {
		return nil
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == "storage.decode_failed" && strings.Contains(diagnostic.Message, "filesystem or project data access failed") {
			return ioFailure("storage.read_failed", "The project specification could not be read.")
		}
	}
	if hasDiagnosticPrefix(diagnostics, "schema.") || hasDiagnosticPrefix(diagnostics, "project.") {
		return &commandFailure{
			Code:        "project.invalid_configuration",
			Message:     "The project configuration is invalid.",
			ExitCode:    3,
			Diagnostics: diagnostics,
			Mutation:    "none",
			Retry:       "select-or-upgrade-project",
		}
	}
	hasDraftDiagnostic := false
	for _, diagnostic := range diagnostics {
		if draftOnlyDiagnostic(diagnostic) {
			hasDraftDiagnostic = true
			continue
		}
		return validationFailure("validation.failed", "The specification is not valid.", diagnostics)
	}
	if hasDraftDiagnostic {
		return conflictFailure("change_set.assessment_incomplete", "The change-set impact assessment is incomplete or stale.", diagnostics)
	}
	return validationFailure("validation.failed", "The specification is not valid.", diagnostics)
}

func draftOnlyDiagnostic(diagnostic specvalidation.Diagnostic) bool {
	return diagnostic.Code == "change_set.incomplete_impact" ||
		diagnostic.Code == "change_set.stale_impact" ||
		(diagnostic.Code == "change_set.missing_field" && diagnostic.Field == "impact_assessment")
}

func draftIncompleteDiagnostic(diagnostic specvalidation.Diagnostic) bool {
	return diagnostic.Code == "change_set.incomplete_impact" ||
		diagnostic.Code == "change_set.stale_impact" ||
		(diagnostic.Code == "change_set.missing_field" && diagnostic.Field == "impact_assessment")
}

func hasDiagnosticPrefix(diagnostics []specvalidation.Diagnostic, prefix string) bool {
	for _, diagnostic := range diagnostics {
		if strings.HasPrefix(diagnostic.Code, prefix) {
			return true
		}
	}
	return false
}

func validateCandidate(snapshot specvalidation.Snapshot, allowDraft bool) *commandFailure {
	result := specvalidation.Validate(snapshot)
	return failureFromValidation(result, allowDraft)
}

func validateCandidateForChangeSet(snapshot specvalidation.Snapshot, changeSetID string, allowDraft bool) *commandFailure {
	snapshot.Context = specvalidation.ValidationContext{ProposedChangeSets: map[string]bool{changeSetID: true}}
	return validateCandidate(snapshot, allowDraft)
}

func validateScopedCheck(snapshot specvalidation.Snapshot, targetIndex int) *commandFailure {
	scopedContext := specvalidation.ValidationContext{}
	fullSnapshot := cloneSnapshot(snapshot)
	fullSnapshot.Context = scopedContext
	full := specvalidation.Validate(fullSnapshot)
	staged := cloneSnapshot(snapshot)
	target := staged.ChangeSets[targetIndex]
	staged.ChangeSets = []specvalidation.Document[records.ChangeSet]{target}
	staged.Context = scopedContext
	targetResult := specvalidation.Validate(staged)
	diagnostics := make([]specvalidation.Diagnostic, 0, len(full.Diagnostics)+len(targetResult.Diagnostics))
	for _, diagnostic := range full.Diagnostics {
		if !isImpactDiagnostic(diagnostic) {
			diagnostics = append(diagnostics, diagnostic)
		}
	}
	for _, diagnostic := range targetResult.Diagnostics {
		if isImpactDiagnostic(diagnostic) {
			diagnostics = append(diagnostics, diagnostic)
		}
	}
	sort.SliceStable(diagnostics, func(i, j int) bool {
		left, right := diagnostics[i], diagnostics[j]
		for _, pair := range [][2]string{
			{left.Path, right.Path}, {left.Code, right.Code}, {left.RecordID, right.RecordID},
			{left.Field, right.Field}, {left.Severity, right.Severity},
			{left.Message, right.Message}, {left.Hint, right.Hint},
		} {
			if pair[0] != pair[1] {
				return pair[0] < pair[1]
			}
		}
		return false
	})
	unique := diagnostics[:0]
	seen := make(map[specvalidation.Diagnostic]bool, len(diagnostics))
	for _, diagnostic := range diagnostics {
		if seen[diagnostic] {
			continue
		}
		seen[diagnostic] = true
		unique = append(unique, diagnostic)
	}
	return failureFromDiagnostics(unique)
}

func isImpactDiagnostic(diagnostic specvalidation.Diagnostic) bool {
	if !strings.HasPrefix(diagnostic.Code, "change_set.") {
		return false
	}
	return strings.Contains(diagnostic.Code, "impact") ||
		(diagnostic.Code == "change_set.missing_field" && diagnostic.Field == "impact_assessment")
}

func cloneSnapshot(snapshot specvalidation.Snapshot) specvalidation.Snapshot {
	clone := snapshot
	clone.Config = cloneProjectConfig(snapshot.Config)
	clone.ConfigFields = cloneFields(snapshot.ConfigFields)
	if snapshot.ArtifactContents != nil {
		clone.ArtifactContents = make(map[string][]byte, len(snapshot.ArtifactContents))
		for path, data := range snapshot.ArtifactContents {
			clone.ArtifactContents[path] = append([]byte(nil), data...)
		}
	}
	clone.Requirements = make([]specvalidation.Document[records.Requirement], len(snapshot.Requirements))
	for index, document := range snapshot.Requirements {
		clone.Requirements[index] = specvalidation.Document[records.Requirement]{
			Path:   document.Path,
			Value:  cloneRequirement(document.Value),
			Fields: cloneFields(document.Fields),
		}
	}
	clone.Interfaces = make([]specvalidation.Document[records.InterfaceRecord], len(snapshot.Interfaces))
	for index, document := range snapshot.Interfaces {
		clone.Interfaces[index] = specvalidation.Document[records.InterfaceRecord]{
			Path:   document.Path,
			Value:  document.Value,
			Fields: cloneFields(document.Fields),
		}
	}
	clone.ChangeSets = make([]specvalidation.Document[records.ChangeSet], len(snapshot.ChangeSets))
	for index, document := range snapshot.ChangeSets {
		clone.ChangeSets[index] = specvalidation.Document[records.ChangeSet]{
			Path:   document.Path,
			Value:  cloneChangeSet(document.Value),
			Fields: cloneFields(document.Fields),
		}
	}
	return clone
}

func cloneProjectConfig(value records.ProjectConfig) records.ProjectConfig {
	value.Artifacts = append([]records.ArtifactEntry(nil), value.Artifacts...)
	value.Stores = value.Stores.WithDefaults()
	return value
}

func cloneRequirement(value records.Requirement) records.Requirement {
	value.AppliesTo.Interfaces = append([]string(nil), value.AppliesTo.Interfaces...)
	value.AppliesTo.Scopes = append([]string(nil), value.AppliesTo.Scopes...)
	value.Relationships = append([]records.Relationship(nil), value.Relationships...)
	return value
}

func cloneChangeSet(value records.ChangeSet) records.ChangeSet {
	value.Operations = append([]records.RequirementOperation(nil), value.Operations...)
	value.InterfaceOperations = append([]records.InterfaceOperation(nil), value.InterfaceOperations...)
	value.ArtifactOperations = append([]records.ArtifactOperation(nil), value.ArtifactOperations...)
	value.AffectedInterfaces = append([]string(nil), value.AffectedInterfaces...)
	value.AffectedScopes = append([]string(nil), value.AffectedScopes...)
	value.ImpactAssessment = append([]records.ImpactAssessment(nil), value.ImpactAssessment...)
	return value
}

func cloneFields(fields map[string]bool) map[string]bool {
	return maps.Clone(fields)
}

func storePath(config records.ProjectConfig, kind records.StoreKind, id string) (string, error) {
	filename, err := records.FilenameFor(kind, id)
	if err != nil {
		return "", err
	}
	stores := config.Stores.WithDefaults()
	directory := stores.Requirements
	switch kind {
	case records.InterfaceStore:
		directory = stores.Interfaces
	case records.ChangeSetStore:
		directory = stores.ChangeSets
	case records.RequirementStore:
	default:
		return "", fmt.Errorf("unsupported store kind %q", kind)
	}
	return filepath.ToSlash(filepath.Join(directory, filename)), nil
}

func configPath(snapshot specvalidation.Snapshot) string {
	if snapshot.ConfigPath != "" {
		return filepath.ToSlash(snapshot.ConfigPath)
	}
	return ".protobot/project.yaml"
}

func findRequirement(snapshot specvalidation.Snapshot, id string) (int, records.Requirement, bool) {
	for index, document := range snapshot.Requirements {
		if document.Value.ID == id {
			return index, document.Value, true
		}
	}
	return -1, records.Requirement{}, false
}

func findInterface(snapshot specvalidation.Snapshot, id string) (int, records.InterfaceRecord, bool) {
	for index, document := range snapshot.Interfaces {
		if document.Value.ID == id {
			return index, document.Value, true
		}
	}
	return -1, records.InterfaceRecord{}, false
}

func findChangeSet(snapshot specvalidation.Snapshot, id string) (int, records.ChangeSet, bool) {
	for index, document := range snapshot.ChangeSets {
		if document.Value.ID == id {
			return index, document.Value, true
		}
	}
	return -1, records.ChangeSet{}, false
}

func upsertRequirement(snapshot *specvalidation.Snapshot, value records.Requirement) (string, error) {
	path, err := storePath(snapshot.Config, records.RequirementStore, value.ID)
	if err != nil {
		return "", err
	}
	for index := range snapshot.Requirements {
		if snapshot.Requirements[index].Value.ID == value.ID {
			snapshot.Requirements[index].Path = path
			snapshot.Requirements[index].Value = value
			return path, nil
		}
	}
	snapshot.Requirements = append(snapshot.Requirements, specvalidation.Document[records.Requirement]{Path: path, Value: value})
	return path, nil
}

func upsertInterface(snapshot *specvalidation.Snapshot, value records.InterfaceRecord) (string, error) {
	path, err := storePath(snapshot.Config, records.InterfaceStore, value.ID)
	if err != nil {
		return "", err
	}
	for index := range snapshot.Interfaces {
		if snapshot.Interfaces[index].Value.ID == value.ID {
			snapshot.Interfaces[index].Path = path
			snapshot.Interfaces[index].Value = value
			return path, nil
		}
	}
	snapshot.Interfaces = append(snapshot.Interfaces, specvalidation.Document[records.InterfaceRecord]{Path: path, Value: value})
	return path, nil
}

func upsertChangeSet(snapshot *specvalidation.Snapshot, value records.ChangeSet) (string, error) {
	path, err := storePath(snapshot.Config, records.ChangeSetStore, value.ID)
	if err != nil {
		return "", err
	}
	for index := range snapshot.ChangeSets {
		if snapshot.ChangeSets[index].Value.ID == value.ID {
			snapshot.ChangeSets[index].Path = path
			snapshot.ChangeSets[index].Value = value
			return path, nil
		}
	}
	snapshot.ChangeSets = append(snapshot.ChangeSets, specvalidation.Document[records.ChangeSet]{Path: path, Value: value})
	return path, nil
}

func addWrite(writes *[]fileWrite, path string, value any) error {
	data, err := storage.Encode(value)
	if err != nil {
		return err
	}
	*writes = append(*writes, fileWrite{path: path, data: data})
	return nil
}

func addRequirementWrites(writes *[]fileWrite, snapshot *specvalidation.Snapshot, sourcePath string, source records.Requirement, targetIndexes []int) error {
	if err := addWrite(writes, sourcePath, source); err != nil {
		return err
	}
	for _, targetIndex := range targetIndexes {
		if err := addWrite(writes, snapshot.Requirements[targetIndex].Path, snapshot.Requirements[targetIndex].Value); err != nil {
			return err
		}
	}
	return nil
}

func addConfigWrite(root string, snapshot *specvalidation.Snapshot, writes *[]fileWrite, observed map[string]fileExpectation) error {
	overrides := make(map[string][]byte, len(*writes))
	for _, write := range *writes {
		overrides[filepath.ToSlash(write.path)] = write.data
	}
	stores := snapshot.Config.Stores.WithDefaults()
	digests := records.StoreDigests{}
	var err error
	observedPaths := make(map[string]bool, len(observed))
	for path := range observed {
		observedPaths[filepath.ToSlash(path)] = true
	}
	if digests.Requirements, err = specvalidation.CanonicalStoreDigestWithOverridesAndObserved(root, stores.Requirements, overrides, observedPaths); err != nil {
		return err
	}
	if digests.Interfaces, err = specvalidation.CanonicalStoreDigestWithOverridesAndObserved(root, stores.Interfaces, overrides, observedPaths); err != nil {
		return err
	}
	if digests.ChangeSets, err = specvalidation.CanonicalStoreDigestWithOverridesAndObserved(root, stores.ChangeSets, overrides, observedPaths); err != nil {
		return err
	}
	snapshot.Config.StoreDigests = digests
	return addWrite(writes, configPath(*snapshot), snapshot.Config)
}

func configWriteFailure(err error, fallback string) *commandFailure {
	if errors.Is(err, specvalidation.ErrUnobservedStoreEntry) {
		return conflictFailure("change_set.concurrent_update", "The project changed while the command was preparing its write.", nil)
	}
	return internalFailure(fallback)
}

func addRawWrite(writes *[]fileWrite, path string, data []byte) {
	*writes = append(*writes, fileWrite{path: path, data: append([]byte(nil), data...)})
}

func deduplicateWrites(writes []fileWrite) []fileWrite {
	byPath := make(map[string]fileWrite, len(writes))
	for _, write := range writes {
		byPath[filepath.ToSlash(write.path)] = fileWrite{path: filepath.ToSlash(write.path), data: write.data}
	}
	result := make([]fileWrite, 0, len(byPath))
	for _, write := range byPath {
		result = append(result, write)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].path < result[j].path })
	return result
}

type originalFile struct {
	path          string
	data          []byte
	mode          fs.FileMode
	wasPresent    bool
	parentCreated []string
}

var transactionSequence uint64

func applyTransaction(rootPath string, writes []fileWrite, expected map[string]fileExpectation, postValidate func() *commandFailure) (Mutation, *commandFailure) {
	paths := writePaths(writes)
	writes = deduplicateWrites(writes)
	expected = cloneExpectations(expected)
	for _, write := range writes {
		path := filepath.ToSlash(write.path)
		if _, exists := expected[path]; !exists {
			expected[path] = fileExpectation{}
		}
	}
	if len(writes) == 0 {
		return Mutation{}, internalFailure("the mutation did not produce any files")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return Mutation{}, ioFailure("storage.write_failed", "The governed write root could not be opened.")
	}
	defer func() { _ = root.Close() }()
	originals := make([]originalFile, 0, len(writes))
	for _, write := range writes {
		relative := filepath.ToSlash(write.path)
		expectation := expected[relative]
		if _, err := storage.ValidatePathWithinNoSymlinks(rootPath, filepath.FromSlash(relative)); err != nil {
			return Mutation{}, validationFailure("storage.write_not_allowed", "A governed write path is not allowed.", []specvalidation.Diagnostic{{Code: "storage.write_not_allowed", Severity: "error", Path: relative, Message: "The write path is outside the project root or resolves through a symlink.", Hint: "Use a project-relative governed path."}})
		}
		info, lstatErr := root.Lstat(filepath.FromSlash(relative))
		if lstatErr != nil && !errors.Is(lstatErr, fs.ErrNotExist) {
			return Mutation{}, ioFailure("storage.write_failed", "The governed write target could not be inspected.")
		}
		original := originalFile{path: relative}
		if lstatErr == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return Mutation{}, validationFailure("storage.write_not_allowed", "A governed write target must be a regular file.", []specvalidation.Diagnostic{{Code: "storage.write_not_allowed", Severity: "error", Path: relative, Message: "The governed write target is not a regular file.", Hint: "Replace the target with a regular file."}})
			}
		}
		current, err := readRootExpectation(root, filepath.FromSlash(relative))
		if err != nil {
			return Mutation{}, ioFailure("storage.write_failed", "The governed write target could not be inspected.")
		}
		if current.present != expectation.present || (current.present && !bytes.Equal(current.data, expectation.data)) {
			return Mutation{}, conflictFailure("change_set.concurrent_update", "The project changed while the command was preparing its write.", nil)
		}
		if lstatErr == nil {
			original.wasPresent = true
			original.mode = info.Mode()
			original.data = current.data
		}
		parentCreated, err := ensureParent(root, filepath.Dir(filepath.FromSlash(relative)))
		if err != nil {
			return Mutation{}, ioFailure("storage.write_failed", "The governed write directory could not be created.")
		}
		original.parentCreated = parentCreated
		originals = append(originals, original)
	}

	for index, write := range writes {
		relative := filepath.FromSlash(originals[index].path)
		if err := root.MkdirAll(filepath.Dir(relative), 0o755); err != nil {
			if rollbackErr := rollbackFiles(root, originals); rollbackErr != nil {
				return Mutation{}, unknownIOFailure("storage.write_unknown", "A governed write directory failed and its final state could not be established.")
			}
			return Mutation{}, ioFailure("storage.write_failed", "The governed write directory could not be created.")
		}
		if err := replaceFile(root, relative, write.data); err != nil {
			if rollbackErr := rollbackFiles(root, originals[:index+1]); rollbackErr != nil {
				return Mutation{}, unknownIOFailure("storage.write_unknown", "A governed write failed and its final state could not be established.")
			}
			return Mutation{}, ioFailure("storage.write_failed", "A governed write failed; no mutation was applied.")
		}
	}

	if postValidate != nil {
		if failure := postValidate(); failure != nil {
			if rollbackErr := rollbackFiles(root, originals); rollbackErr != nil {
				return Mutation{}, unknownIOFailure("storage.write_unknown", "Post-write validation failed and rollback could not be established.")
			}
			return Mutation{}, failure
		}
	}
	return Mutation{Applied: true, Paths: paths}, nil
}

func writePaths(writes []fileWrite) []string {
	paths := make([]string, 0, len(writes))
	seen := make(map[string]bool, len(writes))
	for _, write := range writes {
		path := filepath.ToSlash(write.path)
		if seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	return paths
}

func readRootExpectation(root *os.Root, relative string) (fileExpectation, error) {
	_, err := root.Lstat(relative)
	if errors.Is(err, fs.ErrNotExist) {
		return fileExpectation{}, nil
	}
	if err != nil {
		return fileExpectation{}, err
	}
	file, err := root.OpenFile(relative, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fileExpectation{}, err
	}
	defer func() { _ = file.Close() }()
	data, err := readOpenRegularFile(file)
	if err != nil {
		return fileExpectation{}, err
	}
	return fileExpectation{present: true, data: data}, nil
}

var errNotRegularFile = errors.New("file is not regular")

func readOpenRegularFile(file *os.File) ([]byte, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errNotRegularFile
	}
	return io.ReadAll(file)
}

func openAndReadRegularFile(open func() (*os.File, error)) ([]byte, error) {
	file, err := open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return readOpenRegularFile(file)
}

func ensureParent(root *os.Root, directory string) ([]string, error) {
	missing := []string{}
	current := filepath.Clean(directory)
	for {
		info, err := root.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return nil, fmt.Errorf("parent path is not a regular directory")
			}
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		missing = append(missing, current)
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return missing, nil
}

func replaceFile(root *os.Root, path string, data []byte) error {
	directory := filepath.Dir(path)
	temporaryName := filepath.Join(directory, fmt.Sprintf(".ears-manager-txn-%d-%d", os.Getpid(), atomic.AddUint64(&transactionSequence, 1)))
	temporary, err := root.OpenFile(temporaryName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(temporaryName) }()
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return root.Rename(temporaryName, path)
}

func rollbackFiles(root *os.Root, files []originalFile) error {
	var rollbackErr error
	for index := len(files) - 1; index >= 0; index-- {
		file := files[index]
		var err error
		if file.wasPresent {
			err = replaceFile(root, filepath.FromSlash(file.path), file.data)
			if err == nil {
				err = root.Chmod(filepath.FromSlash(file.path), file.mode.Perm())
			}
		} else {
			err = root.Remove(filepath.FromSlash(file.path))
			if errors.Is(err, fs.ErrNotExist) {
				err = nil
			}
		}
		if rollbackErr == nil && err != nil {
			rollbackErr = err
		}
	}
	for index := len(files) - 1; index >= 0; index-- {
		for _, directory := range files[index].parentCreated {
			if err := root.Remove(directory); rollbackErr == nil && err != nil && !errors.Is(err, fs.ErrNotExist) {
				rollbackErr = err
			}
		}
	}
	return rollbackErr
}

func persistedValidation(root string, allowDraft bool, changeSetID string) *commandFailure {
	_, err := specvalidation.Load(root)
	if err != nil {
		return ioFailure("storage.post_write_failed", "The written project could not be reloaded for validation.")
	}
	snapshot, err := specvalidation.LoadWithContext(root, specvalidation.ValidationContext{ProposedChangeSets: map[string]bool{changeSetID: true}})
	if err != nil {
		return ioFailure("storage.post_write_failed", "The written project could not be reloaded for validation.")
	}
	return validateCandidate(snapshot, allowDraft)
}

func pathForArtifact(root, relative string) (string, *commandFailure) {
	abs, err := storage.ValidatePathWithinNoSymlinks(root, filepath.FromSlash(relative))
	if err != nil {
		return "", validationFailure("artifact.invalid_path", "The artifact path is not inside the project root.", []specvalidation.Diagnostic{{Code: "artifact.invalid_path", Severity: "error", Path: relative, Field: "path", Message: "Artifact paths must remain inside the project root.", Hint: "Use a project-relative regular-file path."}})
	}
	return abs, nil
}

func readRegularFile(root, relative string) ([]byte, *commandFailure) {
	_, failure := pathForArtifact(root, relative)
	if failure != nil {
		return nil, failure
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return nil, ioFailure("artifact.read_failed", "The requested artifact could not be inspected.")
	}
	defer func() { _ = rootHandle.Close() }()
	data, err := openAndReadRegularFile(func() (*os.File, error) {
		return rootHandle.OpenFile(filepath.FromSlash(relative), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, validationFailure("artifact.read_failed", "The requested artifact was not found.", nil)
	}
	if errors.Is(err, errNotRegularFile) {
		return nil, validationFailure("artifact.read_failed", "The requested artifact is not a readable regular file.", nil)
	}
	if err != nil {
		return nil, ioFailure("artifact.read_failed", "The requested artifact could not be read.")
	}
	return data, nil
}
