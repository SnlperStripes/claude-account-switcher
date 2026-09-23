package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// claudeDesktop finds the app data folder. The Microsoft Store app keeps it in
// its package folder; processes started by that app see it as %APPDATA%\Claude,
// all others do not. The folder with the newest config.json wins.
func claudeDesktop() desktop {
	dirs, _ := filepath.Glob(filepath.Join(os.Getenv("LOCALAPPDATA"), "Packages", "Claude_*", "LocalCache", "Roaming", "Claude"))
	dirs = append(dirs, filepath.Join(os.Getenv("APPDATA"), "Claude"))
	best, newest := dirs[len(dirs)-1], time.Time{}
	for _, dir := range dirs {
		if info, err := os.Stat(filepath.Join(dir, "config.json")); err == nil && info.ModTime().After(newest) {
			best, newest = dir, info.ModTime()
		}
	}
	return desktop{dir: best}
}

// startedByDesktop reports whether this process runs below the Claude app,
// for example from a Claude Code terminal. Such processes share the Store
// app's file and registry redirection.
func startedByDesktop() bool {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(snap)
	parent := map[uint32]uint32{}
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		parent[e.ProcessID] = e.ParentProcessID
	}
	pids, _ := desktopProcesses()
	for pid, i := parent[uint32(os.Getpid())], 0; pid != 0 && i < 32; pid, i = parent[pid], i+1 {
		if slices.Contains(pids, pid) {
			return true
		}
	}
	return false
}

// relaunchOutsideDesktop starts the switcher again through Explorer when it
// was started below the Claude app, so it survives Claude quitting.
func relaunchOutsideDesktop() bool {
	if !startedByDesktop() {
		return false
	}
	exe, err := os.Executable()
	return err == nil && start("explorer.exe", exe) == nil
}

// desktopProcesses returns the running Claude desktop processes and the app's
// executable. The Claude Code CLI is also called claude.exe, so a process
// only counts when its folder holds the Electron bundle (resources\app.asar).
func desktopProcesses() (pids []uint32, exe string) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, ""
	}
	defer windows.CloseHandle(snap)
	isApp := map[string]bool{}
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if !strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), "claude.exe") {
			continue
		}
		path := processPath(e.ProcessID)
		if path == "" {
			continue
		}
		dir := filepath.Dir(path)
		ok, seen := isApp[dir]
		if !seen {
			_, statErr := os.Stat(filepath.Join(dir, "resources", "app.asar"))
			ok = statErr == nil
			isApp[dir] = ok
		}
		if ok {
			pids = append(pids, e.ProcessID)
			exe = path
		}
	}
	return pids, exe
}

func processPath(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

func desktopRunning() bool {
	pids, _ := desktopProcesses()
	return len(pids) > 0
}

// quitDesktop asks the app to close, then ends it if it is still running.
func quitDesktop() error {
	pids, _ := desktopProcesses()
	if len(pids) == 0 {
		return nil
	}
	run("taskkill", pidArgs(pids)...)
	if waitGone(8 * time.Second) {
		return nil
	}
	pids, _ = desktopProcesses()
	// No /T: that would also end anything started from a Claude terminal,
	// including this switcher. Every app process is listed in pids anyway.
	run("taskkill", append([]string{"/F"}, pidArgs(pids)...)...)
	if waitGone(10 * time.Second) {
		return nil
	}
	return errors.New("Claude did not quit")
}

func pidArgs(pids []uint32) []string {
	var args []string
	for _, p := range pids {
		args = append(args, "/PID", strconv.FormatUint(uint64(p), 10))
	}
	return args
}

func waitGone(timeout time.Duration) bool {
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(300 * time.Millisecond) {
		if !desktopRunning() {
			// Give the file system a moment to release the cookie store.
			time.Sleep(700 * time.Millisecond)
			return true
		}
	}
	return false
}

var appIDPattern = regexp.MustCompile(`<Application\s+Id="([^"]+)"`)

// startDesktop launches the app. Store (MSIX) installs cannot be started from
// their exe, they are started through their app user model ID instead.
func startDesktop(exe string) error {
	if aumid := storeAppID(exe); aumid != "" {
		return start("explorer.exe", `shell:AppsFolder\`+aumid)
	}
	if exe != "" {
		if _, err := os.Stat(exe); err == nil {
			return start(exe)
		}
	}
	for _, p := range []string{
		filepath.Join(os.Getenv("LOCALAPPDATA"), "AnthropicClaude", "claude.exe"),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Claude", "Claude.exe"),
	} {
		if _, err := os.Stat(p); err == nil {
			return start(p)
		}
	}
	out, err := output("powershell", "-NoProfile", "-Command", "(Get-StartApps | Where-Object Name -eq 'Claude' | Select-Object -First 1).AppID")
	if id := strings.TrimSpace(out); err == nil && id != "" {
		return start("explorer.exe", `shell:AppsFolder\`+id)
	}
	return errors.New("could not find the Claude app")
}

// storeAppID derives "Claude_<publisher>!Claude" from a path like
// ...\WindowsApps\Claude_2.7032.0.0_x64__<publisher>\app\Claude.exe.
func storeAppID(exe string) string {
	if !strings.Contains(strings.ToLower(exe), `\windowsapps\`) {
		return ""
	}
	pkgDir := filepath.Dir(filepath.Dir(exe))
	base := filepath.Base(pkgDir)
	name, _, ok1 := strings.Cut(base, "_")
	sep := strings.LastIndex(base, "__")
	manifest, err := os.ReadFile(filepath.Join(pkgDir, "AppxManifest.xml"))
	if !ok1 || sep < 0 || err != nil {
		return ""
	}
	m := appIDPattern.FindSubmatch(manifest)
	if m == nil {
		return ""
	}
	return name + "_" + base[sep+2:] + "!" + string(m[1])
}

func openFolder(path string) { _ = start("explorer.exe", path) }

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const runValue = "ClaudeAccountSwitcher"

func autostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runValue)
	return err == nil
}

func setAutostart(on bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		return k.DeleteValue(runValue)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return k.SetStringValue(runValue, `"`+exe+`"`)
}

func hidden(cmd *exec.Cmd) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	return cmd
}

func run(name string, args ...string) { _ = hidden(exec.Command(name, args...)).Run() }

func output(name string, args ...string) (string, error) {
	out, err := hidden(exec.Command(name, args...)).Output()
	return string(out), err
}

func start(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
