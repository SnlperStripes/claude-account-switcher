package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestUsageReadsModelLimit(t *testing.T) {
	const body = `{"five_hour":{"utilization":12.0,"resets_at":"2026-11-14T20:00:00+00:00"},
		"seven_day":{"utilization":50.0,"resets_at":"2026-11-16T22:59:59.580458+00:00"},
		"limits":[{"kind":"session","percent":12,"resets_at":null,"scope":null},
			{"kind":"weekly_all","percent":50,"resets_at":"2026-11-16T22:59:59.580458+00:00","scope":null},
			{"kind":"weekly_scoped","percent":40,"resets_at":"2026-11-16T22:59:59.580601+00:00","scope":{"model":{"id":null,"display_name":"Fable"},"surface":null}},
			{"kind":"weekly_scoped","percent":15,"resets_at":"2026-11-17T10:00:00+00:00","scope":{"model":{"id":null,"display_name":"Opus"},"surface":null}}]}`
	var b usageBody
	if err := json.Unmarshal([]byte(body), &b); err != nil {
		t.Fatal(err)
	}
	u := b.usage(time.Now())
	if u.Session != 12 || u.Weekly != 50 {
		t.Fatalf("session %v, weekly %v, want 12 and 50", u.Session, u.Weekly)
	}
	if u.Model != 40 || u.ModelName != "Fable" || u.ModelReset.UTC().Day() != 16 {
		t.Fatalf("model limit = %v %q %v, want the fuller one, Fable at 40", u.Model, u.ModelName, u.ModelReset)
	}

	b = usageBody{}
	if err := json.Unmarshal([]byte(`{"five_hour":{"utilization":3},"limits":[{"kind":"weekly_all","percent":50}]}`), &b); err != nil {
		t.Fatal(err)
	}
	if u := b.usage(time.Now()); u.Model != 0 || u.ModelName != "" {
		t.Fatalf("model limit = %v %q on a plan without one", u.Model, u.ModelName)
	}
}

func TestCurrentResetsModelLimit(t *testing.T) {
	now := time.Now()
	u := Usage{Model: 100, ModelName: "Fable", ModelReset: now.Add(-time.Minute)}
	if got := u.current(now).Model; got != 0 {
		t.Fatalf("model = %v after its reset, want 0", got)
	}
}

func TestAutoTargetModelLimit(t *testing.T) {
	now := time.Now()
	idle := now.Add(-time.Hour)
	later := now.Add(48 * time.Hour)
	s := &switcher{st: State{AutoSwitch: true, Threshold: 96}, active: "a"}
	used := &Account{UUID: "a", Usage: &Usage{Session: 10, Weekly: 60, Model: 100, ModelName: "Fable", ModelReset: later, CheckedAt: now}}
	usedToo := &Account{UUID: "b", Usage: &Usage{Session: 0, Weekly: 50, Model: 95, ModelName: "Fable", ModelReset: later, CheckedAt: now}}
	roomy := &Account{UUID: "c", Usage: &Usage{Session: 30, Weekly: 70, Model: 40, ModelName: "Fable", ModelReset: later, CheckedAt: now}}
	s.st.Accounts = []*Account{used, usedToo, roomy}

	if got, _ := s.autoTarget(true, idle, now); got != nil {
		t.Fatalf("switched to %s on the model limit with the toggle off", got.UUID)
	}
	s.st.ModelLimit = true
	if got, cause := s.autoTarget(true, idle, now); got != roomy || cause != nearlyFull {
		t.Fatalf("picked %v, want c: b has no room left in the model limit", got)
	}
	used.Usage.Model = 50
	if got, _ := s.autoTarget(true, idle, now); got != nil {
		t.Fatalf("switched to %s below the threshold", got.UUID)
	}
}

func TestUsageTextShowsModelLimit(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.Local)
	reset := now.Add(2 * 24 * time.Hour)
	a := Account{Usage: &Usage{Session: 20, Weekly: 40, WeeklyReset: reset, Model: 35, ModelName: "Fable", ModelReset: reset}}
	if got, want := usageText(a, 96, now), "5h 20% · week 40%, resets Thu 12:00 · Fable 35%"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	a.Usage.Model = 100
	if got, want := usageText(a, 96, now), "5h 20% · week 40%, resets Thu 12:00 · Fable 100% until Thu 12:00"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
