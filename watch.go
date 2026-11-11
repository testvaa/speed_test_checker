package main

// Наблюдение за хостами: фоновые ICMP-пинги по списку, заданному через
// ИИ-агента («следи за router.local каждые 5 минут»). Исполнитель —
// сам приложение (LLM «живёт» только во время запроса): агент лишь
// заводит/останавливает задания, результаты видны в блоке «Наблюдение».
// Список переживает перезапуск (watch.json рядом с config.json).

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// WatchJob — одно наблюдение: раз в IntervalMin пингуем Host и копим итог.
type WatchJob struct {
	Host        string    `json:"host"`
	IntervalMin int       `json:"intervalMin"`
	Since       time.Time `json:"since"`
	Total       int       `json:"total"`
	Fails       int       `json:"fails"`
	FailStreak  int       `json:"failStreak"`
	LastMs      int       `json:"lastMs"` // -1 — ответа нет
	LastAt      time.Time `json:"lastAt"`
}

// WatchMon — реестр наблюдений + фоновый цикл.
type WatchMon struct {
	mu   sync.Mutex
	jobs map[string]*WatchJob
	path string
	stop chan struct{}
}

// watchPath кладёт watch.json в тот же каталог, что и config.json.
func watchPath(cfgPath string) string {
	return filepath.Join(filepath.Dir(cfgPath), "watch.json")
}

func NewWatchMon(path string) *WatchMon {
	w := &WatchMon{jobs: map[string]*WatchJob{}, path: path, stop: make(chan struct{})}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &w.jobs) // повреждённый файл — стартуем с пустого
	}
	return w
}

// Start запускает фоновый цикл (раз в секунду проверяет, кому пора пинговать).
func (w *WatchMon) Start() {
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-w.stop:
				return
			case <-t.C:
				w.tick()
			}
		}
	}()
}

// Stop останавливает фоновый цикл.
func (w *WatchMon) Stop() { close(w.stop) }

func (w *WatchMon) tick() {
	w.mu.Lock()
	var due []*WatchJob
	now := time.Now()
	for _, j := range w.jobs {
		if j.LastAt.IsZero() || now.Sub(j.LastAt) >= time.Duration(j.IntervalMin)*time.Minute {
			due = append(due, j)
		}
	}
	w.mu.Unlock()
	for _, j := range due {
		ms, err := pingOnce(j.Host, 3*time.Second)
		w.mu.Lock()
		j.Total++
		j.LastAt = time.Now()
		if err != nil {
			j.Fails++
			j.FailStreak++
			j.LastMs = -1
		} else {
			j.FailStreak = 0
			j.LastMs = ms
		}
		w.mu.Unlock()
	}
}

// validHost — граница доверия для хостов из чата ИИ: имя/IP без флагов
// (иначе «хост» вроде -f уйдёт опцией в системный ping).
func validHost(s string) bool {
	if s == "" || len(s) > 253 || strings.HasPrefix(s, "-") {
		return false
	}
	for _, c := range s {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '.' || c == '-' || c == ':' || c == '_' // ':' — IPv6-запись
		if !ok {
			return false
		}
	}
	return true
}

// PingHost — разовая диагностика: count пингов и человекочитаемый итог
// (для инструмента ping_host).
func (w *WatchMon) PingHost(host string, count int) (string, error) {
	if !validHost(host) {
		return "", fmt.Errorf("некорректный хост %q", host)
	}
	if count <= 0 || count > 10 {
		count = 4
	}
	ok, fail, sum, min, max := 0, 0, 0, 1<<30, 0
	var lastErr string
	for i := 0; i < count; i++ {
		ms, err := pingOnce(host, 3*time.Second)
		if err != nil {
			fail++
			lastErr = err.Error()
			continue
		}
		ok++
		sum += ms
		if ms < min {
			min = ms
		}
		if ms > max {
			max = ms
		}
	}
	if ok == 0 {
		return fmt.Sprintf("%s: нет ответа (%d из %d попыток)%s",
			host, fail, count, suffixErr(lastErr)), nil
	}
	out := fmt.Sprintf("%s: отклик %d/%d, среднее %d мс (мин %d, макс %d)",
		host, ok, count, sum/ok, min, max)
	if fail > 0 {
		out += fmt.Sprintf(", потеряно %d", fail)
	}
	return out, nil
}

func suffixErr(s string) string {
	if s == "" {
		return ""
	}
	return " — " + s
}

// Watch — завести или изменить наблюдение (инструмент watch_host).
func (w *WatchMon) Watch(host string, minutes int) (string, error) {
	if !validHost(host) {
		return "", fmt.Errorf("некорректный хост %q", host)
	}
	if minutes <= 0 || minutes > 1440 {
		minutes = 5
	}
	w.mu.Lock()
	j := w.jobs[host]
	if j == nil {
		j = &WatchJob{Host: host, Since: time.Now()}
		w.jobs[host] = j
	}
	j.IntervalMin = minutes
	w.mu.Unlock()
	w.Save()
	return fmt.Sprintf("Наблюдаю за %s каждые %d мин.", host, minutes), nil
}

// Unwatch — остановить наблюдение (инструмент unwatch_host).
func (w *WatchMon) Unwatch(host string) (string, error) {
	w.mu.Lock()
	_, ok := w.jobs[host]
	delete(w.jobs, host)
	w.mu.Unlock()
	w.Save()
	if !ok {
		return "", fmt.Errorf("хост %s не наблюдается", host)
	}
	return fmt.Sprintf("Наблюдение за %s остановлено.", host), nil
}

// UnwatchAll — остановить все наблюдения (инструмент unwatch_all).
func (w *WatchMon) UnwatchAll() (string, error) {
	w.mu.Lock()
	n := len(w.jobs)
	w.jobs = map[string]*WatchJob{}
	w.mu.Unlock()
	w.Save()
	if n == 0 {
		return "Активных наблюдений не было.", nil
	}
	return fmt.Sprintf("Остановлены все наблюдения (%d).", n), nil
}

// List — снимок всех наблюдений (для /api/state).
func (w *WatchMon) List() []WatchJob {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]WatchJob, 0, len(w.jobs))
	for _, j := range w.jobs {
		out = append(out, *j)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Host < out[k].Host })
	return out
}

// Save пишет список атомарно (tmp + rename), как traffic.json.
func (w *WatchMon) Save() {
	w.mu.Lock()
	data, err := json.MarshalIndent(w.jobs, "", "  ")
	w.mu.Unlock()
	if err != nil {
		return
	}
	tmp := w.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, w.path)
}
