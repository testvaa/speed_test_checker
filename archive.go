package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Архив данных: история замеров + трафик в одном .jsonl-файле, по строке
// на замер — импорт идёт построчно с прогрессом и пересчитывает пересечения.
//
//	{"kind":"stt-archive","v":1,"created":"…","checks":N}
//	{"kind":"check","ts":"…","down":…, …}   ← dashEntry как есть
//	{"kind":"traffic","days":{…},"months":{…}}
const (
	archKind     = "stt-archive"
	archVersion  = 1
	archMaxBytes = 64 << 20 // предел размера архива (граница доверия)
)

// archStatus — состояние экспорта/импорта для оверлея и ИИ-агента.
type archStatus struct {
	Status string  `json:"status"`          // idle | busy | error
	Op     string  `json:"op,omitempty"`    // export | import
	Phase  string  `json:"phase,omitempty"` // что происходит сейчас
	Prog   float64 `json:"prog,omitempty"`  // 0..1 прогресс построчного импорта
	Lines  int     `json:"lines,omitempty"` // обработано строк
	Total  int     `json:"total,omitempty"` // всего строк данных
	Result string  `json:"result,omitempty"`
}

// importResult — отчёт проверки/загрузки архива.
type importResult struct {
	OK              bool   `json:"ok"`
	Applied         bool   `json:"applied"`      // false = режим check (только проверка)
	Lines           int    `json:"lines"`        // строк данных в архиве
	Checks          int    `json:"checks"`       // валидных замеров
	Invalid         int    `json:"invalid"`      // битых строк пропущено
	DupInFile       int    `json:"dupInFile"`    // дублей внутри самого архива
	Overlap         int    `json:"overlapCount"` // замеров, совпавших по времени с историей
	Added           int    `json:"added"`
	Replaced        int    `json:"replaced"`
	Skipped         int    `json:"skipped"`
	From            string `json:"from,omitempty"` // период архива
	To              string `json:"to,omitempty"`
	HasTraffic      bool   `json:"hasTraffic"`
	TrafficAdded    int    `json:"trafficAdded"`
	TrafficReplaced int    `json:"trafficReplaced"`
	Message         string `json:"message"`
}

// archCheckLine — строка замера в архиве (dashEntry без изменений).
type archCheckLine struct {
	Kind string `json:"kind"`
	dashEntry
}

// archTrafficLine — строка трафика в архиве.
type archTrafficLine struct {
	Kind   string              `json:"kind"`
	Days   map[string][2]int64 `json:"days"`
	Months map[string][2]int64 `json:"months"`
}

// archHeader — шапка архива (первая строка).
type archHeader struct {
	Kind    string `json:"kind"`
	V       int    `json:"v"`
	Created string `json:"created,omitempty"`
	Checks  int    `json:"checks,omitempty"`
}

// SetNetMon подключает монитор трафика (для строки traffic архива).
func (d *Dashboard) SetNetMon(n *NetMon) {
	d.mu.Lock()
	d.net = n
	d.mu.Unlock()
}

// SetWatchMon подключает реестр наблюдений за хостами (для /api/state).
func (d *Dashboard) SetWatchMon(w *WatchMon) {
	d.mu.Lock()
	d.watch = w
	d.mu.Unlock()
}

// ArchiveStatus — снимок состояния операции с архивом.
func (d *Dashboard) ArchiveStatus() archStatus {
	d.archMu.Lock()
	defer d.archMu.Unlock()
	return d.archSt
}

func (d *Dashboard) setArchBusy(op, phase string) {
	d.archMu.Lock()
	d.archSt = archStatus{Status: "busy", Op: op, Phase: phase}
	d.archMu.Unlock()
}

func (d *Dashboard) setArchProg(phase string, frac float64, lines, total int) {
	d.archMu.Lock()
	d.archSt.Phase, d.archSt.Prog = phase, frac
	d.archSt.Lines, d.archSt.Total = lines, total
	d.archMu.Unlock()
}

func (d *Dashboard) setArchDone(result string) {
	d.archMu.Lock()
	d.archSt = archStatus{Status: "idle", Result: result}
	d.archMu.Unlock()
}

func (d *Dashboard) setArchErr(err string) {
	d.archMu.Lock()
	d.archSt = archStatus{Status: "error", Result: err}
	d.archMu.Unlock()
}

// archiveBytes собирает JSONL-архив: шапка, по строке на замер, трафик.
// Возвращает данные, число замеров и число дней трафика.
func (d *Dashboard) archiveBytes() ([]byte, int, int, error) {
	d.mu.Lock()
	hist := make([]dashEntry, len(d.history))
	copy(hist, d.history)
	d.mu.Unlock()
	var traf TrafficStore
	tdays := 0
	if d.net != nil {
		traf = d.net.TrafficCopy()
		tdays = len(traf.Days)
	}
	var b strings.Builder
	head, _ := json.Marshal(archHeader{Kind: archKind, V: archVersion,
		Created: time.Now().Format(time.RFC3339), Checks: len(hist)})
	b.Write(head)
	b.WriteByte('\n')
	for _, e := range hist {
		line, err := json.Marshal(archCheckLine{Kind: "check", dashEntry: e})
		if err != nil {
			continue
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	tl, err := json.Marshal(archTrafficLine{Kind: "traffic",
		Days: traf.Days, Months: traf.Months})
	if err != nil {
		return nil, 0, 0, err
	}
	b.Write(tl)
	b.WriteByte('\n')
	return []byte(b.String()), len(hist), tdays, nil
}

// ExportArchiveFile сохраняет архив рядом с config.json (для ИИ-агента)
// и возвращает готовое человекочитаемое сообщение.
func (d *Dashboard) ExportArchiveFile(path string) (string, error) {
	d.setArchBusy("export", T("arch_collect"))
	data, n, tdays, err := d.archiveBytes()
	if err != nil {
		d.setArchErr(T("arch_export_err", err.Error()))
		return "", err
	}
	d.setArchProg(T("arch_write_file"), 0.5, 0, 0)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		d.setArchErr(T("arch_export_err", err.Error()))
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		d.setArchErr(T("arch_export_err", err.Error()))
		return "", err
	}
	msg := fmt.Sprintf(T("arch_saved"), filepath.Base(path), n, tdays)
	d.setArchDone(msg)
	return msg, nil
}

// ImportArchive разбирает архив построчно. mode: "check" — только проверить
// и посчитать пересечения; "overwrite" — перезаписать совпавшие замеры;
// "skip" — пропустить совпавшие, добавить только новые.
func (d *Dashboard) ImportArchive(rd io.Reader, mode string) (importResult, error) {
	var res importResult
	data, err := io.ReadAll(io.LimitReader(rd, archMaxBytes+1))
	if err != nil {
		return res, err
	}
	if len(data) > archMaxBytes {
		return res, fmt.Errorf(T("arch_too_big"), archMaxBytes>>20)
	}

	// шапка: доверие формату проверяем до всего остального
	raw := strings.Split(string(data), "\n")
	var head archHeader
	for i, ln := range raw {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		if err := json.Unmarshal([]byte(ln), &head); err != nil || head.Kind != archKind {
			return res, fmt.Errorf(T("arch_not_ours"), i+1)
		}
		raw = raw[i+1:]
		break
	}
	if head.V != archVersion {
		return res, fmt.Errorf(T("arch_bad_ver"), head.V)
	}

	if mode == "overwrite" || mode == "skip" {
		d.setArchBusy("import", T("arch_read_lines"))
	}

	// проход 1: построчная проверка и сбор валидных данных
	var incoming []dashEntry
	var traf *archTrafficLine
	seen := map[int64]bool{}
	res.Lines = len(raw)
	var from, to time.Time
	for i, ln := range raw {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			res.Lines--
			continue
		}
		if mode == "overwrite" || mode == "skip" {
			d.setArchProg(T("arch_read_lines"), float64(i)/float64(res.Lines), i, res.Lines)
		}
		var probe struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal([]byte(ln), &probe) != nil {
			res.Invalid++
			continue
		}
		switch probe.Kind {
		case "check":
			var c archCheckLine
			if json.Unmarshal([]byte(ln), &c) != nil || !validEntry(&c.dashEntry) {
				res.Invalid++
				continue
			}
			if seen[c.TS.Unix()] { // дубль внутри архива — берём последнюю версию
				res.DupInFile++
				for j := range incoming {
					if incoming[j].TS.Unix() == c.TS.Unix() {
						incoming[j] = c.dashEntry
					}
				}
				continue
			}
			seen[c.TS.Unix()] = true
			incoming = append(incoming, c.dashEntry)
			if from.IsZero() || c.TS.Before(from) {
				from = c.TS
			}
			if c.TS.After(to) {
				to = c.TS
			}
		case "traffic":
			var t archTrafficLine
			if json.Unmarshal([]byte(ln), &t) == nil && (len(t.Days) > 0 || len(t.Months) > 0) {
				traf = &t
			}
		default:
			res.Invalid++
		}
	}
	res.Checks = len(incoming)
	if res.Checks == 0 && traf == nil {
		return res, fmt.Errorf(T("arch_empty"), res.Invalid)
	}
	res.From, res.To = from.Format("02.01 15:04"), to.Format("02.01 15:04")
	if traf != nil {
		res.HasTraffic = true
	}

	// пересечения с существующей историей (по времени замера)
	d.mu.Lock()
	exist := map[int64]bool{}
	for i := range d.history {
		exist[d.history[i].TS.Unix()] = true
	}
	d.mu.Unlock()
	for _, e := range incoming {
		if exist[e.TS.Unix()] {
			res.Overlap++
		}
	}

	if mode == "check" {
		res.OK = true
		res.Message = fmt.Sprintf(T("arch_check_msg"),
			res.Checks, res.From, res.To, res.Overlap)
		return res, nil
	}

	// проход 2: применяем построчно
	overwrite := mode == "overwrite"
	d.mu.Lock()
	for i, e := range incoming {
		d.setArchProg(T("arch_write_lines"), float64(i)/float64(len(incoming)), i, len(incoming))
		idx := -1
		for j := range d.history {
			if d.history[j].TS.Unix() == e.TS.Unix() {
				idx = j
				break
			}
		}
		switch {
		case idx >= 0 && overwrite:
			d.history[idx] = e
			res.Replaced++
		case idx >= 0:
			res.Skipped++
		default:
			d.history = append(d.history, e)
			res.Added++
		}
	}
	sort.Slice(d.history, func(i, j int) bool { return d.history[i].TS.Before(d.history[j].TS) })
	if n := len(d.history); n > dashHistCap {
		d.history = d.history[n-dashHistCap:]
	}
	d.mu.Unlock()
	d.saveHistory()

	// трафик: overwrite заменяет совпадающие дни/месяцы, skip добавляет новые
	if traf != nil && d.net != nil {
		res.TrafficAdded, res.TrafficReplaced =
			d.net.MergeTraffic(TrafficStore{Days: traf.Days, Months: traf.Months}, overwrite)
		d.net.Save()
	}
	d.setArchDone(fmt.Sprintf(T("arch_loaded"),
		res.Added, res.Replaced, res.Skipped, res.Invalid))

	res.OK = true
	res.Applied = true
	res.Message = fmt.Sprintf(T("arch_loaded_full"),
		res.Added, res.Replaced, res.Skipped, res.Invalid, res.TrafficAdded, res.TrafficReplaced)
	return res, nil
}

// ImportArchiveFile — импорт из файла по пути (для ИИ-агента); возвращает
// человекочитаемый итог.
func (d *Dashboard) ImportArchiveFile(path, mode string) (string, error) {
	switch mode {
	case "check", "overwrite", "skip":
	default:
		return "", fmt.Errorf(T("arch_bad_mode"), mode)
	}
	st, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf(T("arch_no_file"), path)
	}
	if st.Size() > archMaxBytes {
		return "", fmt.Errorf(T("arch_file_big"), archMaxBytes>>20)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	res, err := d.ImportArchive(f, mode)
	if err != nil {
		return "", err
	}
	return res.Message, nil
}

// validEntry — проверка замера на границе доверия: без нулевых дат,
// будущего времени и физически невозможных чисел.
func validEntry(e *dashEntry) bool {
	if e.TS.IsZero() || e.TS.After(time.Now().Add(24*time.Hour)) {
		return false
	}
	return e.Down >= 0 && e.Down < 1e6 && e.Up >= 0 && e.Up < 1e6 &&
		e.Ping >= 0 && e.Ping < 3.6e6 && e.Jitter >= 0 && e.Jitter < 3.6e6 &&
		e.Loss >= 0 && e.Loss <= 100 && e.DurMs >= 0
}

// ── HTTP ────────────────────────────────────────────────────────────

// serveArchiveExport отдаёт архив файлом в загрузки браузера.
func (d *Dashboard) serveArchiveExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, T("err_get_only"), http.StatusMethodNotAllowed)
		return
	}
	d.setArchBusy("export", T("arch_collect"))
	data, n, tdays, err := d.archiveBytes()
	if err != nil {
		d.setArchErr(T("arch_export_err", err.Error()))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	name := "speedtest-archive-" + time.Now().Format("2006-01-02") + ".jsonl"
	w.Header().Set("Content-Type", "application/jsonl; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	d.setArchDone(T("arch_done", n, tdays))
	_, _ = w.Write(data)
}

// serveArchiveImport — проверка (mode=check) или загрузка архива.
func (d *Dashboard) serveArchiveImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, T("err_post_only"), http.StatusMethodNotAllowed)
		return
	}
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = "check"
	}
	switch mode {
	case "check", "overwrite", "skip":
	default:
		http.Error(w, "mode: check|overwrite|skip", http.StatusBadRequest)
		return
	}
	res, err := d.ImportArchive(http.MaxBytesReader(w, r.Body, archMaxBytes), mode)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(res)
}

// serveArchiveStatus — прогресс текущей операции (опрос оверлеем).
func (d *Dashboard) serveArchiveStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(d.ArchiveStatus())
}
