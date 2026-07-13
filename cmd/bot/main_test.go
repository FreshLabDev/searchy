package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTelegramGetMeUsesParameterlessGET(t *testing.T) {
	t.Parallel()

	const token = "123456:test-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/bot"+token+"/getMe" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"result":{"id":42,"is_bot":true,"first_name":"Searchy","username":"srchybot"}}`)
	}))
	defer server.Close()

	me, err := telegramGetMe(context.Background(), server.Client(), server.URL+"/", token)
	if err != nil {
		t.Fatal(err)
	}
	if me.ID != 42 || me.Username != "srchybot" {
		t.Fatalf("unexpected user: %+v", me)
	}
}

func TestTelegramGetMeWithRetryWaitsForBotAPIReadiness(t *testing.T) {
	t.Parallel()

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"result":{"id":42,"is_bot":true,"first_name":"Searchy","username":"srchybot"}}`)
	}))
	defer server.Close()

	me, err := telegramGetMeWithRetry(
		context.Background(), server.Client(), server.URL, "test-token",
		time.Millisecond, 2*time.Millisecond, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	if me.Username != "srchybot" {
		t.Fatalf("username = %q", me.Username)
	}
}

func TestTelegramGetMeWithRetryFailsFastOnBadToken(t *testing.T) {
	t.Parallel()

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := telegramGetMeWithRetry(
		context.Background(), server.Client(), server.URL, "bad-token",
		time.Millisecond, 2*time.Millisecond, nil,
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestTelegramGetMeWithRetryFailsFastWithoutLeakingTokenOnBadBaseURL(t *testing.T) {
	t.Parallel()

	const token = "123456:do-not-log"
	_, err := telegramGetMeWithRetry(
		context.Background(), http.DefaultClient, "ftp://bad-base", token,
		time.Millisecond, 2*time.Millisecond, nil,
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); got != "invalid Telegram Bot API base URL" {
		t.Fatalf("unexpected safe error: %q", got)
	}
}

func TestTelegramAPIEndpointRejectsMalformedBaseURLs(t *testing.T) {
	t.Parallel()

	for _, baseURL := range []string{
		"http://:8081",
		"https://user@example.com",
		"https://example.com?mode=local",
		"https://example.com#fragment",
	} {
		if _, err := telegramAPIEndpoint(baseURL, "test-token", "getMe"); err == nil {
			t.Errorf("telegramAPIEndpoint(%q) accepted malformed base", baseURL)
		}
	}
}

func TestTelegramGetMeWithRetryStopsOnContextDeadline(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	_, err := telegramGetMeWithRetry(
		ctx, server.Client(), server.URL, "test-token",
		time.Second, time.Second, nil,
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline", err)
	}
}

func TestTelegramGetMeErrorDoesNotExposeToken(t *testing.T) {
	t.Parallel()

	const token = "123456:do-not-log"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	_, err := telegramGetMe(context.Background(), server.Client(), server.URL, token)
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); got != "getMe returned HTTP 502" {
		t.Fatalf("unexpected safe error: %q", got)
	}
}

func TestTelegramDeleteWebhookUsesParameterlessGET(t *testing.T) {
	t.Parallel()

	const token = "123456:test-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/bot"+token+"/deleteWebhook" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("drop_pending_updates"); got != "false" {
			t.Errorf("drop_pending_updates = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	}))
	defer server.Close()

	if err := telegramDeleteWebhook(context.Background(), server.Client(), server.URL, token); err != nil {
		t.Fatal(err)
	}
}
