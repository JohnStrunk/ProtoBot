package scm

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/redhat-et/protobot/source-control-manager/internal/gitx"
	"github.com/redhat-et/protobot/source-control-manager/internal/jsonx"
	"github.com/redhat-et/protobot/source-control-manager/internal/project"
	"github.com/redhat-et/protobot/source-control-manager/internal/result"
)

func uncommitted(paths []string) *result.Failure {
	return result.Fail(result.UncommittedChanges, "A tracked file has an uncommitted change.", jsonx.F("paths", paths))
}

// overwrittenFiles parses the files that git refuses to overwrite.
var overwrittenLine = regexp.MustCompile(`(?m)^\t(.+)$`)

func overwritten(stderr []byte) ([]string, bool) {
	text := string(stderr)
	if !strings.Contains(text, "would be overwritten") {
		return nil, false
	}
	var files []string
	for _, m := range overwrittenLine.FindAllStringSubmatch(text, -1) {
		files = append(files, strings.TrimSpace(m[1]))
	}
	sort.Strings(files)
	if files == nil {
		files = []string{}
	}
	return files, true
}

// branchInit cuts the initialization branch before a project exists.
func (c *call) branchInit() (*outcome, *result.Failure) {
	prefix, def := c.req.branchPrefix, c.req.defaultBranch

	// Step 1.
	if _, err := os.Lstat(filepath.Join(c.proj.Root, ".protobot")); err == nil {
		return nil, result.Fail(result.AlreadyInitialized, ".protobot/ exists at the working-tree root.")
	}
	// Step 2.
	if prefix == project.ReservedPrefix {
		return nil, result.Fail(result.ReservedPrefix, "The branch prefix is reserved.", jsonx.F("branch_prefix", prefix))
	}
	// Step 3.
	changes, err := c.git.TrackedChanges()
	if err != nil {
		return nil, c.gitFailure(err)
	}
	if len(changes) > 0 {
		return nil, uncommitted(changes)
	}
	// Step 4: the local default branch, its upstream, and the upstream
	// remote's HEAD.
	notFound := func(reason string) *result.Failure {
		return result.Fail(result.DefaultNotFound, "The local default branch cannot be tied to its upstream.",
			jsonx.F("default_branch", def), jsonx.F("reason", reason))
	}
	localRef := "refs/heads/" + def
	local, ok, err := c.git.Commit(localRef)
	if err != nil {
		return nil, c.gitFailure(err)
	}
	if !ok {
		return nil, notFound("the local default branch does not exist or has no commit")
	}
	upRemotes, err := c.git.ConfigAll("branch." + def + ".remote")
	if err != nil {
		return nil, c.gitFailure(err)
	}
	if len(upRemotes) != 1 || upRemotes[0] == "." || upRemotes[0] == "" {
		return nil, notFound("the local default branch has no upstream branch on a remote")
	}
	upRemote := upRemotes[0]
	// def is a checked branch name, so it cannot read as an option;
	// --symbolic-full-name prints --end-of-options instead of taking it.
	tracking, err := c.git.Read("rev-parse", "--symbolic-full-name", def+"@{upstream}")
	if err != nil || !strings.HasPrefix(tracking, "refs/remotes/"+upRemote+"/") {
		return nil, notFound("the local default branch has no upstream branch on a remote")
	}
	if tracking != "refs/remotes/"+upRemote+"/"+def {
		return nil, notFound("the local default branch tracks a branch of another name")
	}
	remoteHead, ok, err := c.git.SymbolicRef("refs/remotes/" + upRemote + "/HEAD")
	if err != nil {
		return nil, c.gitFailure(err)
	}
	if !ok {
		// A clone made with git init and git remote add has no remote HEAD.
		f := notFound("the upstream remote has no HEAD")
		f.Details = append(f.Details, jsonx.F("next", []string{"git", "remote", "set-head", upRemote, "--auto"}))
		return nil, f
	}
	if remoteHead != tracking {
		return nil, notFound("the local default branch is not the branch that its upstream remote's HEAD names")
	}

	// Step 5: check the upstream remote, and fetch it.
	if failure := c.checkRemote(upRemote, "The remote's URL carries userinfo."); failure != nil {
		return nil, failure
	}
	if failure := c.fetch(upRemote); failure != nil {
		return nil, failure
	}
	target, ok, err := c.git.Commit(tracking)
	if err != nil {
		return nil, c.gitFailure(err)
	}
	if !ok {
		return nil, notFound("the upstream branch no longer exists")
	}

	// Step 6.
	behind := local != target
	if behind {
		ancestor, err := c.git.IsAncestor(local, target)
		if err != nil {
			return nil, c.gitFailure(err)
		}
		if !ancestor {
			return nil, result.Fail(result.DefaultDiverged, "The local default branch has commits that its upstream lacks.",
				jsonx.F("default_branch", def))
		}
	}
	here, elsewhere, failure := c.checkedOut(def)
	if failure != nil {
		return nil, failure
	}
	if behind && elsewhere && !here {
		return nil, result.Fail(result.DefaultDiverged, "The local default branch is behind and checked out in another worktree.",
			jsonx.F("default_branch", def))
	}
	initBranch := prefix + "00001-project-init"
	initRef := "refs/heads/" + initBranch
	localExists, err := c.git.RefExists(initRef)
	if err != nil {
		return nil, c.gitFailure(err)
	}
	remoteExists := false
	remotes, failure := c.remoteURLs()
	if failure != nil {
		return nil, failure
	}
	// The upstream remote is asked directly, so a stale tracking ref of a
	// deleted branch does not count. Before project.yaml exists, #34 lets
	// the SCM reach no other remote, so for those the local tracking ref is
	// what it may read.
	for name := range remotes {
		var exists bool
		if name == upRemote {
			if exists, failure = c.remoteHasRef(upRemote, initRef); failure != nil {
				return nil, failure
			}
		} else if exists, err = c.git.RefExists("refs/remotes/" + name + "/" + initBranch); err != nil {
			return nil, c.gitFailure(err)
		}
		remoteExists = remoteExists || exists
	}
	if localExists || remoteExists {
		where := "both"
		if !remoteExists {
			where = "local"
		} else if !localExists {
			where = "remote"
		}
		return nil, result.Fail(result.BranchExists, "The initialization branch exists.",
			jsonx.F("branch", initBranch), jsonx.F("where", where))
	}

	// The branch is cut from the short name of the local default branch;
	// a tag of the same name would win git's lookup.
	if ok, failure := c.unambiguous(def, localRef); failure != nil {
		return nil, failure
	} else if !ok {
		return nil, ambiguousName(def, localRef)
	}

	// Step 7: fast-forward the local default branch when it is behind.
	var refs []string
	cutFrom := local
	if behind {
		if failure := c.allowWrite(localRef, false); failure != nil {
			return nil, failure
		}
		if here {
			short := strings.TrimPrefix(tracking, "refs/remotes/")
			if ok, failure := c.unambiguous(short, tracking); failure != nil {
				return nil, failure
			} else if !ok {
				return nil, ambiguousName(short, tracking)
			}
			args := []string{"merge", "--ff-only", short}
			res, err := c.git.Run(gitx.Opts{Record: true}, args...)
			if err != nil {
				return nil, c.gitFailure(err)
			}
			if !res.OK() {
				if files, ok := overwritten(res.Stderr); ok {
					return nil, uncommitted(files)
				}
				return nil, commandFailure(args, res)
			}
			if now, _, _ := c.git.Commit(localRef); now != target {
				return nil, result.Fail(result.GitFailed, "The fast-forward moved the default branch to another commit.",
					jsonx.F("expected", target), jsonx.F("found", nullable(now))).WithMutation(result.MutationUnknown)
			}
		} else {
			args := []string{"update-ref", localRef, target, local}
			res, err := c.git.Run(gitx.Opts{Record: true}, args...)
			if err != nil {
				return nil, c.gitFailure(err)
			}
			if !res.OK() {
				return nil, commandFailure(args, res)
			}
		}
		refs = append(refs, localRef)
		cutFrom = target
	}

	// Step 8: create and check out the initialization branch.
	if failure := c.allowWrite(initRef, false); failure != nil {
		return nil, failure.WithMutation(partialIf(refs))
	}
	args := []string{"switch", "--no-track", "--create", initBranch, def}
	res, err := c.git.Run(gitx.Opts{Record: true}, args...)
	if err != nil {
		return nil, c.gitFailure(err).WithMutation(partialIf(refs))
	}
	if !res.OK() {
		var f *result.Failure
		if files, ok := overwritten(res.Stderr); ok {
			f = uncommitted(files)
		} else {
			f = commandFailure(args, res)
		}
		if len(refs) > 0 {
			f.Details = append(f.Details, jsonx.F("refs", refs))
		}
		return nil, f.WithMutation(partialIf(refs))
	}
	refs = append(refs, initRef)
	return &outcome{
		outcome:  result.OutcomeApplied,
		data:     jsonx.O(jsonx.F("branch", initBranch), jsonx.F("cut_from", cutFrom)),
		mutation: result.Mutation{Applied: true, Refs: refs},
	}, nil
}

func partialIf(refs []string) string {
	if len(refs) > 0 {
		return result.MutationPartial
	}
	return result.MutationNone
}

// branchResume switches HEAD to the one local branch of a change set.
func (c *call) branchResume() (*outcome, *result.Failure) {
	id := c.req.changeSetID
	number := strings.TrimPrefix(id, "CS-")
	refs, err := c.git.Refs("refs/heads/" + c.config().Repository.BranchPrefix)
	if err != nil {
		return nil, c.gitFailure(err)
	}
	var matches []gitx.Ref
	for _, ref := range refs {
		if n, ok := c.changeSetNumber(strings.TrimPrefix(ref.Name, "refs/heads/")); ok && n == number {
			matches = append(matches, ref)
		}
	}
	notFound := func(fields ...jsonx.Field) *result.Failure {
		return result.Fail(result.ChangeSetNotFound, "No local branch holds the change set.",
			append([]jsonx.Field{jsonx.F("change_set_id", id)}, fields...)...)
	}
	switch len(matches) {
	case 0:
		return nil, notFound()
	case 1:
	default:
		var names []string
		for _, m := range matches {
			names = append(names, strings.TrimPrefix(m.Name, "refs/heads/"))
		}
		return nil, result.Fail(result.AmbiguousBranch, "More than one local branch carries the change-set number.",
			jsonx.F("change_set_id", id), jsonx.F("branches", names))
	}
	branch := strings.TrimPrefix(matches[0].Name, "refs/heads/")
	tip := matches[0].Object
	show, call, failure := c.em.ShowChangeSet(id, tip)
	if failure != nil {
		return nil, failure
	}
	if show == nil {
		return nil, notFound(jsonx.F("envelope", call.Raw))
	}
	c.object.ChangeSetID = id
	data := jsonx.O(jsonx.F("branch", branch), jsonx.F("head", tip))

	current, onBranch, failure := c.currentBranch()
	if failure != nil {
		return nil, failure
	}
	if onBranch && current == branch {
		return &outcome{outcome: result.OutcomeUnchanged, data: data}, nil
	}
	changes, err := c.git.TrackedChanges()
	if err != nil {
		return nil, c.gitFailure(err)
	}
	if len(changes) > 0 {
		return nil, uncommitted(changes)
	}
	args := []string{"switch", branch}
	res, err := c.git.Run(gitx.Opts{Record: true}, args...)
	if err != nil {
		return nil, c.gitFailure(err)
	}
	if !res.OK() {
		if files, ok := overwritten(res.Stderr); ok {
			return nil, uncommitted(files)
		}
		f := commandFailure(args, res)
		if now, ok, _ := c.currentBranch(); !ok || now != current || !onBranch {
			f = f.WithMutation(result.MutationUnknown)
		}
		return nil, f
	}
	return &outcome{outcome: result.OutcomeApplied, data: data, mutation: result.Mutation{Applied: true}}, nil
}
