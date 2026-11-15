package main

import (
	"reflect"
	"testing"
	"time"
)

func TestEscapeMarkup(t *testing.T) {
	if got, want := escapeMarkup(`A & B <b>x</b> > "q"`), `A &amp; B &lt;b&gt;x&lt;/b&gt; &gt; "q"`; got != want {
		t.Fatalf("escapeMarkup = %q, want %q", got, want)
	}
}

func TestZenityEntryArgs(t *testing.T) {
	defer func(l string) { language = l }(language)
	language = "en"
	got := zenityEntryArgs("Auto-switch at", "Use 1 & <2>", "85")
	want := []string{
		"--entry",
		"--title=Auto-switch at",
		"--text=Use 1 &amp; &lt;2&gt;",
		"--entry-text=85",
		"--ok-label=Set",
		"--cancel-label=Cancel",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("zenityEntryArgs:\n got %q\nwant %q", got, want)
	}
}

func TestKdialogInputArgs(t *testing.T) {
	got := kdialogInputArgs("Auto-switch at", "Use 1 & <2>", "85")
	want := []string{"--title", "Auto-switch at", "--inputbox", "Use 1 & <2>", "85"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("kdialogInputArgs:\n got %q\nwant %q", got, want)
	}
}

func TestZenityChecklistArgs(t *testing.T) {
	defer func(l string) { language = l }(language)
	language = "en"
	got := zenityChecklistArgs("Continue chats?", "A & B", []string{"first", "<second>"}, 90*time.Second)
	want := []string{
		"--list", "--checklist",
		"--title=Continue chats?",
		"--text=A &amp; B",
		"--column=Continue",
		"--column=#",
		"--column=Chat",
		"--hide-column=2",
		"--print-column=2",
		"--separator=\n",
		"--ok-label=Continue",
		"--cancel-label=Don't continue",
		"--width=460",
		"--height=260",
		"--timeout=90",
		"TRUE", "0", "first",
		"TRUE", "1", "<second>",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("zenityChecklistArgs:\n got %q\nwant %q", got, want)
	}
}

func TestZenityChecklistTimeoutAtLeastOneSecond(t *testing.T) {
	got := zenityChecklistArgs("T", "P", []string{"a"}, 200*time.Millisecond)
	if !reflect.DeepEqual(got[len(got)-3:], []string{"TRUE", "0", "a"}) {
		t.Fatalf("rows changed: %q", got)
	}
	if !contains(got, "--timeout=1") {
		t.Fatalf("timeout is not at least 1 second: %q", got)
	}
}

func TestZenityChecklistHeightCapped(t *testing.T) {
	titles := make([]string, 20)
	if !contains(zenityChecklistArgs("T", "P", titles, time.Minute), "--height=380") {
		t.Fatal("height is not capped at six rows")
	}
}

func TestKdialogChecklistArgs(t *testing.T) {
	got := kdialogChecklistArgs("Continue chats?", "P", []string{"first", "second"})
	want := []string{"--title", "Continue chats?", "--checklist", "P", "0", "first", "on", "1", "second", "on"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("kdialogChecklistArgs:\n got %q\nwant %q", got, want)
	}
}

func TestParseZenityIndices(t *testing.T) {
	tests := []struct {
		out  string
		n    int
		want []bool
	}{
		{"0\n2\n", 3, []bool{true, false, true}},
		{"", 2, []bool{false, false}},
		{"garbage\n1\n", 2, []bool{false, true}},
		{"5\n-1\n1\n", 2, []bool{false, true}},
		{"1 \n", 2, []bool{false, true}},
	}
	for _, tt := range tests {
		if got := parseZenityIndices(tt.out, tt.n); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("parseZenityIndices(%q, %d) = %v, want %v", tt.out, tt.n, got, tt.want)
		}
	}
}

func TestParseKdialogTags(t *testing.T) {
	tests := []struct {
		out  string
		n    int
		want []bool
	}{
		{`"0" "2"`, 3, []bool{true, false, true}},
		{"0 2", 3, []bool{true, false, true}},
		{"", 2, []bool{false, false}},
		{`"x" "9" "1"`, 2, []bool{false, true}},
		{"-3 1", 2, []bool{false, true}},
	}
	for _, tt := range tests {
		if got := parseKdialogTags(tt.out, tt.n); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("parseKdialogTags(%q, %d) = %v, want %v", tt.out, tt.n, got, tt.want)
		}
	}
}

func TestNoDisplayFallsBack(t *testing.T) {
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	if tool := linuxDialogTool(); tool != "" {
		t.Fatalf("linuxDialogTool = %q without a display", tool)
	}
	if text, ok := nativeAskNumber("T", "P", "85"); text != "" || ok {
		t.Fatalf("nativeAskNumber = %q, %v; want \"\", false", text, ok)
	}
	keep := nativeResume([]string{"a", "b", "c"}, time.Second)
	if !reflect.DeepEqual(keep, []bool{true, true, true}) {
		t.Fatalf("nativeResume = %v; want all true", keep)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestChecklistTitlesAreNeverOptions(t *testing.T) {
	titles := []string{"--help", "-x", "fine"}
	z := zenityChecklistArgs("t", "p", titles, time.Minute)
	if got := z[len(z)-9:]; !reflect.DeepEqual(got, []string{"TRUE", "0", " --help", "TRUE", "1", " -x", "TRUE", "2", "fine"}) {
		t.Errorf("zenity rows = %q", got)
	}
	k := kdialogChecklistArgs("t", "p", titles)
	if got := k[len(k)-9:]; !reflect.DeepEqual(got, []string{"0", " --help", "on", "1", " -x", "on", "2", "fine", "on"}) {
		t.Errorf("kdialog rows = %q", got)
	}
}
