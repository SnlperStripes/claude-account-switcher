package main

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"

	"fyne.io/systray"
)

const maxAccounts = 8

var thresholds = []float64{90, 94, 96, 98}

type tray struct {
	s          *switcher
	status     *systray.MenuItem
	accounts   [maxAccounts]*systray.MenuItem
	auto       *systray.MenuItem
	thresholds []*systray.MenuItem
	add        *systray.MenuItem
	importNow  *systray.MenuItem
	autostart  *systray.MenuItem
	lastIcon   string
	updates    chan struct{}
}

func (t *tray) onReady() {
	systray.SetTooltip("Claude Account Switcher")
	t.setIcon(-1, defaultThreshold)

	t.status = systray.AddMenuItem("", "")
	t.status.Disable()
	t.status.Hide()
	for i := range t.accounts {
		t.accounts[i] = systray.AddMenuItemCheckbox("", "Switch the Claude app to this account", false)
		t.accounts[i].Hide()
	}
	systray.AddSeparator()
	t.auto = systray.AddMenuItemCheckbox("Auto-switch when nearly full", "Switch to the account with the most room left", false)
	limit := systray.AddMenuItem("Auto-switch at", "")
	for _, v := range thresholds {
		t.thresholds = append(t.thresholds, limit.AddSubMenuItemCheckbox(fmt.Sprintf("%.0f%%", v), "", false))
	}
	t.add = systray.AddMenuItem("Add account…", "Restart Claude on its sign-in screen, your current account is kept")
	t.importNow = systray.AddMenuItem("Import chats from all accounts", "Restart Claude so every chat shows up in this account")
	systray.AddSeparator()
	t.autostart = systray.AddMenuItemCheckbox("Start at login", "", autostartEnabled())
	folder := systray.AddMenuItem("Open data folder", "")
	quit := systray.AddMenuItem("Quit", "")

	for i, item := range t.accounts {
		go func() {
			for range item.ClickedCh {
				snap := t.s.snapshot()
				if i < len(snap.Accounts) && snap.Accounts[i].UUID != snap.Active {
					go t.s.run("Switch", func() error { return t.s.switchTo(snap.Accounts[i].UUID) })
				}
				t.s.onChange()
			}
		}()
	}
	for i, item := range t.thresholds {
		go func() {
			for range item.ClickedCh {
				t.s.setThreshold(thresholds[i])
			}
		}()
	}
	go func() {
		for {
			select {
			case <-t.auto.ClickedCh:
				t.s.setAuto(!t.auto.Checked())
			case <-t.add.ClickedCh:
				go t.s.run("Add account", t.s.addAccount)
			case <-t.importNow.ClickedCh:
				go t.s.run("Import", t.s.importChats)
			case <-t.autostart.ClickedCh:
				if err := setAutostart(!t.autostart.Checked()); err != nil {
					t.s.setStatus("Start at login: " + err.Error())
				}
				setChecked(t.autostart, autostartEnabled())
			case <-folder.ClickedCh:
				openFolder(t.s.dir)
			case <-quit.ClickedCh:
				systray.Quit()
			}
		}
	}()

	// Menu updates arrive from worker goroutines; one goroutine applies them.
	t.s.onChange = func() {
		select {
		case t.updates <- struct{}{}:
		default:
		}
	}
	go func() {
		for range t.updates {
			t.render()
		}
	}()
	t.s.onChange()
	go t.loop()
}

// loop polls every few seconds while a new sign-in is expected, otherwise every minute.
func (t *tray) loop() {
	var last time.Time
	for ; ; time.Sleep(5 * time.Second) {
		if !t.s.adding() && time.Since(last) < activeEvery {
			continue
		}
		last = time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		t.s.refresh(ctx)
		cancel()
	}
}

func (t *tray) render() {
	snap := t.s.snapshot()
	now := time.Now()

	if snap.Status != "" {
		t.status.SetTitle(snap.Status)
		t.status.Show()
	} else {
		t.status.Hide()
	}
	if len(snap.Accounts) == 0 && snap.Status == "" {
		t.status.SetTitle("Open Claude and sign in")
		t.status.Show()
	}

	activePct := -1.0
	tip := "Claude Account Switcher"
	for i, item := range t.accounts {
		if i >= len(snap.Accounts) {
			item.Hide()
			continue
		}
		a := snap.Accounts[i]
		detail := usageText(a, snap.Threshold, now)
		item.SetTitle(a.label() + menuTab + detail)
		setChecked(item, a.UUID == snap.Active)
		if snap.Busy {
			item.Disable()
		} else {
			item.Enable()
		}
		item.Show()
		if a.UUID == snap.Active {
			tip = a.label() + "\n" + detail
			if a.Usage != nil {
				activePct = a.Usage.current(now).Session
			}
		}
	}
	systray.SetTooltip(tip)
	t.setIcon(activePct, snap.Threshold)

	setChecked(t.auto, snap.AutoSwitch)
	for i, item := range t.thresholds {
		setChecked(item, thresholds[i] == snap.Threshold)
	}
	for _, item := range []*systray.MenuItem{t.add, t.importNow} {
		if snap.Busy {
			item.Disable()
		} else {
			item.Enable()
		}
	}
}

// menuTab right-aligns the usage on Windows, where a tab in a menu title
// starts a right-aligned column.
var menuTab = map[bool]string{true: "\t", false: "    "}[runtime.GOOS == "windows"]

func usageText(a Account, threshold float64, now time.Time) string {
	if a.Problem != "" {
		return a.Problem
	}
	if a.Usage == nil {
		return "usage unknown"
	}
	u := a.Usage.current(now)
	parts := []string{fmt.Sprintf("5h %.0f%%", u.Session), fmt.Sprintf("week %.0f%%", u.Weekly)}
	if u.Session >= threshold && !u.SessionReset.IsZero() {
		parts[0] += " until " + resetText(u.SessionReset, now)
	}
	if u.Weekly >= threshold && !u.WeeklyReset.IsZero() {
		parts[1] += " until " + resetText(u.WeeklyReset, now)
	}
	return strings.Join(parts, " · ")
}

func resetText(at, now time.Time) string {
	at, now = at.Local(), now.Local()
	if at.YearDay() == now.YearDay() && at.Year() == now.Year() {
		return at.Format("15:04")
	}
	return at.Format("Mon 15:04")
}

func (t *tray) setIcon(pct, threshold float64) {
	key := fmt.Sprintf("%.0f/%.0f", pct, threshold)
	if key == t.lastIcon {
		return
	}
	t.lastIcon = key
	systray.SetIcon(trayIcon(pct, threshold))
}

func setChecked(item *systray.MenuItem, on bool) {
	if on != item.Checked() {
		if on {
			item.Check()
		} else {
			item.Uncheck()
		}
	}
}
