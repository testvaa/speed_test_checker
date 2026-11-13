package main

import "testing"

// правила раскладки — чистая функция, проверяем на всех трёх ОС
func TestCategorizeFor(t *testing.T) {
	cases := []struct {
		goos  string
		rel   []string
		isSys bool
		want  string
	}{
		// windows, системный том
		{"windows", []string{"Windows", "System32"}, true, "system"},
		{"windows", []string{"Program Files", "App"}, true, "apps"},
		{"windows", []string{"$Recycle.Bin", "SID"}, true, "trash"},
		{"windows", []string{"Users", "u", "Documents", "work"}, true, "docs"},
		{"windows", []string{"Users", "u", "Downloads"}, true, "downloads"},
		{"windows", []string{"Users", "u", "Pictures"}, true, "photos"},
		{"windows", []string{"Users", "u", "Videos", "clips"}, true, "videos"},
		{"windows", []string{"Users", "u", "AppData", "Local", "Temp", "x"}, true, "caches"},
		{"windows", []string{"Users", "u", "AppData", "Roaming", "App"}, true, "apps"},
		{"windows", []string{"Users", "u", "AppData", "Local", "Google", "Chrome", "User Data", "Cache"}, true, "caches"},
		{"windows", []string{"SomeGame"}, true, "other"},
		{"windows", []string{"pagefile.sys"}, true, "system"},
		// windows, несистемный том (D:, E:) — раскладка по типовым папкам
		{"windows", []string{"Games", "Stalker2"}, false, "apps"},
		{"windows", []string{"Игры"}, false, "apps"},
		{"windows", []string{"Фильмы"}, false, "videos"},
		{"windows", []string{"Музыка"}, false, "music"},
		{"windows", []string{"Downloads"}, false, "downloads"},
		{"windows", []string{"$Recycle.Bin", "SID"}, false, "trash"},
		{"windows", []string{"System Volume Information"}, false, "system"},
		{"windows", []string{"Backup2026"}, false, "other"},
		// darwin
		{"darwin", []string{"Applications", "App.app"}, true, "apps"},
		{"darwin", []string{"Library", "Logs"}, true, "system"},
		{"darwin", []string{"Library", "Caches", "com.app"}, true, "caches"},
		{"darwin", []string{"Users", "u", "Movies"}, true, "videos"},
		{"darwin", []string{"Users", "u", ".Trash"}, true, "trash"},
		{"darwin", []string{"Users", "u", "Library", "Application Support", "X"}, true, "apps"},
		{"darwin", []string{"private", "tmp", "t"}, true, "caches"},
		{"darwin", []string{"private", "var", "log"}, true, "system"},
		{"darwin", []string{"private", "var", "folders", "zz"}, true, "caches"},
		// linux
		{"linux", []string{"usr", "lib"}, true, "system"},
		{"linux", []string{"opt", "app"}, true, "apps"},
		{"linux", []string{"var", "cache", "x"}, true, "caches"},
		{"linux", []string{"var", "lib", "x"}, true, "system"},
		{"linux", []string{"home", "u", ".cache", "x"}, true, "caches"},
		{"linux", []string{"home", "u", ".local", "share", "Trash", "files", "f"}, true, "trash"},
		{"linux", []string{"home", "u", "Documents"}, true, "docs"},
		{"linux", []string{"root", "Downloads"}, true, "downloads"},
		{"linux", []string{"srv"}, true, "other"},
	}
	for _, c := range cases {
		if got := categorizeFor(c.goos, c.rel, c.isSys); got != c.want {
			t.Errorf("categorizeFor(%s, %v, %v) = %q, хочу %q", c.goos, c.rel, c.isSys, got, c.want)
		}
	}
}

// категории фиксированы — полотно и легенда должны совпадать по порядку
func TestDiskCatKeysStable(t *testing.T) {
	if len(diskCatKeys) != 10 {
		t.Fatalf("категорий %d, хочу 10", len(diskCatKeys))
	}
	seen := map[string]bool{}
	for _, k := range diskCatKeys {
		if seen[k] {
			t.Fatalf("дубль категории %q", k)
		}
		seen[k] = true
	}
	if catIndex("other") != len(diskCatKeys)-1 || catIndex("apps") != 0 {
		t.Fatalf("catIndex сломан")
	}
}

// чистая защита корня тома: очистка корня недопустима. Проверяется на
// любой ОС через isVolumeRootFor — filepath.VolumeName на unix пуст.
func TestIsVolumeRoot(t *testing.T) {
	if !isVolumeRootFor("windows", `C:\`) || !isVolumeRootFor("windows", `c:\`) {
		t.Fatalf("windows: C:\\ не распознан как корень")
	}
	if isVolumeRootFor("windows", `C:\Users`) {
		t.Fatalf("windows: C:\\Users не корень")
	}
	if isVolumeRootFor("windows", `D:\data\`) {
		t.Fatalf("windows: D:\\data\\ не корень")
	}
	if !isVolumeRootFor("linux", "/") || !isVolumeRootFor("darwin", "/") {
		t.Fatalf("unix: / не распознан как корень")
	}
	if isVolumeRootFor("linux", "/home") {
		t.Fatalf("unix: /home не корень")
	}
}
