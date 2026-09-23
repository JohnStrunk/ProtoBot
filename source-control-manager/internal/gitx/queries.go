package gitx

import (
	"crypto/sha1" //nolint:gosec // Git's object names, not a security digest.
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// Commit returns the commit that rev names, and false when it names none.
func (r *Runner) Commit(rev string) (string, bool, error) {
	res, err := r.Run(Opts{}, "rev-parse", "-q", "--verify", "--end-of-options", rev+"^{commit}")
	if err != nil {
		return "", false, err
	}
	if !res.OK() {
		return "", false, nil
	}
	return res.Text(), true, nil
}

// RefExists reports whether the full ref name exists.
func (r *Runner) RefExists(ref string) (bool, error) {
	res, err := r.Run(Opts{}, "show-ref", "--verify", "--quiet", ref)
	if err != nil {
		return false, err
	}
	switch res.Status {
	case 0:
		return true, nil
	case 1:
		return false, nil
	default:
		return false, &CommandError{Args: []string{"show-ref", "--verify", "--quiet", ref}, Status: res.Status, Stderr: string(res.Stderr)}
	}
}

// IsAncestor reports whether commit a is an ancestor of, or equal to,
// commit b.
func (r *Runner) IsAncestor(a, b string) (bool, error) {
	args := []string{"merge-base", "--is-ancestor", a, b}
	res, err := r.Run(Opts{}, args...)
	if err != nil {
		return false, err
	}
	switch res.Status {
	case 0:
		return true, nil
	case 1:
		return false, nil
	default:
		return false, &CommandError{Args: args, Status: res.Status, Stderr: string(res.Stderr)}
	}
}

// SymbolicRef returns the ref that name points to, and false when name is
// not a symbolic ref.
func (r *Runner) SymbolicRef(name string) (string, bool, error) {
	res, err := r.Run(Opts{}, "symbolic-ref", "-q", name)
	if err != nil {
		return "", false, err
	}
	if !res.OK() {
		return "", false, nil
	}
	return res.Text(), true, nil
}

// ConfigAll returns every value of a configuration key, in order.
func (r *Runner) ConfigAll(key string) ([]string, error) {
	args := []string{"config", "--null", "--get-all", key}
	res, err := r.Run(Opts{}, args...)
	if err != nil {
		return nil, err
	}
	switch res.Status {
	case 0:
		return SplitZ(res.Stdout), nil
	case 1:
		return nil, nil
	default:
		return nil, &CommandError{Args: args, Status: res.Status, Stderr: string(res.Stderr)}
	}
}

// ConfigBool reads a boolean configuration key; a missing key is false.
func (r *Runner) ConfigBool(key string) (bool, error) {
	args := []string{"config", "--type=bool", "--get", key}
	res, err := r.Run(Opts{}, args...)
	if err != nil {
		return false, err
	}
	switch res.Status {
	case 0:
		return res.Text() == "true", nil
	case 1:
		return false, nil
	default:
		return false, &CommandError{Args: args, Status: res.Status, Stderr: string(res.Stderr)}
	}
}

// Ref is one ref and the object it names.
type Ref struct {
	Name   string
	Object string
}

// Refs lists the refs under a prefix, such as refs/heads/cs/, sorted by
// name.
func (r *Runner) Refs(prefix string) ([]Ref, error) {
	out, err := r.ReadRaw(Opts{}, "for-each-ref", "--format=%(refname)%00%(objectname)%00", prefix)
	if err != nil {
		return nil, err
	}
	fields := SplitZ(out)
	var refs []Ref
	for i := 0; i+1 < len(fields); i += 2 {
		name := strings.TrimLeft(fields[i], "\n")
		refs = append(refs, Ref{Name: name, Object: fields[i+1]})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })
	return refs, nil
}

// ObjectFormat returns the repository's object format, sha1 or sha256.
func (r *Runner) ObjectFormat() (string, error) {
	out, err := r.Read("rev-parse", "--show-object-format")
	if err != nil {
		return "", err
	}
	return out, nil
}

// BlobName returns the name that git gives a blob with this content.
func BlobName(format string, content []byte) string {
	header := fmt.Sprintf("blob %d\x00", len(content))
	if format == "sha256" {
		sum := sha256.New()
		sum.Write([]byte(header))
		sum.Write(content)
		return hex.EncodeToString(sum.Sum(nil))
	}
	sum := sha1.New() //nolint:gosec // Git's object names, not a security digest.
	sum.Write([]byte(header))
	sum.Write(content)
	return hex.EncodeToString(sum.Sum(nil))
}

// Entry is one tree or index entry.
type Entry struct {
	Mode   string
	Type   string
	Object string
}

// TreeEntries returns the entries of rev's tree at exactly these paths.
func (r *Runner) TreeEntries(rev string, paths []string) (map[string]Entry, error) {
	entries := map[string]Entry{}
	if len(paths) == 0 {
		return entries, nil
	}
	args := append([]string{"ls-tree", "-z", "--full-tree", rev, "--"}, paths...)
	out, err := r.ReadRaw(Opts{}, args...)
	if err != nil {
		return nil, err
	}
	for _, line := range SplitZ(out) {
		meta, path, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		parts := strings.Fields(meta)
		if len(parts) != 3 {
			continue
		}
		entries[path] = Entry{Mode: parts[0], Type: parts[1], Object: parts[2]}
	}
	return entries, nil
}

// ObjectType returns the type of the object that spec names, such as
// HEAD:docs, and "" when there is none.
func (r *Runner) ObjectTypes(specs []string) (map[string]string, error) {
	types := map[string]string{}
	if len(specs) == 0 {
		return types, nil
	}
	input := strings.Join(specs, "\n") + "\n"
	out, err := r.ReadRaw(Opts{Stdin: []byte(input)}, "cat-file", "--batch-check=%(objecttype)")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	for i, spec := range specs {
		if i >= len(lines) {
			break
		}
		if strings.HasSuffix(lines[i], " missing") || strings.HasSuffix(lines[i], " ambiguous") {
			continue
		}
		types[spec] = lines[i]
	}
	return types, nil
}

// IndexEntries returns the stage-0 index entries at these paths. env
// selects another index with GIT_INDEX_FILE.
func (r *Runner) IndexEntries(paths []string, env []string) (map[string]Entry, error) {
	entries := map[string]Entry{}
	if len(paths) == 0 {
		return entries, nil
	}
	args := append([]string{"ls-files", "-s", "-z", "--"}, paths...)
	out, err := r.ReadRaw(Opts{Env: env}, args...)
	if err != nil {
		return nil, err
	}
	for _, line := range SplitZ(out) {
		meta, path, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		parts := strings.Fields(meta)
		if len(parts) != 3 || parts[2] != "0" {
			continue
		}
		entries[path] = Entry{Mode: parts[0], Type: "blob", Object: parts[1]}
	}
	return entries, nil
}

// Change is one line of git status.
type Change struct {
	Index    byte
	Worktree byte
	Path     string
}

// Untracked reports an untracked path.
func (c Change) Untracked() bool { return c.Index == '?' }

// Status lists the changed paths of the working tree: tracked changes, and
// untracked files when untracked is true. Ignored files are not listed.
func (r *Runner) Status(untracked bool) ([]Change, error) {
	mode := "--untracked-files=no"
	if untracked {
		mode = "--untracked-files=all"
	}
	out, err := r.ReadRaw(Opts{}, "status", "--porcelain=v1", "-z", "--no-renames", "--ignore-submodules=none", mode)
	if err != nil {
		return nil, err
	}
	var changes []Change
	for _, item := range SplitZ(out) {
		if len(item) < 4 {
			continue
		}
		changes = append(changes, Change{Index: item[0], Worktree: item[1], Path: item[3:]})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
}

// TrackedChanges lists the tracked paths with an uncommitted change.
func (r *Runner) TrackedChanges() ([]string, error) {
	changes, err := r.Status(false)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, change := range changes {
		paths = append(paths, change.Path)
	}
	return paths, nil
}
