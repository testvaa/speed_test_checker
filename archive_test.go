package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func archLine(t time.Time, down float64) string {
	return fmt.Sprintf(`{"kind":"check","ts":%q,"down":%g,"up":%g,"ping":%g}`,
		t.Format(time.RFC3339), down, down, 10.0)
}

func buildArchive(lines ...string) string {
	return `{"kind":"stt-archive","v":1}` + "\n" + strings.Join(lines, "\n") + "\n"
}

func entry(t time.Time, down float64) dashEntry {
	return dashEntry{TS: t, Down: down, Up: down, Ping: 10}
}

// Проверка/применение импорта: пересечения, overwrite/skip, битые строки.
func TestImportArchiveModes(t *testing.T) {
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	// в истории: base=40 и base+1ч=41; в архиве те же две + одна новая (50)
	arch := buildArchive(
		archLine(base, 99),
		archLine(base.Add(time.Hour), 98),
		archLine(base.Add(2*time.Hour), 50),
	)

	d := &Dashboard{history: []dashEntry{entry(base, 40), entry(base.Add(time.Hour), 41)}}

	// check: считает пересечения, ничего не меняет
	res, err := d.ImportArchive(strings.NewReader(arch), "check")
	if err != nil {
		t.Fatal(err)
	}
	if res.Overlap != 2 || res.Checks != 3 || res.Applied || len(d.history) != 2 {
		t.Fatalf("check: %+v, len=%d", res, len(d.history))
	}

	// overwrite: совпавшие заменяются, новый добавляется
	res, err = d.ImportArchive(strings.NewReader(arch), "overwrite")
	if err != nil {
		t.Fatal(err)
	}
	if res.Replaced != 2 || res.Added != 1 || len(d.history) != 3 || d.history[0].Down != 99 {
		t.Fatalf("overwrite: %+v, first=%v", res, d.history[0].Down)
	}

	// skip: совпавшие пропускаются (остались 40/41), новый добавлен
	d2 := &Dashboard{history: []dashEntry{entry(base, 40), entry(base.Add(time.Hour), 41)}}
	res, err = d2.ImportArchive(strings.NewReader(arch), "skip")
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != 2 || res.Added != 1 || d2.history[0].Down != 40 || len(d2.history) != 3 {
		t.Fatalf("skip: %+v, first=%v", res, d2.history[0].Down)
	}
}

// Битые строки и дубли внутри архива отбрасываются и считаются.
func TestImportArchiveBadAndDup(t *testing.T) {
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	arch := buildArchive(
		`{"kind":"check","ts":"не-дата","down":5}`,
		`{"kind":"check","ts":"`+base.Add(time.Hour).Format(time.RFC3339)+`","down":9999999}`,
		archLine(base, 10),
		archLine(base, 11), // дубль по ts — берётся последняя версия
		`{"kind":"unknown","x":1}`,
	)
	d := &Dashboard{}
	res, err := d.ImportArchive(strings.NewReader(arch), "overwrite")
	if err != nil {
		t.Fatal(err)
	}
	if res.Invalid != 3 || res.DupInFile != 1 || res.Checks != 1 || res.Added != 1 {
		t.Fatalf("%+v", res)
	}
	if len(d.history) != 1 || d.history[0].Down != 11 {
		t.Fatalf("history: %v", d.history)
	}
}

// Чужой файл и неподдерживаемая версия не принимаются.
func TestImportArchiveHeader(t *testing.T) {
	d := &Dashboard{}
	if _, err := d.ImportArchive(strings.NewReader(`{"kind":"other","v":1}`+"\n"), "check"); err == nil {
		t.Fatal("чужой формат принят")
	}
	if _, err := d.ImportArchive(strings.NewReader(`{"kind":"stt-archive","v":9}`+"\n"), "check"); err == nil {
		t.Fatal("неподдерживаемая версия принята")
	}
}

// Слияние трафика: skip добавляет только новые дни, overwrite заменяет.
func TestMergeTraffic(t *testing.T) {
	m := NewNetMon(filepath.Join(t.TempDir(), "traffic.json"))
	in := TrafficStore{
		Days:   map[string][2]int64{"2026-11-08": {9, 9}, "2026-11-09": {1, 2}},
		Months: map[string][2]int64{"2026-11": {10, 11}},
	}
	added, replaced := m.MergeTraffic(TrafficStore{
		Days: map[string][2]int64{"2026-11-08": {7, 7}},
	}, false)
	if added != 1 || replaced != 0 {
		t.Fatalf("skip: added=%d replaced=%d", added, replaced)
	}
	added, replaced = m.MergeTraffic(in, true)
	if added != 2 || replaced != 1 {
		t.Fatalf("overwrite: added=%d replaced=%d", added, replaced)
	}
	if m.TrafficCopy().Days["2026-11-08"][0] != 9 {
		t.Fatal("день не перезаписан")
	}
}
