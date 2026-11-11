//go:build windows

package main

// DetectLang определяет язык интерфейса по языку ОС: русский → ru,
// прочее → en. Вызывается один раз — при отсутствии поля lang в config.json.

import "syscall"

var procGetUserDefaultUILanguage = syscall.NewLazyDLL("kernel32.dll").NewProc("GetUserDefaultUILanguage")

func DetectLang() string {
	if r, _, _ := procGetUserDefaultUILanguage.Call(); r&0x3FF == 0x19 { // LANG_RUSSIAN
		return LangRu
	}
	return LangEn
}
