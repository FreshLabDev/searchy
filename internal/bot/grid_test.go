package bot

import (
	"bytes"
	"context"
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

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"searchy/internal/i18n"
	"searchy/internal/search"
)

func TestSendGridPickRejectsVideoWithoutCover(t *testing.T) {
	cover := httptest.NewServer(http.NotFoundHandler())
	defer cover.Close()

	var sentPhoto int
	var sentText string
	api := telegramTestServer(t, func(method string, r *http.Request) {
		switch method {
		case "sendPhoto":
			sentPhoto++
		case "sendMessage":
			sentText = r.FormValue("text")
		}
	})
	defer api.Close()

	h := &Handlers{
		httpClient: http.DefaultClient,
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	h.sendGridPick(context.Background(), newTelegramTestBot(t, api.URL), 42, 0, &gridSession{
		lang: "ru",
		results: []search.MediaResult{{
			ID: "dead-cover", Category: search.CatVideo, Title: "Unavailable video",
			ThumbURL: cover.URL + "/missing.jpg", PageURL: "https://video.example/watch/1",
			Engine: "sepiasearch", Pool: search.PoolDiscovery,
		}},
	}, 0, &models.User{ID: 7})

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
	api := telegramTestServer(t, func(method string, r *http.Request) {
		switch method {
		case "sendPhoto":
			sentPhoto++
			caption = r.FormValue("caption")
		case "sendMessage":
			sentMessage++
		}
	})
	defer api.Close()

	h := &Handlers{
		httpClient: http.DefaultClient,
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	h.sendGridPick(context.Background(), newTelegramTestBot(t, api.URL), 42, 0, &gridSession{
		lang: "ru",
		results: []search.MediaResult{{
			ID: "live-cover", Category: search.CatVideo, Title: "Available video",
			ThumbURL: cover.URL + "/cover.jpg", PageURL: "https://video.example/watch/2",
			Engine: "peertube", Pool: search.PoolDiscovery,
		}},
	}, 0, &models.User{ID: 7})

	if sentPhoto != 1 || sentMessage != 0 {
		t.Fatalf("sentPhoto=%d sentMessage=%d, want 1/0", sentPhoto, sentMessage)
	}
	if !strings.Contains(caption, "Available video") {
		t.Fatalf("caption = %q", caption)
	}
}

func telegramTestServer(t *testing.T, inspect func(string, *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(6 << 20); err != nil {
			t.Errorf("parse Telegram form: %v", err)
		}
		method := strings.TrimPrefix(r.URL.Path, "/bottest-token/")
		inspect(method, r)
		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "sendPhoto", "sendMessage":
			_, _ = fmt.Fprint(w, `{"ok":true,"result":{"message_id":10,"date":0,"chat":{"id":42,"type":"private"}}}`)
		default:
			_, _ = fmt.Fprint(w, `{"ok":true,"result":true}`)
		}
	}))
}

func newTelegramTestBot(t *testing.T, serverURL string) *telegram.Bot {
	t.Helper()
	b, err := telegram.New("test-token", telegram.WithSkipGetMe(), telegram.WithServerURL(serverURL))
	if err != nil {
		t.Fatal(err)
	}
	return b
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
