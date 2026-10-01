package cli

import (
	"os/exec"

	"github.com/redhat-et/protobot/ears-manager/internal/gitcmd"
)

// gitCommand builds a Git child process with the isolation of gitcmd.Command.
// Every Git command of the package runs through it.
func gitCommand(args ...string) *exec.Cmd {
	return gitcmd.Command(args...)
}

// gitEnviron returns env without the variables that redirect Git, plus the
// fixed variables of gitcmd.Environ.
func gitEnviron(env []string) []string {
	return gitcmd.Environ(env)
}
