package host

import (
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := map[string]Class{
		"error connecting to api.github.com":                           ClassUnavailable,
		"HTTP 502: Bad Gateway (https://api.github.com/graphql)":       ClassUnavailable,
		"GraphQL: API rate limit exceeded for user":                    ClassRateLimited,
		"HTTP 401: Bad credentials (https://api.github.com/graphql)":   ClassCredential,
		"To get started with GitHub CLI, please run:  gh auth login":   ClassCredential,
		"GraphQL: Could not resolve to a Repository with the name":     ClassNotFound,
		"pull request create failed: protected branch hook declined":   ClassProtected,
		"GraphQL: A pull request already exists for owner:cs/00002-x.": ClassOther,
	}
	for text, want := range cases {
		if got := Classify(text); got != want {
			t.Errorf("Classify(%q) = %s, want %s", text, got, want)
		}
	}
}

func TestEnviron(t *testing.T) {
	env := Environ([]string{"GH_REPO=a/b", "GH_HOST=github.com", "GIT_DIR=/x", "PATH=/bin", "GH_PROMPT_DISABLED=0"})
	joined := "\n" + strings.Join(env, "\n") + "\n"
	for _, gone := range []string{"GH_REPO=", "GH_HOST=", "GIT_DIR=", "GH_PROMPT_DISABLED=0"} {
		if strings.Contains(joined, "\n"+gone) {
			t.Errorf("the environment keeps %s", gone)
		}
	}
	for _, kept := range []string{"PATH=/bin", "GH_PROMPT_DISABLED=1", "GIT_TERMINAL_PROMPT=0"} {
		if !strings.Contains(joined, "\n"+kept+"\n") {
			t.Errorf("the environment lacks %s", kept)
		}
	}
}

func TestNumberOf(t *testing.T) {
	if n := numberOf("https://github.com/o/r/pull/12"); n != 12 {
		t.Fatalf("numberOf = %d", n)
	}
	if n := numberOf("https://github.com/o/r/issues/12"); n != 0 {
		t.Fatalf("numberOf an issue URL = %d", n)
	}
}
