package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

//go:embed dashboard.html
var dashboardHTML []byte

//go:embed agentorb.js
var agentOrbJS []byte

const (
	dashBasePort     = 8787             // первый кандидат; при занятости — следующие
	dashPortSpan     = 10               // всего пробуем портов: dashBasePort..+span-1
	dashSeriesCap    = 600              // точек живого графика (~20 мин при опросе раз в 2 с)
	dashSeriesWindow = 30 * time.Minute // окно живого графика
	dashHistCap      = 200              // сколько итогов проверок хранить в history.json
)

// dashPoint — одна точка живого графика скоростей.
type dashPoint struct {
	T    time.Time
	Down float64
	Up   float64
}

// dashEntry — сохранённый итог проверки для блока «История».
type dashEntry struct {
	TS     time.Time `json:"ts"`
	Down   float64   `json:"down"`
	Up     float64   `json:"up"`
	Ping   float64   `json:"ping"`
	Jitter float64   `json:"jitter"`
	Loss   float64   `json:"loss"`
	DurMs  int64     `json:"dur_ms"`
	Trunc  bool      `json:"trunc,omitempty"`
	Err    string    `json:"err,omitempty"`
	Server string    `json:"server,omitempty"` // город сервера Cloudflare (куда «пингуемся»)
	Colo   string    `json:"colo,omitempty"`   // IATA-код точки присутствия
	Net    string    `json:"net,omitempty"`    // AS-организация клиента (провайдер / VPN)
}

// Dashboard — встроенный веб-дашборд: страница и JSON-API на 127.0.0.1.
// Работает только на локальной машине, наружу не слушает.
type Dashboard struct {
	mu  sync.Mutex
	url string

	histPath string
	history  []dashEntry // новые в конце

	intervalMin int
	badMbps     float64
	goodMbps    float64
	netRateMs   int

	series  []dashPoint
	lastNet NetSnapshot

	checking   bool
	phase      string
	phaseFrac  float64
	checkStart time.Time

	proc  *ProcNet  // «кто потребляет интернет» для /api/procnet
	agent *Agent    // ИИ-агент (инициализация + чат)
	net   *NetMon   // монитор трафика (строка traffic архива)
	watch *WatchMon // фоновые наблюдения за хостами

	archMu sync.Mutex
	archSt archStatus // экспорт/импорт архива (одна операция за раз)

	onRun     func()
	onNetRate func(int)
	onLang    func(string)
}

// SetAgent подключает агента (вызывается один раз при старте приложения).
func (d *Dashboard) SetAgent(a *Agent) {
	d.mu.Lock()
	d.agent = a
	d.mu.Unlock()
}

// HistoryCopy возвращает копию истории замеров (для агента).
func (d *Dashboard) HistoryCopy() []dashEntry {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]dashEntry, len(d.history))
	copy(out, d.history)
	return out
}

// NewDashboard загружает history.json (если есть) рядом с config.json.
func NewDashboard(cfg *Config, histPath string) *Dashboard {
	d := &Dashboard{
		histPath:    histPath,
		intervalMin: cfg.IntervalMin,
		badMbps:     cfg.BadMbps,
		goodMbps:    cfg.GoodMbps,
		netRateMs:   nearestNetRateMs(cfg.NetRateMs),
		proc:        NewProcNet(),
	}
	if data, err := os.ReadFile(histPath); err == nil {
		_ = json.Unmarshal(data, &d.history) // повреждённый файл — без истории
	}
	return d
}

// SetInterval вызывается из приложения при смене интервала проверок.
func (d *Dashboard) SetInterval(min int) {
	d.mu.Lock()
	d.intervalMin = min
	d.mu.Unlock()
}

// SetLimits вызывается при смене зон оценки (плохо/хорошо).
func (d *Dashboard) SetLimits(bad, good float64) {
	d.mu.Lock()
	d.badMbps, d.goodMbps = bad, good
	d.mu.Unlock()
}

// SetNetRate вызывается из приложения при смене частоты опроса сети.
func (d *Dashboard) SetNetRate(ms int) {
	d.mu.Lock()
	d.netRateMs = ms
	d.mu.Unlock()
}

// Start поднимает сервер на 127.0.0.1 (порты 8787 и следующие за ним).
// Не блокирует; при неудаче дашборд просто выключен.
func (d *Dashboard) Start() {
	for port := dashBasePort; port < dashBasePort+dashPortSpan; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		d.mu.Lock()
		d.url = fmt.Sprintf("http://127.0.0.1:%d", port)
		d.mu.Unlock()

		mux := http.NewServeMux()
		mux.HandleFunc("/", d.servePage)
		mux.HandleFunc("/orb.js", d.serveOrbJS)
		mux.HandleFunc("/api/state", d.serveState)
		mux.HandleFunc("/api/run", d.serveRun)
		mux.HandleFunc("/api/netrate", d.serveNetRate)
		mux.HandleFunc("/api/lang", d.serveLang)
		mux.HandleFunc("/api/procnet", d.serveProcNet)
		mux.HandleFunc("/api/agent", d.serveAgent)
		mux.HandleFunc("/api/agent/init", d.serveAgentInit)
		mux.HandleFunc("/api/agent/stop", d.serveAgentStop)
		mux.HandleFunc("/api/agent/chat", d.serveAgentChat)
		mux.HandleFunc("/api/archive/export", d.serveArchiveExport)
		mux.HandleFunc("/api/archive/import", d.serveArchiveImport)
		mux.HandleFunc("/api/archive/status", d.serveArchiveStatus)
		mux.HandleFunc("/api/history/clear", d.serveHistoryClear)
		// DNS-rebinding: слушаем только 127.0.0.1, но сторонний сайт может
		// резолвить свой домен на 127.0.0.1 и слать запросы из браузера —
		// принимаем только ожидаемый Host
		go func() {
			_ = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Host != fmt.Sprintf("127.0.0.1:%d", port) && r.Host != "localhost" {
					http.Error(w, T("err_local_only"), http.StatusForbidden)
					return
				}
				mux.ServeHTTP(w, r)
			}))
		}()
		return
	}
	fmt.Fprintln(os.Stderr, T("dash_ports_busy", dashBasePort, dashBasePort+dashPortSpan-1))
}

// URL возвращает адрес дашборда (пустая строка — сервер не запущен).
func (d *Dashboard) URL() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.url
}

// PushNet добавляет точку живого графика; вызывается из netLoop раз в 2 с.
func (d *Dashboard) PushNet(s NetSnapshot) {
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastNet = s
	d.series = append(d.series, dashPoint{T: now, Down: s.DownMbps, Up: s.UpMbps})
	// держим окно ~dashSeriesWindow и не больше dashSeriesCap точек
	cutoff := now.Add(-dashSeriesWindow)
	i := 0
	for ; i < len(d.series); i++ {
		if d.series[i].T.After(cutoff) {
			break
		}
	}
	if i > 0 {
		d.series = d.series[i:]
	}
	if len(d.series) > dashSeriesCap {
		d.series = d.series[len(d.series)-dashSeriesCap:]
	}
}

// SetChecking включает/выключает статус «идёт проверка».
func (d *Dashboard) SetChecking(v bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.checking = v
	d.phase, d.phaseFrac = "", 0
	if v {
		d.checkStart = time.Now()
	}
}

// SetProgress продвигает прогресс текущей фазы проверки.
func (d *Dashboard) SetProgress(phase string, frac float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.phase, d.phaseFrac = phase, frac
}

// ApplyResult дописывает итог проверки в историю и сохраняет файл.
func (d *Dashboard) ApplyResult(res *Result) {
	if res == nil {
		return
	}
	d.mu.Lock()
	dur := int64(0)
	if !d.checkStart.IsZero() {
		dur = time.Since(d.checkStart).Milliseconds()
		d.checkStart = time.Time{}
	}
	d.history = append(d.history, dashEntry{
		TS: res.Timestamp, Down: res.DownloadMbps, Up: res.UploadMbps,
		Ping: res.PingMs, Jitter: res.JitterMs, Loss: res.LossPct,
		DurMs: dur, Trunc: res.Truncated, Err: res.Err,
		Server: res.ServerCity, Colo: res.Colo, Net: res.Network,
	})
	if len(d.history) > dashHistCap {
		d.history = d.history[len(d.history)-dashHistCap:]
	}
	d.mu.Unlock()
	d.saveHistory()
}

// saveHistory пишет history.json атомарно (tmp + rename), как traffic.json.
func (d *Dashboard) saveHistory() {
	if d.histPath == "" {
		return // тесты: история только в памяти
	}
	d.mu.Lock()
	data, err := json.MarshalIndent(d.history, "", "  ")
	d.mu.Unlock()
	if err != nil {
		return
	}
	tmp := d.histPath + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, d.histPath)
}

func (d *Dashboard) servePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(dashboardHTML)
}

// serveOrbJS отдаёт движок орба (WebGL-«человечек» агента).
func (d *Dashboard) serveOrbJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(agentOrbJS)
}

// dashState — снимок всего состояния для /api/state.
type dashState struct {
	Now         string       `json:"now"`
	URL         string       `json:"url"`
	Checking    bool         `json:"checking"`
	Phase       string       `json:"phase"`
	Frac        float64      `json:"frac"`
	Net         NetSnapshot  `json:"net"`
	Series      [][3]float64 `json:"series"` // [unixMilli, ↓ Мбит/с, ↑ Мбит/с]
	History     []dashEntry  `json:"history"`
	IntervalMin int          `json:"intervalMin"`
	BadMbps     float64      `json:"badMbps"`
	GoodMbps    float64      `json:"goodMbps"`
	NetRateMs   int          `json:"netRateMs"`
	Lang        string       `json:"lang"`
	Watch       []WatchJob   `json:"watch,omitempty"`
	Agent       *agentStatus `json:"agent,omitempty"`
}

func (d *Dashboard) serveState(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	st := dashState{
		Now:         time.Now().Format(time.RFC3339),
		URL:         d.url,
		Checking:    d.checking,
		Phase:       d.phase,
		Frac:        d.phaseFrac,
		Net:         d.lastNet,
		Series:      make([][3]float64, 0, len(d.series)),
		History:     d.history,
		IntervalMin: d.intervalMin,
		BadMbps:     d.badMbps,
		GoodMbps:    d.goodMbps,
		NetRateMs:   d.netRateMs,
		Lang:        Lang(),
	}
	for _, p := range d.series {
		st.Series = append(st.Series, [3]float64{float64(p.T.UnixMilli()), p.Down, p.Up})
	}
	if d.agent != nil {
		as := d.agent.Status()
		st.Agent = &as
	}
	if d.watch != nil {
		st.Watch = d.watch.List()
	}
	d.mu.Unlock()
	if st.History == nil {
		st.History = []dashEntry{}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(st)
}

// serveRun запускает проверку по кнопке на странице.
func (d *Dashboard) serveRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, T("err_post_only"), http.StatusMethodNotAllowed)
		return
	}
	d.mu.Lock()
	run, checking := d.onRun, d.checking
	d.mu.Unlock()
	if checking {
		http.Error(w, T("err_check_running"), http.StatusConflict)
		return
	}
	if run == nil {
		http.Error(w, T("err_unavailable"), http.StatusServiceUnavailable)
		return
	}
	run()
	w.WriteHeader(http.StatusNoContent)
}

// serveHistoryClear удаляет всю сохранённую историю измерений
// (кнопка «Очистить» на странице, после подтверждения).
func (d *Dashboard) serveHistoryClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, T("err_post_only"), http.StatusMethodNotAllowed)
		return
	}
	d.mu.Lock()
	d.history = []dashEntry{}
	d.mu.Unlock()
	d.saveHistory()
	w.WriteHeader(http.StatusNoContent)
}

// serveNetRate меняет частоту опроса сети/обновления живого графика
// (селектор «обновление» в подвале графика).
func (d *Dashboard) serveNetRate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, T("err_post_only"), http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Ms int `json:"ms"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, T("err_bad_json"), http.StatusBadRequest)
		return
	}
	d.mu.Lock()
	fn := d.onNetRate
	d.mu.Unlock()
	if fn == nil {
		http.Error(w, T("err_unavailable"), http.StatusServiceUnavailable)
		return
	}
	fn(nearestNetRateMs(req.Ms))
	w.WriteHeader(http.StatusNoContent)
}

// serveLang переключает язык интерфейса (кнопка EN/RU на дашборде).
func (d *Dashboard) serveLang(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, T("err_post_only"), http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Lang string `json:"lang"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, T("err_bad_json"), http.StatusBadRequest)
		return
	}
	if req.Lang != LangRu && req.Lang != LangEn {
		http.Error(w, T("err_bad_lang"), http.StatusBadRequest)
		return
	}
	d.mu.Lock()
	fn := d.onLang
	d.mu.Unlock()
	if fn == nil {
		http.Error(w, T("err_unavailable"), http.StatusServiceUnavailable)
		return
	}
	fn(req.Lang)
	w.WriteHeader(http.StatusNoContent)
}

// serveProcNet — «кто потребляет интернет»: процессы с активными
// соединениями, оценка их активности и список соединений.
func (d *Dashboard) serveProcNet(w http.ResponseWriter, r *http.Request) {
	resp := d.proc.Collect()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(resp)
}

// agentOf возвращает агента или пишет 503.
func (d *Dashboard) agentOf(w http.ResponseWriter) *Agent {
	d.mu.Lock()
	a := d.agent
	d.mu.Unlock()
	if a == nil {
		http.Error(w, T("err_agent_unavailable"), http.StatusServiceUnavailable)
		return nil
	}
	return a
}

// serveAgent — состояние агента: статус, «Выводы», история чата.
func (d *Dashboard) serveAgent(w http.ResponseWriter, r *http.Request) {
	a := d.agentOf(w)
	if a == nil {
		return
	}
	resp := struct {
		agentStatus
		Insights []string     `json:"insights"`
		Msgs     []chatMsg    `json:"msgs"`
		Stages   []agentStage `json:"stages"`
	}{
		agentStatus: a.Status(),
		Insights:    a.Insights(),
		Msgs:        a.Msgs(),
		Stages:      a.Stages(),
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(resp)
}

// serveAgentInit — кнопка «Инициализировать»: запуск локального LLM.
func (d *Dashboard) serveAgentInit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, T("err_post_only"), http.StatusMethodNotAllowed)
		return
	}
	a := d.agentOf(w)
	if a == nil {
		return
	}
	a.Init()
	w.WriteHeader(http.StatusAccepted)
}

// serveAgentStop — «выключить»: остановить llama-server.
func (d *Dashboard) serveAgentStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, T("err_post_only"), http.StatusMethodNotAllowed)
		return
	}
	a := d.agentOf(w)
	if a == nil {
		return
	}
	a.Stop()
	w.WriteHeader(http.StatusNoContent)
}

// serveAgentChat — вопрос к агенту; возвращает всю ветку диалога.
func (d *Dashboard) serveAgentChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, T("err_post_only"), http.StatusMethodNotAllowed)
		return
	}
	a := d.agentOf(w)
	if a == nil {
		return
	}
	var req struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&req); err != nil {
		http.Error(w, T("err_bad_json"), http.StatusBadRequest)
		return
	}
	msgs, err := a.Chat(req.Message)
	if err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error(), "msgs": msgs})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"msgs": msgs})
}

// historyPath кладёт history.json в тот же каталог, что и config.json.
func historyPath(cfgPath string) string {
	return filepath.Join(filepath.Dir(cfgPath), "history.json")
}

// openBrowser открывает URL в браузере по умолчанию.
func openBrowser(url string) {
	switch runtime.GOOS {
	case "darwin":
		_ = exec.Command("open", url).Start()
	case "linux":
		_ = exec.Command("xdg-open", url).Start()
	default:
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	}
}
