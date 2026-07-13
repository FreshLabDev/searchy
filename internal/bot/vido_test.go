package bot

import "testing"

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
