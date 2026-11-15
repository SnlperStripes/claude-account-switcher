package main

// insideDesktop walks up from pid through its parents and reports whether
// one of them is the Claude app. The walk stops at the first process with
// no parent and after a fixed number of steps, so a broken parent chain
// cannot loop.
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
