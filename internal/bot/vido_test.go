package bot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	telegram "github.com/go-telegram/bot"
)

func TestParseDownloadCallbacks(t *testing.T) {
	token := "0123456789abcdefghijklmnopqrstuv"
	for data, wantKind := range map[string]string{
		"vd:" + token: "video",
		"va:" + token: "audio",
		"vr:" + token: "retry",
	} {
		gotToken, gotKind, ok := parseDownloadCB(data)
		if !ok || gotToken != token || gotKind != wantKind {
			t.Fatalf("parseDownloadCB(%q) = %q, %q, %v", data, gotToken, gotKind, ok)
		}
	}
	if _, _, ok := parseDownloadCB("vd:short"); ok {
		t.Fatal("short bridge token accepted")
	}
}

func TestSearchyDownloadErrorKey(t *testing.T) {
	tests := map[string]string{
		"error.unsupported_platform": "download.unsupported",
		"error.file_too_large":       "download.too_large",
		"error.drm_protected":        "download.drm",
		"error.auth_required":        "download.auth_required",
		"error.rate_limited":         "download.rate_limited",
		"error.download_timeout":     "download.timeout",
		"error.content_not_found":    "download.not_found",
		"error.audio_only":           "download.audio_only",
		"audio.not_found":            "download.audio_not_found",
		"audio.failed":               "download.audio_failed",
		"error.other":                "download.failed",
	}
	for input, want := range tests {
		if got := searchyDownloadErrorKey(input); got != want {
			t.Fatalf("%s mapped to %q, want %q", input, got, want)
		}
	}
}

func TestAnswerCallbackURLUsesVidoDeepLinkWithoutExtraMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bottest-token/answerCallbackQuery" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if got := r.FormValue("callback_query_id"); got != "callback-1" {
			t.Errorf("callback_query_id = %q", got)
		}
		if got := r.FormValue("url"); got != "https://t.me/vidobot?start=ia_token" {
			t.Errorf("url = %q", got)
		}
		if got := r.FormValue("text"); got != "" {
			t.Errorf("unexpected callback text = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"ok":true,"result":true}`)
	}))
	defer server.Close()

	b, err := telegram.New(
		"test-token",
		telegram.WithSkipGetMe(),
		telegram.WithServerURL(server.URL),
	)
	if err != nil {
		t.Fatal(err)
	}

	(&Handlers{}).answerCBURL(
		context.Background(),
		b,
		"callback-1",
		"https://t.me/vidobot?start=ia_token",
	)
}
