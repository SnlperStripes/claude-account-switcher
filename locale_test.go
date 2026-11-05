package main

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func inChinese(t *testing.T) {
	t.Helper()
	old := language
	language = "zh-CN"
	t.Cleanup(func() { language = old })
}

func TestLanguageFor(t *testing.T) {
	for tag, want := range map[string]string{
		"zh-CN": "zh-CN", "zh-Hans-CN": "zh-CN", "zh-Hans": "zh-CN", "zh_CN.UTF-8": "zh-CN",
		"zh": "zh-CN", "zh-SG": "zh-CN",
		"zh-TW": "", "zh-HK": "", "zh-Hant-TW": "", "de-DE": "", "": "",
		"en-US": "en", "en": "en", "EN_gb": "en",
	} {
		if got := languageFor(tag); got != want {
			t.Errorf("languageFor(%q) = %q, want %q", tag, got, want)
		}
	}
}

func TestChooseLanguage(t *testing.T) {
	for _, tc := range []struct {
		setting string
		system  []string
		want    string
	}{
		{"", []string{"zh-CN", "en-US"}, "zh-CN"},
		{"", []string{"en-US", "zh-CN"}, "en"},
		{"", []string{"zh-TW"}, "en"},
		{"", nil, "en"},
		{"en", []string{"zh-CN"}, "en"},
		{"zh-CN", []string{"de-DE"}, "zh-CN"},
		{"klingon", []string{"zh-Hans-CN"}, "zh-CN"},
	} {
		if got := chooseLanguage(tc.setting, tc.system); got != tc.want {
			t.Errorf("chooseLanguage(%q, %q) = %q, want %q", tc.setting, tc.system, got, tc.want)
		}
	}
}

func TestParseAppleLanguages(t *testing.T) {
	out := "(\n    \"zh-Hans-CN\",\n    \"en-US\"\n)\n"
	if got := parseAppleLanguages(out); !slices.Equal(got, []string{"zh-Hans-CN", "en-US"}) {
		t.Fatalf("got %q", got)
	}
}

func TestUsageTextChinese(t *testing.T) {
	inChinese(t)
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.Local)
	a := Account{Usage: &Usage{Session: 97, SessionReset: now.Add(time.Hour), Weekly: 40, WeeklyReset: now.Add(48 * time.Hour)}}
	if got, want := usageText(a, 96, now), "5小时 97%，13:00 才重置 · 本周 40%，周四 12:00 重置"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	a.Usage.Session, a.Usage.WeeklyReset = 20, now.Add(6*24*time.Hour+time.Hour)
	if got, want := usageText(a, 96, now), "5小时 20%，13:00 重置 · 本周 40%，10月12日 周一 13:00 重置"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got, want := usageText(Account{Problem: signedOut}, 96, now), "登录已失效，请重新添加"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got, want := usageText(Account{}, 96, now), "用量未知"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestAutoNoteChinese(t *testing.T) {
	inChinese(t)
	now := time.Date(2026, 10, 7, 9, 0, 0, 0, time.Local)
	s := &switcher{}
	s.st.Accounts = []*Account{{UUID: "a", Name: "Pepe", Plan: "Pro"}}
	s.st.LastSwitch = now.Add(-6 * time.Hour)
	s.st.LastCause, s.st.LastFrom = nearlyFull, "a"
	if got, want := s.autoNote(now), "已于 03:00 自动切换，Pepe (Pro) 的额度快用完了"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCustomLabelChinese(t *testing.T) {
	inChinese(t)
	if got := customLabel(96); got != "自定义…" {
		t.Fatalf("got %q", got)
	}
	if got := customLabel(85); got != "自定义（85%）…" {
		t.Fatalf("got %q", got)
	}
}

// The dialog pages get the language with their data.
func TestDialogRequestsCarryLanguage(t *testing.T) {
	inChinese(t)
	page, err := fillPage(thresholdPage, thresholdRequest{Threshold: 85, Presets: thresholds, Lang: language})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(page, `"lang":"zh-CN"`) {
		t.Fatal("threshold page lacks the language")
	}
}
