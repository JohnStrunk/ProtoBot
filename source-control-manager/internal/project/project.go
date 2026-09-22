// Package project resolves the ProtoBot project from the working tree and
// reads the Git-facing fields of .protobot/project.yaml. No request ever
// supplies them.
package project

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/redhat-et/protobot/source-control-manager/internal/gitx"
	"github.com/redhat-et/protobot/source-control-manager/internal/jsonx"
	"github.com/redhat-et/protobot/source-control-manager/internal/refname"
	"github.com/redhat-et/protobot/source-control-manager/internal/remoteurl"
	"github.com/redhat-et/protobot/source-control-manager/internal/result"
)

// ConfigPath is the project file, relative to the working-tree root.
const ConfigPath = ".protobot/project.yaml"

// ProjectionPath is the projection manifest.
const ProjectionPath = ".protobot/projection.yaml"

// ReservedPrefix is the one reserved branch prefix today.
const ReservedPrefix = "wi/"

// Artifact is one entry of the artifact registry.
type Artifact struct {
	ID    string `yaml:"id"`
	Kind  string `yaml:"kind"`
	Path  string `yaml:"path"`
	Owner string `yaml:"owner"`
}

// Stores are the store paths of the project.
type Stores struct {
	Requirements string `yaml:"requirements"`
	Interfaces   string `yaml:"interfaces"`
	ChangeSets   string `yaml:"change_sets"`
}

// Config is the part of project.yaml that the SCM reads.
type Config struct {
	Project struct {
		ID   string `yaml:"id"`
		Name string `yaml:"name"`
	} `yaml:"project"`
	Repository struct {
		CanonicalRemote string `yaml:"canonical_remote"`
		DefaultBranch   string `yaml:"default_branch"`
		ReviewMode      string `yaml:"review_mode"`
		BranchPrefix    string `yaml:"branch_prefix"`
	} `yaml:"repository"`
	Stores    Stores     `yaml:"stores"`
	Artifacts []Artifact `yaml:"artifacts"`
}

// Project is a resolved working tree.
type Project struct {
	// Root is the working-tree root, with symbolic links resolved.
	Root string
	// Initialized reports that .protobot/project.yaml exists at the root.
	Initialized bool
	// Config is the project file; nil when not initialized.
	Config *Config
}

// ID returns the project ID, or "" before initialization.
func (p *Project) ID() string {
	if p.Config == nil {
		return ""
	}
	return p.Config.Project.ID
}

// Resolve finds the project from dir by the ordered rule of the SCM's
// repo_state step 1: walk up to the first directory that holds
// .protobot/project.yaml; it must be the working-tree root.
func Resolve(dir string) (*Project, *result.Failure) {
	runner, err := gitx.New(dir)
	if err != nil {
		return nil, result.Fail(result.Internal, "The SCM cannot run git.", jsonx.F("reason", err.Error()))
	}
	defer runner.Close()
	top, err := runner.Read("rev-parse", "--show-toplevel")
	if err != nil {
		return nil, result.Fail(result.ProjectNotFound, "The current directory is not in a Git working tree.")
	}
	root, err := filepath.EvalSymlinks(top)
	if err != nil {
		return nil, result.Fail(result.ProjectNotFound, "The Git working-tree root cannot be resolved.")
	}
	start, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, result.Fail(result.ProjectNotFound, "The current directory cannot be resolved.")
	}
	for current := start; ; {
		candidate := filepath.Join(current, filepath.FromSlash(ConfigPath))
		if _, statErr := os.Lstat(candidate); statErr == nil {
			if current != root {
				found, relErr := filepath.Rel(root, candidate)
				if relErr != nil {
					found = ConfigPath
				}
				return nil, result.Fail(result.ProjectNotAtRoot,
					"The project.yaml that the walk up finds is not at the working-tree root.",
					jsonx.F("found", filepath.ToSlash(found)))
			}
			config, failure := load(root)
			if failure != nil {
				return nil, failure
			}
			return &Project{Root: root, Initialized: true, Config: config}, nil
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return nil, result.Fail(result.ProjectUnreadable, "The project file cannot be read.", jsonx.F("path", ConfigPath))
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	if _, err := os.Lstat(filepath.Join(root, ".protobot")); err == nil {
		return nil, result.Fail(result.ProjectUnreadable, ".protobot/ exists without a readable, valid project.yaml.", jsonx.F("path", ".protobot/"))
	}
	return &Project{Root: root}, nil
}

var prefixPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*/$`)

func load(root string) (*Config, *result.Failure) {
	unreadable := func(reason string) *result.Failure {
		return result.Fail(result.ProjectUnreadable, "The project file is not readable or not valid.",
			jsonx.F("path", ConfigPath), jsonx.F("reason", reason))
	}
	file := filepath.Join(root, filepath.FromSlash(ConfigPath))
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() {
		return nil, unreadable("project.yaml is not a regular file")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, unreadable("project.yaml cannot be read")
	}
	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, unreadable("project.yaml is not valid YAML")
	}
	repo := &config.Repository
	switch {
	case strings.TrimSpace(config.Project.ID) == "":
		return nil, unreadable("project.id is missing")
	case repo.CanonicalRemote == "":
		return nil, unreadable("repository.canonical_remote is missing")
	case remoteurl.Parse(repo.CanonicalRemote).HasCredential():
		return nil, unreadable("repository.canonical_remote carries userinfo")
	case repo.DefaultBranch == "" || !refname.ValidBranch(repo.DefaultBranch):
		return nil, unreadable("repository.default_branch is not a valid branch name")
	case !prefixPattern.MatchString(repo.BranchPrefix) || !refname.ValidBranch(repo.BranchPrefix+"x"):
		return nil, unreadable("repository.branch_prefix is not a valid branch prefix")
	case repo.BranchPrefix == ReservedPrefix:
		return nil, unreadable("repository.branch_prefix is reserved")
	case repo.ReviewMode != "single-player" && repo.ReviewMode != "multi-player":
		return nil, unreadable("repository.review_mode is not single-player or multi-player")
	}
	if config.Stores.Requirements == "" {
		config.Stores.Requirements = ".protobot/requirements"
	}
	if config.Stores.Interfaces == "" {
		config.Stores.Interfaces = ".protobot/interfaces"
	}
	if config.Stores.ChangeSets == "" {
		config.Stores.ChangeSets = ".protobot/change-sets"
	}
	for _, store := range []*string{&config.Stores.Requirements, &config.Stores.Interfaces, &config.Stores.ChangeSets} {
		clean, ok := CleanRelative(*store)
		if !ok {
			return nil, unreadable("a store path leaves the working tree")
		}
		*store = clean
	}
	for i := range config.Artifacts {
		clean, ok := CleanRelative(config.Artifacts[i].Path)
		if !ok {
			return nil, unreadable(fmt.Sprintf("the path of artifact %d leaves the working tree", i+1))
		}
		config.Artifacts[i].Path = clean
	}
	return &config, nil
}

// CleanRelative cleans a root-relative slash path. It refuses an empty,
// absolute, or escaping path. A trailing slash is dropped.
func CleanRelative(p string) (string, bool) {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return "", false
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}

// Under reports whether p is dir or lies below it.
func Under(p, dir string) bool {
	dir = strings.TrimSuffix(dir, "/")
	return p == dir || strings.HasPrefix(p, dir+"/")
}

// ArtifactFor returns the registry entry that holds p: the entry for p
// itself, or a directory entry above it.
func (c *Config) ArtifactFor(p string) (Artifact, bool) {
	var best Artifact
	found := false
	for _, artifact := range c.Artifacts {
		if Under(p, artifact.Path) && (!found || len(artifact.Path) > len(best.Path)) {
			best = artifact
			found = true
		}
	}
	return best, found
}
