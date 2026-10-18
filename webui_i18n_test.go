package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

func TestWebUIInitialLanguage(t *testing.T) {
	for _, language := range []string{"en", "ru"} {
		t.Run(language, func(t *testing.T) {
			html := renderWebUI(language)
			want := []string{`<html lang="ru">`, "warpd — управление прокси", "Подключение…", "Ожидание статуса", `const uiLanguage = 'ru';`}
			if language == "en" {
				want = []string{`<html lang="en">`, "warpd — proxy management", "Connecting…", "Waiting for status", `const uiLanguage = 'en';`}
			}
			for _, text := range want {
				if !strings.Contains(html, text) {
					t.Errorf("initial %s page missing %q", language, text)
				}
			}
		})
	}
}

func TestWebUIRejectsInjectedLocale(t *testing.T) {
	for _, language := range []string{"", "EN", "fr", "en-US", `en\"><script>alert(1)</script>`, "';alert(1);//", "ru\n"} {
		if got := renderWebUI(language); got != indexHTML {
			t.Errorf("untrusted locale %q must return exactly the Russian page", language)
		}
	}
}

// Catch untranslated UI-owned strings even on branches not currently exercised
// by DOM interactions. Backend logs/errors are not part of the page template.
func TestWebUIEnglishHasNoUntranslatedTemplateText(t *testing.T) {
	for _, line := range strings.Split(renderWebUI("en"), "\n") {
		if strings.ContainsFunc(line, func(r rune) bool { return unicode.Is(unicode.Cyrillic, r) }) {
			t.Errorf("untranslated English template line: %s", line)
		}
	}
}

// The Node integration harness consumes these real production-helper results,
// never a test-side translation or substituted English fixture.
func TestWebUILocalizationFixtures(t *testing.T) {
	dir := os.Getenv("WARPD_UI_FIXTURES")
	if dir == "" {
		return
	}
	for _, language := range []string{"en", "ru"} {
		if err := os.WriteFile(filepath.Join(dir, language+".html"), []byte(renderWebUI(language)), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
