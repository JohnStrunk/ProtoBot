package scm

import (
	"path/filepath"
	"strings"

	"github.com/redhat-et/protobot/source-control-manager/internal/gitx"
	"github.com/redhat-et/protobot/source-control-manager/internal/host"
	"github.com/redhat-et/protobot/source-control-manager/internal/jsonx"
	"github.com/redhat-et/protobot/source-control-manager/internal/remoteurl"
	"github.com/redhat-et/protobot/source-control-manager/internal/result"
)

// Branch kinds of repo_state.
const (
	kindChangeSet = "change-set"
	kindDefault   = "default"
	kindOther     = "other"
	kindDetached  = "detached"
)

// States of the local default branch.
const (
	localCurrent       = "current"
	localFastForwarded = "fast-forwarded"
	localBehind        = "behind"
	localDiverged      = "diverged"
)

// Pull-request states that repo_state reports besides the host's.
const (
	prNone        = "none"
	prUnavailable = "unavailable"
	prAmbiguous   = "ambiguous"
)

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// repoState reads the state that the session skill's resume steps need.
func (c *call) repoState() (*outcome, *result.Failure) {
	if !c.proj.Initialized {
		branch, ok, failure := c.currentBranch()
		if failure != nil {
			return nil, failure
		}
		var name any
		if ok {
			name = branch
		}
		data := jsonx.O(jsonx.F("initialized", false), jsonx.F("branch", jsonx.O(jsonx.F("name", name))))
		return &outcome{outcome: result.OutcomeRead, data: data}, nil
	}
	repo := c.config().Repository

	// Step 3: find and check <remote>.
	remote, failure := c.canonicalRemote()
	if failure != nil {
		return nil, failure
	}

	// Step 4: fetch; a fetch that runs and fails is a reported state.
	remoteState := jsonx.O(jsonx.F("name", remote), jsonx.F("reachable", true))
	if failure, ran := c.tryFetch(remote); failure != nil {
		if !ran {
			return nil, failure
		}
		remoteState = jsonx.O(jsonx.F("name", remote), jsonx.F("reachable", false), jsonx.F("code", failure.Code))
	}

	// Step 5: the current branch and its change set.
	defaultHead, hasDefault, err := c.git.Commit("refs/remotes/" + remote + "/" + repo.DefaultBranch)
	if err != nil {
		return nil, c.gitFailure(err)
	}
	branch, onBranch, failure := c.currentBranch()
	if failure != nil {
		return nil, failure
	}
	head, hasHead, err := c.git.Commit("HEAD")
	if err != nil {
		return nil, c.gitFailure(err)
	}
	kind := kindDetached
	if onBranch {
		kind = kindOther
		if branch == repo.DefaultBranch {
			kind = kindDefault
		} else if number, ok := c.changeSetNumber(branch); ok {
			show, _, failure := c.em.ShowChangeSet("CS-"+number, "")
			if failure != nil {
				return nil, failure
			}
			if show != nil {
				kind = kindChangeSet
				c.branch, c.csID, c.show = branch, "CS-"+number, show
				c.object.ChangeSetID = c.csID
			}
		}
	}

	// Step 6: uncommitted changes.
	changeSetPaths := []string{}
	if kind == kindChangeSet && hasHead {
		fs, failure := c.deriveFileSet("HEAD", listMode)
		if failure != nil {
			return nil, failure
		}
		changeSetPaths = fs.listed()
	}
	other, failure := c.otherChanges(changeSetPaths)
	if failure != nil {
		return nil, failure
	}

	// Step 7: the pull request of a change-set branch.
	pull := jsonx.O(jsonx.F("number", nil), jsonx.F("state", prNone), jsonx.F("url", nil), jsonx.F("merge_commit", nil))
	if kind == kindChangeSet {
		pull = c.pullRequestState(branch)
	}

	// Step 8: the local change-set branches.
	branches, failure := c.changeSetBranches(remote)
	if failure != nil {
		return nil, failure
	}

	// The change-set fields, read before step 9, which can move HEAD only
	// when HEAD is the default branch.
	var changeSetFields []jsonx.Field
	if kind == kindChangeSet {
		base := c.show.ChangeSet.BaseCommit
		var moved, mergedIn any
		if hasDefault {
			moved = base != defaultHead
			if hasHead {
				ancestor, err := c.git.IsAncestor(defaultHead, head)
				if err != nil {
					return nil, c.gitFailure(err)
				}
				mergedIn = ancestor
			}
		}
		changeSetFields = []jsonx.Field{
			jsonx.F("change_set_id", c.csID), jsonx.F("base_commit", nullable(base)),
			jsonx.F("default_moved", moved), jsonx.F("default_merged_in", mergedIn),
		}
	}

	// Step 9, last: fast-forward the local default branch. Nothing after
	// it can fail, so a fast-forward never comes with a failed result.
	local, moved, movedHead, failure := c.fastForwardDefault(remote, defaultHead, hasDefault)
	if failure != nil {
		return nil, failure
	}
	var refs []string
	if moved {
		refs = []string{"refs/heads/" + repo.DefaultBranch}
		if movedHead {
			head, hasHead = defaultHead, true
		}
	}

	var headValue any
	if hasHead {
		headValue = head
	}
	var branchName any
	if onBranch {
		branchName = branch
	}
	branchState := jsonx.O(jsonx.F("name", branchName), jsonx.F("kind", kind), jsonx.F("head", headValue))
	branchState = append(branchState, changeSetFields...)
	var defaultValue any
	if hasDefault {
		defaultValue = defaultHead
	}
	data := jsonx.O(
		jsonx.F("initialized", true),
		jsonx.F("project", jsonx.O(
			jsonx.F("id", c.config().Project.ID),
			jsonx.F("default_branch", repo.DefaultBranch),
			jsonx.F("branch_prefix", repo.BranchPrefix),
			jsonx.F("review_mode", repo.ReviewMode),
		)),
		jsonx.F("remote", remoteState),
		jsonx.F("default_branch", jsonx.O(jsonx.F("head", defaultValue), jsonx.F("local", local))),
		jsonx.F("branch", branchState),
		jsonx.F("working_tree", jsonx.O(jsonx.F("change_set_paths", changeSetPaths), jsonx.F("other_paths", other))),
		jsonx.F("pull_request", pull),
		jsonx.F("change_set_branches", branches),
	)
	return &outcome{
		outcome:  result.OutcomeRead,
		data:     data,
		mutation: result.Mutation{Applied: moved, Refs: refs},
	}, nil
}

// hostAdapter returns the host adapter of the project's repository.
func (c *call) hostAdapter() (host.Adapter, bool) {
	repo, err := remoteurl.RepoOf(c.config().Repository.CanonicalRemote)
	if err != nil {
		return nil, false
	}
	return &host.GitHub{Repo: repo, Dir: c.proj.Root, Record: c.git.Record}, true
}

// pullRequestState reads the pull request of a branch for repo_state. A
// host that cannot answer and an ambiguous match are states, not failures.
func (c *call) pullRequestState(branch string) jsonx.Obj {
	state := func(s string) jsonx.Obj {
		return jsonx.O(jsonx.F("number", nil), jsonx.F("state", s), jsonx.F("url", nil), jsonx.F("merge_commit", nil))
	}
	adapter, ok := c.hostAdapter()
	if !ok {
		c.warn(jsonx.F("code", result.HostRequestFailed), jsonx.F("message", "The canonical remote names no host repository."),
			jsonx.F("class", host.ClassOther))
		return state(prUnavailable)
	}
	pulls, herr := adapter.Find(branch, c.config().Repository.DefaultBranch)
	if herr != nil {
		c.warn(jsonx.F("code", hostLookupCode(herr)), jsonx.F("message", "The host cannot answer, so the pull request's state is unavailable."),
			jsonx.F("class", herr.Class))
		return state(prUnavailable)
	}
	switch len(pulls) {
	case 0:
		return state(prNone)
	case 1:
		p := pulls[0]
		return jsonx.O(jsonx.F("number", p.Number), jsonx.F("state", p.State), jsonx.F("url", nullable(p.URL)),
			jsonx.F("merge_commit", nullable(p.MergeCommit)))
	}
	return state(prAmbiguous)
}

// hostLookupCode maps a failed pull-request lookup to its code.
func hostLookupCode(herr *host.Error) result.Code {
	switch herr.Class {
	case host.ClassUnavailable, host.ClassRateLimited:
		return result.HostUnavailable
	case host.ClassCredential:
		return result.CredentialUnavailable
	}
	return result.HostRequestFailed
}

// changeSetBranches lists the local <prefix> branches.
func (c *call) changeSetBranches(remote string) ([]jsonx.Obj, *result.Failure) {
	refs, err := c.git.Refs("refs/heads/" + c.config().Repository.BranchPrefix)
	if err != nil {
		return nil, c.gitFailure(err)
	}
	out := []jsonx.Obj{}
	for _, ref := range refs {
		name := strings.TrimPrefix(ref.Name, "refs/heads/")
		number, ok := c.changeSetNumber(name)
		if !ok {
			continue
		}
		onRemote, err := c.git.RefExists("refs/remotes/" + remote + "/" + name)
		if err != nil {
			return nil, c.gitFailure(err)
		}
		out = append(out, jsonx.O(jsonx.F("change_set_id", "CS-"+number), jsonx.F("branch", name), jsonx.F("on_remote", onRemote)))
	}
	return out, nil
}

// checkedOut reports where a branch is checked out: in this worktree, in
// another one, or in none.
func (c *call) checkedOut(branch string) (here, elsewhere bool, failure *result.Failure) {
	out, err := c.git.ReadRaw(gitx.Opts{}, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return false, false, c.gitFailure(err)
	}
	var worktree string
	for _, line := range gitx.SplitZ(out) {
		switch {
		case strings.HasPrefix(line, "worktree "):
			worktree = strings.TrimPrefix(line, "worktree ")
		case line == "branch refs/heads/"+branch:
			if resolved, err := filepath.EvalSymlinks(worktree); err == nil && resolved == c.proj.Root {
				here = true
			} else {
				elsewhere = true
			}
		}
	}
	return here, elsewhere, nil
}

// fastForwardDefault is repo_state step 9: fast-forward the local default
// branch to <remote>/<default> when that is a fast-forward. It reports the
// state, whether the branch moved, and whether HEAD moved with it.
func (c *call) fastForwardDefault(remote, target string, hasTarget bool) (string, bool, bool, *result.Failure) {
	def := c.config().Repository.DefaultBranch
	localRef := "refs/heads/" + def
	local, hasLocal, err := c.git.Commit(localRef)
	if err != nil {
		return "", false, false, c.gitFailure(err)
	}
	if !hasLocal || !hasTarget {
		return localBehind, false, false, nil
	}
	if local == target {
		return localCurrent, false, false, nil
	}
	behind, err := c.git.IsAncestor(local, target)
	if err != nil {
		return "", false, false, c.gitFailure(err)
	}
	if !behind {
		return localDiverged, false, false, nil
	}
	here, elsewhere, failure := c.checkedOut(def)
	if failure != nil {
		return "", false, false, failure
	}
	if elsewhere && !here {
		return localBehind, false, false, nil
	}
	if failure := c.allowWrite(localRef, false); failure != nil {
		return "", false, false, failure
	}
	if here {
		changes, err := c.git.TrackedChanges()
		if err != nil {
			return "", false, false, c.gitFailure(err)
		}
		if len(changes) > 0 {
			return localBehind, false, false, nil
		}
		// A tag or a local branch named <remote>/<default> would win git's
		// lookup of the short name, so the merge runs only when the name
		// resolves to the remote-tracking ref that was checked.
		short := remote + "/" + def
		if ok, failure := c.unambiguous(short, "refs/remotes/"+short); failure != nil {
			return "", false, false, failure
		} else if !ok {
			return localBehind, false, false, nil
		}
		res, err := c.git.Run(gitx.Opts{Record: true}, "merge", "--ff-only", short)
		if err != nil {
			return "", false, false, c.gitFailure(err)
		}
		if !res.OK() {
			return localBehind, false, false, nil
		}
		if now, _, _ := c.git.Commit(localRef); now != target {
			return "", false, false, result.Fail(result.GitFailed, "The fast-forward moved the default branch to another commit.",
				jsonx.F("expected", target), jsonx.F("found", nullable(now))).WithMutation(result.MutationUnknown)
		}
		return localFastForwarded, true, true, nil
	}
	res, err := c.git.Run(gitx.Opts{Record: true}, "update-ref", localRef, target, local)
	if err != nil {
		return "", false, false, c.gitFailure(err)
	}
	if !res.OK() {
		return localBehind, false, false, nil
	}
	return localFastForwarded, true, false, nil
}
