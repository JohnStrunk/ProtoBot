package scm

import (
	"sort"
	"strings"

	"github.com/redhat-et/protobot/source-control-manager/internal/gitx"
	"github.com/redhat-et/protobot/source-control-manager/internal/jsonx"
	"github.com/redhat-et/protobot/source-control-manager/internal/refname"
	"github.com/redhat-et/protobot/source-control-manager/internal/remoteurl"
	"github.com/redhat-et/protobot/source-control-manager/internal/result"
)

// remoteURLs returns the configured fetch URLs of every remote, by name.
func (c *call) remoteURLs() (map[string][]string, *result.Failure) {
	args := []string{"config", "--null", "--get-regexp", `^remote\..*\.url$`}
	res, err := c.git.Run(gitx.Opts{}, args...)
	if err != nil {
		return nil, c.gitFailure(err)
	}
	urls := map[string][]string{}
	if res.Status == 1 {
		return urls, nil
	}
	if !res.OK() {
		return nil, commandFailure(args, res)
	}
	for _, item := range gitx.SplitZ(res.Stdout) {
		key, value, _ := strings.Cut(item, "\n")
		name := strings.TrimSuffix(strings.TrimPrefix(key, "remote."), ".url")
		if validRemoteName(name) {
			urls[name] = append(urls[name], value)
		}
	}
	return urls, nil
}

// validRemoteName refuses a remote name that git could read as an option,
// or that is not a valid ref component.
func validRemoteName(name string) bool {
	return !strings.HasPrefix(name, "-") && refname.ValidBranch(name)
}

// canonicalRemote finds <remote>, the remote whose configured fetch URL,
// with any userinfo other than the fixed git@ of the SCP form removed,
// equals repository.canonical_remote, and checks it.
func (c *call) canonicalRemote() (string, *result.Failure) {
	urls, failure := c.remoteURLs()
	if failure != nil {
		return "", failure
	}
	canonical := c.config().Repository.CanonicalRemote
	var names []string
	for name, list := range urls {
		for _, url := range list {
			if remoteurl.Parse(url).Stripped() == canonical {
				names = append(names, name)
				break
			}
		}
	}
	if len(names) == 0 {
		return "", result.Fail(result.RemoteNotFound, "No remote has the canonical URL.")
	}
	sort.Strings(names)
	name := names[0]
	if failure := c.checkRemote(name, "The canonical remote's URL carries userinfo."); failure != nil {
		return "", failure
	}
	return name, nil
}

// checkRemote refuses a remote whose configured or effective URL carries
// userinfo, or whose push can go somewhere that a fetch does not read.
// Both errors name the remote, never a URL.
func (c *call) checkRemote(name, credentialMessage string) *result.Failure {
	if !validRemoteName(name) {
		return result.Fail(result.RemoteNotFound, "The remote's name is not a valid remote name.")
	}
	urls, err := c.git.ConfigAll("remote." + name + ".url")
	if err != nil {
		return c.gitFailure(err)
	}
	if len(urls) == 0 {
		return result.Fail(result.RemoteNotFound, "The remote has no URL.", jsonx.F("remote", name))
	}
	pushURLs, err := c.git.ConfigAll("remote." + name + ".pushurl")
	if err != nil {
		return c.gitFailure(err)
	}
	fetchOut, err := c.git.Read("remote", "get-url", "--all", name)
	if err != nil {
		return c.gitFailure(err)
	}
	pushOut, err := c.git.Read("remote", "get-url", "--push", "--all", name)
	if err != nil {
		return c.gitFailure(err)
	}
	effectiveFetch := lines(fetchOut)
	effectivePush := lines(pushOut)
	for _, list := range [][]string{urls, pushURLs, effectiveFetch, effectivePush} {
		for _, url := range list {
			if remoteurl.Parse(url).HasCredential() {
				return result.Fail(result.RemoteCredentialInURL, credentialMessage, jsonx.F("remote", name))
			}
		}
	}
	if len(urls) > 1 || len(pushURLs) > 1 || len(effectiveFetch) != 1 || len(effectivePush) != 1 {
		return result.Fail(result.RemotePushRedirected, "The remote has more than one fetch or push URL.", jsonx.F("remote", name))
	}
	if effectivePush[0] != effectiveFetch[0] {
		return result.Fail(result.RemotePushRedirected, "The push URL of the remote differs from its fetch URL.", jsonx.F("remote", name))
	}
	return nil
}

func lines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// transportClass maps git's answer on a fetch or a push to a remote code,
// or "" when the answer is none of them.
func transportClass(stderr string) result.Code {
	lower := strings.ToLower(stderr)
	has := func(needles ...string) bool {
		for _, needle := range needles {
			if strings.Contains(lower, needle) {
				return true
			}
		}
		return false
	}
	switch {
	case has("authentication failed", "could not read username", "could not read password",
		"terminal prompts disabled", "permission denied (publickey", "permission denied, please try again",
		"http basic: access denied", "invalid username or password", "invalid credentials",
		"the requested url returned error: 401", "the requested url returned error: 403"):
		return result.CredentialUnavailable
	case has("could not resolve host", "connection refused", "connection timed out", "operation timed out",
		"network is unreachable", "unable to access", "could not read from remote repository",
		"does not appear to be a git repository", "the requested url returned error: 5", "host key verification failed",
		"connection reset", "no route to host", "repository not found", "unable to connect"):
		return result.RemoteUnavailable
	}
	return ""
}

// credentialSource names where the credential comes from in this mode.
const credentialSource = "the user's own Git credential helper and gh's store"

// fetch fetches a remote without tags. A fetch moves and prunes
// remote-tracking refs only, so it is listed but it is not a mutation.
func (c *call) fetch(remote string) *result.Failure {
	failure, _ := c.tryFetch(remote)
	return failure
}

// tryFetch fetches, and reports whether git ran at all, so repo_state can
// report a fetch that ran and failed as a state. The refspec is explicit,
// and the empty --refmap stops git from also mapping the fetched refs
// through remote.<name>.fetch, which could name a local branch or a tag.
// --prune drops the tracking ref of a branch the remote deleted, so every
// later step reads refs that this fetch refreshed; with one refspec on the
// command line, git prunes only its destination.
func (c *call) tryFetch(remote string) (*result.Failure, bool) {
	args := []string{"fetch", "--no-tags", "--prune", "--refmap=", remote, "+refs/heads/*:refs/remotes/" + remote + "/*"}
	res, err := c.git.Run(gitx.Opts{Record: true}, args...)
	if err != nil {
		return c.gitFailure(err), false
	}
	if res.OK() {
		return nil, true
	}
	return c.transportFailure(remote, args, res), true
}

// remoteHasRef asks a checked remote whether it has a branch now. It is a
// read, so the result does not list it.
func (c *call) remoteHasRef(remote, ref string) (bool, *result.Failure) {
	args := []string{"ls-remote", "--heads", remote, ref}
	res, err := c.git.Run(gitx.Opts{}, args...)
	if err != nil {
		return false, c.gitFailure(err)
	}
	if !res.OK() {
		return false, c.transportFailure(remote, args, res)
	}
	for _, line := range lines(res.Text()) {
		if _, name, ok := strings.Cut(line, "\t"); ok && name == ref {
			return true, nil
		}
	}
	return false, nil
}

// unambiguous reports whether git resolves a short name, such as
// origin/main, to the full ref the SCM checked. A local branch or a tag
// of the same name would otherwise win git's lookup.
func (c *call) unambiguous(short, full string) (bool, *result.Failure) {
	// short joins a checked remote name and a checked branch name, so it
	// cannot read as an option; --symbolic-full-name prints
	// --end-of-options instead of taking it.
	if strings.HasPrefix(short, "-") {
		return false, nil
	}
	res, err := c.git.Run(gitx.Opts{}, "rev-parse", "--symbolic-full-name", short)
	if err != nil {
		return false, c.gitFailure(err)
	}
	if !res.OK() || res.Text() != full || strings.Contains(string(res.Stderr), "ambiguous") {
		return false, nil
	}
	return true, nil
}

func ambiguousName(short, full string) *result.Failure {
	return result.Fail(result.GitFailed, "A short ref name names another ref than the one the SCM checked.",
		jsonx.F("name", short), jsonx.F("expected", full)).WithRetry(result.RetryUser)
}

func (c *call) transportFailure(remote string, args []string, res gitx.Result) *result.Failure {
	switch transportClass(string(res.Stderr)) {
	case result.CredentialUnavailable:
		return result.Fail(result.CredentialUnavailable, "The credential for the remote is missing or expired.",
			jsonx.F("remote", remote), jsonx.F("credential_source", credentialSource))
	case result.RemoteUnavailable:
		return result.Fail(result.RemoteUnavailable, "The remote cannot be reached.", jsonx.F("remote", remote))
	}
	return commandFailure(args, res)
}
