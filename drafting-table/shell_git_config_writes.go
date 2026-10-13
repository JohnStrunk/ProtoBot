package guard

import (
	"path/filepath"
	"strings"
)

func gitConfigWriteTargets(args []string, state projectState, _ string) ([]string, bool) {
	if len(args) >= 3 && args[1] == "config" && args[2] == "--help" {
		return nil, true
	}
	command, index, ok := gitSubcommand(args)
	if !ok {
		return nil, true
	}
	if command == "apply" {
		if containsAny(args[index+1:], "--check", "--stat", "--numstat", "--summary", "--help", "-h") {
			return nil, true
		}
		return nil, false
	}
	if command != "config" {
		return nil, true
	}
	// A redirected invocation (-C, --git-dir, --work-tree) resolves config
	// file locations against a different directory than the session cwd.
	redirected := false
	for i := 1; i < index; i++ {
		option, _, hasValue := strings.Cut(args[i], "=")
		switch {
		case !hasValue && (option == "-C" || option == "--git-dir" || option == "--work-tree"):
			redirected = true
			i++
		case strings.HasPrefix(args[i], "-C") && len(args[i]) > 2,
			strings.HasPrefix(args[i], "--git-dir="),
			strings.HasPrefix(args[i], "--work-tree="):
			redirected = true
		}
	}
	configArgs := args[index+1:]
	var positionals []string
	filePath, scope := "", ""
	readOnly, readModifier, write := false, false, false
	for i := 0; i < len(configArgs); i++ {
		arg := configArgs[i]
		if arg == "--" {
			positionals = append(positionals, configArgs[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positionals = append(positionals, arg)
			continue
		}
		option, value, hasValue := strings.Cut(arg, "=")
		switch option {
		case "--get", "--get-all", "--get-regexp", "--get-urlmatch", "--get-color", "--get-colorbool", "--list", "-l", "--name-only":
			readOnly = true
		case "--show-origin", "--show-scope":
			readModifier = true
		case "--add", "--replace-all", "--unset", "--unset-all", "--unset-regexp", "--rename-section", "--remove-section", "--edit", "-e":
			write = true
		case "--file", "-f":
			if !hasValue {
				if i+1 >= len(configArgs) {
					return nil, false
				}
				i++
				value = configArgs[i]
			}
			filePath = value
		case "--local", "--global", "--system", "--worktree":
			if scope != "" {
				return nil, false
			}
			scope = option
		case "--type", "--bool", "--int", "--bool-or-int", "--path", "--expiry-date":
			if !hasValue {
				if i+1 >= len(configArgs) {
					return nil, false
				}
				i++
			}
		case "--fixed-value", "--includes", "--no-includes", "--null", "-z":
			if hasValue {
				return nil, false
			}
		default:
			return nil, false
		}
	}
	if readOnly {
		return nil, true
	}
	if !write {
		write = len(positionals) >= 2
	}
	if !write && !readModifier && len(positionals) == 0 {
		return nil, true
	}
	if !write {
		return nil, true
	}
	if filePath != "" {
		if scope != "" || redirected {
			return nil, false
		}
		return []string{filePath}, true
	}
	if scope == "--global" || scope == "--system" {
		return nil, true
	}
	if redirected {
		return nil, false
	}
	if scope == "--worktree" {
		if state.gitDir == "" {
			return nil, false
		}
		return []string{filepath.Join(state.gitDir, "config.worktree")}, true
	}
	if state.gitCommonDir == "" || state.root == "" {
		return nil, false
	}
	return []string{filepath.Join(state.gitCommonDir, "config")}, true
}
