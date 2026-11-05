package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"
)

// The dialog that asks which stopped chats go on.

//go:embed resume.html
var resumePage string

// resumeFlag starts this program as the dialog. It reads a resumeRequest on
// stdin and writes the answers as a JSON array of booleans on stdout.
const resumeFlag = "-resume-dialog"

type resumeRequest struct {
	Account string       `json:"account"`
	Wait    int          `json:"wait"`
	Chats   []resumeChat `json:"chats"`
	Lang    string       `json:"lang"`
}

type resumeChat struct {
	Title  string `json:"title"`
	Folder string `json:"folder"`
}

// chooseResume asks which of the stopped chats should go on and returns one
// answer per chat. After wait without an answer all of them go on. Without
// a web view the platform's plain dialog asks instead.
func chooseResume(account string, chats []cutChat, wait time.Duration) []bool {
	req := resumeRequest{Account: account, Wait: int(wait / time.Second), Lang: language}
	titles := make([]string, len(chats))
	for i, c := range chats {
		req.Chats = append(req.Chats, resumeChat{Title: c.title, Folder: c.folder})
		titles[i] = c.title
	}
	// The page counts down by itself.
	out, err := askInDialogProcess(resumeFlag, req, wait+time.Minute)
	if err == nil {
		var keep []bool
		if json.Unmarshal(out, &keep) == nil && len(keep) == len(req.Chats) {
			return keep
		}
		err = fmt.Errorf("unexpected answer %q", out)
	}
	log.Printf("resume dialog: %v, using the plain one", err)
	return nativeResume(titles, wait)
}

// runResumeDialog is the dialog process. Closing the window counts as
// Don't continue.
func runResumeDialog() int {
	var req resumeRequest
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil || len(req.Chats) == 0 {
		fmt.Fprintln(os.Stderr, "bad request:", err)
		return 2
	}
	useLanguage(req.Lang)
	page, err := fillPage(resumePage, req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	answer, err := showWebDialog(dialogWindow{Title: tr("Continue chats?", "继续聊天？"), Width: 460, Height: 232 + min(len(req.Chats), 5)*64}, page)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var keep []bool
	if answer != nil {
		_ = json.Unmarshal(answer, &keep)
	}
	if keep == nil {
		keep = make([]bool, len(req.Chats))
	}
	out, _ := json.Marshal(keep)
	os.Stdout.Write(out)
	return 0
}
