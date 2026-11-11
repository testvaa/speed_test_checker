//go:build linux

package main

import "os/exec"

// pushToast показывает всплывающее уведомление через notify-send
// (freedesktop; пакеты libnotify). Нет notify-send — молча пропускаем.
func pushToast(title, message, icon string) {
	_ = exec.Command("notify-send", "--expire-time=5000",
		"-i", toastIconPath(icon), title, message).Start()
}
