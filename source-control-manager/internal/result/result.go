// Package result holds the SCM's result protocol: the failure codes, the
// retry policies, and the success and failure envelopes of
// docs/architecture/source-control-manager.md#result-protocol.
package result

import (
	"github.com/redhat-et/protobot/source-control-manager/internal/jsonx"
)

// SchemaVersion is the version of the result schema. The executable, the
// core, and the result schema share one version.
const SchemaVersion = 1

// Faces.
const (
	FaceDraftingTable = "drafting-table"
	FaceApprovedState = "approved-state"
)

// Outcomes of a successful call.
const (
	OutcomeRead      = "read"
	OutcomeApplied   = "applied"
	OutcomeUnchanged = "unchanged"
)

// Mutation states of a failed call.
const (
	MutationNone    = "none"
	MutationPartial = "partial"
	MutationUnknown = "unknown"
)

// Code is a stable failure code of the failure table.
type Code string

// The failure codes.
const (
	UnauthorizedAction         Code = "UNAUTHORIZED_ACTION"
	InvalidRequest             Code = "INVALID_REQUEST"
	ProjectNotFound            Code = "PROJECT_NOT_FOUND"
	ProjectNotAtRoot           Code = "PROJECT_NOT_AT_ROOT"
	ProjectUnreadable          Code = "PROJECT_UNREADABLE"
	SpecToolFailed             Code = "SPEC_TOOL_FAILED"
	AlreadyInitialized         Code = "ALREADY_INITIALIZED"
	ReservedPrefix             Code = "RESERVED_PREFIX"
	DefaultNotFound            Code = "DEFAULT_NOT_FOUND"
	DefaultDiverged            Code = "DEFAULT_DIVERGED"
	BranchExists               Code = "BRANCH_EXISTS"
	RemoteNotFound             Code = "REMOTE_NOT_FOUND"
	RemoteCredentialInURL      Code = "REMOTE_CREDENTIAL_IN_URL"
	RemotePushRedirected       Code = "REMOTE_PUSH_REDIRECTED"
	NotAChangeSetBranch        Code = "NOT_A_CHANGE_SET_BRANCH"
	ChangeSetNotFound          Code = "CHANGE_SET_NOT_FOUND"
	AmbiguousBranch            Code = "AMBIGUOUS_BRANCH"
	UncommittedChanges         Code = "UNCOMMITTED_CHANGES"
	InitRemoteMismatch         Code = "INIT_REMOTE_MISMATCH"
	SpecDigestMismatch         Code = "SPEC_DIGEST_MISMATCH"
	SpecCheckFailed            Code = "SPEC_CHECK_FAILED"
	PathNotStageable           Code = "PATH_NOT_STAGEABLE"
	StagedContentChanged       Code = "STAGED_CONTENT_CHANGED"
	UnsafeText                 Code = "UNSAFE_TEXT"
	NothingToCommit            Code = "NOTHING_TO_COMMIT"
	NothingToPublish           Code = "NOTHING_TO_PUBLISH"
	BaseNotOnDefault           Code = "BASE_NOT_ON_DEFAULT"
	DefaultMoved               Code = "DEFAULT_MOVED"
	BaseCommitStale            Code = "BASE_COMMIT_STALE"
	PRMerged                   Code = "PR_MERGED"
	PRClosed                   Code = "PR_CLOSED"
	AmbiguousPullRequest       Code = "AMBIGUOUS_PULL_REQUEST"
	PushRejectedNonFastForward Code = "PUSH_REJECTED_NON_FAST_FORWARD"
	PushRejectedProtected      Code = "PUSH_REJECTED_PROTECTED"
	CredentialUnavailable      Code = "CREDENTIAL_UNAVAILABLE"
	RemoteUnavailable          Code = "REMOTE_UNAVAILABLE"
	HostUnavailable            Code = "HOST_UNAVAILABLE"
	HostRequestFailed          Code = "HOST_REQUEST_FAILED"
	MergeConflict              Code = "MERGE_CONFLICT"
	NotApproved                Code = "NOT_APPROVED"
	NotAMergeCommit            Code = "NOT_A_MERGE_COMMIT"
	MergeCommitMismatch        Code = "MERGE_COMMIT_MISMATCH"
	GitFailed                  Code = "GIT_FAILED"
	Internal                   Code = "INTERNAL"
)

// Retry is the retry policy of a failure.
type Retry string

// The retry policies.
const (
	RetryRetry         Retry = "retry"
	RetryRefreshBranch Retry = "refresh-branch"
	RetryRevise        Retry = "revise"
	RetryUser          Retry = "user"
	RetryAuthorize     Retry = "authorize"
	RetryReconcile     Retry = "reconcile"
	RetryNever         Retry = "never"
)

// defaultRetry is the Retry column of the failure table.
var defaultRetry = map[Code]Retry{
	UnauthorizedAction:         RetryAuthorize,
	InvalidRequest:             RetryRevise,
	ProjectNotFound:            RetryUser,
	ProjectNotAtRoot:           RetryUser,
	ProjectUnreadable:          RetryUser,
	SpecToolFailed:             RetryUser,
	AlreadyInitialized:         RetryNever,
	ReservedPrefix:             RetryRevise,
	DefaultNotFound:            RetryUser,
	DefaultDiverged:            RetryUser,
	BranchExists:               RetryUser,
	RemoteNotFound:             RetryUser,
	RemoteCredentialInURL:      RetryUser,
	RemotePushRedirected:       RetryUser,
	NotAChangeSetBranch:        RetryNever,
	ChangeSetNotFound:          RetryRevise,
	AmbiguousBranch:            RetryUser,
	UncommittedChanges:         RetryUser,
	InitRemoteMismatch:         RetryUser,
	SpecDigestMismatch:         RetryUser,
	SpecCheckFailed:            RetryRevise,
	PathNotStageable:           RetryNever,
	StagedContentChanged:       RetryUser,
	UnsafeText:                 RetryRevise,
	NothingToCommit:            RetryNever,
	NothingToPublish:           RetryNever,
	BaseNotOnDefault:           RetryUser,
	DefaultMoved:               RetryRefreshBranch,
	BaseCommitStale:            RetryRevise,
	PRMerged:                   RetryNever,
	PRClosed:                   RetryUser,
	AmbiguousPullRequest:       RetryUser,
	PushRejectedNonFastForward: RetryUser,
	PushRejectedProtected:      RetryUser,
	CredentialUnavailable:      RetryAuthorize,
	RemoteUnavailable:          RetryRetry,
	HostUnavailable:            RetryRetry,
	HostRequestFailed:          RetryRetry,
	MergeConflict:              RetryUser,
	NotApproved:                RetryUser,
	NotAMergeCommit:            RetryReconcile,
	MergeCommitMismatch:        RetryReconcile,
	GitFailed:                  RetryReconcile,
	Internal:                   RetryNever,
}

// Failure is a structured failure of one call.
type Failure struct {
	Code     Code
	Message  string
	Details  jsonx.Obj
	Mutation string
	Retry    Retry
}

// Fail builds a failure with no mutation and the default retry of its code.
func Fail(code Code, message string, details ...jsonx.Field) *Failure {
	retry, ok := defaultRetry[code]
	if !ok {
		retry = RetryNever
	}
	return &Failure{Code: code, Message: message, Details: jsonx.O(details...), Mutation: MutationNone, Retry: retry}
}

// Error makes a Failure an error.
func (f *Failure) Error() string { return string(f.Code) + ": " + f.Message }

// WithMutation sets the mutation state.
func (f *Failure) WithMutation(mutation string) *Failure {
	f.Mutation = mutation
	return f
}

// WithRetry sets the retry policy.
func (f *Failure) WithRetry(retry Retry) *Failure {
	f.Retry = retry
	return f
}

// Object names the project and the change set of a call.
type Object struct {
	ProjectID   string `json:"project_id,omitempty"`
	ChangeSetID string `json:"change_set_id,omitempty"`
}

// Mutation is the mutation summary of a successful call.
type Mutation struct {
	Applied bool     `json:"applied"`
	Refs    []string `json:"refs"`
	Remote  bool     `json:"remote"`
}

type success struct {
	SchemaVersion int        `json:"schema_version"`
	OK            bool       `json:"ok"`
	Operation     string     `json:"operation"`
	Face          string     `json:"face"`
	Object        Object     `json:"object"`
	Outcome       string     `json:"outcome"`
	Data          any        `json:"data"`
	Diagnostics   []any      `json:"diagnostics"`
	Commands      [][]string `json:"commands"`
	Mutation      Mutation   `json:"mutation"`
	Trace         any        `json:"trace,omitempty"`
}

type errorBody struct {
	Code     Code      `json:"code"`
	Message  string    `json:"message"`
	Details  jsonx.Obj `json:"details"`
	Mutation string    `json:"mutation"`
	Retry    Retry     `json:"retry"`
}

type failure struct {
	SchemaVersion int        `json:"schema_version"`
	OK            bool       `json:"ok"`
	Operation     string     `json:"operation"`
	Face          string     `json:"face"`
	Object        Object     `json:"object"`
	Error         errorBody  `json:"error"`
	Commands      [][]string `json:"commands"`
	Trace         any        `json:"trace,omitempty"`
}

// Envelope is the finished result of one call.
type Envelope struct {
	OK       bool
	document any
}

// Success builds the envelope of a successful call.
func Success(operation, face string, object Object, outcome string, data any, diagnostics []any, commands [][]string, mutation Mutation, trace any) Envelope {
	if diagnostics == nil {
		diagnostics = []any{}
	}
	if commands == nil {
		commands = [][]string{}
	}
	if mutation.Refs == nil {
		mutation.Refs = []string{}
	}
	return Envelope{OK: true, document: success{
		SchemaVersion: SchemaVersion, OK: true, Operation: operation, Face: face, Object: object,
		Outcome: outcome, Data: data, Diagnostics: diagnostics, Commands: commands, Mutation: mutation, Trace: trace,
	}}
}

// Failed builds the envelope of a failed call.
func Failed(operation, face string, object Object, f *Failure, commands [][]string, trace any) Envelope {
	if commands == nil {
		commands = [][]string{}
	}
	details := f.Details
	if details == nil {
		details = jsonx.O()
	}
	return Envelope{OK: false, document: failure{
		SchemaVersion: SchemaVersion, OK: false, Operation: operation, Face: face, Object: object,
		Error:    errorBody{Code: f.Code, Message: f.Message, Details: details, Mutation: f.Mutation, Retry: f.Retry},
		Commands: commands, Trace: trace,
	}}
}

// JSON returns the envelope as one compact JSON document, with no trailing
// newline.
func (e Envelope) JSON() []byte {
	out, err := jsonx.Marshal(e.document)
	if err != nil {
		// Every value in an envelope is built by this package's callers
		// from strings, numbers, and ordered objects, so this cannot fail.
		panic("result: encode envelope: " + err.Error())
	}
	return out
}
