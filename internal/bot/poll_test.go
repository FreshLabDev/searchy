package bot

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// A poll must confirm the batch it just received, or Telegram redelivers it
// forever and the bot answers every query twice.
func TestRunAdvancesTheOffsetPastEachBatch(t *testing.T) {
	var mu sync.Mutex
	var offsets []string
	polled := make(chan struct{})
	var once sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasSuffix(r.URL.Path, "/getUpdates") {
			_, _ = fmt.Fprint(w, `{"ok":true,"result":true}`)
			return
		}
		mu.Lock()
		offsets = append(offsets, r.URL.Query().Get("offset"))
		round := len(offsets)
		mu.Unlock()
		if round == 1 {
			// Two updates the router drops on the floor: this is about the
			// loop, not the handlers.
			_, _ = fmt.Fprint(w, `{"ok":true,"result":[{"update_id":10},{"update_id":11}]}`)
			return
		}
		once.Do(func() { close(polled) })
		_, _ = fmt.Fprint(w, `{"ok":true,"result":[]}`)
	}))
	defer server.Close()

	h := &Handlers{api: newTestClient(t, server.URL), log: discardLogger(), workers: 4}
	ctx, cancel := context.WithCancel(context.Background())
	finished := runInBackground(t, h, ctx)

	select {
	case <-polled:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop never polled a second time")
	}
	cancel()
	<-finished

	mu.Lock()
	defer mu.Unlock()
	if offsets[0] != "" {
		t.Fatalf("first poll sent offset %q, want none", offsets[0])
	}
	if offsets[1] != "12" {
		t.Fatalf("second poll sent offset %q, want 12 (past update 11)", offsets[1])
	}
}

// A Telegram outage must not end the process: the loop backs off and keeps
// trying. The stub answers 200 with a result the client cannot read, which is
// the one failure the client does not retry on its own — so what recovers here
// is the poll loop and nothing else.
func TestRunRecoversFromAFailedPoll(t *testing.T) {
	var mu sync.Mutex
	var polls int
	recovered := make(chan struct{})
	var once sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		polls++
		round := polls
		mu.Unlock()
		if round <= 2 {
			_, _ = fmt.Fprint(w, `{"ok":true,"result":"not an update list"}`)
			return
		}
		once.Do(func() { close(recovered) })
		_, _ = fmt.Fprint(w, `{"ok":true,"result":[]}`)
	}))
	defer server.Close()

	h := &Handlers{
		api:             newTestClient(t, server.URL),
		log:             discardLogger(),
		workers:         1,
		pollBackoffStep: time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := runInBackground(t, h, ctx)

	select {
	case <-recovered:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop gave up after a failed poll")
	}
	cancel()
	<-finished
}

// The loop has to actually reach the router. A callback for a message that is
// no longer accessible is the cheapest update to prove it with: the handler
// answers the query and returns.
func TestRunDispatchesUpdatesToTheRouter(t *testing.T) {
	answered := make(chan struct{})
	var once sync.Once
	var sent sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			body := `{"ok":true,"result":[]}`
			sent.Do(func() {
				body = `{"ok":true,"result":[{"update_id":1,"callback_query":` +
					`{"id":"cb","from":{"id":7,"is_bot":false,"first_name":"A"},` +
					`"chat_instance":"c","data":"m:7:home"}}]}`
			})
			_, _ = fmt.Fprint(w, body)
		case strings.HasSuffix(r.URL.Path, "/answerCallbackQuery"):
			once.Do(func() { close(answered) })
			_, _ = fmt.Fprint(w, `{"ok":true,"result":true}`)
		default:
			_, _ = fmt.Fprint(w, `{"ok":true,"result":true}`)
		}
	}))
	defer server.Close()

	h := &Handlers{api: newTestClient(t, server.URL), log: discardLogger(), workers: 4}
	ctx, cancel := context.WithCancel(context.Background())
	finished := runInBackground(t, h, ctx)

	select {
	case <-answered:
	case <-time.After(5 * time.Second):
		t.Fatal("the dispatched update never reached a handler")
	}
	cancel()
	// Run returns only once every update it dispatched has returned, so the
	// caller can close the database and the client behind it.
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("Run never returned after its context was cancelled")
	}
}

func runInBackground(t *testing.T, h *Handlers, ctx context.Context) <-chan struct{} {
	t.Helper()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		if err := h.Run(ctx); err != nil {
			t.Errorf("Run: %v", err)
		}
	}()
	return finished
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
