// Command claude-account-switcher is a tray tool for the Claude desktop app.
// It switches the app between signed-in accounts without signing in again,
// keeps Claude Code chats visible in every account and shows plan usage.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"fyne.io/systray"
)

const version = "0.5.2"

func main() {
	// The dialog processes started by askInDialogProcess.
	if len(os.Args) == 2 && os.Args[1] == resumeFlag {
		os.Exit(runResumeDialog())
	}
	if len(os.Args) == 2 && os.Args[1] == thresholdFlag {
		os.Exit(runThresholdDialog())
	}
	status := flag.Bool("status", false, "print accounts and usage, then exit")
	asJSON := flag.Bool("json", false, "print -status as JSON (implies -status)")
	showVersion := flag.Bool("version", false, "print the version, then exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("claude-account-switcher %s\n", version)
		return
	}

	// The home folder, because the Store app redirects AppData for everything
	// it starts but leaves the home folder alone.
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatal(err)
	}
	dir := filepath.Join(home, ".claude-account-switcher")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Fatal(err)
	}
	setupLog(filepath.Join(dir, "switcher.log"), *status)
	s := newSwitcher(dir)
	language = chooseLanguage(s.st.Language, systemLanguages())

	if *status || *asJSON {
		printStatus(s, *asJSON)
		return
	}

	if relaunchOutsideDesktop(dir) {
		return
	}

	lock, ok := lockInstance(instanceAddress())
	if !ok {
		return
	}
	defer lock.Close()
	go removeReplaced()

	log.Printf("claude-account-switcher %s, app data in %s, language %s", version, claudeDesktop().dir, language)
	t := &tray{s: s, updates: make(chan struct{}, 1)}
	// When a newer version starts, a running switch finishes first and no new
	// one begins before this one quits.
	go serveInstance(lock, func() {
		for !t.s.busy.CompareAndSwap(false, true) {
			time.Sleep(200 * time.Millisecond)
		}
		systray.Quit()
	})
	systray.Run(t.onReady, func() {})
}

// setupLog writes to switcher.log and starts it over once it passes 1 MB.
// A tray app has no usable stderr on Windows, so only -status also prints there.
// A panic or fatal error goes to the log too, or a crash would leave no trace.
func setupLog(path string, console bool) {
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if info, err := os.Stat(path); err == nil && info.Size() > 1<<20 {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return
	}
	if err := debug.SetCrashOutput(f, debug.CrashOptions{}); err != nil {
		log.Printf("crash output: %v", err)
	}
	if console {
		log.SetOutput(io.MultiWriter(f, os.Stderr))
	} else {
		log.SetOutput(f)
	}
}

// statusReport is the -status -json output: the active account's UUID and
// every saved account with its usage. Account holds no sign-in data.
type statusReport struct {
	Active   string    `json:"active"`
	Accounts []Account `json:"accounts"`
}

func statusJSON(snap snapshot, now time.Time) ([]byte, error) {
	accounts := make([]Account, len(snap.Accounts))
	copy(accounts, snap.Accounts)
	// Match the human output: a window whose reset time has passed reads as
	// zero. Without this, a failed usage refresh leaves stale numbers here
	// while usageText shows the window as free again.
	for i, a := range accounts {
		if a.Usage != nil {
			current := a.Usage.current(now)
			accounts[i].Usage = &current
		}
	}
	report := statusReport{Active: snap.Active, Accounts: accounts}
	if report.Accounts == nil {
		report.Accounts = []Account{}
	}
	return json.MarshalIndent(report, "", "  ")
}

// printStatus checks once and prints what the menu would show. It only reports:
// a switch that is due is left to the tray.
func printStatus(s *switcher, asJSON bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.check(ctx)
	snap := s.snapshot()
	if asJSON {
		data, err := statusJSON(snap, time.Now())
		if err != nil {
			log.Printf("print status as JSON: %v", err)
			return
		}
		fmt.Println(string(data))
		return
	}
	if len(snap.Accounts) == 0 {
		fmt.Println(tr("no accounts yet, sign in to Claude", "还没有账号，请先在 Claude 中登录"))
	}
	for _, a := range snap.Accounts {
		mark := " "
		if a.UUID == snap.Active {
			mark = "*"
		}
		fmt.Printf("%s %s  %s\n", mark, a.label(), usageText(a, snap.Threshold, time.Now()))
	}
}
