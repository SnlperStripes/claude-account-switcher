package main

import (
	"encoding/json"

	webview "github.com/webview/webview_go"
)

// showWebDialog shows page in a WebKit window and returns what the page
// passed to dialogDone, or nil if the window was closed. The web view package
// keeps this process's main goroutine on the main thread, as Cocoa needs.
func showWebDialog(win dialogWindow, page string) (json.RawMessage, error) {
	w := webview.New(false)
	defer w.Destroy()
	w.SetTitle(win.Title)
	w.SetSize(win.Width, win.Height, webview.HintFixed)
	var answer json.RawMessage
	w.Bind("dialogFit", func(height int) {
		w.SetSize(win.Width, height, webview.HintFixed)
	})
	w.Bind("dialogDone", func(a json.RawMessage) {
		answer = a
		w.Terminate()
	})
	w.SetHtml(page)
	w.Run()
	return answer, nil
}
