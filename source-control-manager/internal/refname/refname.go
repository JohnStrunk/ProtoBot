// Package refname checks branch names by the rules of
// `git check-ref-format --branch`, without running git, so a request is
// checked before any command runs.
package refname

import "strings"

// ValidBranch reports whether name is a valid branch name.
func ValidBranch(name string) bool {
	if name == "" || name == "HEAD" || name == "@" || strings.HasPrefix(name, "-") {
		return false
	}
	if strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, ".lock") {
		return false
	}
	if strings.Contains(name, "..") || strings.Contains(name, "@{") || strings.Contains(name, "//") {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
		switch r {
		case ' ', '~', '^', ':', '?', '*', '[', '\\':
			return false
		}
	}
	for _, component := range strings.Split(name, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}
