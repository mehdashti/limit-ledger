// Package notify shows a desktop notification, quietly doing nothing where no
// notifier is available.
package notify

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"time"
)

// Send shows title and body as a desktop notification. It reports whether a notifier ran.
func Send(title, body string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux", "freebsd", "openbsd", "netbsd":
		if _, err := exec.LookPath("notify-send"); err != nil {
			return false
		}
		cmd = exec.CommandContext(ctx, "notify-send", "--app-name=limit-ledger", title, body)
	case "darwin":
		script := "display notification " + strconv.Quote(body) + " with title " + strconv.Quote(title)
		cmd = exec.CommandContext(ctx, "osascript", "-e", script)
	default:
		return false
	}
	return cmd.Run() == nil
}
