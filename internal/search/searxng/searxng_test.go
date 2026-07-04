package searxng

import (
	"encoding/json"
	"testing"

	"searchy/internal/search"
)

func TestValidURL(t *testing.T) {
	good := []string{
		"https://example.com/a.jpg",
		"https://i.ytimg.com/vi/abc/hqdefault.jpg",
		"https://p16.tiktokcdn.com/cover.jpg?x=1",
	}
	bad := []string{
		"",
		"http://example.com/a.jpg",    // not https
		"https://localhost/a.jpg",     // no dot in host
		"https://example.com/a b.jpg", // space
		"https://example.com/\na.jpg", // control char
		"ftp://example.com/a.jpg",     // wrong scheme
		"https://",                    // no host
		"data:image/png;base64,iVBOR", // data uri
	}
	for _, u := range good {
		if !validURL(u) {
			t.Errorf("validURL(%q) = false, want true", u)
		}
	}
	for _, u := range bad {
		if validURL(u) {
			t.Errorf("validURL(%q) = true, want false", u)
		}
	}
}

func TestParseDuration(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{`"3:21"`, 201},
		{`"1:02:33"`, 3753},
		{`"45"`, 45},
		{`90`, 90},
		{`""`, 0},
		{`null`, 0},
		{`"bogus"`, 0},
	}
	for _, c := range cases {
		got := parseDuration(json.RawMessage(c.in))
		if got != c.want {
			t.Errorf("parseDuration(%s) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestNormalizeImage(t *testing.T) {
	// Untrusted engine (e.g. bing): photo must be the reliable CDN thumbnail_src,
	// NOT the origin img_src (which often 403s / is oversized).
	r := &rawResult{
		Template:     "images.html",
		Engines:      []string{"bing images"},
		Title:        "A cat",
		ImgSrc:       "https://origin.example.com/cat.jpg",
		ThumbnailSrc: "https://tse1.mm.bing.net/th?id=cat",
		Source:       "example.com",
		URL:          "https://example.com/page",
	}
	mr, ok := normalize(r)
	if !ok {
		t.Fatal("expected image to normalize")
	}
	if mr.MediaURL != r.ThumbnailSrc {
		t.Errorf("untrusted engine should send thumbnail_src, got %q", mr.MediaURL)
	}

	// Trusted-origin engine (unsplash): photo should be the higher-res origin.
	r2 := &rawResult{
		Template:     "images.html",
		Engines:      []string{"unsplash"},
		ImgSrc:       "https://images.unsplash.com/photo-123.jpg",
		ThumbnailSrc: "https://images.unsplash.com/photo-123-thumb.jpg",
		URL:          "https://unsplash.com/p/123",
	}
	if mr2, _ := normalize(r2); mr2.MediaURL != r2.ImgSrc {
		t.Errorf("trusted engine should send img_src, got %q", mr2.MediaURL)
	}

	// No usable URL (both http) → dropped.
	r3 := &rawResult{Template: "images.html", Engines: []string{"bing images"}, ImgSrc: "http://x/c.jpg", ThumbnailSrc: "http://x/t.jpg"}
	if _, ok := normalize(r3); ok {
		t.Error("expected image with no https URL to be dropped")
	}
}

func TestNormalizeVideo(t *testing.T) {
	r := &rawResult{
		Template:  "videos.html",
		Title:     "Clip",
		Author:    "Uploader",
		Thumbnail: "https://i.ytimg.com/vi/abc/hqdefault.jpg",
		URL:       "https://youtube.com/watch?v=abc",
		Length:    json.RawMessage(`"3:21"`),
	}
	mr, ok := normalize(r)
	if !ok {
		t.Fatal("expected video to normalize")
	}
	if mr.Category != search.CatVideo {
		t.Errorf("unexpected video category: %+v", mr)
	}
	if mr.ThumbURL != r.Thumbnail || mr.PageURL != r.URL || mr.DurationSec != 201 {
		t.Errorf("unexpected video fields: %+v", mr)
	}

	// No iframe_src needed anymore — a cover + page link is enough (any platform).
	noIframe := &rawResult{
		Template:  "videos.html",
		Title:     "TikTok clip",
		Thumbnail: "https://p16.tiktokcdn.com/cover.jpg",
		URL:       "https://www.tiktok.com/@u/video/123",
	}
	if mr2, ok := normalize(noIframe); !ok || mr2.PageURL == "" {
		t.Error("expected cross-platform video (no iframe) to be kept")
	}

	// A non-HTTPS / missing cover can't render — must be dropped.
	r.Thumbnail = "http://insecure/cover.jpg"
	if _, ok := normalize(r); ok {
		t.Error("expected video with non-https cover to be dropped")
	}
}
