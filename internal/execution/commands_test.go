package execution

import (
	"context"
	"errors"
	"testing"
)

func TestReadCommandsFollowCallerAndServiceCancellation(t *testing.T) {
	f := newFixture(t, "parallel", false)
	for _, service := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if service {
			f.engine.Close()
		} else {
			cancel()
		}
		if _, err := f.engine.View(ctx, f.actor, "missing"); !errors.Is(err, context.Canceled) {
			t.Fatalf("view ignored cancellation (service=%v): %v", service, err)
		}
		if _, err := f.engine.Output(ctx, f.actor, OutputRequest{RunID: "missing", HostID: "missing"}); !errors.Is(err, context.Canceled) {
			t.Fatalf("output ignored cancellation (service=%v): %v", service, err)
		}
		cancel()
	}
}
