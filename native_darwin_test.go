package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPanelSnapshotKeepsAccountsAndMarksStaleUsage(t *testing.T) {
	now := time.Now()
	accounts := []Account{
		{UUID: "active", Name: "Personal", Email: "not-needed@example.test", Plan: "Team", Usage: &Usage{Session: 24, Weekly: 88, CheckedAt: now.Add(-6 * time.Minute), SessionReset: now.Add(time.Hour)}},
		{UUID: "other", Name: "Work", Usage: &Usage{Session: 100, Weekly: 30, CheckedAt: now.Add(-6 * time.Minute), SessionReset: now.Add(-time.Minute)}},
	}
	p := panelSnapshot(snapshot{Accounts: accounts, Active: "active", Threshold: 96}, now)
	if len(p.Accounts) != 2 || !p.Accounts[0].Stale || p.Accounts[1].Stale {
		t.Fatal("freshness must respect active vs inactive poll intervals")
	}
	if p.Accounts[1].Session != 0 {
		t.Fatal("expired session window should not display 100%")
	}
	if p.Accounts[0].Weekly != 88 || p.Accounts[0].SessionReset == 0 {
		t.Fatal("usage and reset data lost")
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "not-needed@example.test") || strings.Contains(string(data), "token") {
		t.Fatal("native view should receive display metadata only")
	}
}

func TestPanelRefreshIssuePreservesLastUsageWithoutRequestingLogin(t *testing.T) {
	now := time.Now()
	a := Account{UUID: "sample", RefreshIssue: "Unable to refresh usage", Usage: &Usage{Session: 42, CheckedAt: now}}
	p := panelSnapshot(snapshot{Accounts: []Account{a}}, now)
	row := p.Accounts[0]
	if row.NeedsLogin || !row.HasUsage || row.Session != 42 || row.Problem != a.RefreshIssue {
		t.Fatal("connection failure must retain usage and not masquerade as a sign-in failure")
	}
	a.Problem = "signed out, add it again"
	p = panelSnapshot(snapshot{Accounts: []Account{a}}, now)
	if !p.Accounts[0].NeedsLogin || p.Accounts[0].Problem != a.Problem {
		t.Fatal("authentication failure must take precedence")
	}
}
