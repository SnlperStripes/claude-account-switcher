package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDesktopExe(t *testing.T) {
	for link, want := range map[string]struct {
		path string
		ok   bool
	}{
		"/usr/lib/claude-desktop/claude-desktop":           {"/usr/lib/claude-desktop/claude-desktop", true},
		"/usr/lib/claude-desktop/claude-desktop (deleted)": {"/usr/lib/claude-desktop/claude-desktop", true},
		"/home/u/.local/bin/claude":                        {"/home/u/.local/bin/claude", false},
		"/usr/lib/claude-desktop/chrome_crashpad_handler":  {"/usr/lib/claude-desktop/chrome_crashpad_handler", false},
		"/usr/lib/claude-desktop/claude-desktop-helper":    {"/usr/lib/claude-desktop/claude-desktop-helper", false},
		"": {"", false},
	} {
		path, ok := desktopExe(link)
		if path != want.path || ok != want.ok {
			t.Errorf("desktopExe(%q) = %q, %v; want %q, %v", link, path, ok, want.path, want.ok)
		}
	}
}

func TestProcessKind(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want procKind
	}{
		{nil, mainProc},
		{[]string{"/usr/lib/claude-desktop/claude-desktop", "--no-sandbox"}, mainProc},
		{[]string{"/usr/lib/claude-desktop/claude-desktop", "--type=renderer"}, helperProc},
		{[]string{"claude-desktop", "--no-sandbox", "--type=gpu-process"}, helperProc},
		{[]string{"claude-desktop", "--type=zygote", "--no-zygote-sandbox"}, helperProc},
	} {
		if got := processKind(tc.args); got != tc.want {
			t.Errorf("processKind(%q) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

// decodeExec reads an Exec value the way a desktop launcher does: first the
// .desktop file's escapes, then the Exec quoting, then the %% field code.
func decodeExec(v string) string {
	s := strings.ReplaceAll(v, `\\`, `\`)
	s = s[1 : len(s)-1]
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return strings.ReplaceAll(b.String(), "%%", "%")
}

func TestDesktopExecQuote(t *testing.T) {
	for path, want := range map[string]string{
		"/usr/bin/claude-account-switcher": `"/usr/bin/claude-account-switcher"`,
		"/opt/My App/bin":                  `"/opt/My App/bin"`,
		`/a$b"c\d`:                         `"/a\\$b\\"c\\\\d"`,
		"/x`y":                             "\"/x\\\\`y\"",
		"/100%/bin":                        `"/100%%/bin"`,
	} {
		if got := desktopExecQuote(path); got != want {
			t.Errorf("desktopExecQuote(%q) = %s, want %s", path, got, want)
		}
		if got := decodeExec(desktopExecQuote(path)); got != path {
			t.Errorf("decodeExec(desktopExecQuote(%q)) = %q", path, got)
		}
	}
}

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLocaleLanguages(t *testing.T) {
	for _, tc := range []struct {
		env  map[string]string
		want []string
	}{
		{map[string]string{"LANG": "zh_CN.UTF-8"}, []string{"zh-CN"}},
		{map[string]string{"LANGUAGE": "zh_CN:en_US", "LANG": "en_US.UTF-8"}, []string{"zh-CN", "en-US"}},
		{map[string]string{"LANGUAGE": "en_US:zh_CN", "LANG": "en_US.UTF-8"}, []string{"en-US", "zh-CN"}},
		{map[string]string{"LC_MESSAGES": "zh_CN.UTF-8", "LANG": "de_DE.UTF-8@euro"}, []string{"zh-CN", "de-DE"}},
		{map[string]string{"LC_ALL": "en_GB.UTF-8", "LANG": "zh_CN.UTF-8"}, []string{"en-GB", "zh-CN"}},
		{map[string]string{"LANG": "C.UTF-8"}, nil},
		{map[string]string{"LANG": "POSIX", "LANGUAGE": ""}, nil},
		{map[string]string{}, nil},
	} {
		if got := localeLanguages(envOf(tc.env)); !slices.Equal(got, tc.want) {
			t.Errorf("localeLanguages(%v) = %q, want %q", tc.env, got, tc.want)
		}
	}
}

func TestLocaleLeadsToChinese(t *testing.T) {
	langs := localeLanguages(envOf(map[string]string{"LANG": "zh_CN.UTF-8"}))
	if got := chooseLanguage("", langs); got != "zh-CN" {
		t.Fatalf("chooseLanguage(%q) = %q, want zh-CN", langs, got)
	}
	langs = localeLanguages(envOf(map[string]string{"LANG": "en_US.UTF-8"}))
	if got := chooseLanguage("", langs); got != "en" {
		t.Fatalf("chooseLanguage(%q) = %q, want en", langs, got)
	}
}

func TestClaudeDesktopDir(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if got, want := claudeDesktop().dir, filepath.Join(xdg, "Claude"); got != want {
		t.Fatalf("with XDG_CONFIG_HOME, dir = %q, want %q", got, want)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "relative/dir")
	if got, want := claudeDesktop().dir, filepath.Join(home, ".config", "Claude"); got != want {
		t.Fatalf("with a relative XDG_CONFIG_HOME, dir = %q, want %q", got, want)
	}
}

func TestAutostart(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if autostartEnabled() {
		t.Fatal("autostart is on before it was set")
	}
	if err := setAutostart(true); err != nil {
		t.Fatal(err)
	}
	if !autostartEnabled() {
		t.Fatal("autostart is off after setAutostart(true)")
	}
	data, err := os.ReadFile(autostartPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"[Desktop Entry]", "Type=Application", "Name=Claude Account Switcher", "Terminal=false", "Exec=\""} {
		if !strings.Contains(string(data), line) {
			t.Errorf("autostart file lacks %q:\n%s", line, data)
		}
	}
	if err := setAutostart(false); err != nil {
		t.Fatal(err)
	}
	if autostartEnabled() {
		t.Fatal("autostart is still on after setAutostart(false)")
	}
	if err := setAutostart(false); err != nil {
		t.Fatalf("removing a missing entry: %v", err)
	}
}

func TestDesktopRestartArgs(t *testing.T) {
	procs := []desktopProc{
		{exe: "/helper", kind: helperProc, args: []string{"helper", "--type=renderer", "--no-sandbox"}},
		{exe: "/app", kind: mainProc, args: []string{"wrapper", "--no-sandbox", "--ozone-platform", "wayland", "--enable-features=WaylandWindowDecorations", "claude://transient", "--unknown", "secret", "--disable-gpu"}},
	}
	exe, args := desktopRestartCommand(procs)
	if exe != "/app" || !slices.Equal(args, []string{"--no-sandbox", "--ozone-platform", "wayland", "--enable-features=WaylandWindowDecorations", "--disable-gpu"}) {
		t.Fatalf("restart = %q %q", exe, args)
	}
}

func TestDesktopRestartSkipsTransientArguments(t *testing.T) {
	procs := []desktopProc{{exe: "/app", kind: mainProc, args: []string{"/app", "--ozone-platform", "--disable-gpu", "--", "--no-sandbox"}}}
	_, args := desktopRestartCommand(procs)
	if !slices.Equal(args, []string{"--disable-gpu"}) {
		t.Fatalf("restart arguments = %q", args)
	}
	exe, args := desktopRestartCommand([]desktopProc{{kind: helperProc, exe: "/helper"}})
	if exe != "" || len(args) != 0 {
		t.Fatalf("helper selected: %q %q", exe, args)
	}
}
