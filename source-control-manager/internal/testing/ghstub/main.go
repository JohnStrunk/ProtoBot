// Command ghstub stands in for gh in the repository fixture. It answers
// the three pr commands of the SCM's host adapter from a JSON state file
// that SCM_GH_STUB_STATE names, and records every call that changes host
// state with its standard input.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Pull is one stored pull request, in gh's JSON field names.
type Pull struct {
	Repo                string  `json:"repo"`
	Number              int     `json:"number"`
	State               string  `json:"state"`
	URL                 string  `json:"url"`
	Title               string  `json:"title"`
	Body                string  `json:"body"`
	HeadRefName         string  `json:"headRefName"`
	BaseRefName         string  `json:"baseRefName"`
	IsCrossRepository   bool    `json:"isCrossRepository"`
	HeadRepository      *Name   `json:"headRepository"`
	HeadRepositoryOwner *Login  `json:"headRepositoryOwner"`
	MergeCommit         *Commit `json:"mergeCommit"`
}

// Name is a repository name.
type Name struct {
	Name string `json:"name"`
}

// Login is an owner login.
type Login struct {
	Login string `json:"login"`
}

// Commit is a commit object.
type Commit struct {
	OID string `json:"oid"`
}

// Call is one recorded call.
type Call struct {
	Argv  []string `json:"argv"`
	Stdin string   `json:"stdin"`
}

// State is the stub's host.
type State struct {
	Down     bool   `json:"down"`
	Next     int    `json:"next_number"`
	Pulls    []Pull `json:"pulls"`
	Recorded []Call `json:"recorded"`
	// Reads counts every call, reads included.
	Calls int `json:"calls"`
	// Fail makes a pr command ("create" or "edit") fail after the stub
	// records it, with this text on stderr, and changes nothing.
	Fail map[string]string `json:"fail,omitempty"`
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	path := os.Getenv("SCM_GH_STUB_STATE")
	if path == "" {
		fmt.Fprintln(os.Stderr, "ghstub: SCM_GH_STUB_STATE is not set")
		return 2
	}
	if os.Getenv("GH_REPO") != "" || os.Getenv("GH_HOST") != "" || os.Getenv("GH_PROMPT_DISABLED") != "1" {
		fmt.Fprintln(os.Stderr, "ghstub: the adapter must clear GH_REPO and GH_HOST and disable prompts")
		return 2
	}
	state, err := load(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ghstub:", err)
		return 2
	}
	state.Calls++
	defer func() { _ = save(path, state) }()
	if state.Down {
		fmt.Fprintln(os.Stderr, "error connecting to api.github.com")
		return 1
	}
	if len(args) < 2 || args[0] != "pr" {
		fmt.Fprintln(os.Stderr, "ghstub: unknown command")
		return 2
	}
	opts, positional := parse(args[2:])
	repo := opts["repo"]
	if strings.Count(repo, "/") != 2 {
		fmt.Fprintln(os.Stderr, "ghstub: --repo must be HOST/OWNER/REPO")
		return 2
	}
	switch args[1] {
	case "list":
		return list(state, repo, opts)
	case "create":
		stdin, _ := io.ReadAll(os.Stdin)
		state.Recorded = append(state.Recorded, Call{Argv: append([]string{"gh"}, args...), Stdin: string(stdin)})
		if text, ok := state.Fail["create"]; ok {
			fmt.Fprintln(os.Stderr, text)
			return 1
		}
		return create(state, repo, opts, string(stdin))
	case "edit":
		stdin, _ := io.ReadAll(os.Stdin)
		state.Recorded = append(state.Recorded, Call{Argv: append([]string{"gh"}, args...), Stdin: string(stdin)})
		if text, ok := state.Fail["edit"]; ok {
			fmt.Fprintln(os.Stderr, text)
			return 1
		}
		if len(positional) != 1 {
			fmt.Fprintln(os.Stderr, "ghstub: edit takes one number")
			return 2
		}
		return edit(state, repo, positional[0], opts, string(stdin))
	}
	fmt.Fprintln(os.Stderr, "ghstub: unknown pr command")
	return 2
}

func parse(args []string) (map[string]string, []string) {
	opts := map[string]string{}
	var positional []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "--") && i+1 < len(args) {
			opts[strings.TrimPrefix(args[i], "--")] = args[i+1]
			i++
			continue
		}
		positional = append(positional, args[i])
	}
	return opts, positional
}

func list(state *State, repo string, opts map[string]string) int {
	if opts["json"] == "" {
		fmt.Fprintln(os.Stderr, "ghstub: list needs --json")
		return 2
	}
	out := []Pull{}
	for _, p := range state.Pulls {
		if p.Repo != repo {
			continue
		}
		if head := opts["head"]; head != "" && p.HeadRefName != head {
			continue
		}
		if base := opts["base"]; base != "" && p.BaseRefName != base {
			continue
		}
		switch opts["state"] {
		case "", "open":
			if p.State != "OPEN" {
				continue
			}
		case "merged":
			if p.State != "MERGED" {
				continue
			}
		case "closed":
			if p.State != "CLOSED" && p.State != "MERGED" {
				continue
			}
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number > out[j].Number })
	data, _ := json.Marshal(out)
	fmt.Println(string(data))
	return 0
}

func create(state *State, repo string, opts map[string]string, body string) int {
	for _, p := range state.Pulls {
		if p.Repo == repo && p.HeadRefName == opts["head"] && !p.IsCrossRepository && p.State == "OPEN" {
			fmt.Fprintln(os.Stderr, "a pull request for branch already exists")
			return 1
		}
	}
	if state.Next == 0 {
		state.Next = 1
	}
	parts := strings.Split(repo, "/")
	p := Pull{
		Repo: repo, Number: state.Next, State: "OPEN", URL: "https://" + repo + "/pull/" + strconv.Itoa(state.Next),
		Title: opts["title"], Body: body, HeadRefName: opts["head"], BaseRefName: opts["base"],
		HeadRepository: &Name{Name: parts[2]}, HeadRepositoryOwner: &Login{Login: parts[1]},
	}
	state.Next++
	state.Pulls = append(state.Pulls, p)
	fmt.Println(p.URL)
	return 0
}

func edit(state *State, repo, number string, opts map[string]string, body string) int {
	n, err := strconv.Atoi(number)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ghstub: bad number")
		return 2
	}
	for i := range state.Pulls {
		if state.Pulls[i].Repo == repo && state.Pulls[i].Number == n {
			state.Pulls[i].Title = opts["title"]
			state.Pulls[i].Body = body
			fmt.Println(state.Pulls[i].URL)
			return 0
		}
	}
	fmt.Fprintln(os.Stderr, "GraphQL: Could not resolve to a PullRequest (HTTP 404)")
	return 1
}

func load(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func save(path string, state *State) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
