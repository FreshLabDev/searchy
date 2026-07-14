package bot

import (
	"strings"
	"testing"

	"searchy/internal/buildinfo"
	"searchy/internal/i18n"
)

func TestAboutVersionHasNoTagPrefix(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "v0.1.0-beta.3", want: "0.1.0-beta.3"},
		{input: "0.1.0-beta.3", want: "0.1.0-beta.3"},
		{input: "  v0.1.0-beta.3  ", want: "0.1.0-beta.3"},
	} {
		if got := aboutVersion(test.input); got != test.want {
			t.Fatalf("aboutVersion(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestAboutPanelShowsOneVersionPrefixInEveryLocale(t *testing.T) {
	previousVersion := buildinfo.Version
	buildinfo.Version = "v0.1.0-beta.3"
	t.Cleanup(func() { buildinfo.Version = previousVersion })

	for _, language := range i18n.LANGUAGE_OPTIONS {
		text, _ := aboutBody(language.Code, 1)
		if strings.Contains(text, "vv0.1.0-beta.3") {
			t.Fatalf("locale %s rendered a duplicated version prefix: %q", language.Code, text)
		}
		if !strings.Contains(text, "v0.1.0-beta.3") {
			t.Fatalf("locale %s omitted the version prefix: %q", language.Code, text)
		}
	}
}
