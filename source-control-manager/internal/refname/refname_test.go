package refname

import "testing"

func TestValidBranch(t *testing.T) {
	for _, name := range []string{"main", "cs/00001-project-init", "release/1.0", "team/cs/x"} {
		if !ValidBranch(name) {
			t.Errorf("ValidBranch(%q) = false", name)
		}
	}
	for _, name := range []string{
		"", "HEAD", "@", "-main", "main/", "main.", "a..b", "a@{1}", "a//b", ".hidden", "a/.b",
		"x.lock", "a b", "a~1", "a^", "a:b", "a?", "a*", "a[", "a\\b", "a\x01",
	} {
		if ValidBranch(name) {
			t.Errorf("ValidBranch(%q) = true", name)
		}
	}
}
