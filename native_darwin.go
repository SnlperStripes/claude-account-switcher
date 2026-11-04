package main

import (
	"bufio"
	"context"
	"encoding/json"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// The native view receives display metadata only. Credentials stay in Go and
// commands travel over anonymous child-process pipes, not a new network API.
type panelAccount struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Plan         string  `json:"plan"`
	Problem      string  `json:"problem"`
	NeedsLogin   bool    `json:"needsLogin"`
	HasUsage     bool    `json:"hasUsage"`
	Session      float64 `json:"session"`
	Weekly       float64 `json:"weekly"`
	SessionReset int64   `json:"sessionReset"`
	WeeklyReset  int64   `json:"weeklyReset"`
	CheckedAt    int64   `json:"checkedAt"`
	Stale        bool    `json:"stale"`
}

type panelState struct {
	Accounts    []panelAccount `json:"accounts"`
	Active      string         `json:"active"`
	AutoSwitch  bool           `json:"autoSwitch"`
	SpendWeekly bool           `json:"spendWeekly"`
	Threshold   float64        `json:"threshold"`
	Status      string         `json:"status"`
	Note        string         `json:"note"`
	Busy        bool           `json:"busy"`
	Autostart   bool           `json:"autostart"`
	Version     string         `json:"version"`
	Update      string         `json:"update"`
}

func epoch(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func panelSnapshot(snap snapshot, now time.Time) panelState {
	p := panelState{Accounts: []panelAccount{}, Active: snap.Active, AutoSwitch: snap.AutoSwitch,
		SpendWeekly: snap.SpendWeekly, Threshold: snap.Threshold, Status: snap.Status,
		Note: snap.Note, Busy: snap.Busy, Version: version, Update: snap.Update, Autostart: autostartEnabled()}
	for _, a := range snap.Accounts {
		name := a.Name
		if name == "" {
			name = a.Email
		}
		row := panelAccount{ID: a.UUID, Name: name, Plan: a.Plan, Problem: a.Problem}
		row.NeedsLogin = a.Problem != ""
		if row.Problem == "" {
			row.Problem = a.RefreshIssue
		}
		if a.Usage != nil {
			u := a.Usage.current(now)
			fresh := idleFresh
			if a.UUID == snap.Active {
				fresh = activeFresh
			}
			row.HasUsage, row.Session, row.Weekly = true, u.Session, u.Weekly
			row.SessionReset, row.WeeklyReset, row.CheckedAt = epoch(u.SessionReset), epoch(u.WeeklyReset), epoch(u.CheckedAt)
			row.Stale = now.Sub(u.CheckedAt) > fresh
		}
		p.Accounts = append(p.Accounts, row)
	}
	return p
}

func runNativeUI(s *switcher, lock net.Listener) bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	helper := filepath.Join(filepath.Dir(exe), "switcher-panel")
	if _, err := os.Stat(helper); err != nil {
		return false
	}
	cmd := exec.Command(helper)
	for _, arg := range os.Args[1:] {
		if arg == "--show-panel" {
			cmd.Args = append(cmd.Args, "--show")
		}
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return false
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		in.Close()
		return false
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		in.Close()
		log.Printf("native panel: %v", err)
		return false
	}
	log.Print("native account overview started")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var writeMu sync.Mutex
	s.onChange = func() {
		state := panelSnapshot(s.snapshot(), time.Now())
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = json.NewEncoder(in).Encode(state)
	}
	stop := func() {
		for !s.busy.CompareAndSwap(false, true) {
			select {
			case <-ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
		}
		cancel()
		in.Close() // EOF closes the native UI cleanly.
	}
	go serveInstance(lock, stop)
	// Serialize polling and settings/actions; a refresh respects server backoff.
	var operationMu sync.Mutex
	refresh := func(force bool) {
		operationMu.Lock()
		defer operationMu.Unlock()
		if force {
			s.mu.Lock()
			s.forceCheck = true
			s.mu.Unlock()
		}
		s.refresh(ctx)
		s.onChange()
	}
	go func() {
		refresh(false)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		last := time.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if s.adding() || time.Since(last) >= hotEvery {
					refresh(false)
					last = time.Now()
				}
			}
		}
	}()
	go func() {
		scan := bufio.NewScanner(out)
		scan.Buffer(make([]byte, 1024), 16*1024)
		for scan.Scan() {
			var action struct {
				Type    string  `json:"type"`
				ID      string  `json:"id"`
				Enabled bool    `json:"enabled"`
				Value   float64 `json:"value"`
			}
			if json.Unmarshal(scan.Bytes(), &action) != nil {
				continue
			}
			if action.Type == "quit" {
				go stop()
				return
			}
			if action.Type == "ready" {
				s.onChange()
				continue
			}
			go func() {
				if action.Type == "refresh" {
					refresh(true)
					return
				}
				operationMu.Lock()
				defer operationMu.Unlock()
				switch action.Type {
				case "switch":
					s.run("Switch", func() error { return s.switchTo(action.ID, byHand) })
				case "add":
					s.run("Add account", s.addAccount)
				case "import":
					s.run("Import", s.importChats)
				case "auto":
					s.setAuto(action.Enabled)
				case "weekly":
					s.setSpendWeekly(action.Enabled)
				case "threshold":
					for _, v := range thresholds {
						if action.Value == v {
							s.setThreshold(v)
						}
					}
				case "autostart":
					if err := setAutostart(action.Enabled); err != nil {
						s.setStatus("Start at login: " + err.Error())
					}
				case "folder":
					shellOpen(s.dir)
				case "releases":
					// The app bundle has a companion helper: do not replace only
					// the Go executable through the single-binary updater.
					shellOpen(releasesURL)
				}
				s.onChange()
			}()
		}
	}()
	s.onChange()
	err = cmd.Wait()
	if err != nil {
		log.Printf("native panel exited: %v", err)
	}
	in.Close()
	return true
}
