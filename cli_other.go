//go:build !darwin

package main

// Windows and Linux: Claude Code keeps its sign-in in .credentials.json.
const cliSupported = true
