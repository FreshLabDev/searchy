package bot

import (
	"context"
	"testing"

	"github.com/FreshLabDev/tg"

	"searchy/internal/core"
	"searchy/internal/i18n"
	"searchy/internal/search"
)

func TestParseQuery(t *testing.T) {
	cases := []struct {
		in       string
		wantCats []search.Category
		wantQ    string
	}{
		{"cats", []search.Category{search.CatImage, search.CatVideo}, "cats"},
		{"i: cats", []search.Category{search.CatImage}, "cats"},
		{"v:funny clip", []search.Category{search.CatVideo}, "funny clip"},
		{"I: Dogs", []search.Category{search.CatImage}, "Dogs"},
	}
	for _, c := range cases {
		cats, q := parseQuery(c.in)
		if q != c.wantQ {
			t.Errorf("parseQuery(%q) query = %q, want %q", c.in, q, c.wantQ)
		}
		if len(cats) != len(c.wantCats) {
			t.Errorf("parseQuery(%q) cats = %v, want %v", c.in, cats, c.wantCats)
			continue
		}
		for i := range cats {
			if cats[i] != c.wantCats[i] {
				t.Errorf("parseQuery(%q) cats = %v, want %v", c.in, cats, c.wantCats)
				break
			}
		}
	}
}

func TestOffsetRoundTrip(t *testing.T) {
	for _, p := range []int{0, 1, 9, 42} {
		if got := decodeOffset(encodeOffset(p)); got != p {
			t.Errorf("offset round-trip %d -> %q -> %d", p, encodeOffset(p), got)
		}
	}
	if decodeOffset("") != 0 || decodeOffset("bad") != 0 || decodeOffset("-3") != 0 {
		t.Error("decodeOffset should fall back to 0 on empty/invalid")
	}
}

func TestFmtDuration(t *testing.T) {
	cases := map[int]string{0: "", 5: "0:05", 201: "3:21", 3753: "1:02:33"}
	for in, want := range cases {
		if got := fmtDuration(in); got != want {
			t.Errorf("fmtDuration(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestLangResolveCachesTelegramFallback(t *testing.T) {
	h := &Handlers{}
	u := &tg.User{ID: 42, LanguageCode: "ru-RU"}

	if got := h.langResolve(context.Background(), u); got != "ru" {
		t.Fatalf("langResolve() = %q, want ru", got)
	}
	u.LanguageCode = "en"
	if got := h.langResolve(context.Background(), u); got != "ru" {
		t.Fatalf("cached langResolve() = %q, want ru", got)
	}
	if _, ok := h.langCache.Load(u.ID); !ok {
		t.Fatal("Telegram fallback language was not cached")
	}
	if got := h.langResolve(context.Background(), nil); got != i18n.DefaultLang {
		t.Fatalf("nil user langResolve() = %q, want %q", got, i18n.DefaultLang)
	}
}

// "Follow Telegram" has to undo a manual pick, not just re-render the panel:
// leaving the choice in the language cache would keep answering in it until the
// process restarted, whatever core says.
func TestClearLanguageRestoresTheTelegramHint(t *testing.T) {
	h := &Handlers{}
	u := &tg.User{ID: 42, LanguageCode: "ru-RU"}

	h.setLanguage(u.ID, "ja", core.SourceManual)
	if got := h.langResolve(context.Background(), u); got != "ja" {
		t.Fatalf("manual pick did not take: langResolve() = %q, want ja", got)
	}

	h.clearLanguage(context.Background(), u.ID)
	if _, ok := h.langCache.Load(u.ID); ok {
		t.Fatal("the manual pick is still cached after Follow Telegram")
	}
	if got := h.langResolve(context.Background(), u); got != "ru" {
		t.Fatalf("langResolve() = %q after Follow Telegram, want the Telegram hint ru", got)
	}
}
