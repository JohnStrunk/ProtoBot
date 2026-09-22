// Package scm is the Source Control Manager core and its two faces: the
// Drafting Table face (repo_state, branch_init, branch_resume, commit,
// publish, and refresh) and the approved-state read face (approved_merge).
//
// Every call runs checks 1 to 5 of "Checks on every call" before the
// operation's own steps, and check 6 before every write. The SCM keeps no
// state between calls: each call resolves the project, the change set, and
// the refs again.
package scm

import (
	"regexp"
	"strings"
	"sync"

	"github.com/redhat-et/protobot/source-control-manager/internal/ears"
	"github.com/redhat-et/protobot/source-control-manager/internal/gitx"
	"github.com/redhat-et/protobot/source-control-manager/internal/jsonx"
	"github.com/redhat-et/protobot/source-control-manager/internal/project"
	"github.com/redhat-et/protobot/source-control-manager/internal/result"
)

// Operation names, as MCP tools name them.
const (
	OpRepoState     = "repo_state"
	OpBranchInit    = "branch_init"
	OpBranchResume  = "branch_resume"
	OpCommit        = "commit"
	OpPublish       = "publish"
	OpRefresh       = "refresh"
	OpApprovedMerge = "approved_merge"
)

var faceOperations = map[string][]string{
	result.FaceDraftingTable: {OpRepoState, OpBranchInit, OpBranchResume, OpCommit, OpPublish, OpRefresh},
	result.FaceApprovedState: {OpApprovedMerge},
}

// Operations returns the operations of a face, in their fixed order.
func Operations(face string) []string {
	return append([]string(nil), faceOperations[face]...)
}

// FaceOf returns the face that serves an operation.
func FaceOf(operation string) (string, bool) {
	for face, ops := range faceOperations {
		for _, op := range ops {
			if op == operation {
				return face, true
			}
		}
	}
	return "", false
}

func inFace(face, operation string) bool {
	for _, op := range faceOperations[face] {
		if op == operation {
			return true
		}
	}
	return false
}

// Server serves calls for one working directory. It holds no state between
// calls, and it runs one call at a time.
type Server struct {
	// Dir is the directory the project is resolved from.
	Dir string

	mu sync.Mutex
}

// Invocation is one call.
type Invocation struct {
	Face      string
	Operation string
	// Args are the request fields, as JSON decodes them.
	Args map[string]any
	// ArgsNotObject reports arguments that are not a JSON object.
	ArgsNotObject bool
	// Trace is the OpenTelemetry trace context of the MCP request, if any.
	Trace any
}

// Invoke runs one call and returns its result envelope.
func (s *Server) Invoke(inv Invocation) result.Envelope {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := &call{op: inv.Operation, face: inv.Face, dir: s.Dir}
	defer c.close()
	out, failure := c.run(inv.Args, inv.ArgsNotObject)
	if failure != nil {
		return result.Failed(c.op, c.face, c.object, failure, c.commands(), inv.Trace)
	}
	return result.Success(c.op, c.face, c.object, out.outcome, out.data, c.diagnostics, c.commands(), out.mutation, inv.Trace)
}

// outcome is what a successful operation returns.
type outcome struct {
	outcome  string
	data     any
	mutation result.Mutation
}

// call is the state of one call. It lives only for the call.
type call struct {
	op, face string
	dir      string
	req      request
	proj     *project.Project
	git      *gitx.Runner
	em       *ears.Client
	object   result.Object

	diagnostics []any

	// The change set of the current branch, for commit, publish, and
	// refresh.
	branch string
	csID   string
	show   *ears.Show

	objectFormat string
}

func (c *call) close() {
	if c.git != nil {
		c.git.Close()
	}
}

func (c *call) commands() [][]string {
	if c.git == nil {
		return nil
	}
	return c.git.Recorded()
}

func (c *call) warn(fields ...jsonx.Field) {
	c.diagnostics = append(c.diagnostics, jsonx.O(fields...))
}

func (c *call) config() *project.Config { return c.proj.Config }

// run performs checks 1 to 5 and then the operation.
func (c *call) run(args map[string]any, argsNotObject bool) (*outcome, *result.Failure) {
	// Check 1: the operation is one of the face's.
	if !inFace(c.face, c.op) {
		return nil, result.Fail(result.UnauthorizedAction, "The face does not serve this operation.",
			jsonx.F("face", c.face), jsonx.F("operation", c.op))
	}
	// Check 2, the trusted Gate context, applies to the hosted face only.
	// This executable serves stdio and the CLI, which are never hosted.

	// Check 3: the request.
	if argsNotObject {
		return nil, result.Fail(result.InvalidRequest, "The arguments are not an object.", jsonx.F("field", "arguments"))
	}
	req, failure := validate(c.op, args)
	if failure != nil {
		return nil, failure
	}
	c.req = req

	// Check 4: the project.
	proj, failure := project.Resolve(c.dir)
	if failure != nil {
		return nil, failure
	}
	if !proj.Initialized && c.op != OpRepoState && c.op != OpBranchInit {
		return nil, result.Fail(result.ProjectNotFound, "No .protobot/project.yaml was found on the walk up.",
			jsonx.F("expected", project.ConfigPath))
	}
	c.proj = proj
	c.object.ProjectID = proj.ID()
	runner, err := gitx.New(proj.Root)
	if err != nil {
		return nil, result.Fail(result.Internal, "The SCM cannot run git.")
	}
	c.git = runner
	c.em = &ears.Client{Dir: proj.Root}
	format, err := c.git.ObjectFormat()
	if err != nil {
		return nil, c.gitFailure(err)
	}
	c.objectFormat = format

	// Check 5: the object.
	switch c.op {
	case OpCommit, OpPublish, OpRefresh:
		if failure := c.resolveChangeSetBranch(); failure != nil {
			return nil, failure
		}
	}

	switch c.op {
	case OpRepoState:
		return c.repoState()
	case OpBranchInit:
		return c.branchInit()
	case OpBranchResume:
		return c.branchResume()
	case OpCommit:
		return c.commit()
	case OpPublish:
		return c.publish()
	case OpRefresh:
		return c.refresh()
	case OpApprovedMerge:
		return c.approvedMerge()
	}
	return nil, result.Fail(result.Internal, "The operation has no implementation.", jsonx.F("operation", c.op))
}

// currentBranch returns the branch that HEAD names, and false for a
// detached HEAD.
func (c *call) currentBranch() (string, bool, *result.Failure) {
	target, ok, err := c.git.SymbolicRef("HEAD")
	if err != nil {
		return "", false, c.gitFailure(err)
	}
	if !ok || !strings.HasPrefix(target, "refs/heads/") {
		return "", false, nil
	}
	return strings.TrimPrefix(target, "refs/heads/"), true, nil
}

// changeSetNumber parses <prefix><nnnnn>-<slug>.
func (c *call) changeSetNumber(branch string) (string, bool) {
	prefix := c.config().Repository.BranchPrefix
	pattern := regexp.MustCompile(`^` + regexp.QuoteMeta(prefix) + `([0-9]{5})-(.+)$`)
	m := pattern.FindStringSubmatch(branch)
	if m == nil {
		return "", false
	}
	return m[1], true
}

func (c *call) initBranch() string {
	return c.config().Repository.BranchPrefix + "00001-project-init"
}

// resolveChangeSetBranch is check 5 for commit, publish, and refresh: the
// change set comes from the current branch and exists in the store of the
// working tree.
func (c *call) resolveChangeSetBranch() *result.Failure {
	notCS := func(branch any) *result.Failure {
		return result.Fail(result.NotAChangeSetBranch, "The current branch is not a change-set branch of the project.",
			jsonx.F("branch", branch))
	}
	branch, ok, failure := c.currentBranch()
	if failure != nil {
		return failure
	}
	if !ok {
		return notCS(nil)
	}
	number, ok := c.changeSetNumber(branch)
	if !ok {
		return notCS(branch)
	}
	id := "CS-" + number
	show, _, failure := c.em.ShowChangeSet(id, "")
	if failure != nil {
		return failure
	}
	if show == nil {
		return notCS(branch)
	}
	c.branch, c.csID, c.show = branch, id, show
	c.object.ChangeSetID = id
	return nil
}

// gitFailure maps an unexpected git failure to GIT_FAILED. The details
// hold the command and its status, never its output, which can name a
// remote URL.
func (c *call) gitFailure(err error) *result.Failure {
	if cmdErr, ok := err.(*gitx.CommandError); ok {
		return result.Fail(result.GitFailed, "A git command failed.",
			jsonx.F("command", append([]string{"git"}, cmdErr.Args...)), jsonx.F("status", cmdErr.Status))
	}
	return result.Fail(result.GitFailed, "git did not run.")
}

// commandFailure is GIT_FAILED for a command that ran and exited non-zero.
func commandFailure(args []string, res gitx.Result) *result.Failure {
	return result.Fail(result.GitFailed, "A git command failed.",
		jsonx.F("command", append([]string{"git"}, args...)), jsonx.F("status", res.Status))
}

// allowWrite is check 6: the ref that a write changes is in the ref policy
// of the drafting-table role.
func (c *call) allowWrite(ref string, onRemote bool) *result.Failure {
	allowed := false
	if c.face == result.FaceDraftingTable {
		repo := c.proj.Config
		switch {
		case c.branch != "" && ref == "refs/heads/"+c.branch:
			allowed = true
		case !onRemote && repo == nil && c.op == OpBranchInit && ref == "refs/heads/"+c.req.branchPrefix+"00001-project-init":
			allowed = true
		case !onRemote && repo == nil && c.op == OpBranchInit && ref == "refs/heads/"+c.req.defaultBranch:
			allowed = true
		case !onRemote && repo != nil && ref == "refs/heads/"+repo.Repository.DefaultBranch && c.op == OpRepoState:
			allowed = true
		}
	}
	if !allowed {
		return result.Fail(result.UnauthorizedAction, "The ref policy of the role does not permit this write.",
			jsonx.F("context", "ref_policy"), jsonx.F("ref", ref))
	}
	return nil
}
