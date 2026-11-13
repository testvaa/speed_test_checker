package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync/atomic"
	"time"

	"fyne.io/systray"
)

// app связывает конфиг, трей и планировщик проверок.
type app struct {
	cfg     *Config
	cfgPath string

	trigger  chan struct{} // «проверить сейчас»
	reset    chan struct{} // интервал изменился — пересчитать ожидание
	netReset chan struct{} // частота опроса сети изменилась
	running  atomic.Bool   // защита от наложения проверок
	ui       *TrayUI
	notif    notifier
	netMon   *NetMon
	watch    *WatchMon
	dash     *Dashboard
	agent    *Agent
}

func main() {
	once := flag.Bool("once", false, T("flag_once"))
	netmon := flag.Bool("netmon", false, T("flag_netmon"))
	optimize := flag.Bool("optimize", false, "run the system optimization helper (windows, elevated)")
	optLog := flag.String("optlog", "", "optimization helper log file")
	optReset := flag.Bool("resetbase", false, "add DISM /ResetBase to the optimization cleanup")
	flag.Parse()

	if *optimize {
		// помощник «Оптимизации системы»: запускается дашбордом с правами
		// администратора, работает отдельно и завершается после очистки
		runOptimizeHelper(*optLog, *optReset)
		return
	}

	cfg, cfgPath := LoadConfig(!*once && !*netmon)
	SetLang(cfg.Lang)

	if *once {
		runOnce(cfg)
		return
	}
	if *netmon {
		runNetMonTest(cfgPath)
		return
	}

	a := &app{
		cfg:      cfg,
		cfgPath:  cfgPath,
		trigger:  make(chan struct{}, 1),
		reset:    make(chan struct{}, 1),
		netReset: make(chan struct{}, 1),
		netMon:   NewNetMon(trafficPath(cfgPath)),
		watch:    NewWatchMon(watchPath(cfgPath)),
		dash:     NewDashboard(cfg, historyPath(cfgPath)),
	}
	a.agent = NewAgent(cfg, cfgPath)
	a.agent.SetProviders(a.dash.HistoryCopy, a.netMon.TrafficCopy)
	a.agent.SetWatchProvider(a.watch.List)
	a.agent.SetMetaProvider(func() agentMeta {
		return agentMeta{
			Interval: a.cfg.IntervalMin,
			DownMB:   a.cfg.DownloadMB,
			UpMB:     a.cfg.UploadMB,
			RateMs:   a.cfg.NetRateMs,
		}
	})
	a.agent.SetActions(AgentActions{
		RunCheck:      a.runNow,
		SetInterval:   func(m int) int { a.setInterval(m); return a.cfg.IntervalMin },
		SetDownloadMB: func(v int) int { a.setDownloadMB(v); return a.cfg.DownloadMB },
		SetUploadMB:   func(v int) int { a.setUploadMB(v); return a.cfg.UploadMB },
		SetLimits: func(b, g float64) (float64, float64) {
			a.setLimits(b, g)
			return a.cfg.BadMbps, a.cfg.GoodMbps
		},
		SetNetRate: func(ms int) int { a.setNetRate(ms); return a.cfg.NetRateMs },
		ExportArchive: func() (string, error) {
			p := filepath.Join(filepath.Dir(a.cfgPath),
				"speedtest-archive-"+time.Now().Format("2006-01-02_15-04")+".jsonl")
			return a.dash.ExportArchiveFile(p)
		},
		ImportArchive: a.dash.ImportArchiveFile,
		PingHost: func(h string) (string, error) {
			out, err := a.watch.PingHost(h, 4)
			a.agent.invalidateDigest()
			return out, err
		},
		WatchHost: func(h string, m int) (string, error) {
			out, err := a.watch.Watch(h, m)
			a.agent.invalidateDigest()
			return out, err
		},
		UnwatchHost: func(h string) (string, error) {
			out, err := a.watch.Unwatch(h)
			a.agent.invalidateDigest()
			return out, err
		},
		UnwatchAll: func() (string, error) {
			out, err := a.watch.UnwatchAll()
			a.agent.invalidateDigest()
			return out, err
		},
	})
	a.dash.SetAgent(a.agent)
	a.dash.SetNetMon(a.netMon)
	a.dash.SetWatchMon(a.watch)
	a.watch.Start()
	a.dash.onRun = a.runNow
	a.dash.onNetRate = a.setNetRate
	a.dash.onLang = a.setLang
	a.dash.Start()
	systray.Run(a.onReady, func() {
		a.netMon.Save()
		a.agent.Stop() // при выходе останавливаем llama-server
		a.watch.Stop()
	})
}

// runNetMonTest — режим -netmon: показывает, что видит монитор трафика.
func runNetMonTest(cfgPath string) {
	attachConsole()
	m := NewNetMon(trafficPath(cfgPath))
	fmt.Println(T("netmon_intro"))
	for i := 0; i < 8; i++ {
		time.Sleep(2 * time.Second)
		m.Poll()
		s := m.Snapshot()
		fmt.Println(fmt.Sprintf(T("netmon_line"),
			(i+1)*2, s.DownMbps, s.UpMbps, fmtBytes(s.DayDown), fmtBytes(s.DayUp)))
	}
}

func (a *app) onReady() {
	a.ui = newTrayUI(a.cfg, a.cfgPath, a.runNow, a.setInterval,
		a.setDownloadMB, a.setUploadMB, a.setLimits, a.setNotifyMode, a.setLang)
	a.ui.dashURL = a.dash.URL()
	a.ui.build()
	go a.safeLoop("loop", a.loop)
	go a.safeLoop("netLoop", a.netLoop)
}

// safeLoop защищает фоновые циклы: паника не убьёт молча весь процесс,
// а попадёт в app_debug.log рядом с конфигом.
func (a *app) safeLoop(name string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprintf("%s: паника: %v\n%s", name, r, debug.Stack())
			fmt.Fprintln(os.Stderr, msg)
			if log, err := os.OpenFile(filepath.Join(filepath.Dir(a.cfgPath), "app_debug.log"),
				os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
				t := time.Now().Format("2006-01-02 15:04:05")
				fmt.Fprintf(log, "%s %s\n", t, msg)
				log.Close()
			}
			go a.safeLoop(name, f) // перезапустить цикл
		}
	}()
	f()
}

// netLoop — постоянный монитор трафика: опрос счётчиков с частотой
// cfg.NetRateMs (селектор «обновление» на дашборде; по умолчанию 2 с).
func (a *app) netLoop() {
	rate := func() time.Duration {
		return time.Duration(nearestNetRateMs(a.cfg.NetRateMs)) * time.Millisecond
	}
	t := time.NewTicker(rate())
	defer t.Stop()
	for {
		select {
		case <-t.C:
			a.netMon.Poll()
			snap := a.netMon.Snapshot()
			a.ui.updateNet(snap)
			a.dash.PushNet(snap)
		case <-a.netReset:
			t.Stop()
			t = time.NewTicker(rate())
		}
	}
}

// loop выполняет проверку сразу после старта, затем по интервалу.
func (a *app) loop() {
	a.check()
	for {
		a.waitInterval()
		a.check()
	}
}

// waitInterval ждёт заданный интервал; прерывается досрочно по «проверить
// сейчас» и пересчитывается при смене интервала.
func (a *app) waitInterval() {
	for {
		timer := time.NewTimer(time.Duration(a.cfg.IntervalMin) * time.Minute)
		select {
		case <-timer.C:
			return
		case <-a.trigger:
			timer.Stop()
			return
		case <-a.reset:
			timer.Stop()
		}
	}
}

func (a *app) runNow() {
	select {
	case a.trigger <- struct{}{}:
	default:
	}
}

func (a *app) setInterval(min int) {
	if min == a.cfg.IntervalMin {
		return
	}
	a.cfg.IntervalMin = min
	a.saveCfg()
	a.ui.intervalChanged(min)
	a.dash.SetInterval(min)
	select {
	case a.reset <- struct{}{}:
	default:
	}
}

func (a *app) setDownloadMB(mb int) {
	if mb == a.cfg.DownloadMB {
		return
	}
	a.cfg.DownloadMB = mb
	a.saveCfg()
	a.ui.downloadMBChanged(mb)
}

func (a *app) setUploadMB(mb int) {
	if mb == a.cfg.UploadMB {
		return
	}
	a.cfg.UploadMB = mb
	a.saveCfg()
	a.ui.uploadMBChanged(mb)
}

// setLimits принимает новые границы зон; если «плохо» налегает на «хорошо»,
// подтягивает вторую границу до ближайшего разумного пресета.
func (a *app) setLimits(bad, good float64) {
	if bad >= good {
		if g := nextAbove(goodChoices, bad); g > 0 {
			good = g
		}
	}
	if good <= bad {
		if b := nextBelow(badChoices, good); b > 0 {
			bad = b
		}
	}
	if bad == a.cfg.BadMbps && good == a.cfg.GoodMbps {
		return
	}
	a.cfg.BadMbps = bad
	a.cfg.GoodMbps = good
	a.saveCfg()
	a.ui.limitsChanged(bad, good)
	a.dash.SetLimits(bad, good)
	a.agent.SetLimits(bad, good)
}

// nextAbove — наименьший пресет больше v (0, если такого нет).
func nextAbove(choices []float64, v float64) float64 {
	for _, c := range choices {
		if c > v {
			return c
		}
	}
	return 0
}

// nextBelow — наибольший пресет меньше v (0, если такого нет).
func nextBelow(choices []float64, v float64) float64 {
	var best float64
	for _, c := range choices {
		if c < v {
			best = c
		}
	}
	return best
}

func (a *app) setNotifyMode(mode string) {
	if mode == a.cfg.NotifyMode {
		return
	}
	a.cfg.NotifyMode = mode
	a.saveCfg()
	a.ui.notifyModeChanged(mode)
}

// setLang меняет язык интерфейса: конфиг, глобальный i18n и меню трея.
func (a *app) setLang(lang string) {
	if lang != LangRu && lang != LangEn {
		return
	}
	if lang != a.cfg.Lang {
		a.cfg.Lang = lang
		a.saveCfg()
	}
	SetLang(lang)
	a.agent.invalidateDigest() // сводка и промпт — на новом языке
	if a.ui != nil {
		a.ui.langChanged(lang)
	}
}

// setNetRate меняет частоту опроса сети/обновления живого графика
// (селектор «обновление» на дашборде).
func (a *app) setNetRate(ms int) {
	ms = nearestNetRateMs(ms)
	if ms != a.cfg.NetRateMs {
		a.cfg.NetRateMs = ms
		a.saveCfg()
	}
	a.dash.SetNetRate(ms)
	select {
	case a.netReset <- struct{}{}:
	default:
	}
}

func (a *app) saveCfg() {
	if err := a.cfg.Save(a.cfgPath); err != nil {
		fmt.Fprintln(os.Stderr, T("cfg_save_err", a.cfgPath, err))
	}
}

func (a *app) check() {
	if !a.running.CompareAndSwap(false, true) {
		return // предыдущая проверка ещё идёт
	}
	defer a.running.Store(false)

	a.ui.setChecking(true)
	a.dash.SetChecking(true)
	res := RunCheck(a.cfg, func(phase string, frac float64) {
		a.ui.updateProgress(phase, frac)
		a.dash.SetProgress(phase, frac)
	})
	a.ui.setChecking(false) // остановить анимацию до перекраски иконки
	a.dash.SetChecking(false)
	a.ui.apply(res)
	a.notif.apply(res, a.cfg)
	a.dash.ApplyResult(res)
}

// runOnce — режим -once: полный отчёт в консоль.
func runOnce(cfg *Config) {
	attachConsole()
	fmt.Println(T("once_intro"))
	start := time.Now()
	var lastPhase string
	res := RunCheck(cfg, func(phase string, frac float64) {
		if phase != lastPhase || frac >= 1 {
			lastPhase = phase
			fmt.Printf("  %s…\n", phaseLabel(phase))
		}
	})
	if res.Err != "" {
		fmt.Fprintln(os.Stderr, T("once_err", res.Err))
		os.Exit(1)
	}
	fmt.Println(report(res, start))
}

func phaseLabel(phase string) string {
	switch phase {
	case "meta":
		return T("ph_meta")
	case "ping":
		return T("ph_ping")
	case "download":
		return T("ph_download")
	case "upload":
		return T("ph_upload")
	case "loss":
		return T("ph_loss")
	}
	return phase
}

func report(res *Result, start time.Time) string {
	lines := []string{
		"──── Speed Test Tray (Cloudflare) ────",
		fmt.Sprintf(T("rep_down"), res.DownloadMbps, truncMark(res)),
		fmt.Sprintf(T("rep_up"), res.UploadMbps),
		fmt.Sprintf(T("rep_ping"), res.PingMs, res.PingDownMs, res.PingUpMs),
		fmt.Sprintf(T("rep_jitter"), res.JitterMs),
		fmt.Sprintf(T("rep_loss"), res.LossPct),
		fmt.Sprintf("IP:          %s (%s)", res.ClientIP, res.IPVersion),
		fmt.Sprintf(T("rep_net"), res.Network, res.ASN),
		fmt.Sprintf(T("rep_server"), res.ServerCity, res.Colo),
		fmt.Sprintf(T("rep_proto"), res.HTTPProtocol),
		fmt.Sprintf(T("rep_time"), time.Since(start).Round(time.Second)),
	}
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}
