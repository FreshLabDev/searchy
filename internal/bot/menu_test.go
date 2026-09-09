package bot

import (
	"strings"
	"testing"

	"github.com/FreshLabDev/tg"

	"searchy/internal/buildinfo"
	"searchy/internal/db"
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
		text, _ := aboutPanel(language.Code, 1, false)
		if strings.Contains(text, "vv0.1.0-beta.3") {
			t.Fatalf("locale %s rendered a duplicated version prefix: %q", language.Code, text)
		}
		if !strings.Contains(text, "v0.1.0-beta.3") {
			t.Fatalf("locale %s omitted the version prefix: %q", language.Code, text)
		}
	}
}

// The About card states the version and nothing else about the build. A build
// stamp here made this card read differently from every other bot's.
func TestAboutPanelStatesTheVersionAndNoBuildStamp(t *testing.T) {
	previousDate := buildinfo.Date
	buildinfo.Date = "2026-09-09T12:00:00Z"
	t.Cleanup(func() { buildinfo.Date = previousDate })

	for _, language := range i18n.LANGUAGE_OPTIONS {
		text, _ := aboutPanel(language.Code, 1, false)
		if strings.Contains(text, "2026-09-09T12:00:00Z") {
			t.Fatalf("locale %s still shows the build stamp: %q", language.Code, text)
		}
	}
}

// The repository is a link inside the text. A button for it as well would be
// two controls for one action.
func TestAboutPanelLinksTheRepositoryInTextAndNotAsAButton(t *testing.T) {
	text, kb := aboutPanel("ru", 1, false)
	if !strings.Contains(text, `<a href="https://github.com/FreshLabDev/searchy">`) {
		t.Fatalf("about text has no repository link: %q", text)
	}
	if !strings.Contains(text, "Apache-2.0") {
		t.Fatalf("about text does not name the license: %q", text)
	}
	for _, row := range kb.InlineKeyboard {
		for _, button := range row {
			if strings.Contains(button.URL, "github.com") {
				t.Fatalf("a source button duplicates the link already in the text: %q", button.Text)
			}
		}
	}
}

// Every value on an About line has to be translated, or a Russian panel reads
// half in English.
func TestAboutPanelIsFullyLocalized(t *testing.T) {
	english, _ := aboutPanel("en", 1, false)
	for _, language := range i18n.LANGUAGE_OPTIONS {
		if language.Code == i18n.DefaultLang {
			continue
		}
		text, _ := aboutPanel(language.Code, 1, false)
		for _, key := range []string{"about.tagline", "about.field.search", "about.value.search",
			"about.field.source", "about.field.admin"} {
			localized := i18n.T(language.Code, key)
			if localized == "" {
				t.Fatalf("%s has no %s", language.Code, key)
			}
			if !strings.Contains(text, localized) {
				t.Fatalf("%s panel is missing %s (%q)", language.Code, key, localized)
			}
			if localized == i18n.T(i18n.DefaultLang, key) && language.Code != "en" {
				// Shared proper nouns are fine; a whole English sentence is not.
				if strings.Count(localized, " ") > 1 {
					t.Fatalf("%s falls back to the English %s: %q", language.Code, key, localized)
				}
			}
		}
		if text == english {
			t.Fatalf("%s panel is identical to the English one", language.Code)
		}
	}
}

// A DM is the panel: there is nothing else in the chat, so Close offers to
// delete the only thing on screen. A group panel is one person's menu in
// everyone's feed, and there it is not optional.
func TestCloseExistsOnlyInGroups(t *testing.T) {
	panels := map[string]func(bool) *tg.InlineKeyboardMarkup{
		"home": func(inGroup bool) *tg.InlineKeyboardMarkup {
			_, kb := homePanel("en", "searchybot", "vidobot", 1, inGroup)
			return kb
		},
		"help": func(inGroup bool) *tg.InlineKeyboardMarkup {
			_, kb := infoPanel("en", 1, inGroup, "help.title", "help.body", "bot", "searchybot")
			return kb
		},
		"about": func(inGroup bool) *tg.InlineKeyboardMarkup {
			_, kb := aboutPanel("en", 1, inGroup)
			return kb
		},
		"language": func(inGroup bool) *tg.InlineKeyboardMarkup {
			_, kb := languagePanel("en", 1, inGroup)
			return kb
		},
	}
	for name, build := range panels {
		if hasCallback(build(false), "m:1:close") {
			t.Errorf("%s panel offers Close in a DM, where there is nothing to close", name)
		}
		if !hasCallback(build(true), "m:1:close") {
			t.Errorf("%s panel has no Close in a group, where the panel is everyone's clutter", name)
		}
	}
}

// A group /start is a different screen, not the personal one with a Close
// bolted on: language and personal statistics belong in a DM.
func TestGroupHomeOffersSearchAndADMInsteadOfPersonalSettings(t *testing.T) {
	_, group := homePanel("en", "searchybot", "vidobot", 1, true)
	if hasCallback(group, "m:1:language") || hasCallback(group, "m:1:statsp") {
		t.Fatal("the group panel puts one member's personal settings in everyone's feed")
	}
	var hasInlineSearch, hasDM bool
	for _, row := range group.InlineKeyboard {
		for _, button := range row {
			if button.SwitchInlineQueryCurrentChat != nil {
				hasInlineSearch = true
			}
			if button.URL == "https://t.me/searchybot" {
				hasDM = true
			}
		}
	}
	if !hasInlineSearch {
		t.Error("the group panel has no way to search")
	}
	if !hasDM {
		t.Error("the group panel does not point at the DM for personal settings")
	}

	_, personal := homePanel("en", "searchybot", "vidobot", 1, false)
	if !hasCallback(personal, "m:1:language") || !hasCallback(personal, "m:1:statsp") {
		t.Fatal("the DM panel lost language or statistics")
	}
}

// Emoji mark state — the selected language, the active tab — and nothing else.
// A picture on every button is decoration that stops meaning anything.
func TestButtonsAndHeadingsCarryNoDecorativeEmoji(t *testing.T) {
	keys := []string{
		"home.title", "btn.language", "btn.stats", "btn.help", "btn.about",
		"btn.search", "btn.download", "btn.video_settings", "btn.open_dm",
		"btn.open_platform", "btn.open_original", "action.back", "action.close",
		"language.title", "help.title", "stats.title.personal", "stats.title.global",
		"download.retry_button",
	}
	for _, language := range i18n.LANGUAGE_OPTIONS {
		for _, key := range keys {
			value := i18n.T(language.Code, key)
			for _, r := range value {
				if isDecorativeSymbol(r) {
					t.Errorf("%s/%s carries %q: %q", language.Code, key, string(r), value)
					break
				}
			}
		}
	}
}

// The selection marks are the deliberate exception, and they must survive the
// sweep. Both states are drawn: a set where only the chosen option is marked
// leaves one row indented two characters past all the others.
func TestStateMarksSurvive(t *testing.T) {
	if stateMark(true) != "◉ " || stateMark(false) != "◎ " {
		t.Fatal("the selection marks lost a state")
	}
}

// Every option of a set is marked, so the column has one left edge. This was
// wrong for a year: fifteen language buttons started at the edge and one did not.
func TestEveryOptionOfASetIsMarked(t *testing.T) {
	_, language := languagePanel("ru", 1, false)
	marked := 0
	for _, option := range i18n.LANGUAGE_OPTIONS {
		button, ok := findButton(language, "m:1:l|"+option.Code)
		if !ok {
			t.Fatalf("the picker lost %s", option.Code)
		}
		if !strings.HasPrefix(button.Text, "◉ ") && !strings.HasPrefix(button.Text, "◎ ") {
			t.Fatalf("%s starts at a different left edge: %q", option.Code, button.Text)
		}
		if strings.HasPrefix(button.Text, "◉ ") {
			marked++
		}
	}
	if marked != 1 {
		t.Fatalf("%d languages claim to be the current one, want exactly 1", marked)
	}

	for _, global := range []bool{false, true} {
		_, stats := statsPanel("en", 1, db.Stats{}, global, "", false)
		for _, data := range []string{"m:1:statsp", "m:1:statsg"} {
			button, ok := findButton(stats, data)
			if !ok {
				t.Fatalf("the stats panel lost %s", data)
			}
			if !strings.HasPrefix(button.Text, "◉ ") && !strings.HasPrefix(button.Text, "◎ ") {
				t.Fatalf("tab %s is unmarked: %q", data, button.Text)
			}
		}
	}
}

// Success says "this is the state you are in": the current language and the open
// statistics tab report state, they do not perform an action.
func TestStateCarriesSuccessAndNothingElseIsColoured(t *testing.T) {
	_, language := languagePanel("ru", 1, false)
	for _, option := range i18n.LANGUAGE_OPTIONS {
		button, _ := findButton(language, "m:1:l|"+option.Code)
		want := ""
		if option.Code == "ru" {
			want = tg.StyleSuccess
		}
		if button.Style != want {
			t.Errorf("%s has style %q, want %q", option.Code, button.Style, want)
		}
	}
	for _, global := range []bool{false, true} {
		open, shut := "m:1:statsp", "m:1:statsg"
		if global {
			open, shut = shut, open
		}
		_, stats := statsPanel("en", 1, db.Stats{}, global, "", false)
		if button, _ := findButton(stats, open); button.Style != tg.StyleSuccess {
			t.Errorf("the open tab has style %q, want Success — it reports state, not an action", button.Style)
		}
		if button, _ := findButton(stats, shut); button.Style != "" {
			t.Errorf("the closed tab is coloured %q", button.Style)
		}
	}
}

// At most one Primary per screen, and on the DM home it is Language: nothing
// else on that screen is readable until the language is right.
func TestAtMostOnePrimaryPerScreen(t *testing.T) {
	screens := map[string]*tg.InlineKeyboardMarkup{}
	_, screens["home.dm"] = homePanel("en", "searchybot", "vidobot", 1, false)
	_, screens["home.group"] = homePanel("en", "searchybot", "vidobot", 1, true)
	_, screens["language"] = languagePanel("en", 1, true)
	_, screens["stats"] = statsPanel("en", 1, db.Stats{}, false, "", true)
	_, screens["help"] = infoPanel("en", 1, true, "help.title", "help.body", "bot", "searchybot")
	_, screens["about"] = aboutPanel("en", 1, true)

	for name, kb := range screens {
		var primaries []string
		for _, row := range kb.InlineKeyboard {
			for _, button := range row {
				if button.Style == tg.StylePrimary {
					primaries = append(primaries, button.Text)
				}
			}
		}
		if len(primaries) > 1 {
			t.Errorf("%s singles out nothing: %d primaries %v", name, len(primaries), primaries)
		}
	}
	if button, ok := findButton(screens["home.dm"], "m:1:language"); !ok || button.Style != tg.StylePrimary {
		t.Errorf("the DM home does not lead with Language: %q", button.Style)
	}
	// The group home leads by position with a SwitchInlineQuery button, which
	// is the one thing everyone in the room can act on.
	for _, row := range screens["home.group"].InlineKeyboard {
		for _, button := range row {
			if button.Style == tg.StylePrimary {
				t.Errorf("the group home colours %q; the inline-search button already leads by position", button.Text)
			}
		}
	}
}

// Close is written in exactly one place, so the word and the destructive colour
// cannot drift apart. Every Close in the bot comes from closeButton.
func TestEveryCloseIsDanger(t *testing.T) {
	screens := map[string]*tg.InlineKeyboardMarkup{}
	_, screens["home.group"] = homePanel("en", "searchybot", "vidobot", 1, true)
	_, screens["language"] = languagePanel("en", 1, true)
	_, screens["stats"] = statsPanel("en", 1, db.Stats{}, false, "", true)
	_, screens["help"] = infoPanel("en", 1, true, "help.title", "help.body", "bot", "searchybot")
	_, screens["about"] = aboutPanel("en", 1, true)
	screens["grid"] = gridKeyboard("en", "tok", 0, 3)

	closeLabel := i18n.T("en", "action.close")
	for name, kb := range screens {
		found := false
		for _, row := range kb.InlineKeyboard {
			for _, button := range row {
				if button.Text != closeLabel {
					continue
				}
				found = true
				if button.Style != tg.StyleDanger {
					t.Errorf("%s: Close has style %q, want Danger — it dismisses the panel", name, button.Style)
				}
			}
		}
		if !found {
			t.Errorf("%s has no Close", name)
		}
	}
}

func findButton(kb *tg.InlineKeyboardMarkup, data string) (tg.InlineKeyboardButton, bool) {
	for _, row := range kb.InlineKeyboard {
		for _, button := range row {
			if button.CallbackData == data {
				return button, true
			}
		}
	}
	return tg.InlineKeyboardButton{}, false
}

func hasCallback(kb *tg.InlineKeyboardMarkup, data string) bool {
	for _, row := range kb.InlineKeyboard {
		for _, button := range row {
			if button.CallbackData == data {
				return true
			}
		}
	}
	return false
}

// isDecorativeSymbol covers the pictograph, dingbat and arrow blocks a label
// would use as an icon. It deliberately excludes the geometric shapes ◉ and ◎,
// which are the state marks.
func isDecorativeSymbol(r rune) bool {
	switch {
	case r >= 0x1F300 && r <= 0x1FAFF: // emoji and pictographs
		return true
	case r >= 0x2190 && r <= 0x21FF: // arrows
		return true
	case r >= 0x2600 && r <= 0x27BF: // dingbats, misc symbols
		return true
	case r == 0x2139 || r == 0xFE0F: // ℹ and the emoji variation selector
		return true
	}
	return false
}
