// Package i18n provides translations for the bot's user-facing text. Strings live
// in translations.json (key -> {lang: text}) embedded at build time. The voice
// and language set mirror the sibling "vido" bot. T(lang, key, pairs...) returns
// the localized string with {placeholder} interpolation, falling back to English.
package i18n

import (
	_ "embed"
	"encoding/json"
	"strings"
)

const DefaultLang = "en"

//go:embed translations.json
var translationsJSON []byte

// translations[key][lang] = text
var translations map[string]map[string]string

func init() {
	if err := json.Unmarshal(translationsJSON, &translations); err != nil {
		panic("i18n: invalid translations.json: " + err.Error())
	}
}

// LangOption is a selectable language for the settings keyboard.
type LangOption struct {
	Code   string
	Label  string // flag + native name, e.g. "🇬🇧 English"
	Native string
}

// LANGUAGE_OPTIONS — order shown in the language picker. Mirrors vido's set.
var LANGUAGE_OPTIONS = []LangOption{
	{"en", "🇬🇧 English", "English"},
	{"ru", "🇷🇺 Русский", "Русский"},
	{"uk", "🇺🇦 Українська", "Українська"},
	{"es", "🇪🇸 Español", "Español"},
	{"fr", "🇫🇷 Français", "Français"},
	{"de", "🇩🇪 Deutsch", "Deutsch"},
	{"it", "🇮🇹 Italiano", "Italiano"},
	{"pl", "🇵🇱 Polski", "Polski"},
	{"cs", "🇨🇿 Čeština", "Čeština"},
	{"tr", "🇹🇷 Türkçe", "Türkçe"},
	{"sv", "🇸🇪 Svenska", "Svenska"},
	{"be", "🇧🇾 Беларуская", "Беларуская"},
	{"ca", "🇦🇩 Català", "Català"},
	{"zh", "🇨🇳 中文", "中文"},
	{"ja", "🇯🇵 日本語", "日本語"},
	{"ar", "🇦🇪 العربية", "العربية"},
}

var supported = func() map[string]bool {
	m := make(map[string]bool, len(LANGUAGE_OPTIONS))
	for _, o := range LANGUAGE_OPTIONS {
		m[o.Code] = true
	}
	return m
}()

// IsSupported reports whether code is one of our languages.
func IsSupported(code string) bool { return supported[code] }

// Resolve maps a Telegram language_code (e.g. "en-US", "uk") to a supported
// language, falling back to English.
func Resolve(languageCode string) string {
	c := normalize(languageCode)
	if supported[c] {
		return c
	}
	return DefaultLang
}

// normalize lowercases and strips region: "en-US" / "zh_CN" -> "en" / "zh".
func normalize(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if i := strings.IndexAny(code, "-_"); i > 0 {
		code = code[:i]
	}
	return code
}

// LabelOf returns the flag+native label for a language code.
func LabelOf(code string) string {
	for _, o := range LANGUAGE_OPTIONS {
		if o.Code == code {
			return o.Label
		}
	}
	return code
}

// T returns the localized string for key in lang, interpolating {name}
// placeholders from pairs (name, value, name, value, …). Falls back to English,
// then to "[key]" if the key is unknown.
func T(lang, key string, pairs ...string) string {
	byLang, ok := translations[key]
	if !ok {
		return "[" + key + "]"
	}
	text, ok := byLang[lang]
	if !ok || text == "" {
		if text, ok = byLang[DefaultLang]; !ok {
			return "[" + key + "]"
		}
	}
	if len(pairs) >= 2 {
		repl := make([]string, 0, len(pairs))
		for i := 0; i+1 < len(pairs); i += 2 {
			repl = append(repl, "{"+pairs[i]+"}", pairs[i+1])
		}
		text = strings.NewReplacer(repl...).Replace(text)
	}
	return text
}
