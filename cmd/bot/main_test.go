package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
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
