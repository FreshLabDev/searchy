package vido

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestBridgeLeastPrivilegeLifecycle(t *testing.T) {
	databaseURL := os.Getenv("SEARCHY_BRIDGE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("requires disposable bridge Postgres")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	bridge, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()

	chatID := int64(-100515151)
	token, err := bridge.MintIntent(ctx, Intent{
		OwnerUserID:   515151,
		Kind:          "video",
		DeliveryMode:  "searchy_chat",
		SourceURL:     "https://example.com/video/515151",
		Platform:      "other",
		SourceSurface: "searchy_chat",
		OriginChatID:  &chatID,
		Username:      "searchy-bridge-test",
		FirstName:     "Searchy",
		TelegramLang:  "en",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 32 {
		t.Fatalf("token length = %d, want 32", len(token))
	}
	if err := bridge.BindIntentMessage(ctx, token, 515151, chatID, 91); err != nil {
		t.Fatal(err)
	}
	_, err = bridge.Enqueue(ctx, EnqueueArgs{
		Token: token, ActorID: 999999, ChatID: chatID, MessageID: 91,
		RequestKey: "integration:wrong-owner",
	})
	if !errors.Is(err, ErrNotOwner) {
		t.Fatalf("wrong owner error = %v", err)
	}
	state, err := bridge.Enqueue(ctx, EnqueueArgs{
		Token: token, ActorID: 515151, ChatID: chatID, MessageID: 91,
		RequestKey: "integration:searchy",
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "queued" || state.JobID <= 0 {
		t.Fatalf("unexpected state: %+v", state)
	}
	replay, err := bridge.Enqueue(ctx, EnqueueArgs{
		Token: token, ActorID: 515151, ChatID: chatID, MessageID: 91,
		RequestKey: "integration:searchy-replay",
	})
	if err != nil || replay.JobID != state.JobID {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if _, err := bridge.pool.Exec(ctx, "SELECT count(*) FROM vido.bridge_jobs"); err == nil {
		t.Fatal("searchy_core unexpectedly read a private bridge table")
	}
}
