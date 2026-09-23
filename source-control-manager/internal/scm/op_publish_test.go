package scm

import (
	"testing"

	"github.com/redhat-et/protobot/source-control-manager/internal/result"
)

func TestPushTransportFailure(t *testing.T) {
	cases := []struct {
		code     result.Code
		mutation string
		retry    result.Retry
	}{
		// The connection can fail after the remote updated the ref.
		{result.RemoteUnavailable, result.MutationUnknown, result.RetryReconcile},
		// Git authenticates before it sends the pack.
		{result.CredentialUnavailable, result.MutationNone, result.RetryAuthorize},
	}
	for _, c := range cases {
		f := pushTransportFailure(c.code, result.Fail(c.code, "test"))
		if f.Mutation != c.mutation || f.Retry != c.retry {
			t.Errorf("%s: mutation %s, retry %s; want %s, %s", c.code, f.Mutation, f.Retry, c.mutation, c.retry)
		}
	}
}
