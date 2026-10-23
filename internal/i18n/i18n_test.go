package i18n

import (
	"strings"
	"testing"
)

func TestSearchModesAndErrorsHaveEveryLocale(t *testing.T) {
	for _, key := range []string{"help.body", "help.hint", "inline.prompt.desc", "search.error_title", "search.expired", "search.invalid", "search.busy", "about.value.search"} {
		for _, lang := range LANGUAGE_OPTIONS {
			text := translations[key][lang.Code]
			if strings.TrimSpace(text) == "" {
				t.Errorf("%s has no %s translation", key, lang.Code)
			}
			if strings.HasPrefix(key, "help.") || key == "inline.prompt.desc" {
				for _, prefix := range []string{"i:", "v:"} {
					if !strings.Contains(text, prefix) {
						t.Errorf("%s/%s does not explain %s", key, lang.Code, prefix)
					}
				}
				if strings.Contains(text, "f:") {
					t.Errorf("%s/%s still mentions the removed f: prefix", key, lang.Code)
				}
			}
			if lang.Code != DefaultLang && text == translations[key][DefaultLang] {
				t.Errorf("%s/%s duplicates the English text", key, lang.Code)
			}
		}
	}
}
