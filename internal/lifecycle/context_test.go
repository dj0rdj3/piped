package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestOperationFollowsBothAuthoritiesAndKeepsCallerValues(t *testing.T) {
	for _, source := range []string{"caller", "service", "deadline", "explicit"} {
		t.Run(source, func(t *testing.T) {
			type key struct{}
			parent, cancelParent := context.WithCancel(context.WithValue(context.Background(), key{}, "caller authority"))
			defer cancelParent()
			service, cancelService := context.WithCancel(context.Background())
			defer cancelService()
			ctx, cancel := Operation(parent, service, 30*time.Millisecond)
			defer cancel()
			if ctx.Value(key{}) != "caller authority" {
				t.Fatal("operation lost its authenticated caller")
			}
			expected := context.Canceled
			switch source {
			case "caller":
				cancelParent()
			case "service":
				cancelService()
			case "explicit":
				cancel()
			case "deadline":
				expected = context.DeadlineExceeded
			}
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
				t.Fatal("operation ignored cancellation/deadline")
			}
			if !errors.Is(ctx.Err(), expected) {
				t.Fatal("wrong cancellation reason", ctx.Err())
			}
			if source == "explicit" && (parent.Err() != nil || service.Err() != nil) {
				t.Fatal("operation cancelled its caller or service")
			}
		})
	}
}

func TestOperationDoesNotExtendCallerDeadline(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ctx, stop := Operation(parent, context.Background(), time.Hour)
	defer stop()
	expected, _ := parent.Deadline()
	actual, _ := ctx.Deadline()
	if actual != expected {
		t.Fatal("operation extended the request deadline")
	}
}

func TestOperationRejectsAnAlreadyStoppedServiceImmediately(t *testing.T) {
	service, stop := context.WithCancel(context.Background())
	stop()
	for range 100 {
		operation, cancel := Operation(context.Background(), service, time.Hour)
		if !errors.Is(operation.Err(), context.Canceled) {
			cancel()
			t.Fatal("operation started after service shutdown")
		}
		cancel()
	}
}
