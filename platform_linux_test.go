package main

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
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

func TestStatParent(t *testing.T) {
	for stat, want := range map[string]int{
		"1234 (bash) S 1200 1234 1234 34816 0":            1200,
		"4321 (a b) (c)) R 77 4321 4321 0 -1":             77,
		"99 (claude-desktop) S 1 99 99 0 -1 4194560 1000": 1,
		"broken":      0,
		"5 (short) S": 0,
	} {
		if got := statParent(stat); got != want {
			t.Errorf("statParent(%q) = %d, want %d", stat, got, want)
		}
	}
}

func TestInsideDesktop(t *testing.T) {
	// 40 (shell) -> 30 (claude-desktop helper) -> 20 (claude-desktop) -> 1
	parents := map[int]int{40: 30, 30: 20, 20: 1, 50: 1}
	parent := func(pid int) int { return parents[pid] }
	isDesktop := func(pid int) bool { return pid == 20 || pid == 30 }
	if !insideDesktop(40, parent, isDesktop) {
		t.Error("a shell under the Claude app should count as inside")
	}
	if insideDesktop(50, parent, isDesktop) {
		t.Error("a process under init is not inside the Claude app")
	}
	loop := func(pid int) int { return pid } // a chain that never ends
	if insideDesktop(60, loop, func(int) bool { return false }) {
		t.Error("a looping chain must end as not inside")
	}
}

func TestDesktopEnvDropsRunAsNode(t *testing.T) {
	in := []string{"HOME=/h", "ELECTRON_RUN_AS_NODE=1", "WAYLAND_DISPLAY=wayland-0", "ELECTRON_NO_ATTACH_CONSOLE=1"}
	got := desktopEnv(in)
	if !slices.Equal(got, []string{"HOME=/h", "WAYLAND_DISPLAY=wayland-0"}) {
		t.Errorf("desktopEnv = %q", got)
	}
	if len(in) != 4 {
		t.Error("desktopEnv changed its input")
	}
}

func TestScopeArgs(t *testing.T) {
	got := scopeArgs("/usr/lib/claude-desktop/claude-desktop", []string{"--ozone-platform=wayland"})
	want := []string{"--user", "--scope", "--quiet", "--collect", "--", "/usr/lib/claude-desktop/claude-desktop", "--ozone-platform=wayland"}
	if !slices.Equal(got, want) {
		t.Errorf("scopeArgs = %q", got)
	}
}

func TestStartWatched(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	if err := startWatched(nil, sh, "-c", "exit 3"); err == nil {
		t.Error("a quick failure should be reported")
	}
	if err := startWatched(nil, sh, "-c", "exit 0"); err != nil {
		t.Errorf("a clean quick exit counts as started: %v", err)
	}
	start := time.Now()
	if err := startWatched(nil, sh, "-c", "sleep 5"); err != nil {
		t.Errorf("a running program counts as started: %v", err)
	}
	if d := time.Since(start); d > scopeStartWait+time.Second {
		t.Errorf("startWatched waited %v", d)
	}
}

func TestInstanceSocketPerUser(t *testing.T) {
	network, addr := instanceAddress()
	if network != "unix" || addr != "@claude-account-switcher-"+strconv.Itoa(os.Getuid()) {
		t.Fatalf("instanceAddress = %q %q", network, addr)
	}
	// A test name of its own, so a switcher running on this machine is left alone.
	addr += "-test-" + strconv.Itoa(os.Getpid())
	l, ok := lockInstance(network, addr)
	if !ok {
		t.Fatal("lock not taken")
	}
	defer l.Close()
	quit := make(chan struct{})
	go serveInstance(l, func() { close(quit) })
	if got := hello(network, addr, version); got != "stay" {
		t.Errorf("same version: %q, want stay", got)
	}
	if got := hello(network, addr, "99.0.0"); got != "quit" {
		t.Errorf("newer version from the same user: %q, want quit", got)
	}
	select {
	case <-quit:
	case <-time.After(2 * time.Second):
		t.Error("quit was not called")
	}
}

func TestSameUserNeedsUnixPeer(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	if sameUser(a) {
		t.Error("a connection without credentials must not count as the same user")
	}
}

// TestHelperHello is not a test of its own: TestOtherUserCannotQuit runs the
// test binary again as another user, and this says hello from there.
func TestHelperHello(t *testing.T) {
	addr := os.Getenv("SWITCHER_HELLO_ADDR")
	if addr == "" {
		t.Skip("helper only")
	}
	os.Stdout.WriteString("answer=" + hello("unix", addr, "99.0.0") + "\n")
}

func TestOtherUserCannotQuit(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("needs root to run a helper as another user")
	}
	addr := "@claude-account-switcher-test-" + strconv.Itoa(os.Getpid())
	l, ok := lockInstance("unix", addr)
	if !ok {
		t.Fatal("lock not taken")
	}
	defer l.Close()
	quit := make(chan struct{})
	go serveInstance(l, func() { close(quit) })

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHello$")
	cmd.Env = append(os.Environ(), "SWITCHER_HELLO_ADDR="+addr)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "answer=stay") {
		t.Errorf("another user's newer version got %q, want stay", out)
	}
	select {
	case <-quit:
		t.Error("another user made the switcher quit")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestOwnedByMe(t *testing.T) {
	if !ownedByMe("/proc/self") {
		t.Error("ownedByMe(/proc/self) = false, want true")
	}
	if ownedByMe(filepath.Join(t.TempDir(), "gone")) {
		t.Error("ownedByMe of a missing folder = true, want false")
	}
	if os.Getuid() != 0 && ownedByMe("/proc/1") {
		t.Error("ownedByMe(/proc/1) = true for a user who is not root")
	}
}
