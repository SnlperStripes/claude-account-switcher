package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Dialogs are web pages in their own process: on macOS the tray already owns
// the main thread a web view needs, and a crash in a dialog cannot take the
// tray with it.

// dialogWindow is how a dialog's window starts out. The page sets its own
// height once it is laid out, with dialogFit.
type dialogWindow struct {
	Title string
	// In CSS pixels on Windows, points on macOS.
	Width, Height int
}

// askInDialogProcess starts this program with flag, hands it req as JSON on
// stdin and returns what it wrote on stdout. timeout only catches a dialog
// that hangs.
func askInDialogProcess(flag string, req any, timeout time.Duration) ([]byte, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	in, _ := json.Marshal(req)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, flag)
	cmd.Stdin = bytes.NewReader(in)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("no answer")
		}
		return nil, fmt.Errorf("%v %s", err, strings.TrimSpace(stderr.String()))
	}
	return out.Bytes(), nil
}

// fillPage puts data into page in place of its sample data. json.Marshal
// escapes <, > and &, so strings in data cannot end the script.
func fillPage(page string, data any) (string, error) {
	const slot = "/*DATA*/"
	start := strings.Index(page, slot)
	if start < 0 {
		return "", errors.New("page has no data slot")
	}
	// The sample data has no semicolon of its own.
	end := strings.Index(page[start:], ";")
	if end < 0 {
		return "", errors.New("page data slot is not closed")
	}
	js, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	return page[:start] + string(js) + page[start+end:], nil
}
