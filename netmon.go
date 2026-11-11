package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/net"
)

// TrafficStore — накопленные объёмы трафика (в байтах) по дням и месяцам.
type TrafficStore struct {
	Days   map[string][2]int64 `json:"days"`   // "2026-11-08" -> {↓, ↑}
	Months map[string][2]int64 `json:"months"` // "2026-11" -> {↓, ↑}
}

// NetSnapshot — текущее состояние монитора для интерфейса.
type NetSnapshot struct {
	DownMbps float64 `json:"downMbps"`
	UpMbps   float64 `json:"upMbps"`
	DayDown  int64   `json:"dayDown"`
	DayUp    int64   `json:"dayUp"`
	MonDown  int64   `json:"monDown"`
	MonUp    int64   `json:"monUp"`
}

// NetMon — постоянный монитор сетевой активности: опрашивает счётчики
// интерфейсов Windows, считает текущие скорости и накапливает объёмы
// за сегодня и за месяц. Персистентность — traffic.json рядом с config.json.
type NetMon struct {
	mu    sync.Mutex
	store TrafficStore
	path  string

	prev     map[string][2]uint64 // счётчики интерфейсов на прошлом опросе
	lastPoll time.Time
	lastSave time.Time
	win      []sample // дельты последних ~6 сек для расчёта скорости

	downMbps float64
	upMbps   float64
}

// sample — дельта трафика за один опрос.
type sample struct {
	t        time.Time
	down, up int64
}

// speedWindow — окно усреднения скорости: значение затухает в ноль за это
// время простоя и не мигает на редких пакетах.
const speedWindow = 6 * time.Second

func NewNetMon(path string) *NetMon {
	m := &NetMon{path: path, prev: map[string][2]uint64{}}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &m.store); err != nil {
			// повреждённый файл — начинаем с пустых корзин
			m.store = TrafficStore{}
		}
	}
	if m.store.Days == nil {
		m.store.Days = map[string][2]int64{}
	}
	if m.store.Months == nil {
		m.store.Months = map[string][2]int64{}
	}
	m.poll(true) // базовая линия счётчиков, без дельт
	return m
}

// Poll снимает счётчики, обновляет скорости и корзины. Вызывать раз в 1–2 с.
func (m *NetMon) Poll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.poll(false)
}

func (m *NetMon) poll(baseline bool) {
	stats, netErr := net.IOCounters(true)
	if netErr != nil {
		m.debugf("poll: IOCounters error: %v", netErr)
		return
	}
	now := time.Now()
	counters := map[string][2]uint64{}
	for _, s := range stats {
		// локальная петля не считается интернет-трафиком:
		// Windows — «Loopback Pseudo-Interface 1», Linux — «lo», macOS — «lo0»
		if strings.Contains(s.Name, "Loopback") || s.Name == "lo" || s.Name == "lo0" {
			continue
		}
		counters[s.Name] = [2]uint64{s.BytesRecv, s.BytesSent}
	}
	if baseline || len(m.prev) == 0 || m.lastPoll.IsZero() {
		m.prev, m.lastPoll = counters, now
		m.debugf("baseline: %d интерфейсов", len(counters))
		return
	}

	var dDown, dUp int64
	for name, c := range counters {
		p, ok := m.prev[name]
		if !ok {
			continue // интерфейс появился — учтём со следующего опроса
		}
		if c[0] >= p[0] { // сброс счётчика (перезагрузка и т.п.) пропускаем
			dDown += int64(c[0] - p[0])
		}
		if c[1] >= p[1] {
			dUp += int64(c[1] - p[1])
		}
	}
	m.prev, m.lastPoll = counters, now

	// Скорость — средняя по окну speedWindow: обновляется на каждом опросе
	// (в простое затухает в ноль), поэтому не залипает и не мигает.
	if dDown > 0 || dUp > 0 {
		m.win = append(m.win, sample{t: now, down: dDown, up: dUp})
	}
	cutoff := now.Add(-speedWindow)
	i := 0
	for ; i < len(m.win) && m.win[i].t.Before(cutoff); i++ {
	}
	if i > 0 {
		m.win = m.win[i:]
	}
	if len(m.win) == 0 {
		m.downMbps, m.upMbps = 0, 0
	} else {
		win := now.Sub(m.win[0].t).Seconds()
		if win > 0 {
			var sumDown, sumUp int64
			for _, s := range m.win {
				sumDown += s.down
				sumUp += s.up
			}
			m.downMbps = float64(sumDown) * 8 / win / 1e6
			m.upMbps = float64(sumUp) * 8 / win / 1e6
		}
	}
	m.debugf("poll: dDown=%d dUp=%d speed=%.1f/%.1f ifaces=%d", dDown, dUp, m.downMbps, m.upMbps, len(counters))

	if dDown == 0 && dUp == 0 {
		return
	}

	day := [2]int64(m.store.Days[now.Format("2006-01-02")])
	day[0] += dDown
	day[1] += dUp
	m.store.Days[now.Format("2006-01-02")] = day
	month := [2]int64(m.store.Months[now.Format("2006-01")])
	month[0] += dDown
	month[1] += dUp
	m.store.Months[now.Format("2006-01")] = month

	if now.Sub(m.lastSave) >= time.Minute {
		m.saveLocked(now)
	}
}

// debugf дописывает строку в netmon_debug.log рядом с traffic.json,
// но только если задана переменная окружения SPEEDTEST_DEBUG=1.
func (m *NetMon) debugf(format string, a ...any) {
	if os.Getenv("SPEEDTEST_DEBUG") != "1" {
		return
	}
	f, err := os.OpenFile(filepath.Join(filepath.Dir(m.path), "netmon_debug.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, time.Now().Format("15:04:05.000")+" "+format+"\n", a...)
}

// TrafficCopy возвращает копию накопленных объёмов (для агента).
func (m *NetMon) TrafficCopy() TrafficStore {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := TrafficStore{Days: map[string][2]int64{}, Months: map[string][2]int64{}}
	for k, v := range m.store.Days {
		c.Days[k] = v
	}
	for k, v := range m.store.Months {
		c.Months[k] = v
	}
	return c
}

// MergeTraffic вливает трафик из архива: overwrite заменяет совпадающие
// дни/месяцы, иначе совпавшие пропускаются (только новые добавляются).
// Возвращает число добавленных и заменённых ключей.
func (m *NetMon) MergeTraffic(in TrafficStore, overwrite bool) (added, replaced int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, v := range in.Days {
		if _, ok := m.store.Days[k]; ok && !overwrite {
			continue
		}
		if _, ok := m.store.Days[k]; ok {
			replaced++
		} else {
			added++
		}
		m.store.Days[k] = v
	}
	for k, v := range in.Months {
		if _, ok := m.store.Months[k]; ok && !overwrite {
			continue
		}
		if _, ok := m.store.Months[k]; ok {
			replaced++
		} else {
			added++
		}
		m.store.Months[k] = v
	}
	return added, replaced
}

// Snapshot возвращает текущие скорости и накопленные объёмы.
func (m *NetMon) Snapshot() NetSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	day := m.store.Days[now.Format("2006-01-02")]
	mon := m.store.Months[now.Format("2006-01")]
	return NetSnapshot{
		DownMbps: m.downMbps,
		UpMbps:   m.upMbps,
		DayDown:  day[0], DayUp: day[1],
		MonDown: mon[0], MonUp: mon[1],
	}
}

// Save немедленно записывает накопленное на диск.
func (m *NetMon) Save() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saveLocked(time.Now())
}

// saveLocked чистит старые корзины и пишет файл атомарно (tmp + rename).
func (m *NetMon) saveLocked(now time.Time) {
	m.lastSave = now
	pruneStore(m.store.Days, 95)   // ~3 месяца дней
	pruneStore(m.store.Months, 25) // ~2 года месяцев
	data, err := json.MarshalIndent(m.store, "", "  ")
	if err != nil {
		return
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, m.path)
}

// pruneStore удаляет всё, кроме последних n ключей (формат даты сортируется).
func pruneStore(m map[string][2]int64, n int) {
	if len(m) <= n {
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys[:len(keys)-n] {
		delete(m, k)
	}
}

// fmtBytes — человекочитаемый объём: КБ/МБ/ГБ/ТБ (единицы — по языку).
func fmtBytes(b int64) string {
	switch {
	case b >= 1<<40:
		return fmt.Sprintf(T("byte_tb"), float64(b)/(1<<40))
	case b >= 1<<30:
		return fmt.Sprintf(T("byte_gb"), float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf(T("byte_mb"), float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf(T("byte_kb"), float64(b)/(1<<10))
	default:
		return fmt.Sprintf(T("byte_b"), b)
	}
}

// trafficPath кладёт traffic.json в тот же каталог, что и config.json.
func trafficPath(cfgPath string) string {
	return filepath.Join(filepath.Dir(cfgPath), "traffic.json")
}
