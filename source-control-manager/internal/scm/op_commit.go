package scm

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/redhat-et/protobot/source-control-manager/internal/ears"
	"github.com/redhat-et/protobot/source-control-manager/internal/gitx"
	"github.com/redhat-et/protobot/source-control-manager/internal/jsonx"
	"github.com/redhat-et/protobot/source-control-manager/internal/project"
	"github.com/redhat-et/protobot/source-control-manager/internal/remoteurl"
	"github.com/redhat-et/protobot/source-control-manager/internal/render"
	"github.com/redhat-et/protobot/source-control-manager/internal/result"
)

// Warning codes. They appear only in diagnostics, never as a failure.
const (
	warnProjectionPolicy = "PROJECTION_POLICY_EDIT"
	warnImpactIncomplete = "IMPACT_ASSESSMENT_INCOMPLETE"
	warnDigestOutside    = "DIGEST_MISMATCH_OUTSIDE_FILE_SET"
	warnIndexNotUpdated  = "INDEX_NOT_UPDATED"
)

// digestMismatchCode is the ears-manager diagnostic of #110 for a
// registered path whose content does not match its registry digest.
const digestMismatchCode = "artifact.digest_mismatch"

// inProgress are the operations that a commit could neither conclude nor
// move the branch under.
var inProgress = []struct{ ref, name string }{
	{"MERGE_HEAD", "merge"},
	{"CHERRY_PICK_HEAD", "cherry-pick"},
	{"REVERT_HEAD", "revert"},
}

// commit writes one commit of the change set's file set on its branch.
func (c *call) commit() (*outcome, *result.Failure) {
	// Step 1.
	if c.branch == c.initBranch() {
		if failure := c.checkInitRemote(); failure != nil {
			return nil, failure
		}
	}
	for _, op := range inProgress {
		if _, ok, err := c.git.Commit(op.ref); err != nil {
			return nil, c.gitFailure(err)
		} else if ok {
			return nil, result.Fail(result.UncommittedChanges, "A "+op.name+" is in progress.", jsonx.F("in_progress", op.name))
		}
	}
	parent, ok, err := c.git.Commit("HEAD")
	if err != nil {
		return nil, c.gitFailure(err)
	}
	if !ok {
		return nil, result.Fail(result.Internal, "The change-set branch has no commit.")
	}

	// Step 2: derive the file set and keep the changed paths.
	fs, failure := c.deriveFileSet(parent, strictMode)
	if failure != nil {
		return nil, failure
	}
	bad, failure := c.unstageable(parent, fs.changed)
	if failure != nil {
		return nil, failure
	}
	if len(bad) > 0 {
		return nil, notStageable(bad)
	}
	if len(fs.changed) == 0 {
		return nil, result.Fail(result.NothingToCommit, "No path of the change set differs from HEAD.")
	}
	if fs.projectionPolicy {
		c.warn(jsonx.F("code", warnProjectionPolicy),
			jsonx.F("message", "projection.yaml holds a policy edit, which stays out of the commit."),
			jsonx.F("paths", []string{project.ProjectionPath}))
	}
	paths := fs.changed

	// Step 3: record the content, then run the pre-stage digest check.
	recorded := map[string]content{}
	for _, p := range paths {
		now, isFile, err := c.readContent(p)
		if err != nil || !isFile {
			return nil, notStageable([]string{p})
		}
		recorded[p] = now
	}
	if failure := c.digestCheck(fs.paths); failure != nil {
		return nil, failure
	}

	// Step 4: render the message.
	intent := c.show.ChangeSet.Intent
	message := render.CommitMessage(c.csID, intent, c.req.body)
	// The check runs on the intent and on the message as rendered, so no
	// text can turn into a keyword on the way.
	if render.ActsOnGitHub(intent) || render.ActsOnGitHub(message) {
		return nil, result.Fail(result.UnsafeText, "The intent holds text that GitHub acts on.", jsonx.F("field", "intent"))
	}

	// Step 5: stage and commit exactly those paths, in a private index.
	return c.writeCommit(parent, paths, recorded, message)
}

// checkInitRemote refuses a commit on the initialization branch unless the
// canonical remote is the fetch URL of the upstream remote of the default
// branch, so a fork's URL never becomes the project's canonical remote.
func (c *call) checkInitRemote() *result.Failure {
	def := c.config().Repository.DefaultBranch
	upRemotes, err := c.git.ConfigAll("branch." + def + ".remote")
	if err != nil {
		return c.gitFailure(err)
	}
	var upstream string
	if len(upRemotes) == 1 && upRemotes[0] != "." {
		upstream = upRemotes[0]
	}
	if upstream != "" {
		urls, err := c.git.ConfigAll("remote." + upstream + ".url")
		if err != nil {
			return c.gitFailure(err)
		}
		if len(urls) == 1 && remoteurl.Parse(urls[0]).Stripped() == c.config().Repository.CanonicalRemote {
			return nil
		}
	}
	next := "the user discards the uncommitted .protobot/ and runs project init again with the URL of the upstream remote of " + def
	if upstream != "" {
		next = "the user discards the uncommitted .protobot/ and runs project init again with the URL of " + upstream
	}
	return result.Fail(result.InitRemoteMismatch, "The canonical remote is not the upstream remote of the default branch.",
		jsonx.F("upstream_remote", nullable(upstream)), jsonx.F("next", next))
}

// digestCheck runs `ears-manager check --change-set` and maps its answer.
func (c *call) digestCheck(fileSet []string) *result.Failure {
	call, failure := c.em.Check(c.csID)
	if failure != nil {
		return failure
	}
	checkDetails := func() jsonx.Obj {
		return jsonx.O(jsonx.F("exit", call.Status), jsonx.F("diagnostic_codes", call.Codes()), jsonx.F("envelope", call.Raw))
	}
	switch {
	case call.Status == ears.StatusOK && call.Env.OK:
		return nil
	case ears.ToolStatus(call.Status):
		return ears.ToolFailure(call)
	case call.Status != ears.StatusValidation && call.Status != ears.StatusConflict:
		return ears.ToolFailure(call)
	}

	// Status 4 is a specification that is not valid. Status 5 is an
	// incomplete or stale impact assessment, which is a warning only when
	// it comes alone: a digest mismatch that #34 compares refuses the
	// commit whichever status carries it.

	var errorDiagnostics []ears.Diagnostic
	if call.Env.Error != nil {
		errorDiagnostics = call.Env.Error.Diagnostics
	}
	inSet, stores, outside, otherError := classifyDigests(errorDiagnostics, c.config(), fileSet)
	if len(inSet) > 0 {
		reapply := "ears-manager artifact put"
		if len(stores) > 0 {
			reapply = "ears-manager artifact put, or the matching requirement or interface subcommand"
		}
		details := []jsonx.Field{
			jsonx.F("paths", inSet),
			jsonx.F("check", checkDetails()),
			jsonx.F("routes", jsonx.O(
				jsonx.F("discard", append([]string{"git", "checkout", "--"}, inSet...)),
				jsonx.F("reapply", reapply),
			)),
		}
		// The discard restores the tracked records of a store, but a
		// record added outside ears-manager is untracked and stays, so
		// the details name it for the user to remove.
		if len(stores) > 0 {
			args := append([]string{"ls-files", "--others", "-z", "--"}, stores...)
			out, err := c.git.ReadRaw(gitx.Opts{}, args...)
			if err != nil {
				return c.gitFailure(err)
			}
			if untracked := gitx.SplitZ(out); len(untracked) > 0 {
				sort.Strings(untracked)
				details = append(details, jsonx.F("untracked", untracked))
			}
		}
		return result.Fail(result.SpecDigestMismatch, "A registered artifact or a structured store does not match its digest.", details...)
	}
	if call.Status == ears.StatusValidation && (otherError || len(outside) == 0) {
		return result.Fail(result.SpecCheckFailed, "ears-manager check failed.", jsonx.F("check", checkDetails()))
	}
	if len(outside) > 0 {
		c.warn(jsonx.F("code", warnDigestOutside),
			jsonx.F("message", "A registered path outside the file set does not match its registry digest; the commit leaves it out."),
			jsonx.F("paths", outside), jsonx.F("envelope", call.Raw))
	}
	if call.Status == ears.StatusConflict {
		// A draft can be committed before impact review ends; CI gates the
		// merge on the assessment.
		c.warn(jsonx.F("code", warnImpactIncomplete),
			jsonx.F("message", "The impact assessment is incomplete or stale; CI gates the merge on it."),
			jsonx.F("envelope", call.Raw))
	}
	return nil
}

// storeDigestMismatchCode is the validator's code for a structured store,
// such as the requirement store, whose record set does not match the
// store_digests entry of project.yaml.
const storeDigestMismatchCode = "project.store_digest_mismatch"

// classifyDigests sorts the error diagnostics of check. A digest mismatch
// maps to the artifact or store that it names: an artifact by its
// record_id in the registry, a store by its store_digests.<store> field.
// The diagnostic's own path names project.yaml, where the digest lives, so
// it is read only as the artifact path of an older result shape. #34
// compares every structured store and the artifacts that the change set
// touches, so a store mismatch, or an artifact of the file set, goes to
// inSet, and the stores among them also to stores; an artifact outside
// the file set goes to outside; any other error sets otherError.
func classifyDigests(diagnostics []ears.Diagnostic, config *project.Config, fileSet []string) (inSet, stores, outside []string, otherError bool) {
	for _, d := range diagnostics {
		if d.Severity == "warning" || d.Severity == "info" {
			continue
		}
		p, store, ok := digestPath(d, config)
		switch {
		case !ok:
			otherError = true
		case store:
			inSet = append(inSet, p)
			stores = append(stores, p)
		case contains(fileSet, p):
			inSet = append(inSet, p)
		default:
			outside = append(outside, p)
		}
	}
	return dedupeSorted(inSet), dedupeSorted(stores), dedupeSorted(outside), otherError
}

// digestPath returns the artifact or store path of a digest mismatch, and
// whether it is a store.
func digestPath(d ears.Diagnostic, config *project.Config) (string, bool, bool) {
	switch d.Code {
	case digestMismatchCode:
		if d.RecordID != "" {
			for _, artifact := range config.Artifacts {
				if artifact.ID == d.RecordID {
					return artifact.Path, false, true
				}
			}
			return "", false, false
		}
		if p, ok := project.CleanRelative(d.Path); ok && p != project.ConfigPath {
			return p, false, true
		}
	case storeDigestMismatchCode:
		switch d.Field {
		case "store_digests.requirements":
			return config.Stores.Requirements, true, true
		case "store_digests.interfaces":
			return config.Stores.Interfaces, true, true
		case "store_digests.change_sets":
			return config.Stores.ChangeSets, true, true
		}
	}
	return "", false, false
}

func dedupeSorted(items []string) []string {
	sort.Strings(items)
	var out []string
	for i, item := range items {
		if i == 0 || item != items[i-1] {
			out = append(out, item)
		}
	}
	return out
}

// writeCommit is commit step 5. The SCM never stages into the user's
// index: it builds the commit from a private index and Git's plumbing
// commands, then moves the branch with a compare-and-swap.
func (c *call) writeCommit(parent string, paths []string, recorded map[string]content, message string) (*outcome, *result.Failure) {
	tmp, err := os.MkdirTemp("", "scm-index-")
	if err != nil {
		return nil, result.Fail(result.Internal, "The private index cannot be created.")
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(tmp, "index")}

	run := func(record bool, stdin []byte, args ...string) (gitx.Result, *result.Failure) {
		res, err := c.git.Run(gitx.Opts{Env: env, Record: record, Stdin: stdin}, args...)
		if err != nil {
			return res, c.gitFailure(err)
		}
		if !res.OK() {
			return res, commandFailure(args, res)
		}
		return res, nil
	}
	if _, failure := run(false, nil, "read-tree", parent); failure != nil {
		return nil, failure
	}
	if _, failure := run(true, nil, append([]string{"add", "--"}, paths...)...); failure != nil {
		return nil, failure
	}

	staged, err := c.git.IndexEntries(paths, env)
	if err != nil {
		return nil, c.gitFailure(err)
	}
	// The staged entry must hold the recorded content as a regular file:
	// a symbolic link with the same text as its target has the same blob.
	var changed []string
	for _, p := range paths {
		want := recorded[p]
		entry, ok := staged[p]
		regular := entry.Mode == "100644" || entry.Mode == "100755"
		if want.present != ok || (ok && (!regular || c.blobName(want.data) != entry.Object)) {
			changed = append(changed, p)
		}
	}
	if len(changed) > 0 {
		return nil, result.Fail(result.StagedContentChanged, "A staged file differs from the content that check saw.",
			jsonx.F("paths", changed))
	}
	diff, failure := run(false, nil, "diff-index", "--cached", "--name-only", "-z", parent)
	if failure != nil {
		return nil, failure
	}
	for _, p := range gitx.SplitZ(diff.Stdout) {
		if !contains(paths, p) {
			return nil, result.Fail(result.Internal, "The private index differs from HEAD outside the file set.")
		}
	}
	tree, failure := run(false, nil, "write-tree")
	if failure != nil {
		return nil, failure
	}
	sign, err := c.git.ConfigBool("commit.gpgSign")
	if err != nil {
		return nil, c.gitFailure(err)
	}
	args := []string{"commit-tree", tree.Text(), "-p", parent}
	if sign {
		args = append(args, "-S")
	}
	args = append(args, "-F", "-")
	created, failure := run(true, []byte(message), args...)
	if failure != nil {
		return nil, failure
	}
	commit := created.Text()

	ref := "refs/heads/" + c.branch
	if failure := c.allowWrite(ref, false); failure != nil {
		return nil, failure
	}
	updateArgs := []string{"update-ref", ref, commit, parent}
	res, err := c.git.Run(gitx.Opts{Record: true}, updateArgs...)
	if err != nil {
		return nil, c.gitFailure(err).WithMutation(result.MutationUnknown)
	}
	if !res.OK() {
		found, _, _ := c.git.Commit(ref)
		return nil, result.Fail(result.GitFailed, "The branch moved while the call ran.",
			jsonx.F("command", append([]string{"git"}, updateArgs...)),
			jsonx.F("status", res.Status),
			jsonx.F("expected", parent),
			jsonx.F("found", nullable(found)))
	}

	resetArgs := append([]string{"reset", "--quiet", "--"}, paths...)
	reset, err := c.git.Run(gitx.Opts{Record: true}, resetArgs...)
	if err != nil || !reset.OK() {
		c.warn(jsonx.F("code", warnIndexNotUpdated),
			jsonx.F("message", "The commit stands, but the index entries of its paths were not updated; run the command to update them."),
			jsonx.F("paths", paths),
			jsonx.F("command", append([]string{"git", "reset", "--"}, paths...)))
	}

	data := jsonx.O(
		jsonx.F("commit", commit),
		jsonx.F("parent", parent),
		jsonx.F("subject", render.Subject(c.csID, c.show.ChangeSet.Intent)),
		jsonx.F("trailer", render.Trailer(c.csID)),
		jsonx.F("paths", paths),
	)
	return &outcome{outcome: result.OutcomeApplied, data: data, mutation: result.Mutation{Applied: true, Refs: []string{ref}}}, nil
}
