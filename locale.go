package main

import (
	"log"
	"strings"
)

// The menu and dialogs speak English or Simplified Chinese. They follow the
// system's first display language; "language" in state.json, set by hand to
// "en" or "zh-CN", overrides it. Logs stay in English.

// language is set once in main, before the tray and its goroutines start.
var language = "en"

// tr picks the text for the current language.
func tr(english, chinese string) string {
	if language == "zh-CN" {
		return chinese
	}
	return english
}

// languageFor maps a language tag such as "zh-Hans-CN", "zh_CN.UTF-8" or
// "en-US" to a supported language, or "" when there is none. Traditional
// Chinese (zh-TW, zh-HK, zh-Hant) gets "": its readers are not served by
// Simplified Chinese.
func languageFor(tag string) string {
	t, _, _ := strings.Cut(tag, ".")
	t = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(t), "_", "-"))
	parts := strings.Split(t, "-")
	switch {
	case parts[0] == "en":
		return "en"
	case parts[0] != "zh":
		return ""
	case len(parts) == 1:
		return "zh-CN"
	}
	switch parts[1] {
	case "hans", "cn", "sg", "my":
		return "zh-CN"
	}
	return ""
}

// chooseLanguage picks the language from the setting in state.json, else
// from the first of the system's display languages.
func chooseLanguage(setting string, system []string) string {
	if setting != "" {
		if l := languageFor(setting); l != "" {
			return l
		}
		log.Printf("language %q in state.json is not supported, use en or zh-CN", setting)
	}
	if len(system) > 0 && languageFor(system[0]) == "zh-CN" {
		return "zh-CN"
	}
	return "en"
}

// useLanguage sets the language of a dialog process to the tray's.
func useLanguage(l string) {
	if languageFor(l) == "zh-CN" {
		language = "zh-CN"
	}
}

// parseAppleLanguages reads the list "defaults read -g AppleLanguages"
// prints, such as ( "zh-Hans-CN", "en-US" ).
func parseAppleLanguages(out string) []string {
	var langs []string
	for _, line := range strings.Split(out, "\n") {
		l := strings.Trim(strings.TrimSpace(line), `",`)
		if l != "" && l != "(" && l != ")" {
			langs = append(langs, l)
		}
	}
	return langs
}
