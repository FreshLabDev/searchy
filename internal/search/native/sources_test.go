package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"searchy/internal/search"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fixtureResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}
}

func fixtureSources(t *testing.T, safe int, fn func(*http.Request) (*http.Response, error)) []search.Source {
	t.Helper()
	return NewSources(Options{HTTP: &http.Client{Transport: transportFunc(fn)}, SafeSearch: safe})
}

func sourceFor(t *testing.T, sources []search.Source, name string) search.Source {
	t.Helper()
	for _, source := range sources {
		if source.Name() == name {
			return source
		}
	}
	t.Fatalf("missing source %q", name)
	return nil
}

func bingImage(i int) string {
	b, _ := json.Marshal(map[string]any{"murl": fmt.Sprintf("https://images.example.com/%d.jpg", i), "turl": fmt.Sprintf("https://thumb.example.com/%d.jpg", i), "purl": fmt.Sprintf("https://pages.example.com/%d", i), "t": "Panda & snow", "w": "800", "h": 600})
	return `<a class="iusc" m="` + html.EscapeString(string(b)) + `"></a>`
}

func duckBody(start, count, next int) string {
	var results []map[string]any
	for i := start; i < start+count; i++ {
		results = append(results, map[string]any{"title": fmt.Sprintf("Panda %d", i), "image": fmt.Sprintf("https://images.example.com/%d.jpg", i), "thumbnail": fmt.Sprintf("https://thumb.example.com/%d.jpg", i), "url": fmt.Sprintf("https://pages.example.com/%d", i), "width": 800, "height": 600})
	}
	if results == nil {
		results = make([]map[string]any, 0)
	}
	response := map[string]any{"results": results}
	if next >= 0 {
		response["next"] = fmt.Sprintf("/i.js?q=secret-query&s=%d&vqd=4-123-456", next)
	}
	b, _ := json.Marshal(response)
	return string(b)
}

func TestBingFixtureParsingAndExactOffsets(t *testing.T) {
	var offsets []int
	sources := fixtureSources(t, 0, func(r *http.Request) (*http.Response, error) {
		first, _ := strconv.Atoi(r.URL.Query().Get("first"))
		offsets = append(offsets, first)
		if first == 36 {
			return fixtureResponse(bingImage(35)), nil
		}
		var body strings.Builder
		for i := range 35 {
			body.WriteString(bingImage(i))
		}
		return fixtureResponse(body.String()), nil
	})
	source := sourceFor(t, sources, "Bing images")
	page, err := source.SearchSource(context.Background(), search.SourceRequest{Query: "secret-query", Language: "pl"})
	if err != nil || len(page.Results) != 35 || page.NextCursor != "36" || page.Results[0].Title != "Panda & snow" || page.Results[0].Width != 800 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	next, err := source.SearchSource(context.Background(), search.SourceRequest{Query: "secret-query", Cursor: page.NextCursor})
	if err != nil || len(next.Results) != 1 || next.NextCursor != "" || !reflect.DeepEqual(offsets, []int{1, 36}) {
		t.Fatalf("offsets=%v page=%+v err=%v", offsets, next, err)
	}
}

func TestBingVideoUsesJPEGCoverAndWatchPage(t *testing.T) {
	body := []byte(`<div class="vrhdata" vrhm='{"murl":"https://www.youtube.com/watch?v=abc","vt":"Panda","du":"1:23"}'></div><img class="rms" data-src-hq="https://tse1.mm.bing.net/th?id=OVP.abc&amp;pid=2.1">`)
	results, _, _, degraded, err := parseBing(body, search.CatVideo, "Bing videos", 0)
	if err != nil || degraded || len(results) != 1 || results[0].PageURL != "https://www.youtube.com/watch?v=abc" || results[0].DurationSec != 83 || results[0].ThumbURL != "https://tse1.mm.bing.net/th?id=OVP.abc&pid=2.1" {
		t.Fatalf("results=%+v degraded=%v err=%v", results, degraded, err)
	}
}

func TestDuckPagingDoesNotSkipProviderTail(t *testing.T) {
	var offsets []int
	sources := fixtureSources(t, 0, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/" {
			return fixtureResponse(`vqd="4-123-456"`), nil
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("s"))
		offsets = append(offsets, offset)
		if offset == 123 {
			return fixtureResponse(duckBody(123, 3, -1)), nil
		}
		return fixtureResponse(duckBody(0, 123, 123)), nil
	})
	source := sourceFor(t, sources, "DuckDuckGo images")
	cursor, seen := "", make(map[string]bool)
	var counts []int
	for range 4 {
		page, err := source.SearchSource(context.Background(), search.SourceRequest{Query: "secret-query", Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		counts = append(counts, len(page.Results))
		for _, r := range page.Results {
			if seen[r.ID] {
				t.Fatal("duplicate record across pages")
			}
			seen[r.ID] = true
		}
		if len(page.Results) > 50 {
			t.Fatal("source page exceeds Telegram bound")
		}
		cursor = page.NextCursor
		if cursor != "" {
			decoded, err := base64Cursor(cursor)
			if err != nil || strings.Contains(decoded, "secret-query") || strings.Contains(decoded, "https:") {
				t.Fatal("query or URL in cursor")
			}
		}
	}
	if cursor != "" || len(seen) != 126 || !reflect.DeepEqual(counts, []int{50, 50, 23, 3}) || !reflect.DeepEqual(offsets, []int{0, 0, 0, 123}) {
		t.Fatalf("counts=%v offsets=%v unique=%d next=%q", counts, offsets, len(seen), cursor)
	}
}

func base64Cursor(raw string) (string, error) {
	c, err := decodeDuckCursor(raw)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(c)
	return string(b), err
}

func TestDuckPagingCountsRejectedRecords(t *testing.T) {
	var response map[string]any
	_ = json.Unmarshal([]byte(duckBody(0, 80, -1)), &response)
	records := response["results"].([]any)
	for i := range 15 {
		records[i].(map[string]any)["thumbnail"] = "data:image/svg+xml,no"
		records[i].(map[string]any)["image"] = "https://images.example.com/bad.svg"
	}
	body, _ := json.Marshal(response)
	sources := fixtureSources(t, 0, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/" {
			return fixtureResponse(`vqd='4-123-456'`), nil
		}
		return fixtureResponse(string(body)), nil
	})
	source := sourceFor(t, sources, "DuckDuckGo images")
	first, err := source.SearchSource(context.Background(), search.SourceRequest{})
	if err != nil || len(first.Results) != 50 {
		t.Fatalf("len=%d err=%v", len(first.Results), err)
	}
	c, err := decodeDuckCursor(first.NextCursor)
	if err != nil || c.Skip != 65 || c.Offset != 0 {
		t.Fatalf("cursor=%+v err=%v", c, err)
	}
	second, err := source.SearchSource(context.Background(), search.SourceRequest{Cursor: first.NextCursor})
	if err != nil || len(second.Results) != 15 || second.NextCursor != "" {
		t.Fatalf("second=%+v err=%v", second, err)
	}
}

func TestAllLocalesAndSafeSearchParameters(t *testing.T) {
	for _, language := range []string{"en", "ru", "uk", "es", "fr", "de", "it", "pl", "cs", "tr", "sv", "be", "ca", "zh-CN", "ja", "ar", "en-GB", "fr-CA", "zh-Hans-CN", "all"} {
		for safe := range 3 {
			t.Run(fmt.Sprintf("%s/%d", language, safe), func(t *testing.T) {
				l := resolveLocale(language)
				sources := fixtureSources(t, safe, func(r *http.Request) (*http.Response, error) {
					q := r.URL.Query()
					if r.URL.Hostname() == "www.bing.com" {
						if q.Get("adlt") != []string{"off", "moderate", "strict"}[safe] || q.Get("mkt") != l.market || q.Get("setlang") != l.language || q.Get("cc") != l.country {
							t.Errorf("Bing locale/safe parameters=%v", q)
						}
						if r.URL.Path == "/videos/asyncv2" {
							return fixtureResponse(`<div vrhm='{"murl":"https://www.youtube.com/watch?v=abc","vt":"Panda","thid":"OVP.abc"}'></div>`), nil
						}
						return fixtureResponse(bingImage(0)), nil
					}
					wantSafe := []string{"-2", "-1", "1"}[safe]
					if q.Get("p") != wantSafe || q.Get("kp") != wantSafe || q.Get("l") != l.duck || !strings.Contains(r.Header.Get("Cookie"), "p="+wantSafe) {
						t.Errorf("DDG locale/safe=%v cookies=%s", q, r.Header.Get("Cookie"))
					}
					if l.language != "" && q.Get("ct") != strings.ToUpper(l.language) {
						t.Errorf("DDG language=%v", q)
					}
					if r.URL.Path == "/" {
						return fixtureResponse(`vqd='4-123-456'`), nil
					}
					return fixtureResponse(`{"results":[]}`), nil
				})
				wantCount := 4
				if safe == 0 {
					wantCount = 5
				}
				if len(sources) != wantCount {
					t.Fatalf("sources=%d", len(sources))
				}
				for _, name := range []string{"Bing images", "Bing videos", "DuckDuckGo images", "DuckDuckGo videos"} {
					if _, err := sourceFor(t, sources, name).SearchSource(context.Background(), search.SourceRequest{Language: language}); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
	if got := resolveLocale("zh-CN"); got.market != "zh-CN" || got.duck != "cn-zh" {
		t.Fatal(got)
	}
	if got := resolveLocale("fr-CA"); got.market != "fr-CA" || got.duck != "ca-fr" {
		t.Fatal(got)
	}
	if got := resolveLocale("all"); got.market != "" || got.duck != "wt-wt" {
		t.Fatal(got)
	}
}

func TestWikimediaFixtureOrderingContinuationAndErrors(t *testing.T) {
	body := `{"continue":{"gsroffset":17,"continue":"gsroffset||"},"query":{"pages":{"2":{"title":"File:Second.jpg","index":2,"imageinfo":[{"url":"https://upload.wikimedia.org/Second.jpg","thumburl":"https://upload.wikimedia.org/1280px-Second.jpg","descriptionurl":"https://commons.wikimedia.org/wiki/File:Second.jpg","mime":"image/jpeg","thumbmime":"image/jpeg","size":8000000,"width":4000,"height":3000}]},"1":{"title":"File:First.svg","index":1,"imageinfo":[{"url":"https://upload.wikimedia.org/First.svg","thumburl":"https://upload.wikimedia.org/1280px-First.svg.png","descriptionurl":"https://commons.wikimedia.org/wiki/File:First.svg","mime":"image/svg+xml","thumbmime":"image/png"}]}}}}`
	page, err := parseWikimedia([]byte(body), "Wikimedia", 0)
	if err != nil || len(page.Results) != 1 || page.NextCursor != "17" || page.Results[0].MediaURL != "https://upload.wikimedia.org/1280px-Second.jpg" || page.Results[0].Width != 0 || page.Results[0].Height != 0 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	for _, body := range []string{`{"error":{"code":"ratelimited","info":"secret-query"}}`, `<html>blocked</html>`, `{}`} {
		if _, err := parseWikimedia([]byte(body), "Wikimedia", 0); err == nil || strings.Contains(err.Error(), "secret-query") {
			t.Fatalf("unsanitized/successful failure=%v", err)
		}
	}
	if page, err := parseWikimedia([]byte(`{"batchcomplete":""}`), "Wikimedia", 0); err != nil || len(page.Results) != 0 || page.NextCursor != "" {
		t.Fatalf("empty=%+v %v", page, err)
	}
}

func TestWikimediaRejectsDocumentJPEGPreviewsAndUnrelatedTitles(t *testing.T) {
	types := []struct{ title, mime, mediaType, original string }{
		{"The lake regions of Central Africa.pdf", "application/pdf", "OFFICE", "book.pdf"},
		{"Animate creation natural history.djvu", "image/vnd.djvu", "BITMAP", "book.djvu"},
		{"Mislabelled red panda eating grapes.pdf", "image/jpeg", "BITMAP", "mislabelled.pdf"},
		{"Unrelated landscape.jpg", "image/jpeg", "BITMAP", "landscape.jpg"},
		{"Red panda eating grapes.jpg", "image/jpeg", "BITMAP", "panda.jpg"},
	}
	pages := make(map[string]any)
	for i, item := range types {
		pages[strconv.Itoa(i)] = map[string]any{
			"title": "File:" + item.title, "index": i + 1,
			"imageinfo": []map[string]any{{
				"url":            "https://upload.wikimedia.org/" + item.original,
				"thumburl":       fmt.Sprintf("https://upload.wikimedia.org/preview%d.jpg", i),
				"descriptionurl": fmt.Sprintf("https://commons.wikimedia.org/wiki/File:fixture%d", i),
				"mime":           item.mime, "thumbmime": "image/jpeg", "mediatype": item.mediaType,
				"width": 800, "height": 600, "size": 100000,
			}},
		}
	}
	body, _ := json.Marshal(map[string]any{"query": map[string]any{"pages": pages}, "continue": map[string]any{"gsroffset": 17}})
	parsed, err := parseWikimedia(body, "Wikimedia", 0)
	if err != nil || len(parsed.Results) != 2 {
		t.Fatalf("document preview retained: %+v %v", parsed, err)
	}
	sources := fixtureSources(t, 0, func(*http.Request) (*http.Response, error) { return fixtureResponse(string(body)), nil })
	page, err := sourceFor(t, sources, "Wikimedia").SearchSource(context.Background(), search.SourceRequest{Query: "red panda eating grapes"})
	if err != nil || len(page.Results) != 1 || page.Results[0].Title != "Red panda eating grapes.jpg" || page.NextCursor != "17" {
		t.Fatalf("relevance filter=%+v err=%v", page, err)
	}
}

func TestSourceFailuresAndCursorsDoNotEchoPrivateInput(t *testing.T) {
	for _, body := range []string{`<html>secret-query challenge</html>`, `{"error":"secret-query"}`, `{"results":null}`, `not-json`} {
		sources := fixtureSources(t, 0, func(r *http.Request) (*http.Response, error) {
			if r.URL.Hostname() == "duckduckgo.com" && r.URL.Path == "/" {
				return fixtureResponse(`vqd='4-123-456'`), nil
			}
			return fixtureResponse(body), nil
		})
		for _, source := range sources {
			page, err := source.SearchSource(context.Background(), search.SourceRequest{Query: "secret-query"})
			if err == nil || strings.Contains(err.Error(), "secret-query") || len(page.Results) != 0 || page.NextCursor != "" {
				t.Fatalf("source=%s page=%+v err=%v", source.Name(), page, err)
			}
		}
	}
	sources := fixtureSources(t, 0, func(*http.Request) (*http.Response, error) {
		return nil, errors.New("https://upstream/?q=secret-query")
	})
	for _, source := range sources {
		if _, err := source.SearchSource(context.Background(), search.SourceRequest{Query: "secret-query"}); err == nil || strings.Contains(err.Error(), "secret-query") {
			t.Fatalf("%s leaked error %v", source.Name(), err)
		}
		for _, cursor := range []string{"https://127.0.0.1/secret-query", "-1", "010", "10001", strings.Repeat("x", 513)} {
			if _, err := source.SearchSource(context.Background(), search.SourceRequest{Cursor: cursor}); !errors.Is(err, search.ErrInvalidCursor) {
				t.Fatalf("source=%s cursor=%q err=%v", source.Name(), cursor, err)
			}
		}
	}
}

func TestDuckRejectsUntrustedContinuationWithoutFollowingIt(t *testing.T) {
	for _, next := range []string{"https://127.0.0.1/i.js?s=100", "https://evil.example.com/i.js?s=100", "//evil.example.com/i.js?s=100", "/v.js?s=100", "/i.js?s=0", "/i.js?s=100&s=200", "/i.js?s=-1", "/i.js?s=10001"} {
		if _, err := duckNextOffset(next, "/i.js", 0); err == nil {
			t.Fatalf("accepted %q", next)
		}
	}
	if got, err := duckNextOffset("https://duckduckgo.com/i.js?q=secret-query&s=137", "/i.js", 0); err != nil || got != 137 {
		t.Fatalf("next=%d err=%v", got, err)
	}
}

func TestDuckContinuationAcceptsOnlyPinnedProviderPaths(t *testing.T) {
	for _, endpoint := range []string{"/i.js", "/v.js"} {
		t.Run(endpoint, func(t *testing.T) {
			for _, prefix := range []string{strings.TrimPrefix(endpoint, "/"), endpoint, "https://duckduckgo.com" + endpoint} {
				if next, err := duckNextOffset(prefix+"?q=secret-query&s=137", endpoint, 100); err != nil || next != 137 {
					t.Fatalf("valid continuation %q: next=%d err=%v", prefix, next, err)
				}
			}
			other := "/i.js"
			if endpoint == other {
				other = "/v.js"
			}
			for _, raw := range []string{
				strings.TrimPrefix(other, "/") + "?s=137", other + "?s=137",
				"https://evil.example.com" + endpoint + "?s=137",
				"http://duckduckgo.com" + endpoint + "?s=137",
				"//duckduckgo.com" + endpoint + "?s=137",
				"https://user@duckduckgo.com" + endpoint + "?s=137",
				"https://duckduckgo.com:443" + endpoint + "?s=137",
				"https:" + endpoint + "?s=137", "https:" + strings.TrimPrefix(endpoint, "/") + "?s=137",
				"." + endpoint + "?s=137", ".." + endpoint + "?s=137",
				endpoint + "?s=137#fragment", endpoint + "?s=137#",
				endpoint + "?s=137&s=138", endpoint + "?s=100", endpoint + "?s=10001",
			} {
				if _, err := duckNextOffset(raw, endpoint, 100); err == nil {
					t.Fatalf("accepted untrusted continuation %q", raw)
				}
			}
		})
	}
}

func TestDuckRelativeContinuationPreservesAll52Records(t *testing.T) {
	for _, category := range []search.Category{search.CatImage, search.CatVideo} {
		t.Run(string(category), func(t *testing.T) {
			endpoint := "/i.js"
			if category == search.CatVideo {
				endpoint = "/v.js"
			}
			var offsets []string
			sources := fixtureSources(t, 0, func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/" {
					return fixtureResponse(`vqd="4-123-456"`), nil
				}
				if r.URL.Scheme != "https" || r.URL.Host != "duckduckgo.com" || r.URL.Path != endpoint {
					t.Fatalf("request left pinned endpoint: %s", r.URL.Path)
				}
				offsets = append(offsets, r.URL.Query().Get("s"))
				var response map[string]any
				if r.URL.Query().Get("s") == "52" {
					_ = json.Unmarshal([]byte(duckBody(52, 1, -1)), &response)
				} else {
					_ = json.Unmarshal([]byte(duckBody(0, 52, -1)), &response)
					response["next"] = strings.TrimPrefix(endpoint, "/") + "?q=secret-query&s=52"
				}
				if category == search.CatVideo {
					for _, value := range response["results"].([]any) {
						item := value.(map[string]any)
						item["content"] = item["url"]
						item["images"] = map[string]any{"large": item["thumbnail"]}
					}
				}
				body, _ := json.Marshal(response)
				return fixtureResponse(string(body)), nil
			})
			source := sourceFor(t, sources, "DuckDuckGo "+string(category))
			cursor := ""
			seen := make(map[string]bool)
			for _, expectedCount := range []int{50, 2, 1} {
				page, err := source.SearchSource(context.Background(), search.SourceRequest{Query: "secret-query", Cursor: cursor})
				if err != nil || page.Degraded || len(page.Results) != expectedCount {
					t.Fatalf("count=%d degraded=%v err=%v", len(page.Results), page.Degraded, err)
				}
				for _, result := range page.Results {
					if seen[result.ID] {
						t.Fatal("duplicate across continuation pages")
					}
					seen[result.ID] = true
				}
				cursor = page.NextCursor
			}
			if cursor != "" || len(seen) != 53 || !reflect.DeepEqual(offsets, []string{"0", "0", "52"}) {
				t.Fatalf("cursor=%q unique=%d offsets=%v", cursor, len(seen), offsets)
			}
		})
	}
}

func TestTokenSingleflightCancellationAndFailureRetry(t *testing.T) {
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	sources := fixtureSources(t, 0, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/" {
			return fixtureResponse(`{"results":[]}`), nil
		}
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return fixtureResponse(`vqd='4-123-456'`), nil
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	})
	images := sourceFor(t, sources, "DuckDuckGo images").(*duckSource)
	videos := sourceFor(t, sources, "DuckDuckGo videos").(*duckSource)
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := images.SearchSource(ctx, search.SourceRequest{Query: "private"}); first <- err }()
	<-started
	second := make(chan error, 1)
	go func() {
		_, err := videos.SearchSource(context.Background(), search.SourceRequest{Query: "private"})
		second <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		images.ddg.mu.Lock()
		waiters := 0
		for _, e := range images.ddg.entries {
			waiters += e.waiters
		}
		images.ddg.mu.Unlock()
		if waiters == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second token waiter never attached")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("token acquisitions=%d", calls.Load())
	}
	if _, err := images.SearchSource(context.Background(), search.SourceRequest{Query: "private"}); err != nil || calls.Load() != 1 {
		t.Fatalf("cache err=%v calls=%d", err, calls.Load())
	}
	// Failed acquisition is removed immediately and a later request can recover.
	var attempts atomic.Int32
	sources = fixtureSources(t, 0, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/" {
			return fixtureResponse(`{"results":[]}`), nil
		}
		if attempts.Add(1) == 1 {
			return fixtureResponse("challenge"), nil
		}
		return fixtureResponse(`vqd='4-123-456'`), nil
	})
	images = sourceFor(t, sources, "DuckDuckGo images").(*duckSource)
	if _, err := images.SearchSource(context.Background(), search.SourceRequest{}); err == nil {
		t.Fatal("challenge became successful cache entry")
	}
	if _, err := images.SearchSource(context.Background(), search.SourceRequest{}); err != nil || attempts.Load() != 2 {
		t.Fatalf("retry err=%v attempts=%d", err, attempts.Load())
	}
}

func TestAllCancelledTokenWaitersCancelUpstream(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	sources := fixtureSources(t, 0, func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		close(stopped)
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := sourceFor(t, sources, "DuckDuckGo images").SearchSource(ctx, search.SourceRequest{})
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("orphan token acquisition")
	}
}

func TestHTTPBoundsAndCancellation(t *testing.T) {
	for _, contentLength := range []int64{-1, maxBody + 1} {
		h := newFetcher(&http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
			resp := fixtureResponse(strings.Repeat("x", maxBody+1))
			resp.ContentLength = contentLength
			return resp, nil
		})})
		if _, err := h.get(context.Background(), "https://www.bing.com/images/async?q=private", "en", ""); !errors.Is(err, errTooLarge) {
			t.Fatal(err)
		}
	}
	h := newFetcher(&http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := h.get(ctx, "https://www.bing.com/images/async?q=private", "en", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	var active, maxActive atomic.Int32
	release := make(chan struct{})
	h = newFetcher(&http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		n := active.Add(1)
		for old := maxActive.Load(); n > old && !maxActive.CompareAndSwap(old, n); old = maxActive.Load() {
		}
		defer active.Add(-1)
		select {
		case <-release:
			return fixtureResponse("ok"), nil
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	})})
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() { _, _ = h.get(context.Background(), "https://www.bing.com/images/async", "en", "") })
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if maxActive.Load() > 8 {
		t.Fatalf("concurrency=%d", maxActive.Load())
	}
}

func TestRedirectPolicyPinsOriginAndHTTPS(t *testing.T) {
	h := newFetcher(nil)
	initial, _ := http.NewRequest(http.MethodGet, "https://www.bing.com/images/async?q=private", nil)
	for _, raw := range []string{"http://www.bing.com/images/async", "https://127.0.0.1/", "https://evil.example.com/", "https://duckduckgo.com/", "https://www.bing.com:8443/"} {
		req, _ := http.NewRequest(http.MethodGet, raw, nil)
		if err := h.client.CheckRedirect(req, []*http.Request{initial}); err == nil {
			t.Fatalf("redirect allowed %q", raw)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, "https://www.bing.com/images/async?first=36", nil)
	if err := h.client.CheckRedirect(req, []*http.Request{initial}); err != nil {
		t.Fatal(err)
	}
	// Exercise net/http's redirect path, ensuring a private target never receives
	// a request even when a custom transport avoids real network access.
	var count int
	h = newFetcher(&http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		count++
		resp := fixtureResponse("")
		resp.StatusCode = 302
		resp.Header.Set("Location", "https://127.0.0.1/private-query")
		return resp, nil
	})})
	if _, err := h.get(context.Background(), "https://www.bing.com/images/async?q=private-query", "en", ""); err == nil || strings.Contains(err.Error(), "private-query") || count != 1 {
		t.Fatalf("err=%v calls=%d", err, count)
	}
}

func TestHTTPStatusIsSanitized(t *testing.T) {
	h := newFetcher(&http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		resp := fixtureResponse("secret-query")
		resp.StatusCode = 429
		return resp, nil
	})})
	if _, err := h.get(context.Background(), "https://www.bing.com/images/async?q=secret-query", "en", ""); err == nil || strings.Contains(err.Error(), "secret-query") || !strings.Contains(err.Error(), "429") {
		t.Fatal(err)
	}
}

func TestWikimediaRequestUsesExactContinuation(t *testing.T) {
	sources := fixtureSources(t, 0, func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() != "commons.wikimedia.org" || r.URL.Query().Get("gsroffset") != "137" || r.URL.Query().Get("gsrlimit") != "50" || r.URL.Query().Get("uselang") != "zh" || r.URL.Query().Get("gsrsearch") != "private filetype:bitmap" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		return fixtureResponse(`{"batchcomplete":""}`), nil
	})
	if _, err := sourceFor(t, sources, "Wikimedia").SearchSource(context.Background(), search.SourceRequest{Query: "private", Language: "zh-CN", Cursor: "137"}); err != nil {
		t.Fatal(err)
	}
}

func TestCursorNeverControlsRequestDestination(t *testing.T) {
	u, _ := url.Parse("https://duckduckgo.com/i.js?s=12")
	if n, err := duckNextOffset(u.String(), "/i.js", 0); err != nil || n != 12 {
		t.Fatal(n, err)
	}
	for _, raw := range []string{encodeDuckCursor(duckCursor{Token: "https://evil.example.com", Offset: 0}), encodeDuckCursor(duckCursor{Token: "4-123-456", Offset: -1}), encodeDuckCursor(duckCursor{Token: "4-123-456", Skip: 1001})} {
		if _, err := decodeDuckCursor(raw); !errors.Is(err, search.ErrInvalidCursor) {
			t.Fatalf("accepted cursor %q", raw)
		}
	}
}
