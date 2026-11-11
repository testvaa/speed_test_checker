//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// attachConsole подключает GUI-приложение к консоли родительского процесса,
// чтобы вывод -once был виден в cmd и PowerShell. В mintty/Git Bash stdout
// наследуется и без этого.
func attachConsole() {
	const attachParentProcess = ^uintptr(0) // ATTACH_PARENT_PROCESS = (DWORD)-1
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	r, _, _ := kernel32.NewProc("AttachConsole").Call(attachParentProcess)
	if r == 0 {
		return
	}
	out, err := windows.CreateFile(
		windows.StringToUTF16Ptr("CONOUT$"),
		windows.GENERIC_WRITE, windows.FILE_SHARE_WRITE, nil,
		windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return
	}
	_ = windows.SetStdHandle(windows.STD_OUTPUT_HANDLE, out)
	_ = windows.SetStdHandle(windows.STD_ERROR_HANDLE, out)
	os.Stdout = os.NewFile(uintptr(out), "|stdout")
	os.Stderr = os.NewFile(uintptr(out), "|stderr")
}
