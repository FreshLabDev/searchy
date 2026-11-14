package bot

import (
	"context"
	"slices"
	"strings"
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
		{"  \t\n I:\t cats  and  dogs \n", []search.Category{search.CatImage}, "cats  and  dogs"},
		{" I: V: \t", []search.Category{search.CatVideo}, ""},
		{"f: cats", []search.Category{search.CatImage, search.CatVideo}, "f: cats"},
		{"i:v:i:cats", []search.Category{search.CatImage}, "cats"},
		{"v:i:v:cats", []search.Category{search.CatVideo}, "cats"},
		{"ai: cats", []search.Category{search.CatImage, search.CatVideo}, "ai: cats"},
		{"i:ai: cats", []search.Category{search.CatImage}, "ai: cats"},
		{"find i: cats f: videos", []search.Category{search.CatImage, search.CatVideo}, "find i: cats f: videos"},
		{"https://example.com/f:i:v:cats", []search.Category{search.CatImage, search.CatVideo}, "https://example.com/f:i:v:cats"},
		{"i:https://example.com/v:cats", []search.Category{search.CatImage}, "https://example.com/v:cats"},
		{"i: file f:cat v:dog", []search.Category{search.CatImage}, "file f:cat v:dog"},
		{"file:cat", []search.Category{search.CatImage, search.CatVideo}, "file:cat"},
		{"i :cat", []search.Category{search.CatImage, search.CatVideo}, "i :cat"},
		{"i::cat", []search.Category{search.CatImage}, ":cat"},
		{"", []search.Category{search.CatImage, search.CatVideo}, ""},
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

func TestParseQueryPrefixOrders(t *testing.T) {
	for _, prefixes := range []string{"i", "v", "iv", "vi"} {
		for _, separator := range []string{"", " ", "\t\n", "\u2003"} {
			var b strings.Builder
			want := []search.Category{search.CatImage, search.CatVideo}
			for _, prefix := range prefixes {
				b.WriteString(strings.ToUpper(string(prefix)) + ":" + separator)
				switch prefix {
				case 'i':
					want = []search.Category{search.CatImage}
				case 'v':
					want = []search.Category{search.CatVideo}
				}
			}
			input := b.String() + "Literal  Query v:inside"
			cats, q := parseQuery(input)
			if !slices.Equal(cats, want) || q != "Literal  Query v:inside" {
				t.Errorf("parseQuery(%q) = %v, %q; want %v with literal query", input, cats, q, want)
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
