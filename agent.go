package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

// agentStatus — состояние агента для интерфейса.
type agentStatus struct {
	Status string  `json:"status"`          // off | starting | ready | error
	Phase  string  `json:"phase,omitempty"` // что происходит сейчас
	Model  string  `json:"model,omitempty"` // имя загруженной модели
	Err    string  `json:"err,omitempty"`   // текст ошибки
	Prog   float64 `json:"prog,omitempty"`  // прогресс шага 0..1 (0 — нет данных)
	Step   int     `json:"step"`            // шаг инициализации: 0 сервер · 1 модель · 2 запуск
}

// chatMsg — сообщение диалога с агентом.
type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Agent — «ИИ-агент сети»: по кнопке «Инициализировать» запускает локальный
// llama-server с GGUF-моделью (всё на 127.0.0.1), строит числовую сводку
// замеров и отвечает на вопросы пользователя, опираясь только на неё.
// Без инициализации агент выключен: работает лишь детерминированная часть
// («Выводы») на готовых данных.
type Agent struct {
	mu     sync.Mutex
	chatMu sync.Mutex // один запрос к модели за раз

	cfgPath   string
	exePath   string // абсолютный путь до llama-server.exe
	modelPath string // абсолютный путь до GGUF
	port      int
	baseURL   string
	device    string // режим GPU: auto | cpu | имя устройства (Vulkan1)

	cmd    *exec.Cmd
	status agentStatus
	msgs   []chatMsg // история диалога в памяти сессии

	digest   string
	insights []string
	digestAt time.Time

	history   func() []dashEntry  // снимок истории замеров
	traffic   func() TrafficStore // снимок накопленного трафика
	watchProv func() []WatchJob   // снимок наблюдений за хостами
	meta      func() agentMeta    // текущие настройки приложения
	bad       float64
	good      float64

	actions  AgentActions // «права»: замер и настройки
	stopOnce sync.Once

	dlCtx    context.Context // отменяется «выключить» во время скачивания
	dlCancel context.CancelFunc

	stages []agentStage // прогресс текущего запроса (для «Agent Progress»)
}

// agentStage — шаг текущего запроса агента: что происходит прямо сейчас.
type agentStage struct {
	Name  string `json:"name"`
	State string `json:"state"`          // active | done
	Note  string `json:"note,omitempty"` // уточнение (например, «жду замер… 34 с»)
}

// setStage добавляет шаг или обновляет существующий (по имени).
func (a *Agent) setStage(name, state, note string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.stages {
		if a.stages[i].Name == name {
			a.stages[i].State, a.stages[i].Note = state, note
			return
		}
	}
	a.stages = append(a.stages, agentStage{Name: name, State: state, Note: note})
}

// Stages возвращает копию шагов текущего запроса.
func (a *Agent) Stages() []agentStage {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]agentStage, len(a.stages))
	copy(out, a.stages)
	return out
}

// NewAgent создаёт агента; пути из конфига относительные — привязывает к
// каталогу рядом с config.json (он же каталог exe).
func NewAgent(cfg *Config, cfgPath string) *Agent {
	base := filepath.Dir(cfgPath)
	resolve := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(base, p)
	}
	return &Agent{
		cfgPath:   cfgPath,
		exePath:   resolve(cfg.AgentLlamaServer),
		modelPath: resolve(cfg.AgentModel),
		port:      cfg.AgentPort,
		baseURL:   fmt.Sprintf("http://127.0.0.1:%d", cfg.AgentPort),
		device:    cfg.AgentDevice,
		bad:       cfg.BadMbps,
		good:      cfg.GoodMbps,
		status:    agentStatus{Status: "off"},
	}
}

// SetProviders подключает источники данных (дэшборд и монитор трафика).
func (a *Agent) SetProviders(history func() []dashEntry, traffic func() TrafficStore) {
	a.mu.Lock()
	a.history, a.traffic = history, traffic
	a.mu.Unlock()
}

// SetWatchProvider подключает список активных наблюдений (для дайджеста).
func (a *Agent) SetWatchProvider(f func() []WatchJob) {
	a.mu.Lock()
	a.watchProv = f
	a.mu.Unlock()
}

// SetLimits обновляет границы зон (плохо/хорошо) при смене настроек.
func (a *Agent) SetLimits(bad, good float64) {
	a.mu.Lock()
	a.bad, a.good = bad, good
	a.digestAt = time.Time{} // пересчитать сводку с новыми границами
	a.mu.Unlock()
}

// Status возвращает текущее состояние агента.
func (a *Agent) Status() agentStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status
}

// Msgs возвращает копию истории диалога (для восстановления чата на странице).
func (a *Agent) Msgs() []chatMsg {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]chatMsg, len(a.msgs))
	copy(out, a.msgs)
	return out
}

// Init запускает локальный LLM: если llama-server уже жив на своём порту —
// подхватываем его, иначе стартуем процесс и ждём загрузки модели.
// Не блокирует: статус уходит в starting, итог — в Status().
func (a *Agent) Init() {
	a.mu.Lock()
	if a.status.Status == "starting" || a.status.Status == "ready" {
		a.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.dlCtx, a.dlCancel = ctx, cancel
	a.status = agentStatus{Status: "starting", Phase: T("ag_check_server"), Step: -1}
	a.stopOnce = sync.Once{}
	exe, model, port, baseURL := a.exePath, a.modelPath, a.port, a.baseURL
	a.mu.Unlock()

	go func() {
		// 1. Уже работает? (например, остался с прошлого раза)
		if agentHealthOK(baseURL, 1200*time.Millisecond) {
			// короткая пауза: оверлей успевает показать «подключаюсь»,
			// иначе подхват проскакивает мимо анимации
			a.setStep(2, T("ag_connect"))
			time.Sleep(1500 * time.Millisecond)
			a.setReady(modelDisplayName(model), T("ag_attached"))
			return
		}
		// 2. Чего-то нет — докачаем: сервер с GitHub, модель с HuggingFace.
		a.setStep(0, T("ag_check_files"))
		if _, err := os.Stat(exe); err != nil {
			if err := a.fetchServer(exe); err != nil {
				a.failDL(ctx, T("ag_get_server"), err)
				return
			}
		}
		a.setStep(1, T("ag_check_model"))
		if _, err := os.Stat(model); err != nil {
			if err := a.fetchModel(model); err != nil {
				a.failDL(ctx, T("ag_get_model"), err)
				return
			}
		}
		// 3. Стартуем процесс (консольное окно скрыто).
		devArgs, gpuLabel := a.resolveGPU(exe)
		a.setStep(2, T("ag_start_server", gpuLabel))
		args := []string{"-m", model,
			"--port", fmt.Sprint(port),
			"--ctx-size", "4096",
			"--jinja",
		}
		cmd := exec.Command(exe, append(args, devArgs...)...)
		cmd.SysProcAttr = hideConsoleSysProcAttr()
		if err := cmd.Start(); err != nil {
			a.setError(T("ag_start_err") + err.Error())
			return
		}
		a.mu.Lock()
		a.cmd = cmd
		a.mu.Unlock()

		// 4. Ждём готовности (модель грузится в память до ~минуты на CPU).
		a.setPhase(T("ag_load_model"))
		deadline := time.Now().Add(3 * time.Minute)
		for time.Now().Before(deadline) {
			if agentHealthOK(baseURL, 1200*time.Millisecond) {
				a.setReady(modelDisplayName(model), "")
				return
			}
			// процесс умер?
			a.mu.Lock()
			done := a.cmd == nil || cmd.ProcessState != nil
			a.mu.Unlock()
			if done {
				_ = cmd.Wait()
				a.setError(T("ag_died", fmt.Sprint(port)))
				return
			}
			time.Sleep(700 * time.Millisecond)
		}
		a.setError(T("ag_timeout"))
	}()
}

// resolveGPU решает, на чём считать llama-server.
// auto: спрашиваем llama-server --list-devices и ищем NVIDIA среди
// Vulkan-устройств — нашли, считаем на нём (в разы быстрее CPU).
// Нет NVIDIA — чистый CPU: iGPU с разделяемой памятью на слабых машинах
// часто медленнее процессора. "cpu" форсирует CPU, прочее значение
// трактуется как имя устройства (например Vulkan1).
func (a *Agent) resolveGPU(exe string) ([]string, string) {
	switch dev := a.device; {
	case dev == "cpu":
		return []string{"-ngl", "0"}, "CPU"
	case dev != "" && dev != "auto":
		return []string{"--device", dev}, dev
	}
	if runtime.GOOS == "darwin" {
		// macOS-сборка llama.cpp: Metal включён по умолчанию, флаги не нужны
		return nil, "Metal"
	}
	cmd := exec.Command(exe, "--list-devices")
	cmd.SysProcAttr = hideConsoleSysProcAttr()
	if out, err := cmd.Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			// пример: Vulkan1: NVIDIA GeForce GTX 1050 (2178 MiB, 1883 MiB free)
			if !strings.HasPrefix(line, "Vulkan") || !strings.Contains(line, ":") {
				continue
			}
			name := strings.TrimSpace(strings.SplitN(line[strings.Index(line, ":")+1:], "(", 2)[0])
			if strings.Contains(name, "NVIDIA") {
				id := strings.TrimSuffix(strings.Fields(line)[0], ":")
				return []string{"--device", id}, name
			}
		}
	}
	return []string{"-ngl", "0"}, "CPU"
}

// agentModelURL — GGUF-модель одна на все платформы (HuggingFace).
const agentModelURL = "https://huggingface.co/LiquidAI/LFM3-1.7B-GGUF/resolve/main/LFM3-1.7B-Q4_K_M.gguf"

// serverExeName — имя бинарника llama-server на текущей ОС.
func serverExeName() string {
	if runtime.GOOS == "windows" {
		return "llama-server.exe"
	}
	return "llama-server"
}

// agentServerURL — Vulkan/Metal-сборка llama.cpp b12248 под текущую ОС
// (GitHub releases; винда и линукс — Vulkan, macOS — Metal).
func agentServerURL() string {
	const base = "https://github.com/ggml-org/llama.cpp/releases/download/b12248/"
	switch {
	case runtime.GOOS == "windows" && runtime.GOARCH == "amd64":
		return base + "llama-b12248-bin-win-vulkan-x64.zip"
	case runtime.GOOS == "linux" && runtime.GOARCH == "amd64":
		return base + "llama-b12248-bin-ubuntu-vulkan-x64.tar.gz"
	case runtime.GOOS == "linux" && runtime.GOARCH == "arm64":
		return base + "llama-b12248-bin-ubuntu-vulkan-arm64.tar.gz"
	case runtime.GOOS == "darwin" && runtime.GOARCH == "arm64":
		return base + "llama-b12248-bin-macos-arm64.tar.gz"
	case runtime.GOOS == "darwin":
		return base + "llama-b12248-bin-macos-x64.tar.gz"
	}
	return base + "llama-b12248-bin-win-vulkan-x64.zip"
}

// fetchServer скачивает llama.cpp (Vulkan/Metal-сборка) и распаковывает
// в каталог агента — рядом с будущим llama-server.
func (a *Agent) fetchServer(exe string) error {
	dir := filepath.Dir(exe)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	archURL := agentServerURL()
	archPath := filepath.Join(dir, "llama-server.zip")
	if strings.HasSuffix(archURL, ".tar.gz") {
		archPath = filepath.Join(dir, "llama-server.tar.gz")
	}
	if err := a.download(archURL, archPath, T("ag_dl_server")); err != nil {
		return err
	}
	a.setPhase(T("ag_unpack"))
	if strings.HasSuffix(archPath, ".tar.gz") {
		if err := untargz(archPath, dir); err != nil {
			return err
		}
	} else {
		if err := unzip(archPath, dir); err != nil {
			return err
		}
	}
	_ = os.Remove(archPath)
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf(T("ag_no_exe"), serverExeName())
	}
	return nil
}

// fetchModel скачивает GGUF-модель с HuggingFace.
func (a *Agent) fetchModel(model string) error {
	if err := os.MkdirAll(filepath.Dir(model), 0o755); err != nil {
		return err
	}
	return a.download(agentModelURL, model, T("ag_dl_model"))
}

// download качает url в dst, показывая фазу и прогресс (0..1) в статусе.
// Промежуточно пишет dst+".part": если он остался с прошлого раза и
// сервер поддерживает Range — докачивает, иначе начинает заново.
func (a *Agent) download(url, dst, phase string) error {
	a.mu.Lock()
	ctx := a.dlCtx
	a.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	part := dst + ".part"
	var done int64
	if st, err := os.Stat(part); err == nil {
		done = st.Size()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if done > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(done, 10)+"-")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var total int64
	resum := false
	switch {
	case resp.StatusCode == http.StatusPartialContent && done > 0:
		resum = true
		if _, t, ok := strings.Cut(resp.Header.Get("Content-Range"), "/"); ok {
			total, _ = strconv.ParseInt(t, 10, 64)
		}
	case resp.StatusCode == http.StatusOK:
		done = 0 // докачка не вышла — начинаем заново
		total = resp.ContentLength
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && done > 0:
		// .part уже полного размера — докачивать нечего
		return os.Rename(part, dst)
	default:
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	flag := os.O_CREATE | os.O_WRONLY
	if resum {
		flag |= os.O_APPEND
	} else {
		flag |= os.O_TRUNC
	}
	f, err := os.OpenFile(part, flag, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	buf := make([]byte, 512<<10)
	var got int64
	last := time.Now()
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			got += int64(n)
			if time.Since(last) >= 300*time.Millisecond {
				last = time.Now()
				soFar, frac := done+got, 0.0
				if total > 0 {
					frac = float64(soFar) / float64(total)
				}
				a.setPhaseProg(fmt.Sprintf(T("ag_dl_prog"), phase, soFar>>20, total>>20), frac)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if err := ctx.Err(); err != nil {
		f.Close()
		return err
	}
	if total > 0 && done+got < total {
		f.Close()
		return fmt.Errorf(T("ag_dl_short"), (done+got)>>20, total>>20)
	}
	// Windows не переименует открытый файл — закрываем явно
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(part, dst)
}

// unzip распаковывает zip-архив в каталог (сборка llama.cpp лежит плоско).
func unzip(src, dir string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		name := filepath.Clean(f.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			continue
		}
		dst := filepath.Join(dir, name)
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(dst, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		r, err := f.Open()
		if err != nil {
			return err
		}
		w, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			r.Close()
			return err
		}
		_, err = io.Copy(w, r)
		r.Close()
		w.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// failDL ставит ошибку скачивания; если запрос отменили «выключить» —
// статус уже off и трогать его не надо.
func (a *Agent) failDL(ctx context.Context, prefix string, err error) {
	if ctx.Err() != nil {
		return
	}
	a.setError(prefix + err.Error())
}

// untargz распаковывает tar.gz (linux/macos-сборки llama.cpp) в каталог;
// бинарнику выставляется бит исполнения. Защита от traversal — как в unzip.
func untargz(src, dir string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(hdr.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			continue
		}
		dst := filepath.Join(dir, name)
		if hdr.Typeflag == tar.TypeDir {
			_ = os.MkdirAll(dst, 0o755)
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o755)
		if hdr.Mode&0o100 == 0 {
			mode = 0o644 // без бита исполнения
		}
		w, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, tr)
		w.Close()
		if err != nil {
			return err
		}
	}
}

// Stop выключает агента: убивает наш llama-server (включая подхваченный
// чужой процесс — ищем по пути exe) и возвращает статус off.
func (a *Agent) Stop() {
	a.mu.Lock()
	cmd := a.cmd
	a.cmd = nil
	exePath := a.exePath
	a.status = agentStatus{Status: "off"}
	a.msgs = nil
	cancel := a.dlCancel
	a.dlCtx, a.dlCancel = nil, nil
	a.mu.Unlock()
	if cancel != nil {
		cancel() // прервать идущее скачивание
	}
	if cmd != nil && cmd.Process != nil {
		a.stopOnce.Do(func() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})
	}
	// подхваченный процесс мог быть запущен не нами (пережил перезапуск
	// приложения) — прибираем и его, иначе «выключить» не освободит память
	procs, err := process.Processes()
	if err == nil {
		for _, p := range procs {
			if exe, err := p.Exe(); err == nil && exe != "" &&
				strings.EqualFold(exe, exePath) {
				_ = p.Kill()
			}
		}
	}
}

// AgentActions — «права» агента: применяемые им действия (всё локальное,
// никакой системы: замер и настройки самого приложения).
type AgentActions struct {
	RunCheck      func()
	SetInterval   func(minutes int) int
	SetDownloadMB func(mb int) int
	SetUploadMB   func(mb int) int
	SetLimits     func(bad, good float64) (float64, float64)
	SetNetRate    func(ms int) int
	ExportArchive func() (string, error)
	ImportArchive func(path, mode string) (string, error)
	PingHost      func(host string) (string, error)
	WatchHost     func(host string, minutes int) (string, error)
	UnwatchHost   func(host string) (string, error)
	UnwatchAll    func() (string, error)
}

// SetActions подключает «права» (вызывается один раз при старте).
func (a *Agent) SetActions(act AgentActions) {
	a.mu.Lock()
	a.actions = act
	a.mu.Unlock()
}

// agentMeta — текущие настройки приложения (для сводки).
type agentMeta struct {
	Interval int
	DownMB   int
	UpMB     int
	RateMs   int
}

// SetMetaProvider подключает источник текущих настроек.
func (a *Agent) SetMetaProvider(f func() agentMeta) {
	a.mu.Lock()
	a.meta = f
	a.mu.Unlock()
}

// Chat отправляет вопрос модели, вернув обновлённую историю диалога.
// Модель может попросить действие (JSON {"action":...}) — агент выполняет
// его из белого списка, возвращает наблюдение и просит финальный ответ.
// agentToolMsg — сообщение wire-диалога (включая tool-вызовы).
type agentToolMsg = map[string]any

// agentToolsFor — описания инструментов для llama-server (нативный tool
// calling) на языке интерфейса: маленькая модель следует фразовой карте
// промпта, поэтому описания должны быть на том же языке.
func agentToolsFor(lang string) []map[string]any {
	tr := func(ru, en string) string {
		if lang == LangEn {
			return en
		}
		return ru
	}
	return []map[string]any{
		{"type": "function", "function": map[string]any{
			"name":        "run_check",
			"description": tr("Сделать замер скорости интернета прямо сейчас и вернуть результат", "Run an internet speed test right now and return the result"),
			"parameters":  map[string]any{"type": "object", "properties": map[string]any{}}}},
		{"type": "function", "function": map[string]any{
			"name":        "set_interval",
			"description": tr("Изменить период автопроверок в минутах (5–180)", "Change the auto-check interval in minutes (5–180)"),
			"parameters": map[string]any{"type": "object", "properties": map[string]any{
				"minutes": map[string]any{"type": "integer", "description": tr("минуты", "minutes")}},
				"required": []string{"minutes"}}}},
		{"type": "function", "function": map[string]any{
			"name":        "set_download_mb",
			"description": tr("Объём теста загрузки в МБ (5–200)", "Download test size in MB (5–200)"),
			"parameters": map[string]any{"type": "object", "properties": map[string]any{
				"mb": map[string]any{"type": "integer", "description": tr("МБ", "MB")}},
				"required": []string{"mb"}}}},
		{"type": "function", "function": map[string]any{
			"name":        "set_upload_mb",
			"description": tr("Объём теста отдачи в МБ (5–200)", "Upload test size in MB (5–200)"),
			"parameters": map[string]any{"type": "object", "properties": map[string]any{
				"mb": map[string]any{"type": "integer", "description": tr("МБ", "MB")}},
				"required": []string{"mb"}}}},
		{"type": "function", "function": map[string]any{
			"name":        "set_limits",
			"description": tr("Границы зон оценки: «плохо» ниже bad, «хорошо» выше good, Мбит/с", "Speed zone limits: “bad” below bad, “good” above good, Mbps"),
			"parameters": map[string]any{"type": "object", "properties": map[string]any{
				"bad": map[string]any{"type": "number"}, "good": map[string]any{"type": "number"}},
				"required": []string{"bad", "good"}}}},
		{"type": "function", "function": map[string]any{
			"name":        "set_net_rate",
			"description": tr("Частота обновления живого графика, мс: 1000, 2000, 5000 или 10000", "Live chart refresh rate in ms: 1000, 2000, 5000 or 10000"),
			"parameters": map[string]any{"type": "object", "properties": map[string]any{
				"ms": map[string]any{"type": "integer"}},
				"required": []string{"ms"}}}},
		{"type": "function", "function": map[string]any{
			"name":        "export_archive",
			"description": tr("Сохранить все данные мониторинга (историю замеров и трафик) в архивный файл рядом с config.json", "Save all monitoring data (check history and traffic) into an archive file next to config.json"),
			"parameters":  map[string]any{"type": "object", "properties": map[string]any{}}}},
		{"type": "function", "function": map[string]any{
			"name":        "import_archive",
			"description": tr("Загрузить данные из архивного .jsonl файла. Сначала вызывай с mode=check: если есть пересечение периодов — спроси пользователя «перезаписать совпавшие или пропустить их?», затем вызови ещё раз с выбранным mode", "Load data from an archive .jsonl file. First call with mode=check: if periods overlap — ask the user “overwrite the matching ones or skip them?”, then call again with the chosen mode"),
			"parameters": map[string]any{"type": "object", "properties": map[string]any{
				"path": map[string]any{"type": "string", "description": tr("полный путь к архивному файлу", "full path to the archive file")},
				"mode": map[string]any{"type": "string", "enum": []string{"check", "overwrite", "skip"},
					"description": tr("check — только проверить (по умолчанию); overwrite — перезаписать совпавшие; skip — пропустить совпавшие", "check — verify only (default); overwrite — replace the matching ones; skip — skip the matching ones")}},
				"required": []string{"path"}}}},
		{"type": "function", "function": map[string]any{
			"name":        "ping_host",
			"description": tr("Пинговать хост прямо сейчас (ICMP, 4 пакета) и вернуть статистику отклика: сколько ответило, среднее/мин/макс", "Ping a host right now (ICMP, 4 packets) and return reply statistics: how many replied, avg/min/max"),
			"parameters": map[string]any{"type": "object", "properties": map[string]any{
				"host": map[string]any{"type": "string", "description": tr("имя хоста или IP", "hostname or IP")}},
				"required": []string{"host"}}}},
		{"type": "function", "function": map[string]any{
			"name":        "watch_host",
			"description": tr("Запустить фоновое наблюдение за хостом: пинг каждые N минут, статус виден в блоке «Наблюдение» на дашборде. Наблюдение переживает перезапуск приложения. Остановить — инструмент unwatch_host", "Start a background watch of a host: a ping every N minutes, the status is shown in the “Watch” panel on the dashboard. The watch survives an app restart. Stop it with the unwatch_host tool"),
			"parameters": map[string]any{"type": "object", "properties": map[string]any{
				"host":    map[string]any{"type": "string", "description": tr("имя хоста или IP", "hostname or IP")},
				"minutes": map[string]any{"type": "integer", "description": tr("период пинга в минутах (по умолчанию 5)", "ping interval in minutes (default 5)")}},
				"required": []string{"host"}}}},
		{"type": "function", "function": map[string]any{
			"name":        "unwatch_host",
			"description": tr("Остановить фоновое наблюдение за хостом", "Stop the background watch of a host"),
			"parameters": map[string]any{"type": "object", "properties": map[string]any{
				"host": map[string]any{"type": "string"}},
				"required": []string{"host"}}}},
		{"type": "function", "function": map[string]any{
			"name":        "unwatch_all",
			"description": tr("Остановить сразу ВСЕ наблюдения за хостами (для «останови все наблюдения»)", "Stop ALL host watches at once (for “stop all watches”)"),
			"parameters":  map[string]any{"type": "object", "properties": map[string]any{}}}},
	}
}

// agentSystemPrompt — системный промпт на языке интерфейса: роль, стиль
// и фразовая карта инструментов (маленькая модель следует ей буквально,
// поэтому язык промпта и язык пользователя должны совпадать).
func agentSystemPrompt(lang string) string {
	if lang == LangEn {
		return "You are the built-in network monitoring AI agent of the SpeedTestTray app. " +
			"Answer in English, briefly (2–6 sentences), friendly and to the point, no Markdown headings. " +
			"The only source of facts is the numeric measurement digest given to you in a separate message. " +
			"Inventing numbers, comparisons or conclusions not present in the digest is strictly forbidden; " +
			"if data is missing — say so directly. For informational questions (“when is the best time…”, " +
			"“which location is faster…”) answer strictly from the digest; if it lacks the needed data, " +
			"say honestly that there are not enough measurements yet and advise measuring regularly — " +
			"enable auto-checks for at least an hour, e.g. every 15–30 minutes — after that you can " +
			"answer more precisely. Do not mention tools, the digest, or app internals — speak simply " +
			"about the measurement data. Never write internal reasoning — " +
			"only the final answer. Actions are performed ONLY with tools, phrase map: " +
			"“speed test / check now” → run_check; “ping <host>” → ping_host; " +
			"“watch / monitor / keep an eye on <host> [every N minutes]” → watch_host; " +
			"“stop / cancel watching [<host> / all watches]” → unwatch_host or unwatch_all; " +
			"“save the archive” → export_archive; “load archive <path>” → import_archive; " +
			"“set the interval to N minutes” → set_interval; “test size” → set_download_mb / set_upload_mb; " +
			"“bad/good limits” → set_limits; “chart refresh rate” → set_net_rate. " +
			"For such requests call the tool and restate its result; " +
			"without calling a tool never write the words “done”, “started”, “stopped”."
	}
		return "Ты — встроенный ИИ-агент мониторинга сети в приложении SpeedTestTray. " +
			"Отвечай на русском, кратко (2–6 предложений), дружелюбно и по делу, без Markdown-заголовков. " +
			"Единственный источник фактов — числовая сводка замеров, данная тебе отдельным сообщением. " +
			"Строго запрещено придумывать числа, сравнения и выводы, которых нет в сводке; " +
			"если данных не хватает — прямо скажи об этом. Информационные вопросы («когда лучше…», " +
			"«какая локация быстрее…») отвечай строго по сводке; если нужных данных в ней нет — " +
			"честно скажи, что измерений пока мало, и посоветуй измерять регулярно: включить " +
			"автопроверку хотя бы на час, например каждые 15–30 минут, — тогда можно будет " +
			"ответить точнее. Не упоминай инструменты, сводку и внутреннее устройство — " +
			"говори просто о данных измерений. Никогда не пиши внутренние рассуждения — " +
			"только готовый ответ. Действия выполняются ТОЛЬКО инструментами, карта фраз: " +
			"«замер скорости / проверь сейчас» → run_check; «пингуй <хост>» → ping_host; " +
			"«следи / наблюдай / поставь на контроль / поставь задачу за <хост> [каждые N минут]» → watch_host; " +
			"«останови / отмени / перестань следить [за <хост> / все наблюдения]» → unwatch_host или unwatch_all; " +
			"«сохрани архив» → export_archive; «загрузи архив <путь>» → import_archive; " +
			"«поставь период N минут» → set_interval; «объём теста» → set_download_mb / set_upload_mb; " +
			"«границы плохо/хорошо» → set_limits; «частота обновления графика» → set_net_rate. " +
			"Для таких просьб вызывай инструмент и пересказывай его результат; " +
			"без вызова инструмента слов «готово», «запущено», «остановлено» не пиши."
}

// Chat отправляет вопрос модели, вернув обновлённую историю диалога.
// Модель вызывает инструменты нативно (tool_calls) — агент выполняет их
// из белого списка, возвращает наблюдение и получает финальный ответ.
func (a *Agent) Chat(question string) ([]chatMsg, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return a.Msgs(), nil
	}
	a.chatMu.Lock()
	defer a.chatMu.Unlock()

	a.mu.Lock()
	if a.status.Status != "ready" {
		st := a.status.Status
		a.mu.Unlock()
		return a.Msgs(), fmt.Errorf(T("ag_not_ready"), st)
	}
	baseURL := a.baseURL
	a.mu.Unlock()

	// новый запрос — новый список шагов «Agent Progress»
	a.mu.Lock()
	a.stages = nil
	a.mu.Unlock()
	a.setStage(T("st_read_digest"), "active", "")
	digest := a.Digest()
	a.setStage(T("st_read_digest"), "done", "")

	wire := []agentToolMsg{
		{"role": "system", "content": agentSystemPrompt(Lang())},
		{"role": "user", "content": T("dg_seed_user") + "\n" + digest},
		{"role": "assistant", "content": T("dg_seed_ack")},
	}
	a.mu.Lock()
	tail := a.msgs
	if len(tail) > 8 {
		tail = tail[len(tail)-8:]
	}
	for _, m := range tail {
		wire = append(wire, agentToolMsg{"role": m.Role, "content": m.Content})
	}
	a.mu.Unlock()
	wire = append(wire, agentToolMsg{"role": "user", "content": question})

	// цикл: ответ или инструмент (нативный tool_calls); до 6 ходов
	var final string
	toolUsed := false

	for iter := 0; iter < 6; iter++ {
		step := T("st_answer")
		if iter == 0 {
			step = T("st_think")
		}
		a.setStage(step, "active", "")
		msg, err := a.callLLM(baseURL, wire, "")
		if err != nil {
			// сервер умер, пока агент «был готов» — честно переводим в ошибку
			if strings.Contains(err.Error(), "недоступен") || strings.Contains(err.Error(), "unreachable") {
				a.setError(T("ag_dead"))
			}
			return a.Msgs(), err
		}
		if len(msg.ToolCalls) == 0 {
			if iter == 0 && !toolUsed && !infoIntent.MatchString(question) &&
				(actionIntent.MatchString(question) || actionWords.MatchString(msg.Content)) {
				a.setStage(T("st_clarify"), "active", "")
				// форс-вызов на чистом контексте: история диалога тянет
				// маленькую модель к «знакомому» run_check, поэтому в wire
				// оставляем только систему, сводку и само сообщение
				clean := append(wire[:3:3], wire[len(wire)-1])
				msg2, err2 := a.callLLM(baseURL, clean, "required")
				if err2 == nil && len(msg2.ToolCalls) > 0 {
					msg = msg2
				} else {
					// даже форс не помог — обычно это не «плохая команда», а
					// вопрос, на который пока не хватает данных: даём модели
					// честно ответить по сводке; шаблонный отказ — последний
					// рубеж, если и это не сработало
					a.setStage(step, "done", "")
					honest := append(wire[:3:3], wire[len(wire)-1])
					honest = append(honest, agentToolMsg{"role": "user", "content": T("ag_honest_nudge")})
					if m3, err3 := a.callLLM(baseURL, honest, ""); err3 == nil && strings.TrimSpace(m3.Content) != "" {
						final = sanitizeFinal(m3.Content)
					} else {
						final = T("ag_refusal")
					}
					break
				}
			}
		}
		if len(msg.ToolCalls) == 0 {
			a.setStage(step, "done", "")
			final = sanitizeFinal(msg.Content)
			break
		}
		toolUsed = true
		a.setStage(step, "done", T("st_need_tool"))
		// вернуть assistant-сообщение с tool_calls, затем по одному tool-ответу
		wire = append(wire, agentToolMsg{"role": "assistant", "content": msg.Content, "tool_calls": msg.RawToolCalls})
		for _, tc := range msg.ToolCalls {
			toolName := toolTitle(tc.Function.Name)
			a.setStage(T("st_tool", toolName), "active", "")
			obs := a.execAction(tc.Function.Name, parseArgs(tc.Function.Arguments))
			a.setStage(T("st_tool", toolName), "done", obs)
			wire = append(wire, agentToolMsg{
				"role": "tool", "tool_call_id": tc.ID, "content": obs,
			})
		}
	}
	if strings.TrimSpace(final) == "" {
		final = T("ag_done_fallback")
	}
	a.setStage(T("st_done"), "done", "")

	a.mu.Lock()
	a.msgs = append(a.msgs,
		chatMsg{Role: "user", Content: question},
		chatMsg{Role: "assistant", Content: final},
	)
	if n := len(a.msgs); n > 40 {
		a.msgs = a.msgs[n-40:]
	}
	out2 := make([]chatMsg, len(a.msgs))
	copy(out2, a.msgs)
	a.mu.Unlock()
	return out2, nil
}

// actionIntent — императив в сообщении пользователя: главный признак того,
// что нужен вызов инструмента (ground truth — слова пользователя, а не
// пересказ маленькой модели). Матчит русский и английский императив —
// язык промпта может смениться, привычки пользователя — нет.
var actionIntent = regexp.MustCompile(`(?i)^((давай\s+)?(пингуй|следи|наблюдай|останови|отмени|отменить|перестань|сделай|поставь|измени|смени|сохрани|загрузи|экспортируй|запусти)|((let'?s\s+)?(run|check|measure|ping|watch|monitor|stop|cancel|save|import|export|set|change|start)\b))`)

// actionWords — словесные подтверждения действия в ответе модели; если
// финальный ответ содержит их, а инструмент за запрос ни разу не вызван,
// модель «рассказала» о действии словами — нужен повтор с required
var actionWords = regexp.MustCompile(`(?i)(инструмент|запус|запущ|останов|отмен|выполн|измен|сохран|начат|закрыт|активир|\btool\b|runn|\bstart|\bstop|\bcancel|\bdone|complet|\bsaved|\bimport|\bexport|chang)`)

// infoIntent — вопрос о данных, а не просьба действовать: «когда лучше…»,
// «какая локация…». На вопросы форс-вызов инструмента не нужен — достаточно
// честного ответа по сводке (хватит данных или нет); шаблон «переформулируйте»
// на такие вопросы не отвечает.
var infoIntent = regexp.MustCompile(`(?i)^(когда|как|какой|какая|какие|каких|где|куда|сколько|почему|зачем|что|стоит\s+ли|можно\s+ли|how|what|when|where|which|why|is|are|should|can)\b`)

// parseArgs разбирает строку аргументов tool-вызова (JSON) в карту.
func parseArgs(s string) map[string]any {
	var m map[string]any
	if s = strings.TrimSpace(s); s != "" {
		_ = json.Unmarshal([]byte(s), &m)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m
}

// llmMessage — сообщение из ответа llama-server.
type llmMessage struct {
	Role         string `json:"role"`
	Content      string `json:"content"`
	RawToolCalls any    `json:"-"`
	ToolCalls    []struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

// sanitizeFinal чистит финальный ответ модели от служебных артефактов.
func sanitizeFinal(reply string) string {
	var keep []string
	for _, line := range strings.Split(reply, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "{") || strings.Contains(t, `"action"`) ||
			strings.Contains(t, "НАБЛЮДЕНИЕ:") || strings.Contains(t, "HOST WATCHES") {
			continue
		}
		keep = append(keep, t)
	}
	return strings.Join(keep, "\n")
}

func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		var f float64
		fmt.Sscanf(strings.TrimSpace(x), "%g", &f)
		return f
	}
	return 0
}

// execAction выполняет действие из белого списка и возвращает наблюдение
// для модели. Никакой системы — только функции самого приложения.
func (a *Agent) execAction(name string, args map[string]any) string {
	a.mu.Lock()
	act := a.actions
	a.mu.Unlock()

	switch name {
	case "run_check":
		if act.RunCheck == nil {
			return T("obs_unavailable")
		}
		// база: последний известный замер
		a.mu.Lock()
		var baseline time.Time
		if a.history != nil {
			for _, e := range a.history() {
				if e.TS.After(baseline) {
					baseline = e.TS
				}
			}
		}
		a.mu.Unlock()
		act.RunCheck()
		// ждём завершения (проверка занимает 10–60 с), показывая прогресс
		waitStart := time.Now()
		deadline := waitStart.Add(90 * time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(2 * time.Second)
			a.setStage(T("st_tool", toolTitle("run_check")), "active",
				fmt.Sprintf(T("obs_wait"), int(time.Since(waitStart).Seconds())))
			a.mu.Lock()
			var fresh *dashEntry
			if a.history != nil {
				hist := a.history()
				for i := range hist {
					if hist[i].TS.After(baseline) && (fresh == nil || hist[i].TS.After(fresh.TS)) {
						fresh = &hist[i]
					}
				}
			}
			a.mu.Unlock()
			if fresh != nil {
				a.invalidateDigest()
				if fresh.Err != "" {
					return fmt.Sprintf(T("obs_check_err"), fresh.Err)
				}
				return fmt.Sprintf(T("obs_check_ok"),
					fresh.TS.Format("15:04"), fresh.Down, fresh.Up, fresh.Ping, fresh.Server)
			}
		}
		a.invalidateDigest()
		return T("obs_check_late")
	case "set_interval":
		if act.SetInterval == nil {
			return T("obs_unavailable")
		}
		m := int(num(args["minutes"]))
		if m < 5 || m > 180 {
			return fmt.Sprintf(T("obs_bad_interval"), m)
		}
		applied := act.SetInterval(m)
		a.invalidateDigest()
		return fmt.Sprintf(T("obs_interval_ok"), applied)
	case "set_download_mb":
		if act.SetDownloadMB == nil {
			return T("obs_unavailable")
		}
		v := int(num(args["mb"]))
		if v < 5 || v > 200 {
			return fmt.Sprintf(T("obs_bad_dl"), v)
		}
		applied := act.SetDownloadMB(v)
		a.invalidateDigest()
		return fmt.Sprintf(T("obs_dl_ok"), applied)
	case "set_upload_mb":
		if act.SetUploadMB == nil {
			return T("obs_unavailable")
		}
		v := int(num(args["mb"]))
		if v < 5 || v > 200 {
			return fmt.Sprintf(T("obs_bad_ul"), v)
		}
		applied := act.SetUploadMB(v)
		a.invalidateDigest()
		return fmt.Sprintf(T("obs_ul_ok"), applied)
	case "set_limits":
		if act.SetLimits == nil {
			return T("obs_unavailable")
		}
		bad, good := num(args["bad"]), num(args["good"])
		if bad <= 0 || good <= 0 || bad >= good {
			return fmt.Sprintf(T("obs_bad_limits"), bad, good)
		}
		ab, ag := act.SetLimits(bad, good)
		a.invalidateDigest()
		return fmt.Sprintf(T("obs_limits_ok"), ab, ag)
	case "set_net_rate":
		if act.SetNetRate == nil {
			return T("obs_unavailable")
		}
		ms := int(num(args["ms"]))
		allowed := map[int]bool{1000: true, 2000: true, 5000: true, 10000: true}
		if !allowed[ms] {
			return T("obs_bad_rate")
		}
		applied := act.SetNetRate(ms)
		a.invalidateDigest()
		return fmt.Sprintf(T("obs_rate_ok"), applied)
	case "export_archive":
		if act.ExportArchive == nil {
			return T("obs_unavailable")
		}
		a.setStage(T("st_tool", toolTitle("export_archive")), "active", T("arch_collect"))
		out, err := act.ExportArchive()
		if err != nil {
			return T("obs_refused") + err.Error()
		}
		return out
	case "import_archive":
		if act.ImportArchive == nil {
			return T("obs_unavailable")
		}
		mode, _ := args["mode"].(string)
		if strings.TrimSpace(mode) == "" {
			mode = "check"
		}
		p, _ := args["path"].(string)
		if strings.TrimSpace(p) == "" {
			return T("obs_need_path")
		}
		a.setStage(T("st_tool", toolTitle("import_archive")), "active", fmt.Sprintf(T("obs_mode"), mode))
		out, err := act.ImportArchive(p, mode)
		if err != nil {
			return T("obs_refused") + err.Error()
		}
		return out
	case "ping_host":
		if act.PingHost == nil {
			return T("obs_unavailable")
		}
		host, _ := args["host"].(string)
		if strings.TrimSpace(host) == "" {
			return T("obs_need_host")
		}
		a.setStage(T("st_tool", toolTitle("ping_host")), "active", host)
		out, err := act.PingHost(host)
		if err != nil {
			return T("obs_refused") + err.Error()
		}
		return out
	case "watch_host":
		if act.WatchHost == nil {
			return T("obs_unavailable")
		}
		host, _ := args["host"].(string)
		if strings.TrimSpace(host) == "" {
			return T("obs_need_host")
		}
		minutes := int(num(args["minutes"]))
		a.setStage(T("st_tool", toolTitle("watch_host")), "active", host)
		out, err := act.WatchHost(host, minutes)
		if err != nil {
			return T("obs_refused") + err.Error()
		}
		return out
	case "unwatch_host":
		if act.UnwatchHost == nil {
			return T("obs_unavailable")
		}
		host, _ := args["host"].(string)
		if strings.TrimSpace(host) == "" {
			return T("obs_need_host2")
		}
		a.setStage(T("st_tool", toolTitle("unwatch_host")), "active", host)
		out, err := act.UnwatchHost(host)
		if err != nil {
			return T("obs_refused") + err.Error()
		}
		return out
	case "unwatch_all":
		if act.UnwatchAll == nil {
			return T("obs_unavailable")
		}
		a.setStage(T("st_tool", toolTitle("unwatch_all")), "active", "")
		out, err := act.UnwatchAll()
		if err != nil {
			return T("obs_refused") + err.Error()
		}
		return out
	}
	return T("obs_unknown", name)
}

// invalidateDigest сбрасывает кэш сводки (после действий/новых замеров).
func (a *Agent) invalidateDigest() {
	a.mu.Lock()
	a.digestAt = time.Time{}
	a.mu.Unlock()
}

// callLLM — один проход к llama-server (OpenAI-совместимый /chat/completions).
func (a *Agent) callLLM(baseURL string, msgs []agentToolMsg, toolChoice string) (llmMessage, error) {
	var out struct {
		Choices []struct {
			Message llmMessage `json:"message"`
		} `json:"choices"`
	}
	payload := map[string]any{
		"messages":    msgs,
		"tools":       agentToolsFor(Lang()),
		"temperature": 0.3,
		"max_tokens":  400,
		"stream":      false,
		// LFM3 — «думающая» модель: выключаем блок размышлений,
		// иначе токены уходят в reasoning_content, а не в ответ.
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
	}
	if toolChoice != "" {
		payload["tool_choice"] = toolChoice // "required" — принудительный вызов
	}
	payloadBytes, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", baseURL+"/v1/chat/completions", bytes.NewReader(payloadBytes))
	if err != nil {
		return llmMessage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return llmMessage{}, fmt.Errorf(T("ag_llm_unreachable"), err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return llmMessage{}, fmt.Errorf(T("ag_llm_code"), resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, &out); err != nil || len(out.Choices) == 0 {
		return llmMessage{}, fmt.Errorf("%s", T("ag_llm_unexpected"))
	}
	m := out.Choices[0].Message
	// сырые tool_calls нужны для обратного рендера в wire (id/type/arguments)
	var raw struct {
		Choices []struct {
			Message struct {
				RawToolCalls any `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	_ = json.Unmarshal(body, &raw)
	if len(raw.Choices) > 0 {
		m.RawToolCalls = raw.Choices[0].Message.RawToolCalls
	}
	// «думающая» модель иногда протекает тегами размышлений в контент —
	// вырезаем блоки и одиночные теги
	m.Content = stripThink(m.Content)
	if m.Content == "" && len(m.ToolCalls) == 0 {
		m.Content = T("ag_empty_reply")
	}
	return m, nil
}

// thinkRe вырезает блок размышлений целиком.
var thinkRe = regexp.MustCompile(`(?s)<think>.*?</think>`)

func stripThink(s string) string {
	s = thinkRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "</think>", "")
	s = strings.ReplaceAll(s, "<think>", "")
	return strings.TrimSpace(s)
}

// toolTitle — человекочитаемое имя инструмента для «Agent Progress».
func toolTitle(name string) string {
	switch name {
	case "run_check":
		return T("tt_run_check")
	case "set_interval":
		return T("tt_set_interval")
	case "set_download_mb":
		return T("tt_set_download")
	case "set_upload_mb":
		return T("tt_set_upload")
	case "set_limits":
		return T("tt_set_limits")
	case "set_net_rate":
		return T("tt_set_net_rate")
	case "export_archive":
		return T("tt_export")
	case "import_archive":
		return T("tt_import")
	case "ping_host":
		return T("tt_ping")
	case "watch_host":
		return T("tt_watch")
	case "unwatch_host":
		return T("tt_unwatch")
	case "unwatch_all":
		return T("tt_unwatch_all")
	}
	return name
}

// Digest возвращает числовую сводку (кэш на минуту) — единый источник
// фактов и для «Выводов», и для модели.
func (a *Agent) Digest() string {
	a.mu.Lock()
	if a.history == nil || a.traffic == nil {
		a.mu.Unlock()
		return ""
	}
	if !a.digestAt.IsZero() && time.Since(a.digestAt) < time.Minute && a.digest != "" {
		d := a.digest
		a.mu.Unlock()
		return d
	}
	// снимаем всё нужное и отпускаем замок: провайдеры делают свои
	// вызовы с локами дашборда/монитора — вложенный a.mu запрещён
	mf, hist, traf, bad, good, wprov := a.meta, a.history, a.traffic, a.bad, a.good, a.watchProv
	a.mu.Unlock()
	var m agentMeta
	if mf != nil {
		m = mf()
	}
	digest, insights := buildDigest(hist(), traf(), bad, good, m)
	if wprov != nil {
		if jobs := wprov(); len(jobs) > 0 {
			parts := make([]string, len(jobs))
			for i, j := range jobs {
				parts[i] = fmt.Sprintf(T("dg_watch_item"), j.Host, j.IntervalMin)
			}
			digest += "\n" + T("dg_watch_intro") + strings.Join(parts, ", ")
		}
	}
	a.mu.Lock()
	a.digest, a.insights, a.digestAt = digest, insights, time.Now()
	a.mu.Unlock()
	return digest
}

// Insights — детерминированные «Выводы» (работают и без инициализации).
func (a *Agent) Insights() []string {
	a.Digest()
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, len(a.insights))
	copy(out, a.insights)
	return out
}

func (a *Agent) setReady(model, note string) {
	a.mu.Lock()
	a.status = agentStatus{Status: "ready", Model: model}
	a.mu.Unlock()
	if note != "" {
		fmt.Fprintf(os.Stderr, "agent: %s\n", note)
	}
}

func (a *Agent) setPhase(p string) {
	a.mu.Lock()
	a.status.Phase = p
	a.status.Prog = 0
	a.mu.Unlock()
}

// setStep фиксирует шаг инициализации (0 сервер · 1 модель · 2 запуск)
// и фазу без прогресса.
func (a *Agent) setStep(step int, phase string) {
	a.mu.Lock()
	a.status.Step = step
	a.status.Phase = phase
	a.status.Prog = 0
	a.mu.Unlock()
}

// setPhaseProg обновляет фазу с реальным прогрессом (0..1) — скачивание.
func (a *Agent) setPhaseProg(p string, frac float64) {
	a.mu.Lock()
	a.status.Phase = p
	a.status.Prog = frac
	a.mu.Unlock()
}

func (a *Agent) setError(msg string) {
	a.mu.Lock()
	a.status = agentStatus{Status: "error", Err: msg}
	a.mu.Unlock()
}

// agentHealthOK — жив ли llama-server и готов ли принимать запросы.
func agentHealthOK(baseURL string, timeout time.Duration) bool {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(baseURL + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	return resp.StatusCode == http.StatusOK
}

// modelDisplayName — короткое имя модели из пути к файлу.
func modelDisplayName(path string) string {
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

// buildDigest считает сводку по истории и трафику: строку — для модели
// и список «Выводов» — для страницы.
// metaFunc безопасно достаёт текущие настройки (nil-safe).
func (a *Agent) metaFunc() agentMeta {
	a.mu.Lock()
	f := a.meta
	a.mu.Unlock()
	if f == nil {
		return agentMeta{}
	}
	return f()
}

func buildDigest(history []dashEntry, traffic TrafficStore, bad, good float64, m agentMeta) (string, []string) {
	now := time.Now()
	mk := func(from time.Time) (n, errs, trunc int, downs, ups, pings, losses []float64, locs map[string][]float64) {
		locs = map[string][]float64{}
		for i := range history {
			e := &history[i]
			if e.TS.Before(from) {
				continue
			}
			n++
			if e.Err != "" {
				errs++
				continue
			}
			if e.Trunc {
				trunc++
			}
			downs = append(downs, e.Down)
			ups = append(ups, e.Up)
			pings = append(pings, e.Ping)
			losses = append(losses, e.Loss)
			loc := e.Server
			if loc == "" {
				loc = T("dg_unknown_loc")
			}
			locs[loc] = append(locs[loc], e.Down)
		}
		return
	}
	day := now.Add(-24 * time.Hour)
	yest := now.Add(-48 * time.Hour)
	week := now.Add(-7 * 24 * time.Hour)

	nD, _, _, downsD, upsD, pingsD, _, _ := mk(day)
	nW, errW, truncW, downsW, _, pingsW, lossesW, locsW := mk(week)
	prev := mkRange(history, yest, day) // предыдущие сутки для тренда

	avg := func(v []float64) float64 {
		if len(v) == 0 {
			return 0
		}
		s := 0.0
		for _, x := range v {
			s += x
		}
		return s / float64(len(v))
	}

	var b strings.Builder
	var ins []string

	if nW == 0 {
		b.WriteString(T("dg_no_data"))
		return b.String(), []string{T("dg_no_data_ins")}
	}

	// скорость
	if len(downsD) > 0 {
		fmt.Fprintln(&b, fmt.Sprintf(T("dg_day"),
			nD, avg(downsD), minOf(downsD), maxOf(downsD), avg(upsD), avg(pingsD)))
		ins = append(ins, fmt.Sprintf(T("dg_day_ins"),
			avg(downsD), minOf(downsD), maxOf(downsD), nD))
	}
	if len(prev) > 0 && len(downsD) > 0 {
		pa, ca := avg(prev), avg(downsD)
		if pa > 0 {
			d := (ca - pa) / pa * 100
			wd := T("dg_unchanged")
			switch {
			case d > 10:
				wd = fmt.Sprintf(T("dg_better"), d)
			case d < -10:
				wd = fmt.Sprintf(T("dg_worse"), -d)
			}
			fmt.Fprintln(&b, fmt.Sprintf(T("dg_trend"), ca, pa, wd))
			if wd != T("dg_unchanged") {
				ins = append(ins, fmt.Sprintf(T("dg_trend_ins"), wd))
			}
		}
	}

	// зоны и аномалии
	if len(downsW) > 0 {
		worst := 0
		for _, d := range downsW {
			if d < bad {
				worst++
			}
		}
		goodn := 0
		for _, d := range downsW {
			if d >= good {
				goodn++
			}
		}
		fmt.Fprintln(&b, fmt.Sprintf(T("dg_week"),
			len(downsW), 100*float64(goodn)/float64(len(downsW)), good, worst, bad, errW, truncW, avg(pingsW), avg(lossesW)))
		if worst > 0 {
			ins = append(ins, fmt.Sprintf(T("dg_anomalies"), worst, bad))
		}
		if errW > 0 {
			ins = append(ins, fmt.Sprintf(T("dg_errors"), errW))
		}
	}

	// лучшая локация
	if len(locsW) > 0 {
		type lc struct {
			name string
			avg  float64
			n    int
		}
		var list []lc
		for k, v := range locsW {
			list = append(list, lc{k, avg(v), len(v)})
		}
		sort.Slice(list, func(i, j int) bool { return list[i].avg > list[j].avg })
		b.WriteString(T("dg_locs"))
		for i, l := range list {
			if i >= 4 {
				break
			}
			fmt.Fprintf(&b, T("dg_loc_item"), l.name, l.avg, l.n)
		}
		b.WriteString("\n")
		ins = append(ins, fmt.Sprintf(T("dg_best_loc"),
			list[0].name, list[0].avg, list[0].n))
	}

	// лучший/худший час за 7 дней
	if len(downsW) > 0 {
		hsum := map[int]float64{}
		hn := map[int]int{}
		for i := range history {
			e := &history[i]
			if e.Err != "" || e.TS.Before(week) {
				continue
			}
			h := e.TS.Hour()
			hsum[h] += e.Down
			hn[h]++
		}
		var bh, wh int
		bestv, worstv := -1.0, -1.0
		first := true
		for h := 0; h < 24; h++ {
			if hn[h] == 0 {
				continue
			}
			v := hsum[h] / float64(hn[h])
			if first || v > bestv {
				bestv, bh = v, h
			}
			if first || v < worstv {
				worstv, wh = v, h
			}
			first = false
		}
		if !first && hn[bh] != hn[wh] {
			fmt.Fprintln(&b, fmt.Sprintf(T("dg_hours"), bh, bestv, wh, worstv))
			ins = append(ins, fmt.Sprintf(T("dg_hours_ins"), bh, bestv, wh, worstv))
		}
	}

	// полная почасовая картина: чтобы агент мог ответить на «когда лучше
	// качать большие файлы», а не только назвать два крайних часа.
	// Публикуем при достатке данных: ≥8 замеров за неделю и ≥4 часа
	if nW >= 8 {
		var hsum [24]float64
		var hn [24]int
		for i := range history {
			e := &history[i]
			if e.Err != "" || e.TS.Before(week) {
				continue
			}
			hsum[e.TS.Hour()] += e.Down
			hn[e.TS.Hour()]++
		}
		var hb strings.Builder
		hours := 0
		for h := 0; h < 24; h++ {
			if hn[h] > 0 {
				fmt.Fprintf(&hb, "; %02d:00 ↓%.0f (%d)", h, hsum[h]/float64(hn[h]), hn[h])
				hours++
			}
		}
		if hours >= 4 {
			b.WriteString(T("dg_hours_intro"))
			b.WriteString(hb.String())
			b.WriteString("\n")
		}
	}

	// трафик
	var dayDown, dayUp, monDown, monUp int64
	key := now.Format("2006-01-02")
	if v, ok := traffic.Days[key]; ok {
		dayDown, dayUp = v[0], v[1]
	}
	mkey := now.Format("2006-01")
	if v, ok := traffic.Months[mkey]; ok {
		monDown, monUp = v[0], v[1]
	}
	fmt.Fprintln(&b, fmt.Sprintf(T("dg_traffic"),
		fmtBytes(dayDown), fmtBytes(dayUp), fmtBytes(monDown), fmtBytes(monUp)))
	ins = append(ins, fmt.Sprintf(T("dg_traffic"),
		fmtBytes(dayDown), fmtBytes(dayUp), fmtBytes(monDown), fmtBytes(monUp)))

	// текущие настройки приложения
	if m.Interval > 0 {
		fmt.Fprintln(&b, fmt.Sprintf(T("dg_settings"),
			m.Interval, m.DownMB, m.UpMB, bad, good, m.RateMs))
	}

	// последняя проверка
	var last *dashEntry
	for i := range history {
		if history[i].Err == "" && (last == nil || history[i].TS.After(last.TS)) {
			last = &history[i]
		}
	}
	if last != nil {
		fmt.Fprintln(&b, fmt.Sprintf(T("dg_last"),
			last.TS.Format("15:04"), last.Down, last.Up, last.Ping, last.Server))
	}

	return b.String(), ins
}

// mk с двумя диапазонами для тренда: от prev до day.
// (вспомогательная перегрузка — отдельная функция, т.к. в Go нет дефолтных аргументов)
func mkRange(history []dashEntry, from, to time.Time) []float64 {
	var downs []float64
	for i := range history {
		e := &history[i]
		if e.Err != "" || e.TS.Before(from) || e.TS.After(to) {
			continue
		}
		downs = append(downs, e.Down)
	}
	return downs
}

func minOf(v []float64) float64 {
	m := v[0]
	for _, x := range v {
		if x < m {
			m = x
		}
	}
	return m
}

func maxOf(v []float64) float64 {
	m := v[0]
	for _, x := range v {
		if x > m {
			m = x
		}
	}
	return m
}
