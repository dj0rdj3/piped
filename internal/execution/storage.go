package execution

import (
	"context"
	"time"

	"piped/internal/store"
)

// These ports describe durable boundaries, not transport or SQL details. Failure
// tests can reproduce an exact boundary without corrupting a live schema.
type admissionStorage interface {
	AdmitRun(context.Context, store.Actor, store.RunAdmission) (store.Admission, error)
	CreateScheduledRun(context.Context, string, string, int64, int64, func() time.Time) (store.CreatedRun, error)
}

type resultStorage interface {
	SetRunHost(context.Context, string, string, string, string, *int) error
	FinishRun(context.Context, string, string) error
	FailUnlaunchedRun(context.Context, string) error
	AppendOutput(context.Context, string, string, []byte) error
}

// commandAuthority rechecks current session/key authority at control admission.
type commandAuthority interface {
	Authorize(context.Context, store.Actor, string) error
}
