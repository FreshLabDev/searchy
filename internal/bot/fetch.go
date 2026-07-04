package bot

import (
	"context"
	"io"
	"net/http"
	"strings"
)

const maxImageBytes = 5 << 20 // 5 MiB per image

// downloadImage fetches a single image URL, returning its bytes. It enforces an
// image/* content type and a size cap. Fetching bytes ourselves — instead of
// handing Telegram the URL — avoids WEBPAGE_CURL_FAILED and lets us composite
// the grid locally.
func (h *Handlers) downloadImage(ctx context.Context, url string) ([]byte, bool) {
	if h.httpClient == nil || url == "" {
		return nil, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; searchy-bot/1.0)")
	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, false
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/") {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxImageBytes {
		return nil, false
	}
	return data, true
}
