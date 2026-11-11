//go:build !windows

package main

// DetectLang определяет язык интерфейса по локали ОС (Linux/macOS):
// ru_* → ru, прочее → en.

import (
	"os"
	"strings"
)

func DetectLang() string {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(k); strings.HasPrefix(strings.ToLower(v), "ru") {
			return LangRu
		}
	}
	return LangEn
}
