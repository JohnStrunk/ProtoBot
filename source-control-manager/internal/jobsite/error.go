package jobsite

import (
	"errors"
	"fmt"
)

// Stable failure codes for export and patch decisions.
const (
	CodePolicyInvalid       = "policy.invalid"
	CodePolicyUnsupported   = "policy.unsupported"
	CodePolicyDigest        = "policy.digest_mismatch"
	CodeExportExists        = "export.exists"
	CodeExportSource        = "export.source"
	CodeExportUnsafe        = "export.unsafe"
	CodePatchRejected       = "patch.rejected"
	CodePatchStale          = "patch.stale_base"
	CodePatchTampered       = "patch.tampered"
	CodePatchRollbackFailed = "patch.rollback_failed"
)

// Error is a fail-closed export or patch decision.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func fail(code, message string) error {
	return &Error{Code: code, Message: message}
}

func errorCode(err error) string {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Code
	}
	return ""
}
