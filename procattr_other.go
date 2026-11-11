//go:build !windows

package main

import "syscall"

// hideConsoleSysProcAttr — на macOS/Linux дочерний процесс не открывает
// консольного окна, настройки не нужны.
func hideConsoleSysProcAttr() *syscall.SysProcAttr { return nil }
