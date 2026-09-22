//go:build !unix

package gitx

import "os/exec"

// Detach does nothing where there is no Unix session to leave.
func Detach(*exec.Cmd) {}
