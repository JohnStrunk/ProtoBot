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
