package main

import "testing"

func TestInsideDesktop(t *testing.T) {
	// 40 (shell) -> 30 (Claude helper) -> 20 (Claude) -> 1
	parents := map[int]int{40: 30, 30: 20, 20: 1, 50: 1}
	parent := func(pid int) int { return parents[pid] }
	isDesktop := func(pid int) bool { return pid == 20 || pid == 30 }
	if !insideDesktop(40, parent, isDesktop) {
		t.Error("a shell under the Claude app should count as inside")
	}
	if insideDesktop(50, parent, isDesktop) {
		t.Error("a process under init is not inside the Claude app")
	}
	if insideDesktop(70, parent, isDesktop) {
		t.Error("a process whose parent is gone is not inside the Claude app")
	}
	loop := func(pid int) int { return pid } // a chain that never ends
	if insideDesktop(60, loop, func(int) bool { return false }) {
		t.Error("a looping chain must end as not inside")
	}
}
