package main

// Сканер занятого места на диске для карточки «Диск» дашборда.
// Один проход по системному тому: размеры раскладываются по категориям
// (приложения, документы, медиа, кэши, корзина…), параллельно по
// верхнеуровневым папкам. Очистка — только «одноразовые» места:
// кэши (Temp, *.cache) и корзина.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
)

// walkEntry — запись каталога для быстрого обхода: размер и тип уже
// извлечены перечислителем (FindFirstFile на Windows / ReadDir на unix).
type walkEntry struct {
	name string
	size int64
	dir  bool
	link bool // симлинк или junction — не разворачиваем
}

// diskCatKeys — фиксированный порядок категорий: полотно и легенда
// рисуются всегда в одном и том же порядке.
var diskCatKeys = []string{"apps", "docs", "downloads", "photos", "music", "videos", "caches", "system", "trash", "other"}

// diskCatInfo — итог одной категории.
type diskCatInfo struct {
	Key     string `json:"key"`
	Bytes   int64  `json:"bytes"`
	Largest string `json:"largest,omitempty"` // самый большой файл категории
	LargSz  int64  `json:"largSz,omitempty"`
}

// diskDrive — подключённый том для чипов выбора диска.
type diskDrive struct {
	Name  string `json:"name"` // "C", "D"…
	Root  string `json:"root"` // "C:\"
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
	Used  uint64 `json:"used"`
}

// diskOpt — ход «Оптимизации системы» (Windows): запускается отдельным
// elevated-процессом, ход работы публикуется через лог-файл.
type diskOpt struct {
	Running bool     `json:"running"`
	Done    bool     `json:"done"`
	Err     string   `json:"err,omitempty"`
	Step    int      `json:"step"`            // 1..Steps
	Steps   int      `json:"steps"`           // всего шагов (4)
	Pct     int      `json:"pct"`             // процент внутри шага, 0..100
	Log     []string `json:"log,omitempty"`   // последние строки вывода утилит
}

// diskState — снапшот для /api/disk.
type diskState struct {
	Scanning bool          `json:"scanning"`
	Clearing string        `json:"clearing,omitempty"` // caches | trash — идёт очистка
	Done     bool          `json:"done"`
	Stopped  bool          `json:"stopped,omitempty"` // скан прерван пользователем — итог неполный
	Current  string        `json:"current,omitempty"` // путь, который смотрим прямо сейчас
	Volume   string        `json:"volume"`            // выбранный том, "C:\" или "/"
	Total    uint64        `json:"total"`             // объём тома, байт
	Free     uint64        `json:"free"`
	Used     uint64        `json:"used"`
	Progress float64       `json:"progress"` // 0..1 — доля просканированных байт от used
	Cats     []diskCatInfo `json:"cats"`
	Drives   []diskDrive   `json:"drives"`   // все подключённые тома
	CanOpt   bool          `json:"canOpt"`   // «Оптимизировать систему» доступна (Windows)
	Opt      *diskOpt      `json:"opt,omitempty"`
}

// DiskScanner — состояние скана и очистки; один на приложение.
type DiskScanner struct {
	mu       sync.Mutex
	st       diskState
	sel      string       // выбранный том для скана ("C:\"); пусто — системный
	running  bool
	restart  bool
	stopping atomic.Bool  // «стоп»: работники сворачиваются, итог остаётся частичным
	clearing string       // caches | trash — очистка в процессе
	everScan bool         // автозапуск первого скана при открытии дашборда
	curPath  atomic.Value // строка: папка, которую работники смотрят сейчас
	drives   []diskDrive
	drivesAt time.Time
	opt      diskOpt
	optAt    time.Time // старт оптимизации — для таймаута наблюдения
}

// NewDiskScanner создаёт сканер; скан запускается лениво, при первом
// открытии карточки (см. StateAndArm).
func NewDiskScanner() *DiskScanner {
	s := &DiskScanner{}
	s.st.Cats = emptyCats()
	return s
}

func emptyCats() []diskCatInfo {
	out := make([]diskCatInfo, 0, len(diskCatKeys))
	for _, k := range diskCatKeys {
		out = append(out, diskCatInfo{Key: k})
	}
	return out
}

// scanVolumeRoot — корень скана: системный том.
func scanVolumeRoot() string {
	if runtime.GOOS == "windows" {
		if sd := os.Getenv("SystemDrive"); sd != "" {
			return filepath.VolumeName(sd) + string(os.PathSeparator)
		}
		return `C:\`
	}
	return "/"
}

// StateAndArm отдаёт снапшот и при первом обращении запускает автоскан
// системного тома — карточка начинает «вычислять» сразу, как в референсе.
func (s *DiskScanner) StateAndArm() diskState {
	s.mu.Lock()
	if !s.everScan {
		s.everScan = true
		if s.sel == "" {
			s.sel = scanVolumeRoot()
		}
		if !s.running && s.clearing == "" {
			s.startLocked()
		}
	}
	if time.Since(s.drivesAt) > 2*time.Second {
		if dv, err := listDrives(); err == nil {
			s.drives = dv
		}
		s.drivesAt = time.Now()
	}
	st := s.snapshotLocked()
	s.mu.Unlock()
	if st.Scanning {
		if cur, ok := s.curPath.Load().(string); ok {
			st.Current = cur
		}
	}
	return st
}

// StartScan запускает новый проход по тому volume ("" — текущий выбранный);
// если скан уже идёт — помечает перезапуск.
func (s *DiskScanner) StartScan(volume string) {
	s.mu.Lock()
	if volume != "" {
		s.sel = volume
	} else if s.sel == "" {
		s.sel = scanVolumeRoot()
	}
	if s.running {
		s.restart = true
		s.mu.Unlock()
		return
	}
	s.everScan = true
	s.startLocked()
	s.mu.Unlock()
}

// Selected возвращает выбранный том (для хендлеров).
func (s *DiskScanner) Selected() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sel == "" {
		return scanVolumeRoot()
	}
	return s.sel
}

func (s *DiskScanner) startLocked() {
	s.stopping.Store(false)
	s.running = true
	s.st = diskState{Scanning: true, Volume: s.sel, Cats: emptyCats()}
	go s.run()
}

// run — цикл проходов: после завершения перезапускает себя, если попросили.
func (s *DiskScanner) run() {
	for {
		s.scanOnce(s.sel)

		s.mu.Lock()
		again := s.restart
		s.restart = false
		if again {
			s.stopping.Store(false)
			s.st = diskState{Scanning: true, Volume: s.sel, Cats: emptyCats()}
			s.mu.Unlock()
			continue
		}
		s.running = false
		s.mu.Unlock()
		return
	}
}

// StopScan — кнопка «стоп»: работники сворачиваются, в состоянии остаётся
// частичный итог (Done + Stopped). На незапущенный скан не влияет: флаг
// снимется при следующем старте.
func (s *DiskScanner) StopScan() {
	s.stopping.Store(true)
}

// listDrives — подключённые тома: все диски и смонтированные носители
// (на Windows — все буквы; на unix — корень и точки монтирования носителей).
func listDrives() ([]diskDrive, error) {
	parts, err := disk.Partitions(false)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []diskDrive
	add := func(root string) {
		if root == "" || seen[root] {
			return
		}
		seen[root] = true
		d := diskDrive{Name: driveName(root), Root: root}
		if u, err := disk.Usage(root); err == nil {
			d.Total, d.Free, d.Used = u.Total, u.Free, u.Used
		}
		out = append(out, d)
	}
	for _, p := range parts {
		mp := p.Mountpoint
		switch runtime.GOOS {
		case "windows":
			if len(mp) >= 2 && mp[1] == ':' {
				if len(mp) == 2 {
					mp += `\`
				}
				add(mp)
			}
		case "darwin":
			if mp == "/" || strings.HasPrefix(mp, "/Volumes/") {
				add(mp)
			}
		default: // linux и прочие
			if mp == "/" || strings.HasPrefix(mp, "/media/") || strings.HasPrefix(mp, "/mnt/") || strings.HasPrefix(mp, "/run/media/") {
				add(mp)
			}
		}
	}
	return out, nil
}

// driveName — короткое имя тома для чипа: "C" на Windows, "/" и "/Volumes/X"
// на unix.
func driveName(root string) string {
	if runtime.GOOS == "windows" {
		return strings.TrimSuffix(root, `\`)
	}
	return strings.TrimSuffix(root, "/")
}

// diskTally — общий накопитель всех работников: атомарные счётчики
// позволяют публиковать промежуточные итоги карточке прямо во время скана.
// Байты категории идут в свою атомарную ячейку без локов; «самый большой
// файл» трекается через редкий (только новые максимумы) мьютекс.
type diskTally struct {
	bytes   map[string]*atomic.Int64
	largest map[string]string
	largSz  map[string]*atomic.Int64
	mu      sync.Mutex // защищает largest (строку) при замене
}

func newTally() *diskTally {
	t := &diskTally{
		bytes:   make(map[string]*atomic.Int64, len(diskCatKeys)),
		largest: make(map[string]string, len(diskCatKeys)),
		largSz:  make(map[string]*atomic.Int64, len(diskCatKeys)),
	}
	for _, k := range diskCatKeys {
		t.bytes[k] = new(atomic.Int64)
		t.largSz[k] = new(atomic.Int64)
	}
	return t
}

func (t *diskTally) bucket(cat string) *atomic.Int64 { return t.bytes[cat] }

func (t *diskTally) add(cat, name string, sz int64) {
	if a, ok := t.bytes[cat]; ok {
		a.Add(sz)
	}
	t.noteLargest(cat, name, sz)
}

// noteLargest — горячий путь без локов: лок берётся только когда размер
// файла превысил текущий максимум категории (таких единицы на миллион).
func (t *diskTally) noteLargest(cat, name string, sz int64) {
	a := t.largSz[cat]
	if a == nil || sz <= a.Load() {
		return
	}
	t.mu.Lock()
	if sz > t.largSz[cat].Load() {
		t.largSz[cat].Store(sz)
		t.largest[cat] = name
	}
	t.mu.Unlock()
}

// snapshot — срез категорий в фиксированном порядке diskCatKeys.
func (t *diskTally) snapshot() []diskCatInfo {
	out := emptyCats()
	for _, k := range diskCatKeys {
		ci := out[catIndex(k)]
		ci.Bytes = t.bytes[k].Load()
		ci.LargSz = t.largSz[k].Load()
		out[catIndex(k)] = ci
	}
	t.mu.Lock()
	for _, k := range diskCatKeys {
		if ci := &out[catIndex(k)]; ci.LargSz > 0 {
			ci.Largest = t.largest[k]
		}
	}
	t.mu.Unlock()
	return out
}

// scanOnce — один полный проход по тому root.
func (s *DiskScanner) scanOnce(root string) {
	sysRoot := scanVolumeRoot()
	isSys := strings.EqualFold(root, sysRoot)

	st := diskState{Scanning: true, Volume: root, Cats: emptyCats()}
	if u, err := disk.Usage(root); err == nil {
		st.Total, st.Free, st.Used = u.Total, u.Free, u.Used
	}

	var scanned atomicInt64
	s.curPath.Store("")
	tally := newTally()

	// объёмы тома и живые итоги публикуем сразу, а не после прохода —
	// иначе шапка весь скан показывает нули, полоса серая, sweep не движется
	s.mu.Lock()
	s.st.Total, s.st.Free, s.st.Used = st.Total, st.Free, st.Used
	s.mu.Unlock()
	done := make(chan struct{})
	defer close(done)
	go func() {
		tick := time.NewTicker(400 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				s.mu.Lock()
				s.st.Cats = tally.snapshot()
				if st.Used > 0 {
					p := float64(scanned.Load()) / float64(st.Used)
					if p > 1 {
						p = 1
					}
					s.st.Progress = p
				}
				s.mu.Unlock()
			}
		}
	}()

	entries, err := readDirEntries(root)
	if err != nil {
		s.mu.Lock()
		cats := s.st.Cats
		s.st = diskState{Done: true, Volume: root, Cats: cats, Total: st.Total, Free: st.Free, Used: st.Used, Progress: 1}
		s.mu.Unlock()
		return
	}

	// пропускаем виртуальные и «чужие» точки монтирования
	skip := map[string]bool{}
	switch runtime.GOOS {
	case "darwin":
		skip["/Volumes"] = true
		skip["/dev"] = true
		skip["/System/Volumes"] = true
	case "linux":
		for _, p := range []string{"/proc", "/sys", "/dev", "/run", "/media", "/mnt"} {
			skip[p] = true
		}
	}

	// стартовая очередь: верхнеуровневые папки тома
	jobs := make([]diskJob, 0, len(entries))
	type rootFile struct {
		name string
		size int64
	}
	var rootFiles []rootFile
	for i := range entries {
		e := &entries[i]
		if e.link {
			continue // симлинки и junction не разворачиваем
		}
		p := joinDir(root, e.name)
		if skip[p] {
			continue
		}
		if e.dir {
			jobs = append(jobs, diskJob{p, []string{e.name}})
		} else {
			rootFiles = append(rootFiles, rootFile{e.name, e.size})
		}
	}

	// обход через общую очередь: работники берут директории по мере
	// появления и складывают туда подпапки — статичная раздача
	// «работник = верхняя папка» простаивала всех, пока один грыз
	// WinSxS или Users
	q := &dirQueue{}
	for _, j := range jobs {
		q.push(j)
	}
	workers := runtime.NumCPU() * 2
	if workers > 16 {
		workers = 16
	}
	if workers > len(jobs) {
		workers = len(jobs)
	}
	if workers < 1 {
		workers = 1
	}
	var active int64
	var once sync.Once
	allDone := make(chan struct{})
	finish := func() { once.Do(func() { close(allDone) }) }

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-allDone:
					return
				default:
				}
				if s.stopping.Load() {
					finish() // «стоп»: сворачиваем всех, итог останется частичным
					return
				}
				atomic.AddInt64(&active, 1)
				j, ok := q.pop()
				if !ok {
					atomic.AddInt64(&active, -1)
					if q.len() == 0 && atomic.LoadInt64(&active) == 0 {
						finish()
						return
					}
					time.Sleep(100 * time.Microsecond)
					continue
				}
				tally.processDir(s, j, q, isSys, &scanned)
				atomic.AddInt64(&active, -1)
			}
		}()
	}
	<-allDone
	wg.Wait()

	// файлы в корне тома (pagefile.sys и пр.) — размер уже из перечисления
	for _, rf := range rootFiles {
		tally.add(categorize(runtime.GOOS, []string{rf.name}, isSys), rf.name, rf.size)
		scanned.Add(rf.size)
	}

	if st.Used > 0 {
		p := float64(scanned.Load()) / float64(st.Used)
		if p > 1 {
			p = 1
		}
		st.Progress = p
	} else {
		st.Progress = 1
	}
	st.Done = true
	st.Scanning = false // иначе состояние «сканируется» висит вечно и «стоп» холостой
	st.Stopped = s.stopping.Load()
	st.Current = ""
	st.Cats = tally.snapshot()

	s.mu.Lock()
	s.st = st
	s.mu.Unlock()
}

// diskJob — директория в очереди обхода: путь + относительный путь
// (для правил категорий).
type diskJob struct {
	path string
	rel  []string
}

// joinDir — конкатенация без filepath.Join: Clean на каждом пути —
// лишняя работа при миллионах вызовов.
func joinDir(parent, name string) string {
	if n := len(parent); n > 0 && (parent[n-1] == '\\' || parent[n-1] == '/') {
		return parent + name
	}
	return parent + string(os.PathSeparator) + name
}

// dirQueue — общая очередь директорий с динамическим распределением:
// работники тянут по одной и подкидывают подпапки обратно.
type dirQueue struct {
	mu   sync.Mutex
	jobs []diskJob
}

func (q *dirQueue) push(j diskJob) {
	q.mu.Lock()
	q.jobs = append(q.jobs, j)
	q.mu.Unlock()
}

func (q *dirQueue) pop() (diskJob, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.jobs) == 0 {
		return diskJob{}, false
	}
	j := q.jobs[len(q.jobs)-1] // LIFO: работаем рядом с родителем — кеш дружелюбнее
	q.jobs = q.jobs[:len(q.jobs)-1]
	return j, true
}

func (q *dirQueue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.jobs)
}

// processDir — перечисление одной директории: размеры файлов — в счётчик
// категории, подпапки — обратно в очередь.
func (t *diskTally) processDir(s *DiskScanner, j diskJob, q *dirQueue, isSys bool, scanned *atomicInt64) {
	s.curPath.Store(j.path)
	cat := categorize(runtime.GOOS, j.rel, isSys)
	entries, err := readDirEntries(j.path)
	if err != nil {
		return // нет прав — считаем, что папки нет
	}
	bucket := t.bucket(cat)
	for i := range entries {
		if s.stopping.Load() {
			return // стоп посреди каталога — остальные файлы не считаем
		}
		e := &entries[i]
		if e.link {
			continue // симлинки и junction не разворачиваем
		}
		if e.dir {
			q.push(diskJob{
				joinDir(j.path, e.name),
				append(append(make([]string, 0, len(j.rel)+1), j.rel...), e.name),
			})
			continue
		}
		bucket.Add(e.size)
		scanned.Add(e.size)
		t.noteLargest(cat, e.name, e.size)
	}
}

// atomicInt64 — синоним ради читаемости: счётчик просканированных байт.
type atomicInt64 = atomic.Int64

// atomicCounter — раздача индексов работникам без накладных на канал.
type atomicCounter struct {
	mu sync.Mutex
	i  int
}

func (c *atomicCounter) next(n int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.i >= n {
		return -1
	}
	v := c.i
	c.i++
	return v
}

func catIndex(key string) int {
	for i, k := range diskCatKeys {
		if k == key {
			return i
		}
	}
	return len(diskCatKeys) - 1 // other
}

// snapshotLocked — копия состояния под мьютексом.
func (s *DiskScanner) snapshotLocked() diskState {
	st := s.st
	st.Cats = make([]diskCatInfo, len(s.st.Cats))
	copy(st.Cats, s.st.Cats)
	st.Drives = make([]diskDrive, len(s.drives))
	copy(st.Drives, s.drives)
	st.CanOpt = runtime.GOOS == "windows"
	if runtime.GOOS == "windows" {
		opt := s.opt
		opt.Log = append([]string(nil), s.opt.Log...)
		st.Opt = &opt
	}
	return st
}

// Clear запускает очистку кэшей или корзины; одна очистка за раз.
func (s *DiskScanner) Clear(target string) error {
	if target != "caches" && target != "trash" {
		return errors.New("unknown target")
	}
	s.mu.Lock()
	if s.clearing != "" {
		s.mu.Unlock()
		return errors.New("already clearing")
	}
	s.clearing = target
	s.st.Clearing = target
	s.mu.Unlock()

	go func() {
		switch target {
		case "caches":
			clearCacheDirs()
		case "trash":
			_ = emptyTrash()
		}
		// сброс «очищаю» и автопересчёт — под одним локом: между ними не
		// должно быть окна, когда ни clearing, ни scanning не активны,
		// иначе фронтенд остановит опрос на устаревших данных
		s.mu.Lock()
		s.clearing = ""
		s.st.Clearing = ""
		if !s.running {
			s.startLocked() // после очистки пересчитываем, чтобы показать факт
		}
		s.mu.Unlock()
	}()
	return nil
}

// ── правила раскладки по категориям ─────────────────────────────────────

// cacheDirNames — имена папок-кэшей внутри AppData/Library.
var cacheDirNames = map[string]bool{
	"cache": true, "caches": true, "code cache": true, "gpucache": true,
	"cachestorage": true, "service worker": true, "crashdumps": true,
	"crashreports": true, "d3dscache": true, "shadercache": true,
	"grshadercache": true, "inetcache": true, "temp": true,
}

// categorizeFor — чистая функция правил: по ОС, относительному пути и
// признаку «системный ли том». Устроена по префиксам: чем глубже
// совпадение, тем точнее категория.
func categorizeFor(goos string, rel []string, isSys bool) string {
	if len(rel) == 0 {
		return "other"
	}
	top := rel[0]
	if !isSys {
		// несистемный том (D:, E:, флешка): привычной структуры Windows
		// нет — раскладываем по типовым именам папок, остальное — other
		switch lower(top) {
		case "$recycle.bin", "$recycle.bin/":
			return "trash"
		case "system volume information":
			return "system"
		case "documents", "документы", "docs":
			return "docs"
		case "downloads", "загрузки":
			return "downloads"
		case "pictures", "photos", "фото", "фотографии":
			return "photos"
		case "music", "музыка":
			return "music"
		case "videos", "video", "видео", "фильмы", "movies":
			return "videos"
		case "games", "игры":
			return "apps"
		case "programs", "program files", "apps", "программы":
			return "apps"
		}
		return "other"
	}
	homeRule := func(i int) string { // rel[i] — внутри домашней папки
		if i >= len(rel) {
			return "other"
		}
		name := lower(rel[i])
		switch name {
		case "documents", "desktop":
			return "docs"
		case "downloads":
			return "downloads"
		case "pictures":
			return "photos"
		case "music":
			return "music"
		case "videos", "movies":
			return "videos"
		case ".cache":
			return "caches"
		case ".trash":
			return "trash"
		case ".local":
			if i+2 < len(rel) && lower(rel[i+1]) == "share" && lower(rel[i+2]) == "trash" {
				return "trash"
			}
			return "other"
		case "library":
			if goos == "darwin" {
				if i+1 < len(rel) {
					sub := lower(rel[i+1])
					if sub == "caches" {
						return "caches"
					}
					if sub == "application support" {
						return "apps"
					}
				}
				return "system"
			}
			return "other"
		}
		return "other"
	}

	switch goos {
	case "windows":
		switch lower(top) {
		case "$recycle.bin":
			return "trash"
		case "windows":
			return "system"
		case "program files", "program files (x86)", "programdata":
			return "apps"
		case "users":
			if len(rel) < 2 {
				return "other"
			}
			if len(rel) >= 3 && lower(rel[2]) == "appdata" {
				for _, seg := range rel[3:] {
					if cacheDirNames[lower(seg)] {
						return "caches"
					}
				}
				return "apps"
			}
			return homeRule(2)
		case "intel", "perflogs", "recovery", "$winreagent", "drivers":
			return "system"
		case "pagefile.sys", "hiberfil.sys", "swapfile.sys":
			return "system"
		}
		return "other"

	case "darwin":
		switch lower(top) {
		case "applications":
			return "apps"
		case "system":
			return "system"
		case "library":
			if len(rel) >= 2 && lower(rel[1]) == "caches" {
				return "caches"
			}
			return "system"
		case "private":
			if len(rel) >= 2 {
				switch lower(rel[1]) {
				case "tmp":
					return "caches"
				case "var":
					if len(rel) >= 3 {
						switch lower(rel[2]) {
						case "folders", "tmp":
							return "caches"
						}
					}
					return "system"
				case "etc", "db":
					return "system"
				}
			}
			return "other"
		case "tmp", "etc", "var":
			return "system"
		case "opt":
			return "apps"
		case "users":
			if len(rel) < 2 {
				return "other"
			}
			return homeRule(2)
		}
		return "other"

	default: // linux и остальное
		switch lower(top) {
		case "usr", "etc", "boot", "sbin", "bin", "lib", "lib32", "lib64", "libx32", "snap":
			return "system"
		case "opt":
			return "apps"
		case "tmp":
			return "caches"
		case "var":
			if len(rel) >= 2 {
				switch lower(rel[1]) {
				case "cache", "tmp":
					return "caches"
				}
			}
			return "system"
		case "home", "root":
			if top == "root" {
				return homeRule(1)
			}
			if len(rel) < 2 {
				return "other"
			}
			return homeRule(2)
		}
		return "other"
	}
}

// lower — ASCII + кириллица: имена папок пользователей бывают русскими
// («Загрузки», «Игры»), byte-версия их не берёт.
func lower(s string) string {
	r := []rune(s)
	for i := range r {
		switch {
		case r[i] >= 'A' && r[i] <= 'Z':
			r[i] += 'a' - 'A'
		case r[i] >= 'А' && r[i] <= 'Я':
			r[i] += 'а' - 'А'
		case r[i] == 'Ё':
			r[i] = 'ё'
		}
	}
	return string(r)
}

func categorize(goos string, rel []string, isSys bool) string {
	return categorizeFor(goos, rel, isSys)
}

// ── очистка ──────────────────────────────────────────────────────────────

// isVolumeRoot — защита от очистки корня тома (на всякий случай: путь
// всё равно приходит только из белого списка cacheDirs).
func isVolumeRoot(dir string) bool {
	dir = filepath.Clean(dir)
	if runtime.GOOS != "windows" {
		return dir == "/" || dir == "."
	}
	vol := filepath.VolumeName(dir)
	return vol != "" && strings.EqualFold(dir, vol+`\`)
}

// clearDirContents удаляет содержимое папки, не трогая саму папку.
// Ошибки (файл занят, нет прав) пропускаются.
func clearDirContents(dir string) {
	if dir == "" || isVolumeRoot(dir) {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		_ = os.RemoveAll(filepath.Join(dir, e.Name()))
	}
}

// cacheDirs — белые списки папок-кэшей для очистки.
func cacheDirs() []string {
	var out []string
	switch runtime.GOOS {
	case "windows":
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			out = append(out, filepath.Join(v, "Temp"))
		}
		out = append(out, os.TempDir())
	case "darwin":
		if c, err := os.UserCacheDir(); err == nil {
			out = append(out, c)
		}
		out = append(out, os.TempDir())
	default:
		if c, err := os.UserCacheDir(); err == nil {
			out = append(out, c)
		}
		out = append(out, os.TempDir())
	}
	// дедупликация
	seen := map[string]bool{}
	uniq := out[:0]
	for _, d := range out {
		if d != "" && !seen[d] {
			seen[d] = true
			uniq = append(uniq, d)
		}
	}
	return uniq
}

func clearCacheDirs() {
	for _, d := range cacheDirs() {
		clearDirContents(d)
	}
}

// ── оптимизация системы (Windows) ───────────────────────────────────────

// optLogPath — файл, через который elevated-помощник докладывает ход.
func optLogPath() string {
	return filepath.Join(os.TempDir(), "stt_optimize.log")
}

// StartOptimize запускает системную очистку: отдельный процесс с правами
// администратора (Windows показывает UAC), помощник гонит DISM/cleanmgr
// и пишет ход в лог-файл; мы читаем его и публикуем в состоянии карточки.
func (s *DiskScanner) StartOptimize(resetBase bool) error {
	s.mu.Lock()
	if s.opt.Running {
		s.mu.Unlock()
		return errors.New("already running")
	}
	s.optAt = time.Now()
	s.opt = diskOpt{Running: true, Step: 1, Steps: 4}
	s.mu.Unlock()

	logPath := optLogPath()
	_ = os.Remove(logPath)

	exe, err := os.Executable()
	if err != nil {
		s.finishOpt(err.Error())
		return err
	}
	// эскалация через PowerShell -Verb RunAs: Windows покажет UAC;
	// путь exe и аргументы — в одинарных кавычках (апострофы удваиваем)
	args := []string{"-optimize", "-optlog", logPath}
	if resetBase {
		args = append(args, "-resetbase")
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", "''") + "'"
	}
	ps := fmt.Sprintf("Start-Process -FilePath '%s' -ArgumentList %s -Verb RunAs -WindowStyle Hidden",
		strings.ReplaceAll(exe, "'", "''"), strings.Join(quoted, ","))
	cmd := exec.Command("powershell", "-NoProfile", "-Command", ps)
	hideConsole(cmd) // окно PowerShell тоже вводит в заблуждение
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := T("opt_uac_cancelled")
		if len(out) > 0 {
			msg += ": " + strings.TrimSpace(string(out))
		}
		s.finishOpt(msg)
		return errors.New(msg)
	}
	go s.watchOptimizeLog(logPath)
	return nil
}

// watchOptimizeLog — читает лог помощника и публикует ход в состояние,
// пока не появится #done/#error (или не истечёт разумный таймаут).
func (s *DiskScanner) watchOptimizeLog(logPath string) {
	deadline := time.Now().Add(45 * time.Minute)
	for {
		time.Sleep(500 * time.Millisecond)
		data, err := os.ReadFile(logPath)
		finished := false
		opt := diskOpt{Running: true, Step: 1, Steps: 4}
		if err == nil {
			var log []string
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimRight(line, "\r")
				switch {
				case strings.HasPrefix(line, "#step "):
					fmt.Sscanf(line, "#step %d", &opt.Step)
				case strings.HasPrefix(line, "#pct "):
					fmt.Sscanf(line, "#pct %d", &opt.Pct)
				case strings.HasPrefix(line, "#log "):
					if v := strings.TrimPrefix(line, "#log "); v != "" {
						log = append(log, v)
					}
				case line == "#done":
					opt.Done = true
					opt.Running = false
					finished = true
				case strings.HasPrefix(line, "#error "):
					opt.Err = strings.TrimPrefix(line, "#error ")
					if opt.Err == "#cancelled" {
						opt.Err = T("opt_cancelled_user") // маркер кооперативной отмены
					}
					opt.Running = false
					finished = true
				}
			}
			if n := len(log); n > 12 {
				log = log[n-12:]
			}
			opt.Log = log
		}
		if !finished && time.Now().After(deadline) {
			opt.Err = T("opt_timeout")
			opt.Running = false
			finished = true
		}
		s.mu.Lock()
		s.opt = opt
		s.mu.Unlock()
		if finished {
			return
		}
	}
}

// StopOptimize — кооперативная отмена «Оптимизации системы»: создаёт
// stop-файл, помощник замечает его и сам убивает текущий dism
// (elevated-процесс извне убить нельзя).
func (s *DiskScanner) StopOptimize() error {
	if runtime.GOOS != "windows" {
		return errors.New("windows only")
	}
	s.mu.Lock()
	running := s.opt.Running
	s.mu.Unlock()
	if !running {
		return errors.New("not running")
	}
	return os.WriteFile(optLogPath()+".stop", []byte("stop"), 0644)
}

// finishOpt — мгновенный финал без помощника (отмена UAC, ошибка запуска).
func (s *DiskScanner) finishOpt(err string) {
	s.mu.Lock()
	s.opt = diskOpt{Done: true, Err: err, Steps: 4}
	s.mu.Unlock()
}

// ── HTTP-хендлеры дашборда ──────────────────────────────────────────────

// serveDisk — состояние сканера; заодно «взводит» автоскан первого открытия.
func (d *Dashboard) serveDisk(w http.ResponseWriter, r *http.Request) {
	st := d.disk.StateAndArm()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(st)
}

// serveDiskScan — «Пересканировать» или выбор диска: {"volume":"D:\"} (пусто — текущий).
func (d *Dashboard) serveDiskScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Volume string `json:"volume"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	d.disk.StartScan(req.Volume)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// serveDiskOptimize — «Оптимизировать систему»: запуск elevated-помощника
// с DISM/cleanmgr; ход работы публикуется через Opt в состоянии.
func (d *Dashboard) serveDiskOptimize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if runtime.GOOS != "windows" {
		http.Error(w, "windows only", http.StatusNotImplemented)
		return
	}
	var req struct {
		ResetBase bool `json:"resetBase"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if err := d.disk.StartOptimize(req.ResetBase); err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// serveDiskStop — кнопка «стоп» скана.
func (d *Dashboard) serveDiskStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	d.disk.StopScan()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// serveDiskOptimizeStop — кнопка «Отменить» в оверлее оптимизации.
func (d *Dashboard) serveDiskOptimizeStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if err := d.disk.StopOptimize(); err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// serveDiskClear — очистка кэшей/корзины: {"target":"caches"|"trash"}.
func (d *Dashboard) serveDiskClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if err := d.disk.Clear(req.Target); err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// diskNow — плейсхолдер удалён; время сканеру не нужно, длительности считает фронтенд.
