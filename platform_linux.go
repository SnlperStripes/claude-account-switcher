package main

import (
	"errors"
	"io/fs"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The Linux app comes as a .deb. Its main process runs this binary without a
// --type= argument; Chromium starts every helper (renderer, GPU, and so on)
// with one, so only the main process counts as the app.
const desktopBinary = "claude-desktop"

type procKind int

const (
	otherProc procKind = iota
	mainProc
	helperProc
)

// desktopProc is a running process of the Claude desktop app.
type desktopProc struct {
	pid  int
	exe  string
	kind procKind
	args []string
}

// configHome is $XDG_CONFIG_HOME, or ~/.config. The XDG spec ignores a
// relative value, so only an absolute one is used.
func configHome() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(dir) {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}

// The app keeps its data where Electron puts userData: a Claude folder under
// the config home.
func claudeDesktop() desktop {
	return desktop{dir: filepath.Join(configHome(), "Claude")}
}

// desktopExe takes the target of a /proc/<pid>/exe link and reports the
// program's path, and whether it is the desktop app. After an upgrade the
// old file shows as deleted, but it still was the app, so the suffix is cut.
func desktopExe(link string) (string, bool) {
	path := strings.TrimSuffix(link, " (deleted)")
	return path, filepath.Base(path) == desktopBinary
}

// processKind tells the main process from a helper by its arguments.
func processKind(args []string) procKind {
	for _, a := range args {
		if strings.HasPrefix(a, "--type=") {
			return helperProc
		}
	}
	return mainProc
}

// readCmdline returns a process's arguments. The kernel separates them with NUL.
func readCmdline(dir string) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "cmdline"))
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00"), nil
}

// scanDesktop lists the running Claude desktop processes, helpers included.
// Other programs are left out, even the Claude Code CLI, which is called
// "claude". Our own process is skipped.
func scanDesktop() []desktopProc {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var procs []desktopProc
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		dir := filepath.Join("/proc", e.Name())
		link, err := os.Readlink(filepath.Join(dir, "exe"))
		if err != nil {
			continue // another user's process, or it just exited
		}
		exe, ok := desktopExe(link)
		if !ok {
			continue
		}
		args, err := readCmdline(dir)
		if err != nil {
			continue // it exited while we looked
		}
		procs = append(procs, desktopProc{pid: pid, exe: exe, kind: processKind(args), args: args})
	}
	return procs
}

// desktopRestartCommand retains only persistent, recognized launch options.
// Positional arguments can be one-shot deep links or files and are never replayed.
func desktopRestartCommand(procs []desktopProc) (string, []string) {
	for _, p := range procs {
		if p.kind != mainProc {
			continue
		}
		var kept []string
		flags := map[string]bool{"--no-sandbox": false, "--disable-gpu": false, "--disable-gpu-sandbox": false, "--disable-dev-shm-usage": false, "--ozone-platform": true, "--ozone-platform-hint": true, "--enable-features": true, "--disable-features": true, "--password-store": true, "--use-gl": true, "--class": true}
		for i := 1; i < len(p.args); i++ {
			arg := p.args[i]
			if arg == "--" {
				break
			}
			key, _, equals := strings.Cut(arg, "=")
			value, ok := flags[key]
			if !ok {
				continue
			}
			if equals {
				if value {
					kept = append(kept, arg)
				}
				continue
			}
			if value {
				if i+1 >= len(p.args) || strings.HasPrefix(p.args[i+1], "-") {
					continue
				}
				i++
				kept = append(kept, arg, p.args[i])
				continue
			}
			kept = append(kept, arg)
		}
		return p.exe, kept
	}
	return "", nil
}

func desktopRestartLaunch() (string, []string) { return desktopRestartCommand(scanDesktop()) }

// desktopProcesses returns the pids of the app's main processes and the
// app's executable. Helpers are left out; quitDesktop ends them too.
func desktopProcesses() ([]uint32, string) {
	var pids []uint32
	exe := ""
	for _, p := range scanDesktop() {
		if p.kind == mainProc {
			pids = append(pids, uint32(p.pid))
			exe = p.exe
		}
	}
	return pids, exe
}

// desktopRunning counts helpers too: a leftover helper still holds the
// app's files.
func desktopRunning() bool {
	return len(scanDesktop()) > 0
}

// quitDesktop asks the app to quit with SIGTERM, which Electron takes as a
// clean quit. If it does not leave in time, every process is killed.
func quitDesktop() error {
	if !desktopRunning() {
		return nil
	}
	signalDesktop(syscall.SIGTERM, false)
	if waitGone(8 * time.Second) {
		return nil
	}
	signalDesktop(syscall.SIGKILL, true)
	if waitGone(10 * time.Second) {
		return nil
	}
	return errors.New(tr("Claude did not quit", "Claude 没有退出"))
}

// signalDesktop sends sig to the main processes only, or to every desktop
// process when all is true.
func signalDesktop(sig syscall.Signal, all bool) {
	for _, p := range scanDesktop() {
		if all || p.kind == mainProc {
			_ = syscall.Kill(p.pid, sig)
		}
	}
}

// waitGone polls until no desktop process is left. Like the other platforms,
// it then waits a moment so the app's files are released.
func waitGone(timeout time.Duration) bool {
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(300 * time.Millisecond) {
		if !desktopRunning() {
			time.Sleep(700 * time.Millisecond)
			return true
		}
	}
	return false
}

// startDetached starts a program in its own session, so it keeps running
// after this process quits, and where systemd runs the session also in a
// scope of its own. Without the scope the program stays in the switcher's
// cgroup. Started at login, that is a systemd service, and before systemd
// 254 (Ubuntu 22.04, Debian 12) the service kills whatever is left in it
// once the switcher quits, Claude included.
func startDetached(path string, args ...string) error {
	return startDetachedEnv(nil, path, args...)
}

// startDetachedEnv is startDetached with env as the environment, nil for
// this process's own.
func startDetachedEnv(env []string, path string, args ...string) error {
	if run, err := exec.LookPath("systemd-run"); err == nil {
		err := startWatched(env, run, scopeArgs(path, args)...)
		if err == nil {
			return nil
		}
		log.Printf("systemd-run could not start %s (%v), starting it directly", path, err)
	}
	cmd := exec.Command(path, args...)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// scopeArgs are the systemd-run arguments that run path in a new scope of
// the user's systemd. systemd-run then becomes the program, so the process
// keeps the switcher's environment.
func scopeArgs(path string, args []string) []string {
	return append([]string{"--user", "--scope", "--quiet", "--collect", "--", path}, args...)
}

// scopeStartWait is how long startWatched waits for a quick failure.
const scopeStartWait = 2 * time.Second

// startWatched starts a program in its own session and waits a moment. An
// exit with an error in that time, such as systemd-run finding no user
// manager, is returned, so the caller can start the program another way.
// A program that is still running, or that ended cleanly, counts as started.
func startWatched(env []string, path string, args ...string) error {
	cmd := exec.Command(path, args...)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(scopeStartWait):
		return nil
	}
}

// desktopEnv is the environment Claude is started with: this process's own,
// without the variables that make an Electron app run as plain Node. A
// switcher started from a terminal inside Claude can inherit them.
func desktopEnv(environ []string) []string {
	return slices.DeleteFunc(slices.Clone(environ), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return name == "ELECTRON_RUN_AS_NODE" || name == "ELECTRON_NO_ATTACH_CONSOLE"
	})
}

// desktopCommand finds the app to start: the program that was running, then
// one on the PATH, then the path the .deb uses.
func desktopCommand(exe string) (string, error) {
	if exe != "" {
		if _, err := os.Stat(exe); err == nil {
			return exe, nil
		}
	}
	if p, err := exec.LookPath(desktopBinary); err == nil {
		return p, nil
	}
	const debPath = "/usr/bin/claude-desktop"
	if _, err := os.Stat(debPath); err == nil {
		return debPath, nil
	}
	return "", errors.New(tr("could not find the Claude app", "找不到 Claude 应用"))
}

func startDesktop(exe string, args ...string) error {
	path, err := desktopCommand(exe)
	if err != nil {
		return err
	}
	return startDetachedEnv(desktopEnv(os.Environ()), path, args...)
}

// shellOpen opens a folder, or a web page in the default browser. xdg-open
// returns at once; a goroutine reaps it when it ends.
func shellOpen(target string) {
	cmd := exec.Command("xdg-open", target)
	if cmd.Start() == nil {
		go cmd.Wait()
	}
}

// launch starts the switcher at exe in its own session.
func launch(exe string) error { return startDetached(exe) }

// askToQuitOld does nothing: no switcher from before 0.3.3 was ever run on
// Linux, so there is none to ask the user to quit.
func askToQuitOld() {}

// relaunchOutsideDesktop starts the switcher again in its own session when it
// was started from inside the Claude app, for example from a Claude Code
// terminal. Otherwise quitting Claude for a switch closes that terminal,
// which ends the switcher before it can start Claude again. A marker file
// stops a relaunch loop should the check ever misjudge.
func relaunchOutsideDesktop(dataDir string) bool {
	if !insideDesktop(os.Getppid(), parentPID, isDesktopPID) {
		return false
	}
	marker := filepath.Join(dataDir, "relaunched")
	if info, err := os.Stat(marker); err == nil && time.Since(info.ModTime()) < 30*time.Second {
		return false
	}
	exe, err := os.Executable()
	if err != nil || os.WriteFile(marker, nil, 0o600) != nil {
		return false
	}
	return startDetached(strings.TrimSuffix(exe, " (deleted)"), os.Args[1:]...) == nil
}

// insideDesktop walks up from pid through its parents and reports whether
// one of them is the Claude app. The walk stops at init and after a fixed
// number of steps, so a broken parent chain cannot loop.
func insideDesktop(pid int, parent func(int) int, isDesktop func(int) bool) bool {
	for range 64 {
		if pid <= 1 {
			return false
		}
		if isDesktop(pid) {
			return true
		}
		pid = parent(pid)
	}
	return false
}

// isDesktopPID reports whether pid runs the Claude app, helpers included.
func isDesktopPID(pid int) bool {
	link, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
	if err != nil {
		return false
	}
	_, ok := desktopExe(link)
	return ok
}

// parentPID reads a process's parent from /proc/<pid>/stat, 0 when it
// cannot be read.
func parentPID(pid int) int {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0
	}
	return statParent(string(raw))
}

// statParent takes the parent pid out of a /proc/<pid>/stat line. The
// program name sits in parentheses and may itself hold spaces and
// parentheses, so the fields are read after the last ")": state, then ppid.
func statParent(stat string) int {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return 0
	}
	fields := strings.Fields(stat[i+1:])
	if len(fields) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(fields[1])
	return ppid
}

// autostartPath is the XDG autostart entry that starts the switcher at login.
func autostartPath() string {
	return filepath.Join(configHome(), "autostart", "claude-account-switcher.desktop")
}

func autostartEnabled() bool {
	_, err := os.Stat(autostartPath())
	return err == nil
}

func setAutostart(on bool) error {
	if !on {
		err := os.Remove(autostartPath())
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// Go reports a binary that an update replaced with the " (deleted)"
	// suffix, which is not part of the path.
	exe = strings.TrimSuffix(exe, " (deleted)")
	// A line break in the path would end the Exec line and start keys of its
	// own, so such a path is refused.
	if strings.ContainsFunc(exe, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return errors.New("the switcher's path contains a control character")
	}
	entry := "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=Claude Account Switcher\n" +
		"Comment=Switch the Claude desktop app between accounts\n" +
		"Exec=" + desktopExecQuote(exe) + "\n" +
		"Terminal=false\n" +
		"X-GNOME-Autostart-enabled=true\n"
	if err := os.MkdirAll(filepath.Dir(autostartPath()), 0o755); err != nil {
		return err
	}
	return os.WriteFile(autostartPath(), []byte(entry), 0o644)
}

// execArgEscaper escapes a program path for an Exec argument. Inside double
// quotes, " ` $ and \ need a backslash. A % starts a field code, so it is
// written %%.
var execArgEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`, `%`, `%%`)

// desktopExecQuote turns a program path into the value of an Exec key. Exec
// has its own quoting, and the .desktop file then reads that value with its
// own escapes, so a backslash is doubled once more at the end.
func desktopExecQuote(path string) string {
	arg := `"` + execArgEscaper.Replace(path) + `"`
	return strings.ReplaceAll(arg, `\`, `\\`)
}

// systemLanguages lists the user's languages, preferred first, from the
// locale variables a desktop session sets.
func systemLanguages() []string { return localeLanguages(os.Getenv) }

// localeLanguages reads the language settings through getenv, LANGUAGE first,
// then LC_ALL, LC_MESSAGES and LANG, and returns them without duplicates.
func localeLanguages(getenv func(string) string) []string {
	raw := strings.Split(getenv("LANGUAGE"), ":")
	raw = append(raw, getenv("LC_ALL"), getenv("LC_MESSAGES"), getenv("LANG"))
	var langs []string
	for _, r := range raw {
		if l := localeTag(r); l != "" && !slices.Contains(langs, l) {
			langs = append(langs, l)
		}
	}
	return langs
}

// localeTag turns a locale such as "zh_CN.UTF-8@x" into "zh-CN". It returns
// "" for empty values and for the C and POSIX locales, which name no language.
func localeTag(v string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(v), ".")
	l, _, _ = strings.Cut(l, "@")
	l = strings.ReplaceAll(l, "_", "-")
	if l == "C" || l == "POSIX" {
		return ""
	}
	return l
}

// instanceAddress is where the single-instance lock listens: an abstract
// Unix socket named after the user. A loopback port is shared by every user
// of the machine, so a second user could not start a switcher of their own
// while the first one runs. The kernel removes an abstract socket with its
// process, so a crash leaves nothing behind.
func instanceAddress() (string, string) {
	return "unix", "@claude-account-switcher-" + strconv.Itoa(os.Getuid())
}

// sameUser reports whether the peer on the lock runs as this user. Anyone on
// the machine can connect to an abstract socket, and only the user's own
// newer switcher may make this one quit.
// A loopback port, which the tests use, carries no user, as on the other
// platforms.
func sameUser(conn net.Conn) bool {
	if _, ok := conn.(*net.TCPConn); ok {
		return true
	}
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return false
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || credErr != nil {
		return false
	}
	return int(cred.Uid) == os.Getuid()
}
