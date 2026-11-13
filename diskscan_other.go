//go:build !windows

package main

// Опустошение корзины на macOS/Linux: удаляем содержимое стандартных
// путей корзины (ошибки пропускаются).

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
)

// readDirEntries — переносимая версия: os.ReadDir сортирует и вызывает
// Info на каждый файл, но на unix-ФС это не является узким местом.
func readDirEntries(path string) ([]walkEntry, error) {
	des, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	out := make([]walkEntry, 0, len(des))
	for _, de := range des {
		e := walkEntry{name: de.Name(), dir: de.IsDir(), link: de.Type()&fs.ModeSymlink != 0}
		if !e.dir && !e.link {
			if info, err := de.Info(); err == nil {
				e.size = info.Size()
			}
		}
		out = append(out, e)
	}
	return out, nil
}

func emptyTrash() error {
	dirs := []string{
		homeDir(".Trash"),                   // macOS
		homeDir(".local/share/Trash/files"), // Linux
		homeDir(".local/share/Trash/info"),  // Linux
	}
	for _, d := range dirs {
		clearDirContents(d)
	}
	return nil
}

// runOptimizeHelper — «Оптимизация системы» только для Windows; на других
// ОС кнопка не показывается и помощник не нужен.
func runOptimizeHelper(logPath string, resetBase bool) {}

// hideConsole — Windows-специфика скрытия дочерних консольных окон.
func hideConsole(cmd *exec.Cmd) {}

func homeDir(sub string) string {
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return ""
	}
	return filepath.Join(h, filepath.FromSlash(sub))
}
