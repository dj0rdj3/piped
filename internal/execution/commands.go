package execution

import (
	"context"
	"strings"
	"unicode/utf8"

	"piped/internal/problem"
	"piped/internal/store"
)

// Commands belong to the application, so each transport uses the same validation,
// scope checks and target semantics. Authentication remains transport-specific.
type HostRequest struct {
	RunID  string `json:"run_id"`
	HostID string `json:"host_id"`
}

type InputRequest struct {
	RunID  string `json:"run_id"`
	HostID string `json:"host_id"`
	Data   string `json:"data"`
}

type ApprovalRequest struct {
	RunID       string `json:"run_id"`
	HostID      string `json:"host_id"`
	Fingerprint string `json:"fingerprint"`
	Accept      bool   `json:"accept"`
}

type OutputRequest struct {
	RunID  string `json:"run_id"`
	HostID string `json:"host_id"`
	After  int64  `json:"after,omitempty"`
	Tail   bool   `json:"tail,omitempty"`
}

type RunView struct {
	Run      store.Run `json:"run"`
	HostKeys []HostKey `json:"host_keys"`
}

var ErrOutputCursor = problem.New(problem.Invalid, "invalid output cursor")

type OutputPage struct {
	Chunks    []store.OutputChunk `json:"chunks"`
	Truncated *bool               `json:"truncated,omitempty"`
}

func (e *Engine) View(ctx context.Context, actor store.Actor, runID string) (RunView, error) {
	ctx, cancel := e.operationContext(ctx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return RunView{}, err
	}
	id, err := parseResourceID(runID)
	if err != nil {
		return RunView{}, err
	}
	run, err := e.db.Run(ctx, actor, id.value)
	if err != nil {
		return RunView{}, err
	}
	keys, err := e.Pending(ctx, actor, runID)
	return RunView{Run: run, HostKeys: keys}, err
}

func (e *Engine) Output(ctx context.Context, actor store.Actor, request OutputRequest) (OutputPage, error) {
	ctx, cancel := e.operationContext(ctx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return OutputPage{}, err
	}
	if err := validateTarget(request.RunID, request.HostID); err != nil {
		return OutputPage{}, err
	}
	if request.After < 0 || request.Tail && request.After != 0 {
		return OutputPage{}, ErrOutputCursor
	}
	if request.Tail {
		chunks, truncated, err := e.db.RunOutputTail(ctx, actor, request.RunID, request.HostID)
		return OutputPage{Chunks: chunks, Truncated: &truncated}, err
	}
	chunks, err := e.db.RunOutput(ctx, actor, request.RunID, request.HostID, request.After)
	return OutputPage{Chunks: chunks}, err
}

// resourceID cannot be constructed by a transport. Validation precedes lookup.
type resourceID struct{ value string }

func parseResourceID(value string) (resourceID, error) {
	if len(value) == 0 || len(value) > 128 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return resourceID{}, ErrInput
	}
	return resourceID{value: value}, nil
}
func validateTarget(runID, hostID string) error {
	if _, err := parseResourceID(runID); err != nil {
		return err
	}
	_, err := parseResourceID(hostID)
	return err
}

func (e *Engine) History(ctx context.Context, actor store.Actor, jobID string) ([]store.Run, error) {
	ctx, cancel := e.operationContext(ctx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id, err := parseResourceID(jobID)
	if err != nil {
		return nil, err
	}
	return e.db.Runs(ctx, actor, id.value)
}
