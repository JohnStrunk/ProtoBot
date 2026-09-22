//go:build unix

package gitx

import (
	"os/exec"
	"syscall"
)

// Detach starts cmd in a new session, with no controlling terminal, so no
// child can prompt on the user's terminal: ssh cannot ask for a host key
// or a passphrase, and a signing program cannot ask for a PIN. They fail
// instead of waiting.
func Detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
