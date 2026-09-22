package scm

import (
	"strings"

	"github.com/redhat-et/protobot/source-control-manager/internal/host"
	"github.com/redhat-et/protobot/source-control-manager/internal/jsonx"
	"github.com/redhat-et/protobot/source-control-manager/internal/project"
	"github.com/redhat-et/protobot/source-control-manager/internal/result"
)

// approvedMerge derives the merge commit of an approved change set from
// Git and confirms it with the host. It writes nothing.
func (c *call) approvedMerge() (*outcome, *result.Failure) {
	repo := c.config().Repository
	id := c.req.changeSetID
	notApproved := func(reason string) *result.Failure {
		return result.Fail(result.NotApproved, "The change set is not approved on the default branch.",
			jsonx.F("change_set_id", id), jsonx.F("reason", reason))
	}

	// Step 1: fetch, and read the manifest at the default head.
	remote, failure := c.canonicalRemote()
	if failure != nil {
		return nil, failure
	}
	if failure := c.fetch(remote); failure != nil {
		return nil, failure
	}
	defaultHead, ok, err := c.git.Commit("refs/remotes/" + remote + "/" + repo.DefaultBranch)
	if err != nil {
		return nil, c.gitFailure(err)
	}
	if !ok {
		return nil, notApproved("the remote has no default branch")
	}
	show, _, failure := c.em.ShowChangeSet(id, defaultHead)
	if failure != nil {
		return nil, failure
	}
	if show == nil || show.Status != "approved" {
		return nil, notApproved("no approved manifest of the change set is on the default branch")
	}
	c.object.ChangeSetID = id
	manifest := show.ManifestPath
	if manifest == "" {
		manifest = c.config().Stores.ChangeSets + "/" + strings.ToLower(id) + ".yaml"
	}
	if clean, ok := project.CleanRelative(manifest); ok {
		manifest = clean
	}

	// Step 2: the one first-parent commit that added the manifest.
	out, err := c.git.Read("log", "--first-parent", "--diff-filter=A", "--format=%H", defaultHead, "--", manifest)
	if err != nil {
		return nil, c.gitFailure(err)
	}
	adding := lines(out)
	if len(adding) != 1 {
		return nil, result.Fail(result.NotAMergeCommit, "The default branch has no single commit that added the manifest.",
			jsonx.F("change_set_id", id), jsonx.F("adding_commits", adding))
	}
	merge := adding[0]

	// Step 3: that commit must be a merge.
	parents, err := c.git.Read("rev-list", "--parents", "-n", "1", merge)
	if err != nil {
		return nil, c.gitFailure(err)
	}
	if len(strings.Fields(parents)) < 3 {
		return nil, result.Fail(result.NotAMergeCommit, "The commit that added the manifest has one parent.",
			jsonx.F("change_set_id", id), jsonx.F("commit", merge))
	}

	// Step 4: the host's record of the merged pull request.
	adapter, ok := c.hostAdapter()
	if !ok {
		return nil, result.Fail(result.HostUnavailable, "The canonical remote names no host repository.")
	}
	number := strings.TrimPrefix(id, "CS-")
	pulls, herr := adapter.FindMerged(repo.BranchPrefix+number+"-", repo.DefaultBranch)
	if herr != nil {
		if herr.Class == host.ClassCredential {
			return nil, result.Fail(result.CredentialUnavailable, "The credential for the host is missing or expired.",
				jsonx.F("credential_source", credentialSource))
		}
		return nil, result.Fail(result.HostUnavailable, "The host cannot be reached to confirm the merged pull request.")
	}
	if len(pulls) == 0 {
		return nil, notApproved("the host has no merged pull request of the change set")
	}
	if len(pulls) > 1 || pulls[0].MergeCommit != merge {
		var hostCommits []string
		for _, p := range pulls {
			hostCommits = append(hostCommits, p.MergeCommit)
		}
		return nil, result.Fail(result.MergeCommitMismatch, "Git and the host name different merge commits.",
			jsonx.F("change_set_id", id), jsonx.F("git", merge), jsonx.F("host", hostCommits))
	}
	data := jsonx.O(jsonx.F("change_set_id", id), jsonx.F("merge_commit", merge), jsonx.F("default_head", defaultHead))
	return &outcome{outcome: result.OutcomeRead, data: data}, nil
}
