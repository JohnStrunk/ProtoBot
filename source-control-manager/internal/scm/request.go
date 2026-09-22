package scm

import (
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/redhat-et/protobot/source-control-manager/internal/jsonx"
	"github.com/redhat-et/protobot/source-control-manager/internal/project"
	"github.com/redhat-et/protobot/source-control-manager/internal/refname"
	"github.com/redhat-et/protobot/source-control-manager/internal/render"
	"github.com/redhat-et/protobot/source-control-manager/internal/result"
)

// Request field names.
const (
	FieldBranchPrefix  = "branch_prefix"
	FieldDefaultBranch = "default_branch"
	FieldChangeSetID   = "change_set_id"
	FieldBody          = "body"
)

// Defaults of branch_init.
const (
	DefaultBranchPrefix  = "cs/"
	DefaultDefaultBranch = "main"
)

// MaxBodyLength is the most characters a commit body may have.
const MaxBodyLength = 2000

// Fields returns the request fields of an operation. No request names a
// ref, a remote, a repository, a path, a title, or a commit, except the
// two checked branch_init fields.
func Fields(operation string) []string {
	return append([]string(nil), operationFields[operation]...)
}

var operationFields = map[string][]string{
	OpRepoState:     {},
	OpBranchInit:    {FieldBranchPrefix, FieldDefaultBranch},
	OpBranchResume:  {FieldChangeSetID},
	OpCommit:        {FieldBody},
	OpPublish:       {},
	OpRefresh:       {},
	OpApprovedMerge: {FieldChangeSetID},
}

var (
	changeSetIDPattern = regexp.MustCompile(`^CS-[0-9]{5}$`)
	// BranchPrefixPattern is the prefix rule of branch_init.
	BranchPrefixPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*/$`)
)

type request struct {
	branchPrefix  string
	defaultBranch string
	changeSetID   string
	body          string
}

func invalid(message, field string) *result.Failure {
	return result.Fail(result.InvalidRequest, message, jsonx.F("field", field))
}

func mismatch(field string) *result.Failure {
	return invalid("A field does not match its rule.", field)
}

// validate is check 3. The requests are closed: an unknown field, a wrong
// type, or a value that does not match its rule is INVALID_REQUEST, and
// nothing runs.
func validate(operation string, args map[string]any) (request, *result.Failure) {
	var req request
	allowed := operationFields[operation]
	names := make([]string, 0, len(args))
	for name := range args {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !contains(allowed, name) {
			return req, invalid("The request has a field that the operation does not take.", name)
		}
	}
	str := func(field string) (string, bool, *result.Failure) {
		value, present := args[field]
		if !present || value == nil {
			return "", false, nil
		}
		s, ok := value.(string)
		if !ok {
			return "", false, mismatch(field)
		}
		return s, true, nil
	}
	switch operation {
	case OpBranchInit:
		prefix, present, failure := str(FieldBranchPrefix)
		if failure != nil {
			return req, failure
		}
		if !present {
			prefix = DefaultBranchPrefix
		}
		if !BranchPrefixPattern.MatchString(prefix) || !refname.ValidBranch(prefix+"x") {
			return req, mismatch(FieldBranchPrefix)
		}
		def, present, failure := str(FieldDefaultBranch)
		if failure != nil {
			return req, failure
		}
		if !present {
			def = DefaultDefaultBranch
		}
		// The same namespace rules as the persisted configuration: Git
		// cannot hold a branch wi and a branch below wi/ at once, so a
		// default branch named wi would block the Job Site's namespace.
		if !refname.ValidBranch(def) || project.Under(def, strings.TrimSuffix(project.ReservedPrefix, "/")) ||
			strings.HasPrefix(def, prefix) {
			return req, mismatch(FieldDefaultBranch)
		}
		req.branchPrefix, req.defaultBranch = prefix, def
	case OpBranchResume, OpApprovedMerge:
		id, present, failure := str(FieldChangeSetID)
		if failure != nil {
			return req, failure
		}
		if !present {
			return req, invalid("A required field is missing.", FieldChangeSetID)
		}
		if !changeSetIDPattern.MatchString(id) {
			return req, mismatch(FieldChangeSetID)
		}
		req.changeSetID = id
	case OpCommit:
		body, _, failure := str(FieldBody)
		if failure != nil {
			return req, failure
		}
		if utf8.RuneCountInString(body) > MaxBodyLength || !utf8.ValidString(body) || hasControl(body) {
			return req, mismatch(FieldBody)
		}
		if render.HasTrailerLine(body) {
			return req, invalid("The body holds a line that starts with Change-Set:.", FieldBody)
		}
		if render.ActsOnGitHub(body) {
			return req, invalid("The body holds text that GitHub acts on.", FieldBody)
		}
		req.body = body
	}
	return req, nil
}

func hasControl(s string) bool {
	for _, r := range s {
		if (r < 0x20 && r != '\n' && r != '\t' && r != '\r') || r == 0x7f {
			return true
		}
	}
	return false
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
