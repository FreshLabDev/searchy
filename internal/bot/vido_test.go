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
	if got := searchyDownloadErrorKey("error.unsupported_platform"); got != "download.unsupported" {
		t.Fatalf("unsupported mapped to %q", got)
	}
	if got := searchyDownloadErrorKey("error.auth_required"); got != "download.failed" {
		t.Fatalf("generic failure mapped to %q", got)
	}
}
