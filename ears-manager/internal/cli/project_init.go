package cli

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/redhat-et/protobot/ears-manager/internal/records"
	"github.com/redhat-et/protobot/ears-manager/internal/specvalidation"
	"github.com/redhat-et/protobot/ears-manager/internal/storage"
)

const (
	controlNamespace         = ".protobot"
	projectConfigRelative    = ".protobot/project.yaml"
	defaultInitBranch        = "main"
	defaultInitBranchPrefix  = "cs/"
	defaultVisionPath        = "docs/vision.md"
	defaultArchitecturePath  = "docs/architecture.md"
	projectInitArtifactOwner = "user"
)

// projectInitAfterWrite lets tests inject a failure after the replacement set
// is written and before it is validated, to exercise rollback and retry.
var projectInitAfterWrite func() error

type projectInitData struct {
	Project         projectInitView `json:"project"`
	RegisteredPaths []string        `json:"registered_paths"`
}

type projectInitView struct {
	ID              string                    `json:"id"`
	Name            string                    `json:"name"`
	CanonicalRemote string                    `json:"canonical_remote"`
	DefaultBranch   string                    `json:"default_branch"`
	ReviewMode      string                    `json:"review_mode"`
	BranchPrefix    string                    `json:"branch_prefix"`
	SchemaVersions  projectInitSchemaVersions `json:"schema_versions"`
	Stores          projectInitStores         `json:"stores"`
	StoreDigests    projectInitStores         `json:"store_digests"`
}

type projectInitSchemaVersions struct {
	Specification int `json:"specification"`
	Project       int `json:"project"`
}

type projectInitStores struct {
	Requirements string `json:"requirements"`
	Interfaces   string `json:"interfaces"`
	ChangeSets   string `json:"change_sets"`
}

type initArtifact struct {
	id     string
	kind   records.ArtifactKind
	option string
	path   string
}

func runProjectInit(args []string) (any, Mutation, *commandFailure) {
	parsed, failure := parseOptions(args, valueOptions(
		"id", "name", "canonical-remote", "review-mode",
		"default-branch", "branch-prefix", "vision", "architecture",
	))
	if failure != nil {
		return nil, Mutation{}, failure
	}
	required := map[string]string{}
	for _, name := range []string{"id", "name", "canonical-remote", "review-mode"} {
		value, failure := requireOption(parsed, name)
		if failure != nil {
			return nil, Mutation{}, failure
		}
		required[name] = value
	}
	defaultBranch := optionOrDefault(parsed, "default-branch", defaultInitBranch)
	branchPrefix := optionOrDefault(parsed, "branch-prefix", defaultInitBranchPrefix)
	selected := []initArtifact{
		{id: "architecture", kind: records.ArtifactArchitecture, option: "architecture", path: optionOrDefault(parsed, "architecture", defaultArchitecturePath)},
		{id: "vision", kind: records.ArtifactVision, option: "vision", path: optionOrDefault(parsed, "vision", defaultVisionPath)},
	}

	root, failure := resolveRoot()
	if failure != nil {
		return nil, Mutation{}, failure
	}
	if failure := requireControlNamespaceAbsent(root); failure != nil {
		return nil, Mutation{}, failure
	}
	if failure := rejectMisplacedProject(root); failure != nil {
		return nil, Mutation{}, failure
	}
	if failure := requireInitCommit(root); failure != nil {
		return nil, Mutation{}, failure
	}

	artifacts, registered, failure := initArtifactEntries(root, selected)
	if failure != nil {
		return nil, Mutation{}, failure
	}
	stores := records.StorePaths{}.WithDefaults()
	digests, failure := initStoreDigests(root, stores)
	if failure != nil {
		return nil, Mutation{}, failure
	}
	config := records.CanonicalProjectConfig(records.ProjectConfig{
		Project: records.ProjectIdentity{ID: required["id"], Name: required["name"]},
		Repository: records.RepositoryConfig{
			CanonicalRemote: required["canonical-remote"],
			DefaultBranch:   defaultBranch,
			ReviewMode:      required["review-mode"],
			BranchPrefix:    branchPrefix,
		},
		SchemaVersions: records.SchemaVersions{
			Project:       records.CurrentProjectSchemaVersion,
			Specification: records.CurrentSpecificationSchemaVersion,
		},
		Stores:       stores,
		StoreDigests: digests,
		Artifacts:    artifacts,
	})
	configData, err := storage.Encode(config)
	if err != nil {
		return nil, Mutation{}, internalFailure("the project configuration could not be serialized")
	}
	projectionPaths := append([]string(nil), registered...)
	for _, store := range []string{stores.Requirements, stores.Interfaces, stores.ChangeSets} {
		projectionPaths = append(projectionPaths, store+"/")
	}
	projectionData, _, err := specvalidation.AddSharedClassifications(nil, projectionPaths)
	if err != nil {
		return nil, Mutation{}, internalFailure("the projection manifest could not be serialized")
	}
	if failure := validateInitCandidate(root, configData, projectionData); failure != nil {
		return nil, Mutation{}, failure
	}
	if _, failure := resolveDefaultBranchRefs(root, config.Repository); failure != nil {
		return nil, Mutation{}, initStateFailure("repository.default_branch", "The default branch has no commit to base the project on.", "Create the default branch with at least one commit before initializing.")
	}

	if failure := requireControlNamespaceAbsent(root); failure != nil {
		return nil, Mutation{}, failure
	}
	writes := []fileWrite{}
	addRawWrite(&writes, projectConfigRelative, configData)
	addRawWrite(&writes, specvalidation.ProjectionPath, projectionData)
	expected := map[string]fileExpectation{
		projectConfigRelative:         {},
		specvalidation.ProjectionPath: {},
	}
	mutation, failure := applyTransaction(root, writes, expected, func() *commandFailure {
		if projectInitAfterWrite != nil {
			if err := projectInitAfterWrite(); err != nil {
				return ioFailure("storage.post_write_failed", "The initialized project could not be validated; no mutation was applied.")
			}
		}
		return persistedInitValidation(root)
	})
	if failure != nil {
		return nil, Mutation{}, failure
	}
	return projectInitData{Project: toProjectInitView(config), RegisteredPaths: registered}, mutation, nil
}

func optionOrDefault(parsed options, name, fallback string) string {
	if parsed.has(name) {
		return parsed.one(name)
	}
	return fallback
}

// rejectMisplacedProject refuses initialization when a project configuration
// exists in a directory between the current directory and the working-tree
// root. It walks up only, the rule project resolution and the Source Control
// Manager use, so a nested repository, worktree, or fixture elsewhere in the
// tree never blocks initialization.
func rejectMisplacedProject(root string) *commandFailure {
	cwd, err := os.Getwd()
	if err != nil {
		return ioFailure("storage.read_failed", "The current directory could not be inspected for a misplaced project configuration.")
	}
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return ioFailure("storage.read_failed", "The current directory could not be resolved while checking for a misplaced project configuration.")
	}
	current := filepath.Clean(resolved)
	root = filepath.Clean(root)
	for current != root && strings.HasPrefix(current, root+string(filepath.Separator)) {
		candidate := filepath.Join(current, controlNamespace, "project.yaml")
		if _, statErr := os.Lstat(candidate); statErr == nil {
			relative, relErr := filepath.Rel(root, candidate)
			if relErr != nil {
				return ioFailure("storage.read_failed", "The misplaced project configuration path could not be resolved.")
			}
			return misplacedProjectFailure(filepath.ToSlash(relative))
		} else if !errors.Is(statErr, fs.ErrNotExist) && !errors.Is(statErr, syscall.ENOTDIR) {
			return ioFailure("storage.read_failed", "A project configuration in the current directory ancestry could not be inspected.")
		}
		current = filepath.Dir(current)
	}
	return nil
}

func misplacedProjectFailure(path string) *commandFailure {
	failure := projectFailure("project.not_git_root", "A .protobot/project.yaml exists between the current directory and the Git working-tree root.")
	diagnostic := specvalidation.Diagnostic{
		Code:     "project.not_git_root",
		Severity: "error",
		Message:  "The current directory is inside a nested project configuration; ears-manager never relocates it.",
		Hint:     "Run project initialization from the Git working-tree root, or move the nested project configuration through a reviewed change.",
	}
	if canonical, err := specvalidation.CanonicalProjectPath(path); err == nil {
		diagnostic.Path = canonical
	}
	failure.Diagnostics = []specvalidation.Diagnostic{diagnostic}
	return failure
}

func requireControlNamespaceAbsent(root string) *commandFailure {
	info, err := os.Lstat(filepath.Join(root, controlNamespace))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return ioFailure("project.configuration_unreadable", "The project control namespace could not be inspected.")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return alreadyInitializedFailure("Something other than a directory is named .protobot at the working-tree root.")
	}
	return alreadyInitializedFailure("The working-tree root already has a .protobot control namespace.")
}

func alreadyInitializedFailure(detail string) *commandFailure {
	return conflictFailure("project.already_initialized", "The project is already initialized.", []specvalidation.Diagnostic{{
		Code:     "project.already_initialized",
		Severity: "error",
		Path:     controlNamespace,
		Message:  detail,
		Hint:     "Use the existing project, or remove the control namespace through a reviewed change before initializing.",
	}})
}

// requireInitCommit refuses a repository without a commit. Initialization
// records no base commit; change-set create records it later.
func requireInitCommit(root string) *commandFailure {
	command := exec.Command("git", "-C", root, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err := command.Run(); err != nil {
		return initStateFailure("", "The repository has no commits.", "Create an initial commit on the default branch before initializing; an empty repository is initialized outside ears-manager.")
	}
	return nil
}

func initStateFailure(field, message, hint string) *commandFailure {
	failure := projectFailure("project.invalid_configuration", "The repository cannot be initialized.")
	failure.Diagnostics = []specvalidation.Diagnostic{{
		Code:     "project.invalid_configuration",
		Severity: "error",
		Field:    field,
		Message:  message,
		Hint:     hint,
	}}
	return failure
}

func initArtifactEntries(root string, selected []initArtifact) ([]records.ArtifactEntry, []string, *commandFailure) {
	entries := make([]records.ArtifactEntry, 0, len(selected))
	registered := make([]string, 0, len(selected))
	seen := map[string]string{}
	for _, artifact := range selected {
		canonical, err := specvalidation.CanonicalProjectPath(artifact.path)
		if err != nil || specvalidation.IsReservedProjectPath(canonical) {
			return nil, nil, initPathFailure(artifact.option, "", "The selected path must be a slash-separated project-relative path outside control, workflow, and agent-harness paths.")
		}
		if previous, exists := seen[strings.ToLower(canonical)]; exists {
			return nil, nil, initPathFailure(artifact.option, canonical, "The selected path is already selected for --"+previous+".")
		}
		seen[strings.ToLower(canonical)] = artifact.option
		if _, err := storage.ValidatePathWithinNoSymlinks(root, filepath.FromSlash(canonical)); err != nil {
			return nil, nil, initPathFailure(artifact.option, "", "The selected path must resolve inside the project root without symbolic links.")
		}
		data, status := readInitArtifact(root, canonical)
		switch status {
		case "":
		case "not-found":
			return nil, nil, initPathFailure(artifact.option, canonical, "The selected path does not exist; initialization registers existing files and writes no content.")
		case "invalid":
			return nil, nil, initPathFailure(artifact.option, canonical, "The selected path must name a regular file.")
		default:
			return nil, nil, ioFailure("artifact.read_failed", "A selected artifact could not be read.")
		}
		digest, err := specvalidation.CanonicalTextDigest(data)
		if err != nil {
			return nil, nil, validationFailure("project.invalid_configuration", "The project configuration is invalid.", []specvalidation.Diagnostic{{
				Code:     "artifact.invalid_content",
				Severity: "error",
				Path:     canonical,
				Field:    artifact.option,
				Message:  err.Error(),
				Hint:     "Store the artifact as valid UTF-8 text without a BOM before initializing.",
			}})
		}
		if failure := requireCommittedInitArtifact(root, artifact.option, canonical); failure != nil {
			return nil, nil, failure
		}
		entries = append(entries, records.ArtifactEntry{
			ID:     artifact.id,
			Kind:   artifact.kind,
			Path:   canonical,
			Digest: digest,
			Owner:  projectInitArtifactOwner,
		})
		registered = append(registered, canonical)
	}
	sort.Strings(registered)
	return entries, registered, nil
}

// requireCommittedInitArtifact refuses a selected path that is not a file in
// the HEAD commit, or whose working-tree content differs from HEAD. The
// initialization commit holds only the control namespace, so a path that is
// untracked, only staged, or modified would be missing or stale in that
// commit, and ears-manager check would fail in CI.
func requireCommittedInitArtifact(root, option, path string) *commandFailure {
	notCommitted := func(message string) *commandFailure {
		return validationFailure("project.invalid_path", "A selected project path is not allowed.", []specvalidation.Diagnostic{{
			Code:     "project.invalid_path",
			Severity: "error",
			Path:     path,
			Field:    option,
			Message:  message,
			Hint:     "Commit the file on the default branch, then retry initialization.",
		}})
	}
	kind, err := exec.Command("git", "--no-replace-objects", "-C", root, "cat-file", "-t", "HEAD:"+path).Output()
	if err != nil || strings.TrimSpace(string(kind)) != "blob" {
		return notCommitted("The selected path is not committed at HEAD; initialization registers only committed files.")
	}
	diff := exec.Command("git", "--no-replace-objects", "-C", root, "--literal-pathspecs", "diff", "--quiet", "--no-ext-diff", "--no-textconv", "HEAD", "--", path)
	if err := diff.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return notCommitted("The selected path has uncommitted changes; initialization registers the committed content only.")
		}
		return ioFailure("storage.read_failed", "The Git state of a selected path could not be read.")
	}
	return nil
}

func initPathFailure(option, path, message string) *commandFailure {
	return validationFailure("project.invalid_path", "A selected project path is not allowed.", []specvalidation.Diagnostic{{
		Code:     "project.invalid_path",
		Severity: "error",
		Path:     path,
		Field:    option,
		Message:  message,
		Hint:     "Select an existing regular file inside the project root.",
	}})
}

func readInitArtifact(root, relative string) ([]byte, string) {
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return nil, "unreadable"
	}
	defer func() { _ = rootHandle.Close() }()
	info, err := rootHandle.Lstat(filepath.FromSlash(relative))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "not-found"
	}
	if err != nil {
		return nil, "unreadable"
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, "invalid"
	}
	data, err := openAndReadRegularFile(func() (*os.File, error) {
		return rootHandle.OpenFile(filepath.FromSlash(relative), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	})
	if err != nil {
		return nil, "unreadable"
	}
	return data, ""
}

func initStoreDigests(root string, stores records.StorePaths) (records.StoreDigests, *commandFailure) {
	digests := records.StoreDigests{}
	var err error
	if digests.Requirements, err = specvalidation.CanonicalStoreDigestWithOverrides(root, stores.Requirements, nil); err != nil {
		return digests, ioFailure("storage.read_failed", "The requirement store digest could not be computed.")
	}
	if digests.Interfaces, err = specvalidation.CanonicalStoreDigestWithOverrides(root, stores.Interfaces, nil); err != nil {
		return digests, ioFailure("storage.read_failed", "The interface store digest could not be computed.")
	}
	if digests.ChangeSets, err = specvalidation.CanonicalStoreDigestWithOverrides(root, stores.ChangeSets, nil); err != nil {
		return digests, ioFailure("storage.read_failed", "The change-set store digest could not be computed.")
	}
	return digests, nil
}

// validateInitCandidate validates the exact replacement set in memory with the
// same rules `check` applies to a persisted project.
func validateInitCandidate(root string, configData, projectionData []byte) *commandFailure {
	var config records.ProjectConfig
	fields, err := storage.DecodeFields(configData, &config)
	if err != nil {
		return internalFailure("the serialized project configuration could not be decoded")
	}
	projection := specvalidation.ParseProjection(projectionData)
	snapshot := specvalidation.Snapshot{
		Root:         root,
		Config:       config,
		ConfigPath:   projectConfigRelative,
		ConfigFields: fields,
		Projection:   &projection,
	}
	result := specvalidation.Validate(snapshot)
	if len(result.Diagnostics) == 0 {
		return nil
	}
	return initValidationFailure(result.Diagnostics)
}

func initValidationFailure(diagnostics []specvalidation.Diagnostic) *commandFailure {
	code := "project.invalid_configuration"
	message := "The project configuration is invalid."
	for _, diagnostic := range diagnostics {
		switch diagnostic.Code {
		case "project.remote_credentials":
			return validationFailure("project.remote_credentials", "The canonical remote must not contain credentials.", diagnostics)
		case "project.invalid_path", "artifact.invalid_path", "artifact.path_not_found", "artifact.duplicate_path":
			code = "project.invalid_path"
			message = "A selected project path is not allowed."
		}
	}
	return validationFailure(code, message, diagnostics)
}

func persistedInitValidation(root string) *commandFailure {
	result := specvalidation.ValidateProjectWithContext(root, specvalidation.ValidationContext{})
	if !result.Valid || len(result.Diagnostics) > 0 {
		return failureFromValidation(result, false)
	}
	return nil
}

func toProjectInitView(config records.ProjectConfig) projectInitView {
	return projectInitView{
		ID:              config.Project.ID,
		Name:            config.Project.Name,
		CanonicalRemote: config.Repository.CanonicalRemote,
		DefaultBranch:   config.Repository.DefaultBranch,
		ReviewMode:      config.Repository.ReviewMode,
		BranchPrefix:    config.Repository.BranchPrefix,
		SchemaVersions: projectInitSchemaVersions{
			Specification: config.SchemaVersions.Specification,
			Project:       config.SchemaVersions.Project,
		},
		Stores: projectInitStores{
			Requirements: config.Stores.Requirements,
			Interfaces:   config.Stores.Interfaces,
			ChangeSets:   config.Stores.ChangeSets,
		},
		StoreDigests: projectInitStores{
			Requirements: config.StoreDigests.Requirements,
			Interfaces:   config.StoreDigests.Interfaces,
			ChangeSets:   config.StoreDigests.ChangeSets,
		},
	}
}
