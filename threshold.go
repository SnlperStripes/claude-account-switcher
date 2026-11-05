package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// The dialog that asks for an auto-switch threshold of one's own.

//go:embed threshold.html
var thresholdPage string

// thresholdFlag starts this program as the dialog. It reads a
// thresholdRequest on stdin and writes the typed threshold as a JSON string
// on stdout, or null when cancelled.
const thresholdFlag = "-threshold-dialog"

type thresholdRequest struct {
	Threshold float64   `json:"threshold"`
	Presets   []float64 `json:"presets"`
	Headroom  float64   `json:"headroom"`
	Lang      string    `json:"lang"`
}

// chooseThreshold asks for a threshold, starting from current. It is false
// when cancelled. Without a web view the platform's plain dialog asks
// instead.
func chooseThreshold(current float64) (float64, bool) {
	req := thresholdRequest{Threshold: current, Presets: thresholds, Headroom: autoHeadroom, Lang: language}
	// Nothing counts down here, so the dialog may stay open a long while.
	out, err := askInDialogProcess(thresholdFlag, req, 12*time.Hour)
	if err == nil {
		var text *string
		if json.Unmarshal(out, &text) == nil {
			if text == nil {
				return 0, false
			}
			if v, ok := parseThreshold(*text); ok {
				return v, true
			}
		}
		err = fmt.Errorf("unexpected answer %q", out)
	}
	log.Printf("threshold dialog: %v, using the plain one", err)
	return askThresholdPlain(current)
}

// askThresholdPlain asks with the platform's plain dialog, and again after
// an answer that is not a threshold.
func askThresholdPlain(current float64) (float64, bool) {
	prompt := tr("Auto-switch when the active account reaches this much of its 5-hour or weekly limit, in percent.", "当前账号的 5 小时或每周额度用到这个百分比时自动切换。")
	answer := fmt.Sprintf("%.0f", current)
	for {
		text, ok := nativeAskNumber(tr("Auto-switch at", "自动切换阈值"), prompt, answer)
		if !ok {
			return 0, false
		}
		if v, ok := parseThreshold(text); ok {
			return v, true
		}
		prompt, answer = tr("Enter a whole number from 1 to 99, for example 85.", "请输入 1 到 99 之间的整数，例如 85。"), text
	}
}

// parseThreshold reads a typed threshold such as "85" or "85 %".
func parseThreshold(text string) (float64, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(text), "%")))
	if err != nil || n < 1 || n > 99 {
		return 0, false
	}
	return float64(n), true
}

// runThresholdDialog is the dialog process. Closing the window counts as
// Cancel.
func runThresholdDialog() int {
	var req thresholdRequest
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
		fmt.Fprintln(os.Stderr, "bad request:", err)
		return 2
	}
	useLanguage(req.Lang)
	page, err := fillPage(thresholdPage, req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	answer, err := showWebDialog(dialogWindow{Title: tr("Auto-switch at", "自动切换阈值"), Width: 400, Height: 360}, page)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if answer == nil {
		answer = json.RawMessage("null")
	}
	os.Stdout.Write(answer)
	return 0
}
