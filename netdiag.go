package main

// Анализатор сети: одна диагностика за раз. Фазы: окружение → пинг-профиль
// (шлюз + публичные хосты) → DNS → bufferbloat (задержка под нагрузкой) →
// выводы. Результаты публикуются в общий снимок по мере поступления; стоп
// сохраняет частичный результат. Пороги и правила — netdiag_rules.go,
// платформозависимые части (маршрут по умолчанию, Wi-Fi) — netdiag_*.go.

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	ndIdle    = "idle"
	ndRunning = "running"
	ndDone    = "done"
	ndStopped = "stopped"
	ndError   = "error"
)

// порядок и веса фаз в общем прогрессе 0..1 (сумма = 1)
var ndPhaseOrder = []string{"env", "ping", "dns", "bloat", "rules"}

var ndPhaseWeight = map[string]float64{
	"env": 0.05, "ping": 0.25, "dns": 0.20, "bloat": 0.45, "rules": 0.05,
}

// ndEnv — окружение: интерфейс, MTU, шлюз, Wi-Fi.
type ndEnv struct {
	Iface     string `json:"iface,omitempty"`
	MTU       int    `json:"mtu,omitempty"`
	Gateway   string `json:"gateway,omitempty"`
	Wifi      bool   `json:"wifi"`
	SSID      string `json:"ssid,omitempty"`
	SignalPct int    `json:"signalPct,omitempty"` // качество сигнала netsh, %
}

// ndPingHost — пинг-профиль одного хоста.
type ndPingHost struct {
	Host     string  `json:"host"`
	Scope    string  `json:"scope"` // "gw" | "net"
	Sent     int     `json:"sent"`
	Recv     int     `json:"recv"`
	LossPct  float64 `json:"lossPct"`
	MinMs    float64 `json:"minMs"`
	AvgMs    float64 `json:"avgMs"`
	MaxMs    float64 `json:"maxMs"`
	JitterMs float64 `json:"jitterMs"` // средний |Δ| соседних проб (RFC 3550 §6.4.1)
	Err      string  `json:"err,omitempty"`
}

// ndDNSProbe — один резолвер (Label "system" — системный).
type ndDNSProbe struct {
	Label  string  `json:"label"`
	Probes int     `json:"probes"`
	Done   int     `json:"done"`
	OK     int     `json:"ok"`
	AvgMs  float64 `json:"avgMs"`
	Err    string  `json:"err,omitempty"`
}

// ndBloat — задержка под нагрузкой (bufferbloat).
type ndBloat struct {
	IdleMs    float64 `json:"idleMs"`
	DownMs    float64 `json:"downMs"`
	UpMs      float64 `json:"upMs"`
	AddDownMs float64 `json:"addDownMs"`
	AddUpMs   float64 `json:"addUpMs"`
	Grade     string  `json:"grade"` // A..F
	DownMbps  float64 `json:"downMbps,omitempty"` // попутная скорость нагрузки
	UpMbps    float64 `json:"upMbps,omitempty"`
	Err       string  `json:"err,omitempty"`
}

// ndRec — рекомендация: код (текст рендерит фронтенд из i18n, поэтому
// язык переключается без перезапуска диагностики) и важность.
type ndRec struct {
	Code string         `json:"code"`
	Sev  int            `json:"sev"` // 3 плохо, 2 внимание, 1 норма
	Args map[string]any `json:"args,omitempty"`
}

// netDiagState — снимок для /api/netdiag.
type netDiagState struct {
	State     string       `json:"state"`
	Phase     string       `json:"phase,omitempty"`
	Frac      float64      `json:"frac"`
	ElapsedMs int64        `json:"elapsedMs"`
	Err       string       `json:"err,omitempty"`
	Env       *ndEnv       `json:"env,omitempty"`
	Ping      []ndPingHost `json:"ping,omitempty"`
	DNS       []ndDNSProbe `json:"dns,omitempty"`
	Bloat     *ndBloat     `json:"bloat,omitempty"`
	Score     int          `json:"score"`
	Recs      []ndRec      `json:"recs,omitempty"`
	LastRun   string       `json:"lastRun,omitempty"`
}

// NetDiag — контроллер диагностики; снимок отдаётся под мьютексом,
// отмена — кооперативная (флаг + отмена контекста фазы).
type NetDiag struct {
	mu        sync.Mutex
	state     string
	phase     string
	phaseFrac float64
	started   time.Time
	durMs     int64
	lastRun   time.Time
	err       string

	env   *ndEnv
	ping  []ndPingHost
	dns   []ndDNSProbe
	bloat *ndBloat
	score int
	recs  []ndRec

	stopping  atomic.Bool
	curCancel context.CancelFunc

	hist func() []dashEntry // история замеров для правила «падение скорости»
	busy func() bool        // внешняя занятость (идёт замер скорости)
}

// NewNetDiag создаёт контроллер в состоянии idle.
func NewNetDiag() *NetDiag {
	return &NetDiag{state: ndIdle}
}

// SetHistory подключает источник истории замеров.
func (nd *NetDiag) SetHistory(fn func() []dashEntry) { nd.hist = fn }

// SetBusy подключает проверку внешней занятости (замер скорости).
func (nd *NetDiag) SetBusy(fn func() bool) { nd.busy = fn }

// Running — идёт ли диагностика прямо сейчас.
func (nd *NetDiag) Running() bool {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.state == ndRunning
}

// StateWord — текущее состояние одним словом (для /api/state).
func (nd *NetDiag) StateWord() string {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.state
}

// Start запускает диагностику в фоне; ошибки — локализованные тексты
// для HTTP-ответа (409).
func (nd *NetDiag) Start() error {
	nd.mu.Lock()
	if nd.state == ndRunning {
		nd.mu.Unlock()
		return fmt.Errorf("%s", T("nd_err_running"))
	}
	nd.mu.Unlock()
	if nd.busy != nil && nd.busy() {
		return fmt.Errorf("%s", T("nd_err_speedtest"))
	}

	nd.mu.Lock()
	nd.state = ndRunning
	nd.phase = "env"
	nd.phaseFrac = 0
	nd.started = time.Now()
	nd.durMs = 0
	nd.err = ""
	nd.env, nd.ping, nd.dns, nd.bloat = nil, nil, nil, nil
	nd.score, nd.recs = 0, nil
	nd.stopping.Store(false)
	nd.mu.Unlock()

	go nd.run()
	return nil
}

// Stop просит диагностику остановиться: частичный результат сохраняется.
func (nd *NetDiag) Stop() {
	nd.stopping.Store(true)
	nd.mu.Lock()
	c := nd.curCancel
	nd.mu.Unlock()
	if c != nil {
		c() // прервать текущую фазу (нагрузку) немедленно
	}
}

func (nd *NetDiag) stopped() bool { return nd.stopping.Load() }

func (nd *NetDiag) setPhase(phase string) {
	nd.mu.Lock()
	nd.phase, nd.phaseFrac = phase, 0
	nd.mu.Unlock()
}

func (nd *NetDiag) setFrac(phase string, frac float64) {
	nd.mu.Lock()
	if nd.phase == phase {
		nd.phaseFrac = frac
	}
	nd.mu.Unlock()
}

func (nd *NetDiag) setCancel(c context.CancelFunc) {
	nd.mu.Lock()
	nd.curCancel = c
	nd.mu.Unlock()
}

// Snapshot — копия состояния для /api/netdiag (частичные данные видны
// во время работы).
func (nd *NetDiag) Snapshot() netDiagState {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	st := netDiagState{
		State: nd.state, Phase: nd.phase, Err: nd.err,
		Env:   nd.env,
		Ping:  append([]ndPingHost(nil), nd.ping...),
		DNS:   append([]ndDNSProbe(nil), nd.dns...),
		Bloat: nd.bloat,
		Score: nd.score,
		Recs:  append([]ndRec(nil), nd.recs...),
	}
	if !nd.lastRun.IsZero() {
		st.LastRun = nd.lastRun.Format(time.RFC3339)
	}
	if nd.state == ndRunning {
		st.ElapsedMs = time.Since(nd.started).Milliseconds()
		frac := 0.0
		for _, p := range ndPhaseOrder {
			if p == nd.phase {
				frac += ndPhaseWeight[p] * nd.phaseFrac
				break
			}
			frac += ndPhaseWeight[p]
		}
		st.Frac = math.Min(frac, 1)
	} else {
		st.ElapsedMs = nd.durMs
		st.Frac = 1
	}
	return st
}

// run — последовательность фаз; стоп/паника не теряют накопленное.
func (nd *NetDiag) run() {
	defer func() {
		if r := recover(); r != nil {
			nd.finish(ndError, fmt.Sprintf("netdiag: panic: %v", r))
		}
	}()
	nd.phaseEnv()
	if nd.stopped() {
		nd.finish(ndStopped, "")
		return
	}
	nd.phasePing()
	if nd.stopped() {
		nd.finish(ndStopped, "")
		return
	}
	nd.phaseDNS()
	if nd.stopped() {
		nd.finish(ndStopped, "")
		return
	}
	nd.phaseBloat()
	if nd.stopped() {
		nd.finish(ndStopped, "")
		return
	}
	nd.setPhase("rules")
	nd.finish(ndDone, "")
}

// finish фиксирует итог: частичный результат тоже прогоняется через
// правила (что успели — то оценили).
func (nd *NetDiag) finish(state, errMsg string) {
	var score int
	var recs []ndRec
	if nd.anyData() {
		score, recs = analyze(nd.collectMetrics())
	}
	nd.mu.Lock()
	nd.state = state
	nd.err = errMsg
	nd.durMs = time.Since(nd.started).Milliseconds()
	nd.lastRun = time.Now()
	nd.score, nd.recs = score, recs
	nd.curCancel = nil
	nd.mu.Unlock()
}

func (nd *NetDiag) anyData() bool {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.env != nil || len(nd.ping) > 0 || len(nd.dns) > 0 || nd.bloat != nil
}

// ── фаза: окружение ─────────────────────────────────────────────────

func (nd *NetDiag) phaseEnv() {
	nd.setPhase("env")
	env := &ndEnv{}
	ifaceName, gw, mtu, err := ndRouteInfo()
	if err == nil {
		env.Iface, env.Gateway, env.MTU = ifaceName, gw, mtu
	}
	if ssid, pct, ok := ndWifiInfo(); ok {
		env.Wifi, env.SSID, env.SignalPct = true, ssid, pct
	}
	nd.mu.Lock()
	nd.env = env
	nd.mu.Unlock()
	nd.setFrac("env", 1)
}

// ── фаза: пинг-профиль ──────────────────────────────────────────────

const ndPingProbes = 8

func (nd *NetDiag) phasePing() {
	nd.setPhase("ping")
	type hostDef struct{ host, scope string }
	hosts := []hostDef{{"1.1.1.1", "net"}, {"8.8.8.8", "net"}}
	nd.mu.Lock()
	if nd.env != nil && nd.env.Gateway != "" {
		hosts = append(hosts, hostDef{nd.env.Gateway, "gw"})
	}
	nd.mu.Unlock()

	rows := make([]ndPingHost, len(hosts))
	for i, h := range hosts {
		rows[i] = ndPingHost{Host: h.host, Scope: h.scope}
	}
	nd.mu.Lock()
	nd.ping = rows
	nd.mu.Unlock()

	total := int64(len(hosts) * ndPingProbes)
	var doneCount atomic.Int64
	var wg sync.WaitGroup
	for i := range hosts {
		wg.Add(1)
		go func(i int, host string) {
			defer wg.Done()
			var samples []float64
			for p := 0; p < ndPingProbes && !nd.stopped(); p++ {
				rtt, err := pingOnce(host, time.Second)
				nd.mu.Lock()
				r := nd.ping[i]
				r.Sent = p + 1
				if err == nil {
					r.Recv++
					samples = append(samples, float64(rtt))
					r.MinMs, r.AvgMs, r.MaxMs, r.JitterMs = ndPingStats(samples)
				} else if r.Err == "" {
					r.Err = err.Error()
				}
				r.LossPct = float64(r.Sent-r.Recv) / float64(r.Sent) * 100
				nd.ping[i] = r
				nd.mu.Unlock()
				doneCount.Add(1)
				nd.setFrac("ping", float64(doneCount.Load())/float64(total))
				time.Sleep(180 * time.Millisecond)
			}
			nd.setFrac("ping", float64(doneCount.Load())/float64(total))
		}(i, hosts[i].host)
	}
	wg.Wait()
	nd.setFrac("ping", 1)
}

// ndPingStats — мин/сред/макс и джиттер по выборке RTT.
func ndPingStats(samples []float64) (min, avg, max, jitter float64) {
	if len(samples) == 0 {
		return 0, 0, 0, 0
	}
	min, max = samples[0], samples[0]
	var sum float64
	for _, v := range samples {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
		sum += v
	}
	avg = sum / float64(len(samples))
	return min, avg, max, ndJitter(samples)
}

// ndJitter — средний |Δ| между соседними пробами (RFC 3550 §6.4.1).
func ndJitter(samples []float64) float64 {
	if len(samples) < 2 {
		return 0
	}
	var sum float64
	for i := 1; i < len(samples); i++ {
		sum += math.Abs(samples[i] - samples[i-1])
	}
	return sum / float64(len(samples)-1)
}

// ── фаза: DNS ───────────────────────────────────────────────────────

// разные домены по кругу — чтобы кеши не приукрашивали тайминги
var ndDNSDomains = []string{
	"www.google.com", "www.cloudflare.com", "www.wikipedia.org",
	"github.com", "www.youtube.com",
}

const ndDNSProbes = 5

// ndDNSDef — один резолвер фазы DNS (server "" — системный).
type ndDNSDef struct {
	label  string
	server string
}

func (nd *NetDiag) phaseDNS() {
	nd.setPhase("dns")
	defs := []ndDNSDef{
		{"system", ""},
		{"1.1.1.1", "1.1.1.1:53"},
		{"8.8.8.8", "8.8.8.8:53"},
	}
	rows := make([]ndDNSProbe, len(defs))
	for i, d := range defs {
		rows[i] = ndDNSProbe{Label: d.label, Probes: ndDNSProbes}
	}
	nd.mu.Lock()
	nd.dns = rows
	nd.mu.Unlock()

	var wg sync.WaitGroup
	for i := range defs {
		wg.Add(1)
		go func(i int, d ndDNSDef) {
			defer wg.Done()
			nd.dnsProbe(i, d)
		}(i, defs[i])
	}
	wg.Wait()
	nd.setFrac("dns", 1)
}

// dnsProbe прогоняет probes запросов через один резолвер, публикуя
// промежуточный результат в строку i.
func (nd *NetDiag) dnsProbe(i int, d ndDNSDef) {
	var resolver *net.Resolver
	if d.server == "" {
		resolver = net.DefaultResolver
	} else {
		resolver = &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, "udp", d.server)
			},
		}
	}
	var sum float64
	for p := 0; p < ndDNSProbes && !nd.stopped(); p++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		t0 := time.Now()
		_, err := resolver.LookupHost(ctx, ndDNSDomains[p%len(ndDNSDomains)])
		cancel()
		ms := time.Since(t0).Seconds() * 1000
		nd.mu.Lock()
		r := nd.dns[i]
		r.Done = p + 1
		if err == nil {
			r.OK++
			sum += ms
			r.AvgMs = sum / float64(r.OK)
			r.Err = ""
		} else if r.Err == "" {
			r.Err = err.Error()
		}
		nd.dns[i] = r
		nd.mu.Unlock()
	}
}

// ── фаза: bufferbloat ───────────────────────────────────────────────

// нагрузка: 9 с загрузки и 9 с отдачи; объёмы большие, лимит — время.
const (
	ndLoadSec     = 9 * time.Second
	ndUpLoadBytes = int64(100) << 20
)

func (nd *NetDiag) phaseBloat() {
	nd.setPhase("bloat")
	client := newHTTPClient()
	if tr, ok := client.Transport.(*http.Transport); ok {
		defer tr.CloseIdleConnections()
	}
	ctx, cancel := context.WithCancel(context.Background())
	nd.setCancel(cancel)
	defer nd.setCancel(nil)
	defer cancel()

	var bl ndBloat

	// базовая задержка без нагрузки (TTFB к Cloudflare)
	var idle []float64
	for i := 0; i < 3 && !nd.stopped(); i++ {
		if v, err := probeTTFB(ctx, client); err == nil {
			idle = append(idle, v)
		}
	}
	if len(idle) == 0 || nd.stopped() {
		bl.Err = T("nd_bloat_err")
		nd.publishBloat(bl)
		return
	}
	bl.IdleMs = ndMean(idle)
	nd.publishBloat(bl)

	// под загрузкой
	var total atomic.Int64
	stop := make(chan struct{})
	loaded := make(chan float64, 64)
	dlDone := make(chan struct{})
	dctx, dcancel := context.WithTimeout(ctx, ndLoadSec)
	defer dcancel()
	go probeWhileRunning(dctx, client, stop, loaded)
	go func() { defer close(dlDone); ndLoadDownload(dctx, client, &total) }()
	nd.tickFrac(dctx, 0.05, 0.50) // ждём конца окна нагрузки
	close(stop)
	bl.DownMs = avgChan(loaded)
	<-dlDone
	if total.Load() >= 1<<20 {
		bl.DownMbps = mbps(total.Load(), ndLoadSec)
	}
	bl.AddDownMs = bl.DownMs - bl.IdleMs
	nd.publishBloat(bl)
	if nd.stopped() {
		return
	}

	// под отдачей
	cr := &countReader{r: io.LimitReader(rand.Reader, ndUpLoadBytes)}
	stop2 := make(chan struct{})
	loaded2 := make(chan float64, 64)
	ulDone := make(chan struct{})
	uctx, ucancel := context.WithTimeout(ctx, ndLoadSec)
	defer ucancel()
	go probeWhileRunning(uctx, client, stop2, loaded2)
	go func() { defer close(ulDone); ndLoadUpload(uctx, client, cr) }()
	nd.tickFrac(uctx, 0.55, 0.45)
	close(stop2)
	bl.UpMs = avgChan(loaded2)
	<-ulDone
	if cr.read.Load() >= 1<<20 {
		bl.UpMbps = mbps(cr.read.Load(), ndLoadSec)
	}
	bl.AddUpMs = bl.UpMs - bl.IdleMs

	bl.Grade = bloatGrade(math.Max(bl.AddDownMs, bl.AddUpMs))
	nd.publishBloat(bl)
}

// publishBloat выкладывает копию (мьютекс — читатели снимка).
func (nd *NetDiag) publishBloat(bl ndBloat) {
	nd.mu.Lock()
	nd.bloat = &bl
	nd.mu.Unlock()
}

// tickFrac двигает прогресс фазы по времени, пока живёт ctx.
func (nd *NetDiag) tickFrac(ctx context.Context, from, span float64) {
	t := time.NewTicker(300 * time.Millisecond)
	defer t.Stop()
	start := time.Now()
	for {
		select {
		case <-ctx.Done():
			nd.setFrac("bloat", from+span)
			return
		case <-t.C:
			f := from + span*math.Min(time.Since(start).Seconds()/ndLoadSec.Seconds(), 1)
			nd.setFrac("bloat", f)
		}
	}
}

// ndLoadDownload качает из __down, пока живёт контекст.
func ndLoadDownload(ctx context.Context, c *http.Client, total *atomic.Int64) {
	buf := make([]byte, 64*1024)
	for ctx.Err() == nil {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			baseURL+"/__down?bytes="+fmt.Sprint(downloadChunk), nil)
		if err != nil {
			return
		}
		req.Header.Set("User-Agent", userAgent)
		resp, err := c.Do(req)
		if err != nil {
			return // отмена по таймауту — ожидаемое завершение
		}
		if resp.StatusCode >= 400 {
			resp.Body.Close()
			return
		}
		for {
			k, rerr := resp.Body.Read(buf)
			total.Add(int64(k))
			if rerr != nil {
				break
			}
		}
		resp.Body.Close()
	}
}

// ndLoadUpload льёт случайные байты в __up, пока живёт контекст.
func ndLoadUpload(ctx context.Context, c *http.Client, cr *countReader) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/__up", cr)
	if err != nil {
		return
	}
	req.ContentLength = ndUpLoadBytes
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.Do(req)
	if err != nil {
		return // отмена по таймауту — ожидаемое завершение
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

func ndMean(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	var s float64
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

// ── сбор метрик для правил ──────────────────────────────────────────

func (nd *NetDiag) collectMetrics() ndMetrics {
	nd.mu.Lock()
	env, bloat := nd.env, nd.bloat
	ping := append([]ndPingHost(nil), nd.ping...)
	dns := append([]ndDNSProbe(nil), nd.dns...)
	nd.mu.Unlock()

	var m ndMetrics
	if env != nil {
		m.WifiDone = env.Wifi && env.SignalPct > 0
		m.WifiSignalPct = env.SignalPct
	}

	var sentNet, recvNet int
	var avgs []float64
	for _, p := range ping {
		if p.Sent == 0 {
			continue
		}
		if p.Scope == "gw" {
			m.GwDone = true
			if p.Recv > 0 {
				m.GwOK = true
				m.GwLossPct = float64(p.Sent-p.Recv) / float64(p.Sent) * 100
			} else {
				m.GwLossPct = 100
			}
			continue
		}
		m.NetDone = true
		sentNet += p.Sent
		recvNet += p.Recv
		if p.Recv > 0 {
			avgs = append(avgs, p.AvgMs)
			if p.JitterMs > m.NetJitterMs {
				m.NetJitterMs = p.JitterMs
			}
		}
	}
	if sentNet > 0 {
		m.NetLossPct = float64(sentNet-recvNet) / float64(sentNet) * 100
		m.NetOK = recvNet > 0
		m.NetAvgMs = ndMean(avgs)
	}

	if len(dns) > 0 {
		m.DNSDone = true
		for _, d := range dns {
			if d.Done == 0 {
				continue
			}
			if d.Label == "system" {
				m.DNSSysOK = d.OK > 0
				m.DNSSysAvgMs = d.AvgMs
			} else if d.OK > 0 {
				if !m.DNSAltOK || d.AvgMs < m.DNSAltBestMs {
					m.DNSAltOK = true
					m.DNSAltBestMs = d.AvgMs
				}
			}
		}
	}

	if bloat != nil && bloat.Err == "" {
		m.BloatDone, m.BloatOK = true, true
		m.BloatAddMs = math.Max(bloat.AddDownMs, bloat.AddUpMs)
	}

	// скорость: последний успешный замер против максимума за 7 дней
	if nd.hist != nil {
		var last, maxDown float64
		weekAgo := time.Now().Add(-7 * 24 * time.Hour)
		for _, e := range nd.hist() {
			if e.Err != "" || !e.TS.After(weekAgo) || e.Down <= 0 {
				continue
			}
			if e.Down > maxDown {
				maxDown = e.Down
			}
			last = e.Down // новые записи в конце истории
		}
		if last > 0 && maxDown > 0 {
			m.DownKnown = true
			m.DownMbps, m.RecentMaxMbps = last, maxDown
		}
	}
	return m
}

// ── HTTP: /api/netdiag, /api/netdiag/start, /api/netdiag/stop ──────

// SetNetDiag подключает анализатор (вызывается один раз при старте).
func (d *Dashboard) SetNetDiag(nd *NetDiag) {
	d.mu.Lock()
	d.netDiag = nd
	d.mu.Unlock()
}

// IsChecking — идёт ли замер скорости (для взаимной блокировки).
func (d *Dashboard) IsChecking() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.checking
}

func (d *Dashboard) serveNetDiag(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, T("err_get_only"), http.StatusMethodNotAllowed)
		return
	}
	d.mu.Lock()
	nd := d.netDiag
	d.mu.Unlock()
	if nd == nil {
		http.Error(w, T("err_unavailable"), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(nd.Snapshot())
}

func (d *Dashboard) serveNetDiagStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, T("err_post_only"), http.StatusMethodNotAllowed)
		return
	}
	d.mu.Lock()
	nd := d.netDiag
	d.mu.Unlock()
	if nd == nil {
		http.Error(w, T("err_unavailable"), http.StatusServiceUnavailable)
		return
	}
	if err := nd.Start(); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d *Dashboard) serveNetDiagStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, T("err_post_only"), http.StatusMethodNotAllowed)
		return
	}
	d.mu.Lock()
	nd := d.netDiag
	d.mu.Unlock()
	if nd == nil {
		http.Error(w, T("err_unavailable"), http.StatusServiceUnavailable)
		return
	}
	nd.Stop()
	w.WriteHeader(http.StatusNoContent)
}

// ── разбор вывода системных утилит (чистые функции, тестируются) ────

// ndSplitKV — «ключ : значение» из строки вывода netsh/route.
func ndSplitKV(line string) (key, val string, ok bool) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return "", "", false
	}
	key = strings.ToLower(strings.TrimSpace(line[:idx]))
	val = strings.TrimSpace(line[idx+1:])
	return key, val, key != "" && val != ""
}

// parseNetshWlan разбирает «netsh wlan show interfaces» (локали ru/en):
// SSID и сигнал % первой подключённой сети.
func parseNetshWlan(out string) (ssid string, signalPct int, ok bool) {
	lines := strings.Split(out, "\n")
	for _, ln := range lines {
		k, v, has := ndSplitKV(ln)
		if !has {
			continue
		}
		switch k {
		case "name", "имя": // начало описания следующего интерфейса
			ssid, signalPct, ok = "", 0, false
		case "state", "состояние":
			// "disconnected" содержит "connected", поэтому — точные совпадения
			ok = v == "connected" || v == "подключен" || v == "подключено"
			if !ok {
				ssid, signalPct = "", 0
			}
		case "ssid":
			if ok && ssid == "" {
				ssid = v
			}
		case "signal", "сигнал":
			if ok {
				signalPct, _ = strconv.Atoi(strings.TrimSuffix(v, "%"))
			}
		}
	}
	if !ok {
		return "", 0, false
	}
	return ssid, signalPct, true
}

// parseRouteLinux — «default via 192.168.1.1 dev eth0 …» из ip route.
func parseRouteLinux(out string) (gw, dev string, ok bool) {
	for _, ln := range strings.Split(out, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(ln), "default") {
			continue
		}
		fields := strings.Fields(ln)
		for i := 0; i+1 < len(fields); i++ {
			switch fields[i] {
			case "via":
				gw = fields[i+1]
			case "dev":
				dev = fields[i+1]
			}
		}
		return gw, dev, gw != ""
	}
	return "", "", false
}

// parseRouteDarwin — gateway/interface из «route -n get 1.1.1.1».
func parseRouteDarwin(out string) (gw, iface string, ok bool) {
	for _, ln := range strings.Split(out, "\n") {
		k, v, has := ndSplitKV(ln)
		if !has {
			continue
		}
		switch k {
		case "gateway":
			gw = v
		case "interface":
			iface = v
		}
	}
	return gw, iface, gw != ""
}

// ndNetshWlanOut снимает вывод netsh (Windows; нет netsh/Wi-Fi — пусто).
func ndNetshWlanOut() string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "netsh", "wlan", "show", "interfaces")
	cmd.SysProcAttr = hideConsoleSysProcAttr()
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}
