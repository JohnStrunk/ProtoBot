package github

import (
	"slices"

	"github.com/redhat-et/protobot/wms/validation"
)

// subsetOperations is the #68 first-slice surface implemented by the GitHub
// translator: request reads/writes, materialization, status queries, and
// claim for compare-and-swap coverage. It lives in this package, not in the
// backend-neutral wms/adapter surface, so no other backend inherits or
// consults a GitHub-specific operation subset.
var subsetOperations = []validation.Operation{
	validation.OperationRequestCreate,
	validation.OperationRequestRefine,
	validation.OperationRequestUpdatePriority,
	validation.OperationRequestLinkChangeSet,
	validation.OperationRequestLinkBuildWorkItem,
	validation.OperationRequestGet,
	validation.OperationRequestQuery,
	validation.OperationWorkItemGet,
	validation.OperationWorkItemQuery,
	validation.OperationBlockedWorkQuery,
	validation.OperationMaterialize,
	validation.OperationClaim,
}

// isSubsetOperation reports whether operation is in the #68 first slice.
func isSubsetOperation(operation validation.Operation) bool {
	return slices.Contains(subsetOperations, operation)
}
