//go:build darwin

package main

import "os/exec"

// pushToast показывает всплывающее уведомление через osascript
// (Notification Center); иконку показать нельзя — отправляем без неё.
func pushToast(title, message, icon string) {
	script := `display notification "` + message + `" with title "` + title + `"`
	_ = exec.Command("osascript", "-e", script).Start()
}
