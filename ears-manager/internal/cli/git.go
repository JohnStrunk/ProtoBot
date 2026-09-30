package cli

import (
	"os"
	"os/exec"
	"slices"
	"strings"
)

// removedGitEnv are variables that would point Git at another repository,
// index, attribute source, or set of programs than the working tree that the
// command resolved, change how a pathspec matches, or add configuration behind
// the command line. A caller-supplied path never selects the project
// (git-integration.md#the-project-root). The list is the one of the Source
// Control Manager's gitx.Environ.
var removedGitEnv = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_NAMESPACE",
	"GIT_PREFIX", "GIT_QUARANTINE_PATH", "GIT_REFLOG_ACTION",
	"GIT_ATTR_SOURCE", "GIT_EXEC_PATH", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT",
	"GIT_ICASE_PATHSPECS", "GIT_GLOB_PATHSPECS", "GIT_NOGLOB_PATHSPECS", "GIT_LITERAL_PATHSPECS",
}

// removedGitEnvPrefixes are the numbered GIT_CONFIG_KEY_<n> and
// GIT_CONFIG_VALUE_<n> variables.
var removedGitEnvPrefixes = []string{"GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_"}

// fixedGitEnv keeps Git's messages in the C locale, so a refusal is told
// apart by its text, and keeps a read from writing the index.
var fixedGitEnv = []string{"LC_ALL=C", "LANGUAGE=", "GIT_OPTIONAL_LOCKS=0"}

// gitCommand builds a Git child process with the environment of gitEnviron.
// Every Git command of the package runs through it.
func gitCommand(args ...string) *exec.Cmd {
	command := exec.Command("git", args...)
	command.Env = gitEnviron(os.Environ())
	return command
}

// gitEnviron returns env without the variables that redirect Git, plus the
// fixed variables.
func gitEnviron(env []string) []string {
	result := make([]string, 0, len(env)+len(fixedGitEnv))
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if slices.Contains(removedGitEnv, name) || slices.ContainsFunc(removedGitEnvPrefixes, func(prefix string) bool { return strings.HasPrefix(name, prefix) }) || slices.ContainsFunc(fixedGitEnv, func(fixed string) bool { return strings.HasPrefix(fixed, name+"=") }) {
			continue
		}
		result = append(result, entry)
	}
	return append(result, fixedGitEnv...)
}
