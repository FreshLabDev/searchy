package bot

import (
	"testing"

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
