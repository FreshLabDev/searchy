package bot

import (
	"context"
	"testing"
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
	var called int
	var got map[string]string
	server := telegramTestServer(t, func(method string, fields map[string]string) {
		if method != "answerCallbackQuery" {
			t.Errorf("method = %q", method)
		}
		called++
		got = fields
	})
	defer server.Close()

	(&Handlers{api: newTestClient(t, server.URL)}).answerCBURL(
		context.Background(),
		"callback-1",
		"https://t.me/vidobot?start=ia_token",
	)

	if called != 1 {
		t.Fatalf("answerCallbackQuery called %d times, want 1", called)
	}
	if got["callback_query_id"] != "callback-1" {
		t.Errorf("callback_query_id = %q", got["callback_query_id"])
	}
	if got["url"] != "https://t.me/vidobot?start=ia_token" {
		t.Errorf("url = %q", got["url"])
	}
	if _, has := got["text"]; has {
		t.Errorf("a toast alongside the deep link is a second thing to dismiss: %q", got["text"])
	}
}
