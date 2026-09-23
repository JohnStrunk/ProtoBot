// Package host is the SCM's host adapter boundary: the only code that
// talks to the Git host. The core asks it for four things: find the pull
// request of a branch, create one, update one, and classify a failed
// answer. The first and only adapter is GitHub, through gh.
package host

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/redhat-et/protobot/source-control-manager/internal/gitx"
	"github.com/redhat-et/protobot/source-control-manager/internal/remoteurl"
)

// Class is the stable class of a failed host answer.
type Class string

// The classes of a failed host answer.
const (
	ClassNotFound    Class = "not-found"
	ClassProtected   Class = "protected"
	ClassCredential  Class = "credential"
	ClassRateLimited Class = "rate-limited"
	ClassUnavailable Class = "unavailable"
	ClassOther       Class = "other"
)

// Error is a failed host request.
type Error struct {
	Class  Class
	Status int
}

func (e *Error) Error() string { return "host request failed: " + string(e.Class) }

// Open reports a failure after which the host may still have applied the
// request: the host was unavailable, or gh gave no answer, as when its
// deadline killed it. A refusal, such as a credential or a validation
// error, left the host unchanged.
func (e *Error) Open() bool { return e.Class == ClassUnavailable || e.Status < 0 }

// Pull request states.
const (
	StateOpen   = "open"
	StateMerged = "merged"
	StateClosed = "closed"
)

// PullRequest is the host's record of one pull request.
type PullRequest struct {
	Number      int
	State       string
	URL         string
	Title       string
	Body        string
	HeadRef     string
	BaseRef     string
	MergeCommit string
}

// Adapter is the host adapter boundary.
type Adapter interface {
	// Find returns the pull requests, in any state, whose head is branch
	// in the repository itself, never in a fork, and whose base is base.
	Find(branch, base string) ([]PullRequest, *Error)
	// FindMerged returns the merged pull requests into base whose head
	// branch in the repository itself starts with headPrefix.
	FindMerged(headPrefix, base string) ([]PullRequest, *Error)
	// Create opens a pull request and returns its number and URL.
	Create(base, head, title, body string) (PullRequest, *Error)
	// Update sets the title and body of a pull request.
	Update(number int, title, body string) *Error
}

// GitHub drives gh with argument lists, --repo on every call, and the body
// on standard input. It has no merge, api, or auth command.
type GitHub struct {
	Repo remoteurl.Repo
	// Dir is the working directory of gh.
	Dir string
	// Record receives each command that changes host state.
	Record func([]string)
}

var _ Adapter = (*GitHub)(nil)

const listFields = "number,state,url,title,body,headRefName,baseRefName,isCrossRepository,headRepository,headRepositoryOwner,mergeCommit"

type ghPull struct {
	Number            int    `json:"number"`
	State             string `json:"state"`
	URL               string `json:"url"`
	Title             string `json:"title"`
	Body              string `json:"body"`
	HeadRefName       string `json:"headRefName"`
	BaseRefName       string `json:"baseRefName"`
	IsCrossRepository bool   `json:"isCrossRepository"`
	HeadRepository    *struct {
		Name string `json:"name"`
	} `json:"headRepository"`
	HeadRepositoryOwner *struct {
		Login string `json:"login"`
	} `json:"headRepositoryOwner"`
	MergeCommit *struct {
		OID string `json:"oid"`
	} `json:"mergeCommit"`
}

func (g *GitHub) sameRepo(p ghPull) bool {
	if p.IsCrossRepository || p.HeadRepository == nil || p.HeadRepositoryOwner == nil {
		return false
	}
	return strings.EqualFold(p.HeadRepository.Name, g.Repo.Name) && strings.EqualFold(p.HeadRepositoryOwner.Login, g.Repo.Owner)
}

func convert(p ghPull) PullRequest {
	pr := PullRequest{
		Number: p.Number, State: strings.ToLower(p.State), URL: p.URL, Title: p.Title, Body: p.Body,
		HeadRef: p.HeadRefName, BaseRef: p.BaseRefName,
	}
	if p.MergeCommit != nil {
		pr.MergeCommit = p.MergeCommit.OID
	}
	return pr
}

func (g *GitHub) list(extra ...string) ([]ghPull, *Error) {
	args := append([]string{"pr", "list", "--repo", g.Repo.String()}, extra...)
	args = append(args, "--json", listFields)
	out, err := g.run(args, nil)
	if err != nil {
		return nil, err
	}
	var pulls []ghPull
	if json.Unmarshal(out, &pulls) != nil {
		return nil, &Error{Class: ClassOther}
	}
	return pulls, nil
}

// Find implements Adapter.
func (g *GitHub) Find(branch, base string) ([]PullRequest, *Error) {
	pulls, err := g.list("--head", branch, "--base", base, "--state", "all", "--limit", "100")
	if err != nil {
		return nil, err
	}
	var out []PullRequest
	for _, p := range pulls {
		if p.HeadRefName == branch && p.BaseRefName == base && g.sameRepo(p) {
			out = append(out, convert(p))
		}
	}
	return out, nil
}

// FindMerged implements Adapter.
func (g *GitHub) FindMerged(headPrefix, base string) ([]PullRequest, *Error) {
	pulls, err := g.list("--base", base, "--state", "merged", "--limit", "1000")
	if err != nil {
		return nil, err
	}
	var out []PullRequest
	for _, p := range pulls {
		if strings.HasPrefix(p.HeadRefName, headPrefix) && p.BaseRefName == base && g.sameRepo(p) {
			out = append(out, convert(p))
		}
	}
	return out, nil
}

// Create implements Adapter.
func (g *GitHub) Create(base, head, title, body string) (PullRequest, *Error) {
	args := []string{"pr", "create", "--repo", g.Repo.String(), "--base", base, "--head", head, "--title", title, "--body-file", "-"}
	g.record(args)
	out, err := g.run(args, []byte(body))
	if err != nil {
		return PullRequest{}, err
	}
	url := lastLine(out)
	number := numberOf(url)
	if number == 0 {
		return PullRequest{}, &Error{Class: ClassOther}
	}
	return PullRequest{Number: number, State: StateOpen, URL: url, Title: title, Body: body, HeadRef: head, BaseRef: base}, nil
}

// Update implements Adapter.
func (g *GitHub) Update(number int, title, body string) *Error {
	args := []string{"pr", "edit", strconv.Itoa(number), "--repo", g.Repo.String(), "--title", title, "--body-file", "-"}
	g.record(args)
	_, err := g.run(args, []byte(body))
	return err
}

func (g *GitHub) record(args []string) {
	if g.Record != nil {
		g.Record(append([]string{"gh"}, args...))
	}
}

func (g *GitHub) run(args []string, stdin []byte) ([]byte, *Error) {
	path, err := exec.LookPath("gh")
	if err != nil {
		return nil, &Error{Class: ClassOther}
	}
	cmd, cancel := gitx.Command(path, args...)
	defer cancel()
	cmd.Dir = g.Dir
	cmd.Env = Environ(os.Environ())
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if runErr := cmd.Run(); runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			if cmd.Process != nil {
				// gh started and gave no answer: its deadline ended it.
				return nil, &Error{Class: ClassUnavailable, Status: -1}
			}
			return nil, &Error{Class: ClassOther}
		}
		// A signal, as from the deadline, leaves ExitCode at -1, which
		// Open reads as no answer.
		return nil, &Error{Class: Classify(stderr.String() + "\n" + stdout.String()), Status: exitErr.ExitCode()}
	}
	return stdout.Bytes(), nil
}

// Environ returns the environment of gh: GH_REPO and GH_HOST are cleared,
// so gh never falls back to a default host, and prompts, colors, and the
// pager are off.
func Environ(env []string) []string {
	var out []string
	for _, entry := range gitx.Environ(env) {
		name, _, _ := strings.Cut(entry, "=")
		switch name {
		case "GH_REPO", "GH_HOST", "GH_PROMPT_DISABLED", "GH_PAGER", "NO_COLOR", "CLICOLOR", "GH_NO_UPDATE_NOTIFIER", "GH_SPINNER_DISABLED":
			continue
		}
		out = append(out, entry)
	}
	return append(out, "GH_PROMPT_DISABLED=1", "GH_PAGER=cat", "NO_COLOR=1", "CLICOLOR=0", "GH_NO_UPDATE_NOTIFIER=1", "GH_SPINNER_DISABLED=1")
}

// Classify maps gh's answer to a stable class.
func Classify(text string) Class {
	lower := strings.ToLower(text)
	has := func(needles ...string) bool {
		for _, needle := range needles {
			if strings.Contains(lower, needle) {
				return true
			}
		}
		return false
	}
	switch {
	case has("rate limit", "http 429", "abuse detection"):
		return ClassRateLimited
	case has("http 401", "bad credentials", "gh auth login", "not logged in", "authentication required", "requires authentication", "to get started with github cli", "token has expired", "token is expired"):
		return ClassCredential
	case has("protected branch"):
		return ClassProtected
	case has("error connecting to", "connection refused", "no such host", "i/o timeout", "timeout", "http 500", "http 502", "http 503", "http 504", "could not connect", "network is unreachable", "tls handshake"):
		return ClassUnavailable
	case has("http 404", "could not resolve to a repository", "not found"):
		return ClassNotFound
	}
	return ClassOther
}

func lastLine(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func numberOf(url string) int {
	_, tail, ok := strings.Cut(url, "/pull/")
	if !ok {
		return 0
	}
	tail, _, _ = strings.Cut(tail, "/")
	n, err := strconv.Atoi(tail)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}
