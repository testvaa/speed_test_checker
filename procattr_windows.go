//go:build windows

package main

import "syscall"

// hideConsoleSysProcAttr прячет консольное окно у дочерних процессов
// (llama-server, --list-devices) при запуске из GUI-приложения.
func hideConsoleSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
