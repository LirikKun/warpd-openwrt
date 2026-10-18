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
			want := []string{`<html lang="ru">`, "warpd — управление прокси", "Подключение…", "Ожидание статуса", `const uiLanguage = 'ru';`, "Добавить прокси", "Секретная ссылка активации", "Эффективные запреты стран colo", "транспорт только для чтения"}
			if language == "en" {
				want = []string{`<html lang="en">`, "warpd — proxy management", "Connecting…", "Waiting for status", `const uiLanguage = 'en';`, "Add proxy", "Secret activation URL", "Effective colo country exclusions", "transport is read-only"}
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

func TestWebUIRelayCredentialLocalizationAndBoundaries(t *testing.T) {
	for _, language := range []string{"ru", "en"} {
		html := renderWebUI(language)
		want := []string{"Аккаунт туннеля · только этот профиль", "Публичный ключ сервера из [Peer] · не производный ключ клиента", "Регистрация уже выполняется; новый запрос не добавлен.", "Готовность туннеля и выхода — в статусах сети и политики.", "Это не активация Relay", "Общий аккаунт не изменится"}
		if language == "en" {
			want = []string{"Tunnel account · this profile only", "Server public key from [Peer] · not the derived client key", "Registration is already pending; a new request was not queued.", "Tunnel and exit readiness: see network and policy status.", "This is not Relay activation", "The shared account will not change"}
		}
		for _, text := range want {
			if !strings.Contains(html, text) {
				t.Errorf("%s account UI missing %q", language, text)
			}
		}
		for _, boundary := range []string{"input.autocomplete = 'new-password'", "confirmed: true, revision: listing.revision", "const versions = Object.fromEntries", "input.dataset.draftVersion === versions[key]"} {
			if !strings.Contains(html, boundary) {
				t.Errorf("%s missing credential boundary %q", language, boundary)
			}
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
