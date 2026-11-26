package response

import (
	"errors"
	"fmt"
	"testing"

	"piped/internal/problem"
	"piped/internal/store"
)

func TestApplicationFailureMappingDoesNotExposeWrappedPrivateContext(t *testing.T) {
	for _, fixture := range []struct {
		kind   problem.Kind
		status int
	}{
		{problem.Invalid, 400}, {problem.Unauthorized, 401}, {problem.Forbidden, 403},
		{problem.Missing, 404}, {problem.Conflict, 409}, {problem.Unavailable, 503},
	} {
		public := PublicError(fmt.Errorf("private SQL or credential context: %w", problem.New(fixture.kind, "safe public message")))
		if public.Status != fixture.status || public.Message != "safe public message" {
			t.Fatal("application failure leaked wrapper context", public)
		}
	}
	public := PublicError(&store.UncertainCommit{RunID: "private-run", Cause: errors.New("secret database connection")})
	if public.Status != 503 || public.Message != "execution admission outcome is uncertain; retry with the same request_id" {
		t.Fatal("ambiguous commit leaked internals", public)
	}
}
