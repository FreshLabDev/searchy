package bot

import (
	"context"
	"errors"
	"testing"
	"time"

	vidobridge "searchy/internal/vido"
)

func TestOperationMarkupEmptyIsNilInterface(t *testing.T) {
	t.Parallel()

	if got := operationMarkup(nil); got != nil {
		t.Fatalf("empty buttons produced typed non-nil markup: %#v", got)
	}
}

func TestOperationMarkupAudioButton(t *testing.T) {
	t.Parallel()

	if got := operationMarkup([]vidobridge.Button{{Type: "audio", Text: "Audio", Token: "token"}}); got == nil {
		t.Fatal("audio button markup is nil")
	}
}

func TestRetryBridgeCommitRetriesTemporaryFailure(t *testing.T) {
	t.Parallel()

	attempts := 0
	err := retryBridgeCommit(
		context.Background(),
		[]time.Duration{0},
		func(context.Context) error {
			attempts++
			if attempts == 1 {
				return errors.New("temporary database failure")
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("retryBridgeCommit() error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("retryBridgeCommit() attempts = %d, want 2", attempts)
	}
}

func TestRetryBridgeCommitReturnsLastError(t *testing.T) {
	t.Parallel()

	want := errors.New("database unavailable")
	attempts := 0
	err := retryBridgeCommit(
		context.Background(),
		[]time.Duration{0, 0},
		func(context.Context) error {
			attempts++
			return want
		},
	)
	if !errors.Is(err, want) {
		t.Fatalf("retryBridgeCommit() error = %v, want %v", err, want)
	}
	if attempts != 3 {
		t.Fatalf("retryBridgeCommit() attempts = %d, want 3", attempts)
	}
}
