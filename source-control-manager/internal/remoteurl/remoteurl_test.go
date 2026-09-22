package remoteurl

import "testing"

func TestCredentials(t *testing.T) {
	cases := []struct {
		url        string
		credential bool
		stripped   string
	}{
		{"https://github.com/o/r.git", false, "https://github.com/o/r.git"},
		{"https://TOKEN@github.com/o/r.git", true, "https://github.com/o/r.git"},
		{"https://user:pass@github.com/o/r.git", true, "https://github.com/o/r.git"}, // pragma: allowlist secret
		{"ssh://git@github.com/o/r.git", true, "ssh://github.com/o/r.git"},
		{"ssh://github.com/o/r.git", false, "ssh://github.com/o/r.git"},
		{"git@github.com:o/r.git", false, "git@github.com:o/r.git"},
		{"token@github.com:o/r.git", true, "github.com:o/r.git"},
		{"github.com:o/r.git", false, "github.com:o/r.git"},
		{"/srv/git/origin.git", false, "/srv/git/origin.git"},
		{"./relative/path:with-colon", false, "./relative/path:with-colon"},
	}
	for _, c := range cases {
		u := Parse(c.url)
		if got := u.HasCredential(); got != c.credential {
			t.Errorf("HasCredential(%q) = %v, want %v", c.url, got, c.credential)
		}
		if got := u.Stripped(); got != c.stripped {
			t.Errorf("Stripped(%q) = %q, want %q", c.url, got, c.stripped)
		}
	}
}

func TestRepoOf(t *testing.T) {
	cases := map[string]string{
		"https://github.com/protobot-fixture/fixture.git": "github.com/protobot-fixture/fixture",
		"git@github.com:protobot-fixture/fixture.git":     "github.com/protobot-fixture/fixture",
		"ssh://ghe.example.com:2222/team/project":         "ghe.example.com/team/project",
		"https://GitHub.com/Owner/Name/":                  "github.com/Owner/Name",
	}
	for url, want := range cases {
		repo, err := RepoOf(url)
		if err != nil || repo.String() != want {
			t.Errorf("RepoOf(%q) = %q, %v; want %q", url, repo.String(), err, want)
		}
	}
	for _, url := range []string{"/srv/git/origin.git", "https://example.invalid/fixture.git", "https://h/a/b/c"} {
		if _, err := RepoOf(url); err == nil {
			t.Errorf("RepoOf(%q) accepted a URL that names no <host>/<owner>/<name>", url)
		}
	}
}
