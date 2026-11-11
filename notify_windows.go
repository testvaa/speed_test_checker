//go:build windows

package main

import (
	"github.com/go-toast/toast"
)

// pushToast показывает всплывающее уведомление с цветной иконкой (ok/warn/bad)
// через Windows Toast (go-toast).
func pushToast(title, message, icon string) {
	n := toast.Notification{
		AppID:    "Speed Test Tray",
		Title:    title,
		Message:  message,
		Icon:     toastIconPath(icon),
		Duration: toast.Short,
	}
	_ = n.Push()
}
