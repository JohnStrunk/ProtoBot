// Command earsstub stands in for ears-manager in the repository fixture,
// until the command set of issue #110 exists. It implements the subset of
// the #30 contract that the fixture drives: project init, change-set
// create, show, update, and compare, artifact put, impact, and check, with
// #30's envelopes and exit statuses. It reports artifact.digest_mismatch
// for a registered path whose content does not match its digest.
//
// EARS_STUB_CONTROL may name a JSON file whose "extra_paths" map adds paths
// to the change-set show result of a change set, so the fixture can hand
// the SCM a path that a real ears-manager would never list.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Project is .protobot/project.yaml.
type Project struct {
	Project struct {
		ID   string `yaml:"id" json:"id"`
		Name string `yaml:"name" json:"name"`
	} `yaml:"project" json:"project"`
	Repository struct {
		CanonicalRemote string `yaml:"canonical_remote" json:"canonical_remote"`
		DefaultBranch   string `yaml:"default_branch" json:"default_branch"`
		ReviewMode      string `yaml:"review_mode" json:"review_mode"`
		BranchPrefix    string `yaml:"branch_prefix" json:"branch_prefix"`
	} `yaml:"repository" json:"repository"`
	SchemaVersions struct {
		Project       int `yaml:"project" json:"project"`
		Specification int `yaml:"specification" json:"specification"`
	} `yaml:"schema_versions" json:"schema_versions"`
	Stores struct {
		Requirements string `yaml:"requirements" json:"requirements"`
		Interfaces   string `yaml:"interfaces" json:"interfaces"`
		ChangeSets   string `yaml:"change_sets" json:"change_sets"`
	} `yaml:"stores" json:"stores"`
	Artifacts []Artifact `yaml:"artifacts" json:"artifacts"`
}

// Artifact is a registry entry.
type Artifact struct {
	ID     string `yaml:"id" json:"id"`
	Kind   string `yaml:"kind" json:"kind"`
	Path   string `yaml:"path" json:"path"`
	Digest string `yaml:"digest" json:"digest"`
	Owner  string `yaml:"owner" json:"owner"`
}

// Op is a manifest operation. The stub records the content digest of an
// artifact write, so every write with new content changes the manifest.
type Op struct {
	Action        string `yaml:"action" json:"action"`
	RequirementID string `yaml:"requirement_id,omitempty" json:"requirement_id,omitempty"`
	InterfaceID   string `yaml:"interface_id,omitempty" json:"interface_id,omitempty"`
	ArtifactID    string `yaml:"artifact_id,omitempty" json:"artifact_id,omitempty"`
	Digest        string `yaml:"digest,omitempty" json:"-"`
}

// Assessment is an impact disposition.
type Assessment struct {
	RequirementID string `yaml:"requirement_id" json:"requirement_id"`
	Disposition   string `yaml:"disposition" json:"disposition"`
	Rationale     string `yaml:"rationale" json:"rationale"`
	Origin        string `yaml:"origin" json:"origin"`
}

// Manifest is a change-set manifest.
type Manifest struct {
	ID                      string       `yaml:"id" json:"id"`
	BaseCommit              string       `yaml:"base_commit" json:"base_commit"`
	Intent                  string       `yaml:"intent" json:"intent"`
	Operations              []Op         `yaml:"operations" json:"operations"`
	ArtifactOperations      []Op         `yaml:"artifact_operations,omitempty" json:"artifact_operations,omitempty"`
	AffectedInterfaces      []string     `yaml:"affected_interfaces" json:"affected_interfaces"`
	ImplementationRequired  bool         `yaml:"implementation_required" json:"implementation_required"`
	ImplementationRationale string       `yaml:"implementation_rationale,omitempty" json:"implementation_rationale,omitempty"`
	ImpactAssessment        []Assessment `yaml:"impact_assessment,omitempty" json:"impact_assessment,omitempty"`
	Created                 string       `yaml:"created" json:"created"`
	// ImpactStale is the stub's record that the base moved after the last
	// reviewed assessment.
	ImpactStale bool `yaml:"impact_stale,omitempty" json:"-"`
}

// Diagnostic is a #30 diagnostic.
type Diagnostic struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Path     string `json:"path,omitempty"`
	Message  string `json:"message"`
}

type failure struct {
	status      int
	code        string
	message     string
	diagnostics []Diagnostic
}

func fail(status int, code, message string, diagnostics ...Diagnostic) *failure {
	return &failure{status: status, code: code, message: message, diagnostics: diagnostics}
}

type stub struct {
	root    string
	command string
	opts    map[string][]string
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println("ears-manager stub")
		return 0
	}
	if len(args) < 3 || args[0] != "--output" || args[1] != "json" {
		fmt.Fprintln(os.Stderr, "earsstub: only --output json is supported")
		return 2
	}
	args = args[2:]
	command := args[0]
	rest := args[1:]
	if command == "change-set" || command == "project" || command == "artifact" {
		if len(rest) == 0 {
			return emitFailure(command, fail(2, "usage.missing_subcommand", "A subcommand is missing."))
		}
		command += " " + rest[0]
		rest = rest[1:]
	}
	s := &stub{command: command, opts: map[string][]string{}}
	for i := 0; i < len(rest); i++ {
		name := strings.TrimPrefix(rest[i], "--")
		if name == "content-stdin" {
			s.opts[name] = []string{"true"}
			continue
		}
		if i+1 >= len(rest) {
			return emitFailure(command, fail(2, "usage.missing_value", "An option has no value."))
		}
		s.opts[name] = append(s.opts[name], rest[i+1])
		i++
	}
	root, err := gitOut(".", "rev-parse", "--show-toplevel")
	if err != nil {
		return emitFailure(command, fail(3, "project.not_git_root", "The directory is not in a Git working tree."))
	}
	s.root = root
	var data any
	var paths []string
	var f *failure
	switch command {
	case "project init":
		data, paths, f = s.projectInit()
	case "change-set create":
		data, paths, f = s.changeSetCreate()
	case "change-set show":
		data, f = s.changeSetShow()
	case "change-set update":
		data, paths, f = s.changeSetUpdate()
	case "change-set compare":
		data, f = s.changeSetCompare()
	case "artifact put":
		data, paths, f = s.artifactPut()
	case "impact":
		data, f = s.impact()
	case "check":
		data, f = s.check()
	default:
		f = fail(2, "usage.unknown_command", "The command is not known.")
	}
	if f != nil {
		return emitFailure(command, f)
	}
	sort.Strings(paths)
	if paths == nil {
		paths = []string{}
	}
	emit(map[string]any{
		"schema_version": 1, "ok": true, "command": command, "data": data, "diagnostics": []any{},
		"mutation": map[string]any{"applied": len(paths) > 0, "paths": paths},
	})
	return 0
}

func emit(doc any) {
	out, _ := json.Marshal(doc)
	fmt.Println(string(out))
}

func emitFailure(command string, f *failure) int {
	retry := "revise-request"
	switch f.status {
	case 3, 6:
		retry = "fix-environment"
	case 5:
		retry = "refresh"
	}
	diagnostics := f.diagnostics
	if diagnostics == nil {
		diagnostics = []Diagnostic{}
	}
	emit(map[string]any{
		"schema_version": 1, "ok": false, "command": command,
		"error": map[string]any{
			"code": f.code, "message": f.message, "exit_code": f.status,
			"diagnostics": diagnostics, "mutation": "none", "retry": retry,
		},
	})
	return f.status
}

func (s *stub) opt(name string) string {
	if values := s.opts[name]; len(values) > 0 {
		return values[len(values)-1]
	}
	return ""
}

func (s *stub) file(rel string) string { return filepath.Join(s.root, filepath.FromSlash(rel)) }

func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func (s *stub) git(args ...string) (string, error) { return gitOut(s.root, args...) }

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (s *stub) loadProject() (*Project, *failure) {
	data, err := os.ReadFile(s.file(".protobot/project.yaml"))
	if err != nil {
		return nil, fail(3, "project.not_initialized", "The project is not initialized.")
	}
	var p Project
	if yaml.Unmarshal(data, &p) != nil {
		return nil, fail(3, "project.invalid_configuration", "project.yaml is not valid.")
	}
	return &p, nil
}

func (s *stub) saveYAML(rel string, v any) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.file(rel)), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.file(rel), buf.Bytes(), 0o644)
}

// Projection is .protobot/projection.yaml.
type Projection struct {
	Paths []Class `yaml:"paths"`
}

// Class is one projection entry.
type Class struct {
	Path  string `yaml:"path"`
	Class string `yaml:"class"`
}

func (s *stub) loadProjection() Projection {
	var p Projection
	data, err := os.ReadFile(s.file(".protobot/projection.yaml"))
	if err == nil {
		_ = yaml.Unmarshal(data, &p)
	}
	return p
}

func (s *stub) projectInit() (any, []string, *failure) {
	if _, err := os.Lstat(s.file(".protobot")); err == nil {
		return nil, nil, fail(5, "project.already_initialized", "The project is already initialized.")
	}
	var p Project
	p.Project.ID = s.opt("id")
	p.Project.Name = s.opt("name")
	p.Repository.CanonicalRemote = s.opt("canonical-remote")
	p.Repository.ReviewMode = s.opt("review-mode")
	p.Repository.DefaultBranch = orDefault(s.opt("default-branch"), "main")
	p.Repository.BranchPrefix = orDefault(s.opt("branch-prefix"), "cs/")
	if p.Repository.BranchPrefix == "wi/" {
		return nil, nil, fail(4, "project.invalid_configuration", "The branch prefix is reserved.")
	}
	p.SchemaVersions.Project, p.SchemaVersions.Specification = 1, 1
	p.Stores.Requirements, p.Stores.Interfaces, p.Stores.ChangeSets = ".protobot/requirements", ".protobot/interfaces", ".protobot/change-sets"
	var registered []string
	var projection Projection
	for _, a := range []struct{ id, path string }{
		{"architecture", orDefault(s.opt("architecture"), "docs/architecture.md")},
		{"vision", orDefault(s.opt("vision"), "docs/vision.md")},
	} {
		data, err := os.ReadFile(s.file(a.path))
		if err != nil {
			return nil, nil, fail(4, "project.invalid_path", "A selected artifact path does not exist.")
		}
		p.Artifacts = append(p.Artifacts, Artifact{ID: a.id, Kind: a.id, Path: a.path, Digest: digest(data), Owner: "user"})
		registered = append(registered, a.path)
		projection.Paths = append(projection.Paths, Class{Path: a.path, Class: "shared"})
	}
	sort.Strings(registered)
	if err := s.saveYAML(".protobot/project.yaml", p); err != nil {
		return nil, nil, fail(6, "io.write_failed", "The project cannot be written.")
	}
	if err := s.saveYAML(".protobot/projection.yaml", projection); err != nil {
		return nil, nil, fail(6, "io.write_failed", "The projection cannot be written.")
	}
	return map[string]any{"project": p, "registered_paths": registered}, []string{".protobot/project.yaml", ".protobot/projection.yaml"}, nil
}

func orDefault(value, def string) string {
	if value == "" {
		return def
	}
	return value
}

var slugRun = regexp.MustCompile(`[^a-z0-9]+`)

func slug(intent string) string {
	s := strings.Trim(slugRun.ReplaceAllString(strings.ToLower(intent), "-"), "-")
	if s == "" {
		return "change-set"
	}
	if len(s) > 40 {
		cut := strings.LastIndex(s[:40], "-")
		if cut > 0 {
			s = s[:cut]
		} else {
			s = s[:40]
		}
	}
	return s
}

func (s *stub) manifestPath(p *Project, id string) string {
	return p.Stores.ChangeSets + "/" + strings.ToLower(id) + ".yaml"
}

func (s *stub) currentBranch() string {
	out, err := s.git("symbolic-ref", "-q", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return out
}

func (s *stub) changeSetCreate() (any, []string, *failure) {
	p, f := s.loadProject()
	if f != nil {
		return nil, nil, f
	}
	prefix, def := p.Repository.BranchPrefix, p.Repository.DefaultBranch
	base, err := s.git("rev-parse", "refs/heads/"+def)
	if err != nil {
		return nil, nil, fail(5, "change_set.no_base", "The default branch has no commit.")
	}
	initBranch := prefix + "00001-project-init"
	current := s.currentBranch()
	var id, branch string
	cut := true
	if _, err := os.Stat(s.file(s.manifestPath(p, "CS-00001"))); err != nil && current == initBranch {
		// The one branch reuse case: the pre-cut initialization branch,
		// before the project is approved and before it has a manifest.
		id, branch, cut = "CS-00001", initBranch, false
	} else {
		next := 1 + s.highestNumber(p, def)
		id = fmt.Sprintf("CS-%05d", next)
		branch = fmt.Sprintf("%s%05d-%s", prefix, next, slug(s.opt("intent")))
		if _, err := s.git("rev-parse", "-q", "--verify", "refs/heads/"+branch); err == nil {
			return nil, nil, fail(5, "change_set.branch_exists", "The branch already exists.")
		}
	}
	m := Manifest{
		ID: id, BaseCommit: base, Intent: s.opt("intent"), Operations: []Op{},
		AffectedInterfaces:      append([]string{}, s.opts["affected-interface"]...),
		ImplementationRequired:  s.opt("implementation-required") == "true",
		ImplementationRationale: s.opt("implementation-rationale"),
		Created:                 s.opt("created"),
	}
	if cut {
		if _, err := s.git("switch", "--quiet", "--no-track", "--create", branch, def); err != nil {
			return nil, nil, fail(5, "change_set.branch_exists", "The branch cannot be cut.")
		}
	}
	manifest := s.manifestPath(p, id)
	if err := s.saveYAML(manifest, m); err != nil {
		return nil, nil, fail(6, "io.write_failed", "The manifest cannot be written.")
	}
	return map[string]any{"change_set": map[string]any{"id": id, "base_commit": base, "branch": branch, "manifest_path": manifest}}, []string{manifest}, nil
}

var numbered = regexp.MustCompile(`cs-([0-9]{5})\.yaml$`)

func (s *stub) highestNumber(p *Project, def string) int {
	highest := 0
	note := func(name string) {
		if m := numbered.FindStringSubmatch(name); m != nil {
			if n, _ := strconv.Atoi(m[1]); n > highest {
				highest = n
			}
		}
	}
	entries, _ := os.ReadDir(s.file(p.Stores.ChangeSets))
	for _, e := range entries {
		note(e.Name())
	}
	if out, err := s.git("ls-tree", "--name-only", "refs/heads/"+def, p.Stores.ChangeSets+"/"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			note(line)
		}
	}
	branchNumber := regexp.MustCompile(`^refs/heads/` + regexp.QuoteMeta(p.Repository.BranchPrefix) + `([0-9]{5})-`)
	if out, err := s.git("for-each-ref", "--format=%(refname)", "refs/heads/"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			if m := branchNumber.FindStringSubmatch(line); m != nil {
				if n, _ := strconv.Atoi(m[1]); n > highest {
					highest = n
				}
			}
		}
	}
	return highest
}

// readManifest reads a manifest from the working tree, or at a commit.
func (s *stub) readManifest(p *Project, id, at string) (*Manifest, bool) {
	rel := s.manifestPath(p, id)
	var data []byte
	if at != "" {
		out, err := exec.Command("git", "-C", s.root, "show", at+":"+rel).Output()
		if err != nil {
			return nil, false
		}
		data = out
	} else {
		out, err := os.ReadFile(s.file(rel))
		if err != nil {
			return nil, false
		}
		data = out
	}
	var m Manifest
	if yaml.Unmarshal(data, &m) != nil {
		return nil, false
	}
	return &m, true
}

// approved reports a manifest that is on a default branch.
func (s *stub) approved(p *Project, id string) bool {
	rel := s.manifestPath(p, id)
	for _, ref := range []string{"refs/heads/" + p.Repository.DefaultBranch} {
		if _, err := s.git("cat-file", "-e", ref+":"+rel); err == nil {
			return true
		}
	}
	return false
}

func (s *stub) projectAt(at string) *Project {
	if at == "" {
		p, _ := s.loadProject()
		return p
	}
	out, err := exec.Command("git", "-C", s.root, "show", at+":.protobot/project.yaml").Output()
	if err != nil {
		return nil
	}
	var p Project
	if yaml.Unmarshal(out, &p) != nil {
		return nil
	}
	return &p
}

func (s *stub) changeSetShow() (any, *failure) {
	p, f := s.loadProject()
	at := s.opt("at")
	if at != "" {
		p = s.projectAt(at)
		f = nil
		if p == nil {
			f = fail(4, "change_set.not_found", "The change set was not found.")
		}
	}
	if f != nil {
		return nil, f
	}
	id := s.opt("change-set")
	m, ok := s.readManifest(p, id, at)
	if !ok {
		return nil, fail(4, "change_set.not_found", "Change set "+id+" was not found.")
	}
	status := "proposed"
	if at != "" {
		if _, err := s.git("merge-base", "--is-ancestor", at, "refs/remotes/origin/"+p.Repository.DefaultBranch); err == nil {
			status = "approved"
		}
	} else if s.approved(p, id) {
		status = "approved"
	}
	manifest := s.manifestPath(p, id)
	paths := []string{manifest}
	for _, op := range m.ArtifactOperations {
		for _, a := range p.Artifacts {
			if a.ID == op.ArtifactID {
				paths = append(paths, a.Path)
			}
		}
	}
	paths = append(paths, s.extraPaths(id)...)
	sort.Strings(paths)
	return map[string]any{"change_set": m, "status": status, "manifest_path": manifest, "paths": dedupe(paths)}, nil
}

func (s *stub) extraPaths(id string) []string {
	control := os.Getenv("EARS_STUB_CONTROL")
	if control == "" {
		return nil
	}
	data, err := os.ReadFile(control)
	if err != nil {
		return nil
	}
	var doc struct {
		ExtraPaths map[string][]string `json:"extra_paths"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return nil
	}
	return doc.ExtraPaths[id]
}

func dedupe(items []string) []string {
	out := []string{}
	for i, item := range items {
		if i == 0 || item != items[i-1] {
			out = append(out, item)
		}
	}
	return out
}

func (s *stub) proposed(p *Project, id string) (*Manifest, *failure) {
	m, ok := s.readManifest(p, id, "")
	if !ok {
		return nil, fail(4, "change_set.not_found", "Change set "+id+" was not found.")
	}
	if s.approved(p, id) {
		return nil, fail(5, "change_set.not_proposed", "The change set is approved and immutable.")
	}
	return m, nil
}

func (s *stub) changeSetUpdate() (any, []string, *failure) {
	p, f := s.loadProject()
	if f != nil {
		return nil, nil, f
	}
	id := s.opt("change-set")
	m, f := s.proposed(p, id)
	if f != nil {
		return nil, nil, f
	}
	if intent := s.opt("intent"); intent != "" {
		m.Intent = intent
	}
	if base := s.opt("base-commit"); base != "" {
		if base != m.BaseCommit {
			m.ImpactStale = true
		}
		m.BaseCommit = base
	}
	if s.opt("impact-file") == "-" {
		data, _ := io.ReadAll(os.Stdin)
		var list []Assessment
		if json.Unmarshal(data, &list) != nil {
			return nil, nil, fail(4, "change_set.invalid_impact", "The impact file is not valid.")
		}
		m.ImpactAssessment = list
		m.ImpactStale = false
	}
	manifest := s.manifestPath(p, id)
	if err := s.saveYAML(manifest, m); err != nil {
		return nil, nil, fail(6, "io.write_failed", "The manifest cannot be written.")
	}
	status := "complete"
	if m.ImpactStale {
		status = "stale"
	}
	return map[string]any{"change_set_id": id, "assessment_status": status, "changed_paths": []string{manifest}}, []string{manifest}, nil
}

func (s *stub) changeSetCompare() (any, *failure) {
	p, f := s.loadProject()
	if f != nil {
		return nil, f
	}
	id := s.opt("change-set")
	m, ok := s.readManifest(p, id, "")
	if !ok {
		return nil, fail(4, "change_set.not_found", "Change set "+id+" was not found.")
	}
	// An artifact revision carries the registry digests before and after.
	before := map[string]string{}
	if baseProject := s.projectAt(m.BaseCommit); baseProject != nil {
		for _, a := range baseProject.Artifacts {
			before[a.ID] = a.Digest
		}
	}
	after := map[string]string{}
	for _, a := range p.Artifacts {
		after[a.ID] = a.Digest
	}
	changed := []map[string]string{}
	for _, op := range m.ArtifactOperations {
		entry := map[string]string{"action": op.Action, "artifact_id": op.ArtifactID}
		if digest, ok := before[op.ArtifactID]; ok {
			entry["before"] = digest
		}
		if digest, ok := after[op.ArtifactID]; ok {
			entry["after"] = digest
		}
		changed = append(changed, entry)
	}
	for _, op := range m.Operations {
		changed = append(changed, map[string]string{"action": op.Action, "requirement_id": op.RequirementID})
	}
	return map[string]any{
		"change_set_id": id, "against_commit": m.BaseCommit, "changed": changed,
		"exact_duplicates": []any{}, "declared_conflicts": []any{}, "supersession": []any{}, "dependency_cycles": []any{},
		"implementation_required": m.ImplementationRequired,
	}, nil
}

func (s *stub) impact() (any, *failure) {
	p, f := s.loadProject()
	if f != nil {
		return nil, f
	}
	id := s.opt("change-set")
	m, ok := s.readManifest(p, id, "")
	if !ok {
		return nil, fail(4, "change_set.not_found", "Change set "+id+" was not found.")
	}
	status := "complete"
	if m.ImpactStale {
		status = "stale"
	}
	return map[string]any{"change_set_id": id, "against_commit": m.BaseCommit, "candidates": []any{}, "assessment_status": status}, nil
}

func (s *stub) artifactPut() (any, []string, *failure) {
	p, f := s.loadProject()
	if f != nil {
		return nil, nil, f
	}
	id := s.opt("change-set")
	m, f := s.proposed(p, id)
	if f != nil {
		return nil, nil, f
	}
	rel := path.Clean(s.opt("path"))
	if strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, ".git/") || strings.HasPrefix(rel, ".protobot/attestations/") {
		return nil, nil, fail(4, "artifact.write_not_allowed", "The path is not a destination ears-manager writes.")
	}
	content, _ := io.ReadAll(os.Stdin)
	if err := os.MkdirAll(filepath.Dir(s.file(rel)), 0o755); err != nil {
		return nil, nil, fail(6, "io.write_failed", "The artifact cannot be written.")
	}
	if err := os.WriteFile(s.file(rel), content, 0o644); err != nil {
		return nil, nil, fail(6, "io.write_failed", "The artifact cannot be written.")
	}
	artifactID := s.opt("id")
	entry := Artifact{ID: artifactID, Kind: s.opt("kind"), Path: rel, Digest: digest(content), Owner: s.opt("owner")}
	existed := false
	for i := range p.Artifacts {
		if p.Artifacts[i].ID == artifactID {
			p.Artifacts[i] = entry
			existed = true
		}
	}
	if !existed {
		p.Artifacts = append(p.Artifacts, entry)
		sort.Slice(p.Artifacts, func(i, j int) bool { return p.Artifacts[i].ID < p.Artifacts[j].ID })
	}
	action := "add"
	if existed {
		action = "revise"
	}
	recorded := false
	for i := range m.ArtifactOperations {
		if m.ArtifactOperations[i].ArtifactID == artifactID {
			m.ArtifactOperations[i].Digest = entry.Digest
			recorded = true
		}
	}
	if !recorded {
		m.ArtifactOperations = append(m.ArtifactOperations, Op{Action: action, ArtifactID: artifactID, Digest: entry.Digest})
	}
	changed := []string{rel, ".protobot/project.yaml", s.manifestPath(p, id)}
	projection := s.loadProjection()
	classified := false
	for _, c := range projection.Paths {
		if c.Path == rel {
			classified = true
		}
	}
	if !classified {
		projection.Paths = append(projection.Paths, Class{Path: rel, Class: "shared"})
		sort.Slice(projection.Paths, func(i, j int) bool { return projection.Paths[i].Path < projection.Paths[j].Path })
		if err := s.saveYAML(".protobot/projection.yaml", projection); err != nil {
			return nil, nil, fail(6, "io.write_failed", "The projection cannot be written.")
		}
		changed = append(changed, ".protobot/projection.yaml")
	}
	if err := s.saveYAML(".protobot/project.yaml", p); err != nil {
		return nil, nil, fail(6, "io.write_failed", "The project cannot be written.")
	}
	if err := s.saveYAML(s.manifestPath(p, id), m); err != nil {
		return nil, nil, fail(6, "io.write_failed", "The manifest cannot be written.")
	}
	return map[string]any{"artifact": entry, "operation": map[string]string{"action": action, "artifact_id": artifactID}}, changed, nil
}

func (s *stub) check() (any, *failure) {
	p, f := s.loadProject()
	if f != nil {
		return nil, f
	}
	var diagnostics []Diagnostic
	var checked []string
	projection := s.loadProjection()
	classes := map[string]string{}
	for _, c := range projection.Paths {
		classes[c.Path] = c.Class
	}
	for _, a := range p.Artifacts {
		checked = append(checked, a.Path)
		data, err := os.ReadFile(s.file(a.Path))
		if err != nil || digest(data) != a.Digest {
			diagnostics = append(diagnostics, Diagnostic{Code: "artifact.digest_mismatch", Severity: "error", Path: a.Path,
				Message: "The content of the registered path does not match its registry digest."})
		}
		if classes[a.Path] != "shared" {
			diagnostics = append(diagnostics, Diagnostic{Code: "projection.unclassified", Severity: "error", Path: a.Path,
				Message: "The registered path is not classified shared."})
		}
	}
	if len(diagnostics) > 0 {
		sort.Slice(diagnostics, func(i, j int) bool {
			if diagnostics[i].Path != diagnostics[j].Path {
				return diagnostics[i].Path < diagnostics[j].Path
			}
			return diagnostics[i].Code < diagnostics[j].Code
		})
		return nil, fail(4, "validation.failed", "The specification is not valid.", diagnostics...)
	}
	if id := s.opt("change-set"); id != "" {
		if m, ok := s.readManifest(p, id, ""); ok && m.ImpactStale {
			return nil, fail(5, "impact.stale", "The impact assessment is stale.")
		}
	}
	sort.Strings(checked)
	return map[string]any{
		"valid": true, "checked_paths": checked,
		"record_counts": map[string]int{"requirements": 0, "interfaces": 0, "change_sets": 0, "artifacts": len(p.Artifacts)},
	}, nil
}
