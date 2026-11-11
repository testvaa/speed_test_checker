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
	a.status = agentStatus{Status: "starting", Phase: "проверяю llama-server", Step: -1}
	a.stopOnce = sync.Once{}
	exe, model, port, baseURL := a.exePath, a.modelPath, a.port, a.baseURL
	a.mu.Unlock()

	go func() {
		// 1. Уже работает? (например, остался с прошлого раза)
		if agentHealthOK(baseURL, 1200*time.Millisecond) {
			// короткая пауза: оверлей успевает показать «подключаюсь»,
			// иначе подхват проскакивает мимо анимации
			a.setStep(2, "подключаюсь к llama-server")
			time.Sleep(1500 * time.Millisecond)
			a.setReady(modelDisplayName(model), "подхвачен работающий llama-server")
			return
		}
		// 2. Чего-то нет — докачаем: сервер с GitHub, модель с HuggingFace.
		a.setStep(0, "проверяю файлы агента")
		if _, err := os.Stat(exe); err != nil {
			if err := a.fetchServer(exe); err != nil {
				a.failDL(ctx, "не удалось получить llama-server: ", err)
				return
			}
		}
		a.setStep(1, "проверяю модель")
		if _, err := os.Stat(model); err != nil {
			if err := a.fetchModel(model); err != nil {
				a.failDL(ctx, "не удалось получить модель: ", err)
				return
			}
		}
		// 3. Стартуем процесс (консольное окно скрыто).
		devArgs, gpuLabel := a.resolveGPU(exe)
		a.setStep(2, "запускаю llama-server ("+gpuLabel+")")
		args := []string{"-m", model,
			"--port", fmt.Sprint(port),
			"--ctx-size", "4096",
			"--jinja",
		}
		cmd := exec.Command(exe, append(args, devArgs...)...)
		cmd.SysProcAttr = hideConsoleSysProcAttr()
		if err := cmd.Start(); err != nil {
			a.setError("не удалось запустить llama-server: " + err.Error())
			return
		}
		a.mu.Lock()
		a.cmd = cmd
		a.mu.Unlock()

		// 4. Ждём готовности (модель грузится в память до ~минуты на CPU).
		a.setPhase("загружаю модель, это займёт до минуты")
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
				a.setError("llama-server завершился раньше готовности — проверьте пути и порт " + fmt.Sprint(port))
				return
			}
			time.Sleep(700 * time.Millisecond)
		}
		a.setError("llama-server не ответил за 3 минуты")
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
	if err := a.download(archURL, archPath, "скачиваю llama.cpp"); err != nil {
		return err
	}
	a.setPhase("распаковываю llama.cpp")
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
		return fmt.Errorf("в архиве нет %s", serverExeName())
	}
	return nil
}

// fetchModel скачивает GGUF-модель с HuggingFace.
func (a *Agent) fetchModel(model string) error {
	if err := os.MkdirAll(filepath.Dir(model), 0o755); err != nil {
		return err
	}
	return a.download(agentModelURL, model, "скачиваю модель")
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
				a.setPhaseProg(fmt.Sprintf("%s: %d из %d МБ", phase, soFar>>20, total>>20), frac)
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
		return fmt.Errorf("недокачано: %d из %d МБ", (done+got)>>20, total>>20)
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

// agentTools — описание инструментов для llama-server (нативный tool calling).
var agentTools = []map[string]any{
	{"type": "function", "function": map[string]any{
		"name": "run_check", "description": "Сделать замер скорости интернета прямо сейчас и вернуть результат",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{}}}},
	{"type": "function", "function": map[string]any{
		"name": "set_interval", "description": "Изменить период автопроверок в минутах (5–180)",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{
			"minutes": map[string]any{"type": "integer", "description": "минуты"}},
			"required": []string{"minutes"}}}},
	{"type": "function", "function": map[string]any{
		"name": "set_download_mb", "description": "Объём теста загрузки в МБ (5–200)",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{
			"mb": map[string]any{"type": "integer", "description": "МБ"}},
			"required": []string{"mb"}}}},
	{"type": "function", "function": map[string]any{
		"name": "set_upload_mb", "description": "Объём теста отдачи в МБ (5–200)",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{
			"mb": map[string]any{"type": "integer", "description": "МБ"}},
			"required": []string{"mb"}}}},
	{"type": "function", "function": map[string]any{
		"name": "set_limits", "description": "Границы зон оценки: «плохо» ниже bad, «хорошо» выше good, Мбит/с",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{
			"bad": map[string]any{"type": "number"}, "good": map[string]any{"type": "number"}},
			"required": []string{"bad", "good"}}}},
	{"type": "function", "function": map[string]any{
		"name": "set_net_rate", "description": "Частота обновления живого графика, мс: 1000, 2000, 5000 или 10000",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{
			"ms": map[string]any{"type": "integer"}},
			"required": []string{"ms"}}}},
	{"type": "function", "function": map[string]any{
		"name": "export_archive", "description": "Сохранить все данные мониторинга (историю замеров и трафик) в архивный файл рядом с config.json",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{}}}},
	{"type": "function", "function": map[string]any{
		"name": "import_archive", "description": "Загрузить данные из архивного .jsonl файла. Сначала вызывай с mode=check: если есть пересечение периодов — спроси пользователя «перезаписать совпавшие или пропустить их?», затем вызови ещё раз с выбранным mode",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "полный путь к архивному файлу"},
			"mode": map[string]any{"type": "string", "enum": []string{"check", "overwrite", "skip"},
				"description": "check — только проверить (по умолчанию); overwrite — перезаписать совпавшие; skip — пропустить совпавшие"}},
			"required": []string{"path"}}}},
	{"type": "function", "function": map[string]any{
		"name": "ping_host", "description": "Пинговать хост прямо сейчас (ICMP, 4 пакета) и вернуть статистику отклика: сколько ответило, среднее/мин/макс",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{
			"host": map[string]any{"type": "string", "description": "имя хоста или IP"}},
			"required": []string{"host"}}}},
	{"type": "function", "function": map[string]any{
		"name": "watch_host", "description": "Запустить фоновое наблюдение за хостом: пинг каждые N минут, статус виден в блоке «Наблюдение» на дашборде. Наблюдение переживает перезапуск приложения. Остановить — инструмент unwatch_host",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{
			"host":    map[string]any{"type": "string", "description": "имя хоста или IP"},
			"minutes": map[string]any{"type": "integer", "description": "период пинга в минутах (по умолчанию 5)"}},
			"required": []string{"host"}}}},
	{"type": "function", "function": map[string]any{
		"name": "unwatch_host", "description": "Остановить фоновое наблюдение за хостом",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{
			"host": map[string]any{"type": "string"}},
			"required": []string{"host"}}}},
	{"type": "function", "function": map[string]any{
		"name": "unwatch_all", "description": "Остановить сразу ВСЕ наблюдения за хостами (для «останови все наблюдения»)",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{}}}},
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
		return a.Msgs(), fmt.Errorf("агент не готов (%s)", st)
	}
	baseURL := a.baseURL
	a.mu.Unlock()

	// новый запрос — новый список шагов «Agent Progress»
	a.mu.Lock()
	a.stages = nil
	a.mu.Unlock()
	a.setStage("читаю сводку замеров", "active", "")
	digest := a.Digest()
	a.setStage("читаю сводку замеров", "done", "")

	wire := []agentToolMsg{
		{"role": "system", "content": "Ты — встроенный ИИ-агент мониторинга сети в приложении SpeedTestTray. " +
			"Отвечай на русском, кратко (2–6 предложений), дружелюбно и по делу, без Markdown-заголовков. " +
			"Единственный источник фактов — числовая сводка замеров, данная тебе отдельным сообщением. " +
			"Строго запрещено придумывать числа, сравнения и выводы, которых нет в сводке; " +
			"если данных не хватает — прямо скажи об этом. Никогда не пиши внутренние рассуждения — " +
			"только готовый ответ. Действия выполняются ТОЛЬКО инструментами, карта фраз: " +
			"«замер скорости / проверь сейчас» → run_check; «пингуй <хост>» → ping_host; " +
			"«следи / наблюдай / поставь на контроль / поставь задачу за <хост> [каждые N минут]» → watch_host; " +
			"«останови / отмени / перестань следить [за <хост> / все наблюдения]» → unwatch_host или unwatch_all; " +
			"«сохрани архив» → export_archive; «загрузи архив <путь>» → import_archive; " +
			"«поставь период N минут» → set_interval; «объём теста» → set_download_mb / set_upload_mb; " +
			"«границы плохо/хорошо» → set_limits; «частота обновления графика» → set_net_rate. " +
			"Для таких просьб вызывай инструмент и пересказывай его результат; " +
			"без вызова инструмента слов «готово», «запущено», «остановлено» не пиши."},
		{"role": "user", "content": "Числовая сводка замеров (единственный источник фактов):\n" + digest},
		{"role": "assistant", "content": "Принял сводку, работаю с этими данными."},
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
		step := "формулирую ответ"
		if iter == 0 {
			step = "думаю над ответом"
		}
		a.setStage(step, "active", "")
		msg, err := a.callLLM(baseURL, wire, "")
		if err != nil {
			// сервер умер, пока агент «был готов» — честно переводим в ошибку
			if strings.Contains(err.Error(), "недоступен") {
				a.setError("llama-server не отвечает — нажмите «Повторить»")
			}
			return a.Msgs(), err
		}
		if len(msg.ToolCalls) == 0 {
			if iter == 0 && !toolUsed && (actionIntent.MatchString(question) || actionWords.MatchString(msg.Content)) {
				a.setStage("уточняю действие", "active", "")
				// форс-вызов на чистом контексте: история диалога тянет
				// маленькую модель к «знакомому» run_check, поэтому в wire
				// оставляем только систему, сводку и само сообщение
				clean := append(wire[:3:3], wire[len(wire)-1])
				msg2, err2 := a.callLLM(baseURL, clean, "required")
				if err2 == nil && len(msg2.ToolCalls) > 0 {
					msg = msg2
				} else {
					// даже форс не помог — честный отказ вместо выдуманного успеха
					a.setStage(step, "done", "")
					final = "Команда похожа на действие, но вызвать инструмент не получилось. " +
						"Попробуйте переформулировать: «пингуй <хост>», «следи за <хост> каждые N минут», " +
						"«останови наблюдение за <хост>», «останови все наблюдения»."
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
		a.setStage(step, "done", "нужен инструмент")
		// вернуть assistant-сообщение с tool_calls, затем по одному tool-ответу
		wire = append(wire, agentToolMsg{"role": "assistant", "content": msg.Content, "tool_calls": msg.RawToolCalls})
		for _, tc := range msg.ToolCalls {
			toolName := toolTitle(tc.Function.Name)
			a.setStage("инструмент: "+toolName, "active", "")
			obs := a.execAction(tc.Function.Name, parseArgs(tc.Function.Arguments))
			a.setStage("инструмент: "+toolName, "done", obs)
			wire = append(wire, agentToolMsg{
				"role": "tool", "tool_call_id": tc.ID, "content": obs,
			})
		}
	}
	if strings.TrimSpace(final) == "" {
		final = "Готово — действие выполнено."
	}
	a.setStage("готово", "done", "")

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
// что нужен вызов инструмента ( ground truth — слова пользователя, а не
// пересказ маленькой модели)
var actionIntent = regexp.MustCompile(`(?i)^(давай\s+)?(пингуй|следи|наблюдай|останови|отмени|отменить|перестань|сделай|поставь|измени|смени|сохрани|загрузи|экспортируй|запусти)`)

// actionWords — словесные подтверждения действия в ответе модели; если
// финальный ответ содержит их, а инструмент за запрос ни разу не вызван,
// модель «рассказала» о действии словами — нужен повтор с required
var actionWords = regexp.MustCompile(`(?i)(инструмент|запус|запущ|останов|отмен|выполн|измен|сохран|начат|закрыт|активир)`)

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
			strings.Contains(t, "НАБЛЮДЕНИЕ:") {
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
			return "действие недоступно"
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
			a.setStage("инструмент: замер скорости", "active",
				fmt.Sprintf("жду замер… %d с", int(time.Since(waitStart).Seconds())))
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
					return fmt.Sprintf("Проверка завершилась ошибкой: %s", fresh.Err)
				}
				return fmt.Sprintf("Проверка завершена в %s: ↓ %.1f ↑ %.1f Мбит/с, пинг %.0f мс, сервер %s.",
					fresh.TS.Format("15:04"), fresh.Down, fresh.Up, fresh.Ping, fresh.Server)
			}
		}
		a.invalidateDigest()
		return "Проверка запущена, но не завершилась за 90 секунд — результат появится в истории позже."
	case "set_interval":
		if act.SetInterval == nil {
			return "действие недоступно"
		}
		m := int(num(args["minutes"]))
		if m < 5 || m > 180 {
			return fmt.Sprintf("отказ: период автопроверок должен быть 5–180 минут (пришло %d)", m)
		}
		applied := act.SetInterval(m)
		a.invalidateDigest()
		return fmt.Sprintf("Период автопроверок изменён на %d минут.", applied)
	case "set_download_mb":
		if act.SetDownloadMB == nil {
			return "действие недоступно"
		}
		v := int(num(args["mb"]))
		if v < 5 || v > 200 {
			return fmt.Sprintf("отказ: объём теста загрузки должен быть 5–200 МБ (пришло %d)", v)
		}
		applied := act.SetDownloadMB(v)
		a.invalidateDigest()
		return fmt.Sprintf("Объём теста загрузки: %d МБ.", applied)
	case "set_upload_mb":
		if act.SetUploadMB == nil {
			return "действие недоступно"
		}
		v := int(num(args["mb"]))
		if v < 5 || v > 200 {
			return fmt.Sprintf("отказ: объём теста отдачи должен быть 5–200 МБ (пришло %d)", v)
		}
		applied := act.SetUploadMB(v)
		a.invalidateDigest()
		return fmt.Sprintf("Объём теста отдачи: %d МБ.", applied)
	case "set_limits":
		if act.SetLimits == nil {
			return "действие недоступно"
		}
		bad, good := num(args["bad"]), num(args["good"])
		if bad <= 0 || good <= 0 || bad >= good {
			return fmt.Sprintf("отказ: нужны корректные границы, bad < good (пришло bad=%.1f good=%.1f)", bad, good)
		}
		ab, ag := act.SetLimits(bad, good)
		a.invalidateDigest()
		return fmt.Sprintf("Границы зон обновлены: «плохо» < %.0f Мбит/с, «хорошо» ≥ %.0f Мбит/с.", ab, ag)
	case "set_net_rate":
		if act.SetNetRate == nil {
			return "действие недоступно"
		}
		ms := int(num(args["ms"]))
		allowed := map[int]bool{1000: true, 2000: true, 5000: true, 10000: true}
		if !allowed[ms] {
			return "отказ: частота обновления — только 1000, 2000, 5000 или 10000 мс"
		}
		applied := act.SetNetRate(ms)
		a.invalidateDigest()
		return fmt.Sprintf("Частота обновления живого графика: %d мс.", applied)
	case "export_archive":
		if act.ExportArchive == nil {
			return "действие недоступно"
		}
		a.setStage("инструмент: экспорт архива", "active", "собираю данные")
		out, err := act.ExportArchive()
		if err != nil {
			return "отказ: " + err.Error()
		}
		return out
	case "import_archive":
		if act.ImportArchive == nil {
			return "действие недоступно"
		}
		mode, _ := args["mode"].(string)
		if strings.TrimSpace(mode) == "" {
			mode = "check"
		}
		p, _ := args["path"].(string)
		if strings.TrimSpace(p) == "" {
			return "отказ: укажите полный путь к архивному файлу"
		}
		a.setStage("инструмент: загрузка архива", "active", "режим "+mode)
		out, err := act.ImportArchive(p, mode)
		if err != nil {
			return "отказ: " + err.Error()
		}
		return out
	case "ping_host":
		if act.PingHost == nil {
			return "действие недоступно"
		}
		host, _ := args["host"].(string)
		if strings.TrimSpace(host) == "" {
			return "отказ: укажите хост (имя или IP)"
		}
		a.setStage("инструмент: пинг хоста", "active", host)
		out, err := act.PingHost(host)
		if err != nil {
			return "отказ: " + err.Error()
		}
		return out
	case "watch_host":
		if act.WatchHost == nil {
			return "действие недоступно"
		}
		host, _ := args["host"].(string)
		if strings.TrimSpace(host) == "" {
			return "отказ: укажите хост (имя или IP)"
		}
		minutes := int(num(args["minutes"]))
		a.setStage("инструмент: наблюдение за хостом", "active", host)
		out, err := act.WatchHost(host, minutes)
		if err != nil {
			return "отказ: " + err.Error()
		}
		return out
	case "unwatch_host":
		if act.UnwatchHost == nil {
			return "действие недоступно"
		}
		host, _ := args["host"].(string)
		if strings.TrimSpace(host) == "" {
			return "отказ: укажите хост"
		}
		a.setStage("инструмент: остановка наблюдения", "active", host)
		out, err := act.UnwatchHost(host)
		if err != nil {
			return "отказ: " + err.Error()
		}
		return out
	case "unwatch_all":
		if act.UnwatchAll == nil {
			return "действие недоступно"
		}
		a.setStage("инструмент: остановка всех наблюдений", "active", "")
		out, err := act.UnwatchAll()
		if err != nil {
			return "отказ: " + err.Error()
		}
		return out
	}
	return "неизвестное действие: " + name
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
		"tools":       agentTools,
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
		return llmMessage{}, fmt.Errorf("llama-server недоступен: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return llmMessage{}, fmt.Errorf("llama-server вернул %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, &out); err != nil || len(out.Choices) == 0 {
		return llmMessage{}, fmt.Errorf("неожиданный ответ llama-server")
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
		m.Content = "(пустой ответ модели)"
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
		return "замер скорости"
	case "set_interval":
		return "период автопроверок"
	case "set_download_mb":
		return "объём теста загрузки"
	case "set_upload_mb":
		return "объём теста отдачи"
	case "set_limits":
		return "границы зон"
	case "set_net_rate":
		return "частота обновления"
	case "export_archive":
		return "экспорт архива"
	case "import_archive":
		return "загрузка архива"
	case "ping_host":
		return "пинг хоста"
	case "watch_host":
		return "наблюдение за хостом"
	case "unwatch_host":
		return "остановка наблюдения"
	case "unwatch_all":
		return "остановка всех наблюдений"
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
				parts[i] = fmt.Sprintf("%s (каждые %d мин)", j.Host, j.IntervalMin)
			}
			digest += "\nНаблюдения за хостами (остановить можно инструментом unwatch_host/unwatch_all): " +
				strings.Join(parts, ", ")
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
				loc = "неизвестно"
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
		b.WriteString("Измерений пока нет.")
		ins = []string{"Пока нет измерений — нажмите «Проверить сейчас», чтобы собрать первые данные."}
		return b.String(), ins
	}

	// скорость
	if len(downsD) > 0 {
		fmt.Fprintf(&b, "За последние 24 ч: %d измерений, средняя ↓ %.1f Мбит/с (мин %.1f, макс %.1f), средняя ↑ %.1f, средний пинг %.0f мс.\n",
			nD, avg(downsD), minOf(downsD), maxOf(downsD), avg(upsD), avg(pingsD))
		ins = append(ins, fmt.Sprintf("За 24 ч: средняя ↓ %.1f Мбит/с (от %.1f до %.1f), проверок %d.",
			avg(downsD), minOf(downsD), maxOf(downsD), nD))
	}
	if len(prev) > 0 && len(downsD) > 0 {
		pa, ca := avg(prev), avg(downsD)
		if pa > 0 {
			d := (ca - pa) / pa * 100
			wd := "без изменений"
			switch {
			case d > 10:
				wd = fmt.Sprintf("на %.0f%% лучше", d)
			case d < -10:
				wd = fmt.Sprintf("на %.0f%% хуже", -d)
			}
			fmt.Fprintf(&b, "Тренд: средняя за сутки %.1f против %.1f Мбит/с сутками ранее (%s).\n", ca, pa, wd)
			if wd != "без изменений" {
				ins = append(ins, "Тренд: по сравнению с предыдущими сутками скорость "+wd+".")
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
		fmt.Fprintf(&b, "За 7 дней: %d успешных измерений (%.0f%% в зелёной зоне ≥%.0f, %d ниже плохой границы %.0f), ошибок %d, обрезанных тестов %d. Средний пинг %.0f мс, средние потери %.1f%%.\n",
			len(downsW), 100*float64(goodn)/float64(len(downsW)), good, worst, bad, errW, truncW, avg(pingsW), avg(lossesW))
		if worst > 0 {
			ins = append(ins, fmt.Sprintf("Аномалии: %d измерений за неделю ниже «плохой» границы %.0f Мбит/с.", worst, bad))
		}
		if errW > 0 {
			ins = append(ins, fmt.Sprintf("Ошибок проверок за неделю: %d.", errW))
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
		fmt.Fprintf(&b, "Локации за 7 дней: ")
		for i, l := range list {
			if i >= 4 {
				break
			}
			fmt.Fprintf(&b, "%s — средняя %.1f Мбит/с (%d замеров); ", l.name, l.avg, l.n)
		}
		b.WriteString("\n")
		ins = append(ins, fmt.Sprintf("Лучшая локация за неделю: %s — %.1f Мбит/с (%d замеров).",
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
			fmt.Fprintf(&b, "По часам за 7 дней: лучший %02d:00 — %.1f Мбит/с, худший %02d:00 — %.1f Мбит/с.\n", bh, bestv, wh, worstv)
			ins = append(ins, fmt.Sprintf("Лучший час за неделю: %02d:00 (↓ %.1f Мбит/с), худший: %02d:00 (↓ %.1f).", bh, bestv, wh, worstv))
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
	fmt.Fprintf(&b, "Трафик: сегодня ↓ %s ↑ %s, за месяц ↓ %s ↑ %s.\n",
		fmtBytes(dayDown), fmtBytes(dayUp), fmtBytes(monDown), fmtBytes(monUp))
	ins = append(ins, fmt.Sprintf("Трафик: сегодня ↓ %s ↑ %s, за месяц ↓ %s ↑ %s.",
		fmtBytes(dayDown), fmtBytes(dayUp), fmtBytes(monDown), fmtBytes(monUp)))

	// текущие настройки приложения
	if m.Interval > 0 {
		fmt.Fprintf(&b, "Настройки: автопроверка каждые %d мин; тест ↓%d/↑%d МБ; зоны: плохо < %.0f, хорошо ≥ %.0f Мбит/с; обновление графика %d мс.\n",
			m.Interval, m.DownMB, m.UpMB, bad, good, m.RateMs)
	}

	// последняя проверка
	var last *dashEntry
	for i := range history {
		if history[i].Err == "" && (last == nil || history[i].TS.After(last.TS)) {
			last = &history[i]
		}
	}
	if last != nil {
		fmt.Fprintf(&b, "Последняя проверка: %s — ↓ %.1f ↑ %.1f Мбит/с, пинг %.0f мс (%s).\n",
			last.TS.Format("15:04"), last.Down, last.Up, last.Ping, last.Server)
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
