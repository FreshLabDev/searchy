package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/FreshLabDev/tg"

	"searchy/internal/i18n"
	"searchy/internal/search"
)

func TestSendGridPickRejectsVideoWithoutCover(t *testing.T) {
	cover := httptest.NewServer(http.NotFoundHandler())
	defer cover.Close()

	var sentPhoto int
	var sentText string
	api := telegramTestServer(t, func(method string, fields map[string]string) {
		switch method {
		case "sendPhoto":
			sentPhoto++
		case "sendMessage":
			sentText = fields["text"]
		}
	})
	defer api.Close()

	h := &Handlers{
		api:        newTestClient(t, api.URL),
		httpClient: http.DefaultClient,
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	h.sendGridPick(context.Background(), 42, 0, &gridSession{
		lang: "ru",
		results: []search.MediaResult{{
			ID: "dead-cover", Category: search.CatVideo, Title: "Unavailable video",
			ThumbURL: cover.URL + "/missing.jpg", PageURL: "https://video.example/watch/1",
			Engine: "sepiasearch", Pool: search.PoolDiscovery,
		}},
	}, 0, &tg.User{ID: 7})

	if sentPhoto != 0 {
		t.Fatalf("sent %d photo cards for an unavailable cover", sentPhoto)
	}
	if sentText != i18n.T("ru", "load.failed") {
		t.Fatalf("retry text = %q, want %q", sentText, i18n.T("ru", "load.failed"))
	}
}

func TestSendGridPickSendsVideoOnlyAsPhotoCard(t *testing.T) {
	cover := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(testJPEG(t))
	}))
	defer cover.Close()

	var sentPhoto int
	var sentMessage int
	var caption string
	api := telegramTestServer(t, func(method string, fields map[string]string) {
		switch method {
		case "sendPhoto":
			sentPhoto++
			caption = fields["caption"]
		case "sendMessage":
			sentMessage++
		}
	})
	defer api.Close()

	h := &Handlers{
		api:        newTestClient(t, api.URL),
		httpClient: http.DefaultClient,
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	h.sendGridPick(context.Background(), 42, 0, &gridSession{
		lang: "ru",
		results: []search.MediaResult{{
			ID: "live-cover", Category: search.CatVideo, Title: "Available video",
			ThumbURL: cover.URL + "/cover.jpg", PageURL: "https://video.example/watch/2",
			Engine: "peertube", Pool: search.PoolDiscovery,
		}},
	}, 0, &tg.User{ID: 7})

	if sentPhoto != 1 || sentMessage != 0 {
		t.Fatalf("sentPhoto=%d sentMessage=%d, want 1/0", sentPhoto, sentMessage)
	}
	if !strings.Contains(caption, "Available video") {
		t.Fatalf("caption = %q", caption)
	}
}

// telegramTestServer stubs the Bot API and hands each call to inspect. The
// client posts JSON for everything except an upload, so both shapes are
// flattened into the same map of parameters.
func telegramTestServer(t *testing.T, inspect func(method string, fields map[string]string)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimPrefix(r.URL.Path, "/bot"+testToken+"/")
		inspect(method, telegramRequestFields(t, r))
		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "sendPhoto", "sendMessage":
			_, _ = fmt.Fprint(w, `{"ok":true,"result":{"message_id":10,"date":0,"chat":{"id":42,"type":"private"}}}`)
		default:
			_, _ = fmt.Fprint(w, `{"ok":true,"result":true}`)
		}
	}))
}

func telegramRequestFields(t *testing.T, r *http.Request) map[string]string {
	t.Helper()
	fields := map[string]string{}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if err := r.ParseMultipartForm(6 << 20); err != nil {
			t.Errorf("parse Telegram form: %v", err)
			return fields
		}
		for name, values := range r.MultipartForm.Value {
			if len(values) > 0 {
				fields[name] = values[0]
			}
		}
		return fields
	}
	var body map[string]any
	raw, err := io.ReadAll(r.Body)
	if err != nil || json.Unmarshal(raw, &body) != nil {
		t.Errorf("parse Telegram body: %v", err)
		return fields
	}
	for name, value := range body {
		if text, ok := value.(string); ok {
			fields[name] = text
			continue
		}
		encoded, _ := json.Marshal(value)
		fields[name] = string(encoded)
	}
	return fields
}

const testToken = "test-token"

func newTestClient(t *testing.T, serverURL string) *tg.Client {
	t.Helper()
	return tg.New(testToken, tg.WithAPIBase(serverURL))
}

func testJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.White)
	var data bytes.Buffer
	if err := jpeg.Encode(&data, img, nil); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}
