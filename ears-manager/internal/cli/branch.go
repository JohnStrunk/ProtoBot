package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"

	"github.com/redhat-et/protobot/ears-manager/internal/records"
	"github.com/redhat-et/protobot/ears-manager/internal/specvalidation"
)

// `change-set create` writes a new manifest on the change-set branch
// (git-integration.md#change-set-branches). It cuts the branch from the local
// default branch and checks it out, except on the initialization branch,
// which the Source Control Manager's branch_init cut before the project
// existed. ears-manager never fetches: it reads the local default branch,
// which the Source Control Manager's repo_state fast-forwards first.

const (
	defaultChangeSetBranchPrefix = "cs/"
	reservedChangeSetPrefix      = "wi/"
	initializationBranchSuffix   = "00001-project-init"
	initializationChangeSetID    = "CS-00001"
	changeSetSlugLimit           = 40
	lastChangeSetNumber          = 99999
)

// changeSetBranch is where a new change set starts.
type changeSetBranch struct {
	// id is the allocated change-set ID.
	id string
	// name is the short branch name, such as cs/00002-add-the-initial-sketch.
	name string
	// base is the head of the local default branch, a full lowercase hash.
	base string
	// original is the short name of the branch that HEAD was on.
	original string
	// cut is false on the initialization branch, which exists already.
	cut bool
}

// planChangeSetBranch decides the ID, the branch, and the base of a new
// change set, and refuses a repository state that has no safe branch for it.
// It changes nothing.
func planChangeSetBranch(state projectState, intent string) (changeSetBranch, *commandFailure) {
	repository := state.snapshot.Config.Repository
	prefix, failure := changeSetBranchPrefix(repository.BranchPrefix)
	if failure != nil {
		return changeSetBranch{}, failure
	}
	defaultBranch, failure := defaultBranchName(repository.DefaultBranch)
	if failure != nil {
		return changeSetBranch{}, failure
	}
	base, failure := localBranchHead(state.root, defaultBranch)
	if failure != nil {
		return changeSetBranch{}, failure
	}
	original, failure := checkedOutBranch(state.root)
	if failure != nil {
		return changeSetBranch{}, failure
	}
	initBranch := prefix + initializationBranchSuffix
	if original == initBranch && len(state.snapshot.ChangeSets) == 0 {
		// Initialization: the branch exists, and its first change set is
		// CS-00001. The manifest still records the default-branch head.
		descends, failure := isAncestor(state.root, base, state.head)
		if failure != nil {
			return changeSetBranch{}, failure
		}
		if !descends {
			return changeSetBranch{}, conflictFailure("change_set.base_mismatch", fmt.Sprintf("The initialization branch %s does not descend from the default-branch head %s.", initBranch, base), nil)
		}
		return changeSetBranch{id: initializationChangeSetID, name: initBranch, base: base, original: original}, nil
	}
	if !treeHasPath(state.root, base, configPath(state.snapshot)) {
		return changeSetBranch{}, conflictFailure("change_set.unexpected_branch", fmt.Sprintf("The default branch %s holds no project configuration yet. Create change set %s on %s and merge it, then create the next change set.", defaultBranch, initializationChangeSetID, initBranch), nil)
	}
	if failure := requireNoTrackedChanges(state.root); failure != nil {
		return changeSetBranch{}, failure
	}
	highest, failure := highestChangeSetNumber(state, prefix)
	if failure != nil {
		return changeSetBranch{}, failure
	}
	if highest >= lastChangeSetNumber {
		return changeSetBranch{}, conflictFailure("change_set.sequence_exhausted", "No change-set sequence number is available.", nil)
	}
	number := highest + 1
	name := fmt.Sprintf("%s%05d-%s", prefix, number, changeSetSlug(intent))
	if !specvalidation.ValidGitRefName(name) {
		return changeSetBranch{}, internalFailure("the change-set branch name is not a valid Git branch name")
	}
	return changeSetBranch{id: fmt.Sprintf("CS-%05d", number), name: name, base: base, original: original, cut: true}, nil
}

// changeSetBranchPrefix returns repository.branch_prefix, cs/ when it is
// empty, by the rule that check applies to project.yaml.
func changeSetBranchPrefix(value string) (string, *commandFailure) {
	prefix := value
	if prefix == "" {
		prefix = defaultChangeSetBranchPrefix
	}
	if !strings.HasSuffix(prefix, "/") || !specvalidation.ValidGitRefName(strings.TrimSuffix(prefix, "/")) || strings.HasPrefix(prefix, reservedChangeSetPrefix) {
		return "", projectFailure("project.invalid_configuration", "Repository branch prefix is not allowed.")
	}
	return prefix, nil
}

// changeSetSlug derives the branch slug from the intent
// (git-integration.md#one-branch-per-change-set). The intent is lowercased,
// every run of characters other than a-z and 0-9 becomes one hyphen, and a
// hyphen at either end is removed. A longer slug is cut at the last hyphen
// in its first 40 characters, or at 40 characters when they hold no hyphen.
func changeSetSlug(intent string) string {
	var slug strings.Builder
	for _, character := range strings.ToLower(intent) {
		if ('a' <= character && character <= 'z') || ('0' <= character && character <= '9') {
			slug.WriteRune(character)
			continue
		}
		if slug.Len() > 0 && !strings.HasSuffix(slug.String(), "-") {
			slug.WriteByte('-')
		}
	}
	value := strings.TrimSuffix(slug.String(), "-")
	if len(value) > changeSetSlugLimit {
		if cut := strings.LastIndexByte(value[:changeSetSlugLimit], '-'); cut > 0 {
			value = value[:cut]
		} else {
			value = value[:changeSetSlugLimit]
		}
	}
	if value == "" {
		return "change-set"
	}
	return value
}

// highestChangeSetNumber returns the highest change-set number in use: in
// the working tree, at every default-branch ref of the approval rule, and in
// the name of every change-set branch, local or of the canonical remote. So
// the next number names no manifest and no branch that this checkout knows.
func highestChangeSetNumber(state projectState, prefix string) (int, *commandFailure) {
	highest := 0
	note := func(number int) {
		highest = max(highest, number)
	}
	for _, document := range state.snapshot.ChangeSets {
		if number, ok := changeSetNumber(document.Value.ID); ok {
			note(number)
		}
	}
	repository := state.snapshot.Config.Repository
	refs, failure := resolveDefaultBranchRefs(state.root, repository)
	if failure != nil {
		return 0, failure
	}
	store := strings.TrimSuffix(state.snapshot.Config.Stores.WithDefaults().ChangeSets, "/") + "/"
	for _, ref := range refs {
		output, err := gitCommand("--no-replace-objects", "-C", state.root, "ls-tree", "-z", "--name-only", ref, "--", store).Output()
		if err != nil {
			return 0, ioFailure("git.read_failed", fmt.Sprintf("Git could not list the change-set store at %s.", ref))
		}
		for _, name := range strings.Split(string(output), "\x00") {
			if number, ok := manifestNumber(path.Base(name)); ok {
				note(number)
			}
		}
	}
	remotes, failure := canonicalRemoteNames(state.root, repository.CanonicalRemote)
	if failure != nil {
		return 0, failure
	}
	namespaces := []string{"refs/heads/"}
	for _, remote := range remotes {
		namespaces = append(namespaces, "refs/remotes/"+remote+"/")
	}
	output, err := gitCommand(append([]string{"-C", state.root, "for-each-ref", "--format=%(refname)"}, namespaces...)...).Output()
	if err != nil {
		return 0, ioFailure("git.read_failed", "Git could not list the change-set branches.")
	}
	for _, ref := range strings.Split(string(output), "\n") {
		for _, namespace := range namespaces {
			if branch, found := strings.CutPrefix(ref, namespace); found {
				if number, ok := branchNumber(prefix, branch); ok {
					note(number)
				}
			}
		}
	}
	return highest, nil
}

// changeSetNumber returns the sequence number of an ID CS-<nnnnn>.
func changeSetNumber(id string) (int, bool) {
	digits, found := strings.CutPrefix(id, "CS-")
	return fiveDigits(digits, found)
}

// manifestNumber returns the sequence number of a manifest file name
// cs-<nnnnn>.yaml.
func manifestNumber(name string) (int, bool) {
	digits, found := strings.CutPrefix(name, "cs-")
	digits, suffixed := strings.CutSuffix(digits, ".yaml")
	return fiveDigits(digits, found && suffixed)
}

// branchNumber returns the sequence number of a branch <prefix><nnnnn>-...
func branchNumber(prefix, branch string) (int, bool) {
	rest, found := strings.CutPrefix(branch, prefix)
	digits, _, dashed := strings.Cut(rest, "-")
	return fiveDigits(digits, found && dashed)
}

func fiveDigits(digits string, ok bool) (int, bool) {
	if !ok || len(digits) != 5 || strings.Trim(digits, "0123456789") != "" {
		return 0, false
	}
	number, err := strconv.Atoi(digits)
	return number, err == nil
}

// localBranchHead returns the commit of refs/heads/<branch>. The remote-
// tracking ref never counts: the new branch and its manifest name the same
// commit (git-integration.md#when-the-branch-is-created).
func localBranchHead(root, branch string) (string, *commandFailure) {
	output, err := gitCommand("--no-replace-objects", "-C", root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch+"^{commit}").Output()
	commit := strings.ToLower(strings.TrimSpace(string(output)))
	if err != nil || !fullCommitID(commit) {
		return "", projectFailure("project.default_branch_unresolved", fmt.Sprintf("The local default branch %s does not resolve to a commit.", branch))
	}
	return commit, nil
}

// checkedOutBranch returns the short name of the branch that HEAD is on. A
// detached HEAD, as during a rebase or a bisect, is refused.
func checkedOutBranch(root string) (string, *commandFailure) {
	output, err := gitCommand("-C", root, "symbolic-ref", "--quiet", "HEAD").Output()
	var exitErr *exec.ExitError
	if err != nil && (!errors.As(err, &exitErr) || exitErr.ExitCode() != 1) {
		return "", ioFailure("git.read_failed", "Git could not read the branch that HEAD is on.")
	}
	branch, found := strings.CutPrefix(strings.TrimSpace(string(output)), "refs/heads/")
	if err != nil || !found || branch == "" {
		return "", conflictFailure("change_set.unexpected_branch", "HEAD is not on a branch. Check out a branch, then create the change set.", nil)
	}
	return branch, nil
}

// treeHasPath reports whether the tree of commit holds path, with replace
// refs ignored.
func treeHasPath(root, commit, relative string) bool {
	return gitCommand("--no-replace-objects", "-C", root, "rev-parse", "--verify", "--quiet", commit+":"+relative).Run() == nil
}

// requireNoTrackedChanges refuses a tracked file with an uncommitted change,
// staged or not, so a draft of one change set never moves to the branch of
// another. An untracked file stays in the working tree.
func requireNoTrackedChanges(root string) *commandFailure {
	output, err := repositoryGit(root, "status", "--porcelain=v1", "-z", "--untracked-files=no").Output()
	if err != nil {
		return ioFailure("git.read_failed", "Git could not read the status of the working tree.")
	}
	if len(output) > 0 {
		return conflictFailure("change_set.uncommitted_changes", "A tracked file has an uncommitted change. Commit or discard it, then create the change set.", nil)
	}
	return nil
}

// cutChangeSetBranch creates the branch at the default-branch head and
// checks it out. It fails with no change, or restores the branch that HEAD
// was on.
func cutChangeSetBranch(root string, repository records.RepositoryConfig, branch changeSetBranch) *commandFailure {
	if failure := refuseExistingBranch(root, repository, branch.name); failure != nil {
		return failure
	}
	output, err := repositoryGit(root, "switch", "--quiet", "--no-track", "--create", branch.name, branch.base).CombinedOutput()
	if err == nil {
		return nil
	}
	text := strings.TrimSpace(string(output))
	switch {
	case strings.Contains(text, "already exists"):
		return branchExistsFailure(branch.name, "locally")
	case strings.Contains(text, "would be overwritten"):
		// Git checks the tree before it creates the ref, but the rollback
		// keeps the result independent of that order.
		return restoreOriginalBranch(root, branch, conflictFailure("change_set.uncommitted_changes", "An untracked file would be overwritten by the default-branch head. Move or remove it, then create the change set.", nil))
	}
	return restoreOriginalBranch(root, branch, ioFailure("git.write_failed", fmt.Sprintf("Git could not cut and check out branch %s: %s", branch.name, firstLine(text))))
}

// refuseExistingBranch refuses a branch name that exists locally or on the
// canonical remote, as of the last fetch.
func refuseExistingBranch(root string, repository records.RepositoryConfig, name string) *commandFailure {
	remotes, failure := canonicalRemoteNames(root, repository.CanonicalRemote)
	if failure != nil {
		return failure
	}
	local := refExists(root, "refs/heads/"+name)
	remote := false
	for _, each := range remotes {
		remote = remote || refExists(root, "refs/remotes/"+each+"/"+name)
	}
	switch {
	case local && remote:
		return branchExistsFailure(name, "locally and on the canonical remote")
	case local:
		return branchExistsFailure(name, "locally")
	case remote:
		return branchExistsFailure(name, "on the canonical remote")
	}
	return nil
}

func branchExistsFailure(name, where string) *commandFailure {
	return conflictFailure("change_set.branch_exists", fmt.Sprintf("Branch %s already exists %s.", name, where), nil)
}

func refExists(root, ref string) bool {
	return gitCommand("-C", root, "rev-parse", "--verify", "--quiet", ref).Run() == nil
}

// restoreOriginalBranch undoes a cut after a failed create: it checks out the
// original branch again and then deletes the new one, when it still is at the
// base. It never deletes the new branch while HEAD is on it, so a failed
// switch back leaves the new branch checked out. It returns cause when the
// original state is back, and a failure with mutation unknown when it is not.
// After a file write whose own rollback failed, it leaves Git as it is, so the
// caller finds the files on the branch where they were written.
func restoreOriginalBranch(root string, branch changeSetBranch, cause *commandFailure) *commandFailure {
	if cause.Mutation == "unknown" {
		cause.Message = fmt.Sprintf("%s Branch %s is checked out.", cause.Message, branch.name)
		return cause
	}
	ref := "refs/heads/" + branch.name
	if current, failure := checkedOutBranch(root); failure == nil && current == branch.name {
		_ = repositoryGit(root, "switch", "--quiet", "--no-guess", "--", branch.original).Run()
	}
	current, failure := checkedOutBranch(root)
	if failure == nil && current == branch.original && refExists(root, ref) {
		_ = repositoryGit(root, "update-ref", "-d", ref, branch.base).Run()
	}
	if failure == nil && current == branch.original && !refExists(root, ref) {
		return cause
	}
	return unknownIOFailure("git.write_unknown", fmt.Sprintf("%s Branch %s could not be removed, or branch %s could not be checked out again. Check git status and git branch before you retry.", cause.Message, branch.name, branch.original))
}

// repositoryGit builds a Git command that runs no program that a repository
// can ship: no hook and no fsmonitor, as in the Source Control Manager
// (source-control-manager.md#design-principles). It never recurses into a
// submodule, which is another repository. It ignores replace refs, so the
// status it reads and the tree it checks out are those of the commits that
// the refs name, and a new branch starts from the tree of its base commit.
func repositoryGit(root string, args ...string) *exec.Cmd {
	return gitCommand(append([]string{
		"--no-replace-objects",
		"-C", root,
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false",
		"-c", "submodule.recurse=false",
	}, args...)...)
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return strings.TrimSpace(line)
}
