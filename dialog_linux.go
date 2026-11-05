package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// The plain dialogs on Linux are zenity (GNOME) or kdialog (KDE), run as
// child processes. There is no web view here: the rich dialogs would need
// cgo and WebKitGTK, and the whole program would fail to start without that
// library. So showWebDialog always fails and the plain dialogs ask instead.

// showWebDialog always fails on Linux, so the callers fall back to the plain
// dialogs.
func showWebDialog(dialogWindow, string) (json.RawMessage, error) {
	return nil, errors.New("no web view on Linux")
}

// linuxDialogTool names the program that shows the plain dialogs, or "" when
// none can: no display, or neither zenity nor kdialog is installed. On KDE
// kdialog comes first.
func linuxDialogTool() string {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return ""
	}
	order := []string{"zenity", "kdialog"}
	if strings.Contains(os.Getenv("XDG_CURRENT_DESKTOP"), "KDE") {
		order = []string{"kdialog", "zenity"}
	}
	for _, name := range order {
		if _, err := exec.LookPath(name); err == nil {
			return name
		}
	}
	return ""
}

// nativeAskNumber asks for a number under prompt, with value filled in, and
// returns what was typed. It is false when the dialog was cancelled or could
// not open.
func nativeAskNumber(title, prompt, value string) (string, bool) {
	tool := linuxDialogTool()
	var args []string
	switch tool {
	case "zenity":
		args = zenityEntryArgs(title, prompt, value)
	case "kdialog":
		args = kdialogInputArgs(title, prompt, value)
	default:
		return "", false
	}
	out, err := exec.Command(tool, args...).Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// nativeResume asks which of the chats titled titles should go on and
// returns one answer per title. After wait without an answer, or if the
// dialog cannot open, all of them go on. Cancel, or closing the dialog,
// stops all of them.
func nativeResume(titles []string, wait time.Duration) []bool {
	n := len(titles)
	keep := make([]bool, n)
	for i := range keep {
		keep[i] = true
	}
	tool := linuxDialogTool()
	if tool == "" {
		return keep
	}
	title, prompt := tr("Continue chats?", "继续聊天？"),
		tr("These chats were working when Claude closed for the switch. Claude continues the selected ones when it starts again.",
			"切换时 Claude 关闭，这些聊天正在进行中。Claude 再次启动后会继续选中的聊天。")
	var ctx context.Context
	var cancel context.CancelFunc
	var cmd *exec.Cmd
	if tool == "zenity" {
		// zenity counts down with --timeout and exits 5 when it runs out.
		// The extra minute only catches a dialog that hangs.
		ctx, cancel = context.WithTimeout(context.Background(), wait+time.Minute)
		cmd = exec.CommandContext(ctx, tool, zenityChecklistArgs(title, prompt, titles, wait)...)
	} else {
		// kdialog has no timeout of its own, so the context ends it.
		ctx, cancel = context.WithTimeout(context.Background(), wait)
		cmd = exec.CommandContext(ctx, tool, kdialogChecklistArgs(title, prompt, titles)...)
	}
	defer cancel()
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return keep
	}
	if err != nil {
		var exit *exec.ExitError
		// Exit 1 is Don't continue, or the dialog closed. Exit 5 from zenity
		// is the timeout, and anything else means the dialog did not work.
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return make([]bool, n)
		}
		return keep
	}
	if tool == "zenity" {
		return parseZenityIndices(string(out), n)
	}
	return parseKdialogTags(string(out), n)
}

// zenityEntryArgs builds the arguments for the number entry.
func zenityEntryArgs(title, prompt, value string) []string {
	return []string{
		"--entry",
		"--title=" + title,
		"--text=" + escapeMarkup(prompt),
		"--entry-text=" + value,
		"--ok-label=" + tr("Set", "设置"),
		"--cancel-label=" + tr("Cancel", "取消"),
	}
}

// kdialogInputArgs builds the arguments for the number entry. kdialog has no
// custom button labels here.
func kdialogInputArgs(title, prompt, value string) []string {
	return []string{"--title", title, "--inputbox", prompt, value}
}

// zenityChecklistArgs builds the arguments for the list of chats. Each row is
// TRUE, the chat's index (hidden, printed back) and its title. wait is
// passed to zenity as its timeout, in whole seconds.
func zenityChecklistArgs(title, prompt string, titles []string, wait time.Duration) []string {
	secs := max(int(wait/time.Second), 1)
	height := 200 + 30*min(len(titles), 6)
	args := []string{
		"--list", "--checklist",
		"--title=" + title,
		"--text=" + escapeMarkup(prompt),
		"--column=" + tr("Continue", "继续"),
		"--column=#",
		"--column=" + tr("Chat", "聊天"),
		"--hide-column=2",
		"--print-column=2",
		"--separator=\n",
		"--ok-label=" + tr("Continue", "继续"),
		"--cancel-label=" + tr("Don't continue", "不继续"),
		"--width=460",
		fmt.Sprintf("--height=%d", height),
		fmt.Sprintf("--timeout=%d", secs),
	}
	for i, t := range titles {
		args = append(args, "TRUE", strconv.Itoa(i), t)
	}
	return args
}

// kdialogChecklistArgs builds the arguments for the list of chats. Each item
// is its index as the tag, its title and "on".
func kdialogChecklistArgs(title, prompt string, titles []string) []string {
	args := []string{"--title", title, "--checklist", prompt}
	for i, t := range titles {
		args = append(args, strconv.Itoa(i), t, "on")
	}
	return args
}

// escapeMarkup escapes text for zenity, whose --text is Pango markup.
func escapeMarkup(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// parseZenityIndices reads the indices zenity printed, one per line. Entries
// that are not numbers or are out of range are ignored.
func parseZenityIndices(out string, n int) []bool {
	keep := make([]bool, n)
	for _, line := range strings.Split(out, "\n") {
		if i, err := strconv.Atoi(strings.TrimSpace(line)); err == nil && i >= 0 && i < n {
			keep[i] = true
		}
	}
	return keep
}

// parseKdialogTags reads the tags kdialog printed, space-separated and
// possibly double-quoted, such as `"0" "2"`. Entries that are not numbers or
// are out of range are ignored.
func parseKdialogTags(out string, n int) []bool {
	keep := make([]bool, n)
	for _, field := range strings.Fields(out) {
		if i, err := strconv.Atoi(strings.Trim(field, `"`)); err == nil && i >= 0 && i < n {
			keep[i] = true
		}
	}
	return keep
}
