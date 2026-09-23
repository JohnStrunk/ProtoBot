package scm

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/redhat-et/protobot/source-control-manager/internal/gitx"
	"github.com/redhat-et/protobot/source-control-manager/internal/jsonx"
	"github.com/redhat-et/protobot/source-control-manager/internal/project"
	"github.com/redhat-et/protobot/source-control-manager/internal/result"
)

// content is the content of one path in the working tree: absent, or the
// bytes of a regular file.
type content struct {
	present bool
	data    []byte
}

// readContent reads a working-tree path. A path that is not a regular
// file, a symbolic link included, is reported as not a file.
func (c *call) readContent(p string) (content, bool, error) {
	full := filepath.Join(c.proj.Root, filepath.FromSlash(p))
	info, err := os.Lstat(full)
	if errors.Is(err, os.ErrNotExist) {
		return content{}, true, nil
	}
	if err != nil {
		return content{}, false, err
	}
	if !info.Mode().IsRegular() {
		return content{}, false, nil
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return content{}, false, err
	}
	return content{present: true, data: data}, true, nil
}

func (c *call) blobName(data []byte) string { return gitx.BlobName(c.objectFormat, data) }

// fileSet is the file set of a change set, derived as commit step 2
// derives it.
type fileSet struct {
	// paths is the file set, sorted.
	paths []string
	// changed are the paths whose working-tree content differs from HEAD.
	changed []string
	// indexChanged are the paths whose index entry differs from HEAD.
	indexChanged []string
	// projectionPolicy reports a projection.yaml that differs from HEAD by
	// project policy only; it stays out of the file set.
	projectionPolicy bool
}

// deriveMode selects how deriveFileSet treats a path it cannot stage.
type deriveMode int

const (
	// strictMode is commit's: an unstageable path or a mixed
	// projection.yaml is a failure.
	strictMode deriveMode = iota
	// listMode is repo_state's and publish's: it lists what a commit of
	// the same state would hold, and skips what it cannot compare.
	listMode
)

// deriveFileSet derives the file set of the current change set against
// the commit head: the paths that change-set show returns, the manifest
// included, and .protobot/project.yaml; on the initialization branch also
// .protobot/projection.yaml, and elsewhere projection.yaml when it differs
// from head by the change set's own entries only.
func (c *call) deriveFileSet(head string, mode deriveMode) (*fileSet, *result.Failure) {
	var dirs []string
	set := map[string]bool{project.ConfigPath: true}
	var own []string
	for _, raw := range c.show.Paths {
		clean, ok := project.CleanRelative(raw)
		if !ok {
			if mode == strictMode {
				return nil, notStageable([]string{raw})
			}
			dirs = append(dirs, raw)
			continue
		}
		if strings.HasSuffix(raw, "/") {
			dirs = append(dirs, raw)
			continue
		}
		set[clean] = true
		own = append(own, clean)
	}
	for p := range set {
		full := filepath.Join(c.proj.Root, filepath.FromSlash(p))
		info, err := os.Lstat(full)
		if err != nil || info.Mode().IsRegular() {
			continue
		}
		// A directory is never a path of the file set. A symbolic link or
		// another non-regular file cannot be staged either, but list mode
		// still names it as an uncommitted change.
		if info.IsDir() || mode == strictMode {
			dirs = append(dirs, p)
			delete(set, p)
		}
	}
	if len(dirs) > 0 && mode == strictMode {
		sort.Strings(dirs)
		return nil, notStageable(dirs)
	}

	fs := &fileSet{}
	// List mode names every path it cannot compare, a directory where the
	// change set has a file included, as an uncommitted change, so publish
	// never pushes while such a path differs from HEAD.
	if mode == listMode {
		fs.changed = append(fs.changed, dirs...)
	}
	delete(set, project.ProjectionPath)
	if c.branch == c.initBranch() {
		set[project.ProjectionPath] = true
	} else {
		ownEdit, policyEdit, failure := c.projectionDiff(head, own)
		if failure != nil {
			return nil, failure
		}
		switch {
		case ownEdit && policyEdit:
			if mode == strictMode {
				return nil, result.Fail(result.UncommittedChanges, "projection.yaml mixes the change set's entries with a policy edit.",
					jsonx.F("paths", []string{project.ProjectionPath}))
			}
			set[project.ProjectionPath] = true
		case ownEdit:
			set[project.ProjectionPath] = true
		case policyEdit:
			fs.projectionPolicy = true
		}
	}
	for p := range set {
		fs.paths = append(fs.paths, p)
	}
	sort.Strings(fs.paths)

	headEntries, err := c.git.TreeEntries(head, fs.paths)
	if err != nil {
		return nil, c.gitFailure(err)
	}
	indexEntries, err := c.git.IndexEntries(fs.paths, nil)
	if err != nil {
		return nil, c.gitFailure(err)
	}
	for _, p := range fs.paths {
		entry, inHead := headEntries[p]
		now, isFile, err := c.readContent(p)
		if err != nil {
			return nil, result.Fail(result.Internal, "A path of the change set cannot be read.", jsonx.F("path", p))
		}
		indexEntry, inIndex := indexEntries[p]
		if inIndex != inHead || (inIndex && (indexEntry.Object != entry.Object || indexEntry.Mode != entry.Mode)) {
			fs.indexChanged = append(fs.indexChanged, p)
		}
		if !isFile {
			if mode == strictMode {
				return nil, notStageable([]string{p})
			}
			if !c.sameLink(p, entry, inHead) {
				fs.changed = append(fs.changed, p)
			}
			continue
		}
		regularInHead := entry.Mode == "100644" || entry.Mode == "100755"
		differs := now.present != inHead || (now.present && (!regularInHead || c.blobName(now.data) != entry.Object))
		if differs {
			fs.changed = append(fs.changed, p)
		}
	}
	return fs, nil
}

// sameLink reports a working-tree symbolic link that HEAD holds unchanged.
func (c *call) sameLink(p string, entry gitx.Entry, inHead bool) bool {
	target, err := os.Readlink(filepath.Join(c.proj.Root, filepath.FromSlash(p)))
	if err != nil || !inHead || entry.Mode != "120000" {
		return false
	}
	return c.blobName([]byte(target)) == entry.Object
}

// listed returns the paths that repo_state and publish name as uncommitted
// change-set paths: those whose working-tree content or index entry
// differs from HEAD.
func (fs *fileSet) listed() []string {
	seen := map[string]bool{}
	out := []string{}
	for _, p := range append(append([]string{}, fs.changed...), fs.indexChanged...) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func notStageable(paths []string) *result.Failure {
	return result.Fail(result.PathNotStageable, "A path of the change set is one that the SCM never stages.",
		jsonx.F("paths", paths))
}

// protectedPath reports a path that the SCM never stages by #34's path
// rules and the guard's list, with no look at Git. Names compare without
// letter case, because a case-insensitive file system, as on macOS and
// Windows, opens CLAUDE.md for claude.md.
func (c *call) protectedPath(p string) bool {
	if artifact, ok := c.config().ArtifactFor(p); ok && artifact.Owner != "ears-manager" && artifact.Owner != "user" {
		return true
	}
	components := strings.Split(p, "/")
	oneOf := func(name string, names ...string) bool {
		for _, n := range names {
			if strings.EqualFold(name, n) {
				return true
			}
		}
		return false
	}
	if len(components) >= 2 && oneOf(components[0], ".protobot") &&
		(oneOf(components[1], "attestations") || (len(components) == 2 && oneOf(components[1], "test-catalog.jsonl"))) {
		return true
	}
	if oneOf(components[0], ".agents", ".claude", ".codex", ".opencode", ".github") {
		return true
	}
	for _, component := range components {
		if oneOf(component, ".git") {
			return true
		}
	}
	return oneOf(path.Base(p), "AGENTS.md", "CLAUDE.md", "opencode.json", ".gitattributes", ".gitmodules")
}

// unstageable returns the paths that break the path rules: a protected
// path, a path whose parent resolves outside the working tree, and a path
// that replaces a tracked directory or lies below a tracked file.
func (c *call) unstageable(head string, paths []string) ([]string, *result.Failure) {
	var bad []string
	var specs []string
	for _, p := range paths {
		if c.protectedPath(p) || !c.parentInside(p) {
			bad = append(bad, p)
			continue
		}
		specs = append(specs, head+":"+p)
		for dir := path.Dir(p); dir != "."; dir = path.Dir(dir) {
			specs = append(specs, head+":"+dir)
		}
	}
	types, err := c.git.ObjectTypes(specs)
	if err != nil {
		return nil, c.gitFailure(err)
	}
	for _, p := range paths {
		if contains(bad, p) {
			continue
		}
		if types[head+":"+p] == "tree" {
			bad = append(bad, p)
			continue
		}
		for dir := path.Dir(p); dir != "."; dir = path.Dir(dir) {
			if t := types[head+":"+dir]; t != "" && t != "tree" {
				bad = append(bad, p)
				break
			}
		}
	}
	sort.Strings(bad)
	return bad, nil
}

// parentInside reports whether the parent directory of p, with symbolic
// links resolved, stays inside the working tree.
func (c *call) parentInside(p string) bool {
	dir := filepath.Join(c.proj.Root, filepath.FromSlash(path.Dir(p)))
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		// A parent that does not exist holds nothing to stage.
		return errors.Is(err, os.ErrNotExist)
	}
	return resolved == c.proj.Root || strings.HasPrefix(resolved, c.proj.Root+string(filepath.Separator))
}

// projection is a parsed projection manifest: path classes, and every
// other top-level field.
type projection struct {
	classes map[string]string
	rest    map[string]any
}

// parseProjection reads the projection manifest format that this SCM
// understands: a mapping whose `paths` list holds `path` and `class`
// entries. It returns false for anything else.
func parseProjection(data []byte) (*projection, bool) {
	p := &projection{classes: map[string]string{}, rest: map[string]any{}}
	if len(strings.TrimSpace(string(data))) == 0 {
		return p, true
	}
	var doc map[string]any
	if yaml.Unmarshal(data, &doc) != nil {
		return nil, false
	}
	for key, value := range doc {
		if key != "paths" {
			p.rest[key] = value
			continue
		}
		list, ok := value.([]any)
		if !ok {
			return nil, false
		}
		for _, item := range list {
			entry, ok := item.(map[string]any)
			if !ok || len(entry) != 2 {
				return nil, false
			}
			entryPath, ok1 := entry["path"].(string)
			class, ok2 := entry["class"].(string)
			if !ok1 || !ok2 {
				return nil, false
			}
			entryPath = strings.TrimSuffix(entryPath, "/")
			if _, dup := p.classes[entryPath]; dup {
				return nil, false
			}
			p.classes[entryPath] = class
		}
	}
	return p, true
}

// projectionDiff compares the working-tree projection.yaml with head. An
// own edit is a shared entry for a path of the change set, which
// ears-manager writes when it registers the path. Every other difference is
// project policy that a person edits.
func (c *call) projectionDiff(head string, own []string) (ownEdit, policyEdit bool, failure *result.Failure) {
	now, isFile, err := c.readContent(project.ProjectionPath)
	if err != nil {
		return false, false, result.Fail(result.Internal, "projection.yaml cannot be read.")
	}
	if !isFile {
		return false, true, nil
	}
	entries, err := c.git.TreeEntries(head, []string{project.ProjectionPath})
	if err != nil {
		return false, false, c.gitFailure(err)
	}
	entry, inHead := entries[project.ProjectionPath]
	var before []byte
	if inHead {
		before, err = c.git.ReadRaw(gitx.Opts{}, "cat-file", "blob", entry.Object)
		if err != nil {
			return false, false, c.gitFailure(err)
		}
	}
	if now.present == inHead && (!now.present || string(now.data) == string(before)) {
		return false, false, nil
	}
	if !now.present {
		return false, true, nil
	}
	after, okAfter := parseProjection(now.data)
	previous, okBefore := parseProjection(before)
	if !okAfter || !okBefore {
		return false, true, nil
	}
	if !reflect.DeepEqual(after.rest, previous.rest) {
		policyEdit = true
	}
	keys := map[string]bool{}
	for k := range after.classes {
		keys[k] = true
	}
	for k := range previous.classes {
		keys[k] = true
	}
	for k := range keys {
		was, hadBefore := previous.classes[k]
		is, hasNow := after.classes[k]
		if hadBefore == hasNow && was == is {
			continue
		}
		if hasNow && is == "shared" && c.ownsEntry(k, own) {
			ownEdit = true
			continue
		}
		policyEdit = true
	}
	return ownEdit, policyEdit, nil
}

// ownsEntry reports a projection entry that ears-manager writes for the
// change set: the entry of one of its paths, or of a registered directory
// artifact that holds one. A class for any other directory is policy.
func (c *call) ownsEntry(entry string, own []string) bool {
	for _, p := range own {
		if p == entry {
			return true
		}
	}
	for _, artifact := range c.config().Artifacts {
		if artifact.Path != entry {
			continue
		}
		for _, p := range own {
			if project.Under(p, entry) {
				return true
			}
		}
	}
	return false
}

// otherChanges counts the changed paths of the working tree that are not
// in names: tracked changes and untracked files.
func (c *call) otherChanges(names []string) (int, *result.Failure) {
	changes, err := c.git.Status(true)
	if err != nil {
		return 0, c.gitFailure(err)
	}
	count := 0
	for _, change := range changes {
		if !contains(names, change.Path) {
			count++
		}
	}
	return count, nil
}
