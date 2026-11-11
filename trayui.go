package main

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"
)

//go:embed icons
var iconFS embed.FS

// iconBytes отдаёт иконку трея: Windows ожидает ICO, macOS/Linux — PNG
// (на них ICO не отображается).
func iconBytes(name string) []byte {
	ext := ".ico"
	if runtime.GOOS != "windows" {
		ext = ".png"
	}
	b, err := iconFS.ReadFile("icons/" + name + ext)
	if err != nil {
		return nil
	}
	return b
}

// workingFrames — кадры анимации «идёт проверка» (заполняется в build).
var workingFrames [][]byte

// workingSeq — порядок кадров «туда-обратно» для эффекта свипа стрелки.
var workingSeq = []int{0, 1, 2, 3, 4, 5, 6, 7, 6, 5, 4, 3, 2, 1}

// интервалы, доступные в меню (минуты).
var intervalChoices = []int{1, 5, 10, 15, 30, 60}

// объёмы теста и границы зон, доступные в меню «Настройки».
var downloadChoices = []int{5, 10, 25, 50, 100}    // МБ
var uploadChoices = []int{2, 5, 10, 25, 50}        // МБ
var badChoices = []float64{3, 5, 10, 15, 25}       // плохо, если ниже (Мбит/с)
var goodChoices = []float64{25, 50, 100, 250, 500} // хорошо, если выше (Мбит/с)

// TrayUI — меню трея и его обновление по результатам проверок.
type TrayUI struct {
	cfg     *Config
	cfgPath string

	items   map[string]*systray.MenuItem
	menuRun *systray.MenuItem
	menuInt map[int]*systray.MenuItem

	menuDL     map[int]*systray.MenuItem     // объём загрузки
	menuUL     map[int]*systray.MenuItem     // объём отдачи
	menuBad    map[float64]*systray.MenuItem // граница «плохо»
	menuGood   map[float64]*systray.MenuItem // граница «хорошо»
	menuNotif  map[string]*systray.MenuItem  // режим уведомлений
	menuLang   map[string]*systray.MenuItem  // язык интерфейса
	menuDLP    *systray.MenuItem
	menuULP    *systray.MenuItem
	menuBadP   *systray.MenuItem
	menuGoodP  *systray.MenuItem
	menuNotifP *systray.MenuItem
	mTraffic   *systray.MenuItem
	mZones     *systray.MenuItem
	last       *Result

	// реестр заголовков для смены языка без пересборки меню
	retitles []retitle

	// состояние монитора трафика + признак «идёт проверка»
	stateMu  sync.Mutex
	net      NetSnapshot
	checking bool

	// адрес веб-дашборда; пустая строка — сервер не поднялся
	dashURL string

	// анимация «идёт проверка»
	animMu   sync.Mutex
	animStop chan struct{}

	// троттлинг обновлений прогресса в tooltip
	pgMu   sync.Mutex
	pgLast time.Time

	onRunNow     func()
	onInterval   func(min int)
	onDownloadMB func(mb int)
	onUploadMB   func(mb int)
	onLimits     func(bad, good float64)
	onNotifyMode func(mode string)
	onLang       func(lang string)
}

// retitle — пункт меню с функцией-заголовком: при смене языка refreshLang
// пробегает реестр и переставляет тексты (systray не умеет пересобирать меню).
type retitle struct {
	item  *systray.MenuItem
	title func() string
	tip   func() string // nil — tooltip не менять
}

// reg регистрирует заголовок пункта меню для обновления при смене языка.
func (u *TrayUI) reg(item *systray.MenuItem, title func() string, tip func() string) {
	u.retitles = append(u.retitles, retitle{item, title, tip})
}

// refreshLang перекрашивает меню после смены языка: статические пункты —
// из реестра, динамические — перерисовкой из кэша (u.last / u.net).
func (u *TrayUI) refreshLang() {
	for _, r := range u.retitles {
		r.item.SetTitle(r.title())
		if r.tip != nil {
			r.item.SetTooltip(r.tip())
		}
	}
	u.updateTraffic()
	u.stateMu.Lock()
	net, checking := u.net, u.checking
	u.stateMu.Unlock()
	u.updateNet(net)
	if checking {
		systray.SetTooltip(T("tray_checking"))
		return
	}
	if u.last != nil {
		u.apply(u.last)
	}
	systray.SetTooltip(u.tooltipText())
}

// langChanged обновляет галочки подменю языка и всё меню после смены языка.
func (u *TrayUI) langChanged(lang string) {
	for l, item := range u.menuLang {
		if l == lang {
			item.Check()
		} else {
			item.Uncheck()
		}
	}
	u.refreshLang()
}

func newTrayUI(cfg *Config, cfgPath string,
	onRunNow func(), onInterval func(int),
	onDownloadMB func(int), onUploadMB func(int), onLimits func(bad, good float64),
	onNotifyMode func(string), onLang func(string),
) *TrayUI {
	return &TrayUI{
		cfg:          cfg,
		cfgPath:      cfgPath,
		onRunNow:     onRunNow,
		onInterval:   onInterval,
		onDownloadMB: onDownloadMB,
		onUploadMB:   onUploadMB,
		onLimits:     onLimits,
		onNotifyMode: onNotifyMode,
		onLang:       onLang,
	}
}

func (u *TrayUI) build() {
	u.items = map[string]*systray.MenuItem{}
	u.menuInt = map[int]*systray.MenuItem{}

	for i := 1; i <= 8; i++ {
		if b := iconBytes(fmt.Sprintf("working%d", i)); b != nil {
			workingFrames = append(workingFrames, b)
		}
	}

	systray.SetIcon(iconBytes("idle"))
	systray.SetTooltip(T("tray_preparing"))

	// Главная кнопка — первой строкой меню, с маркером, чтобы сразу бросалась в глаза.
	u.menuRun = systray.AddMenuItem(T("tray_run"), T("tray_run_tip"))
	u.reg(u.menuRun, func() string { return T("tray_run") }, func() string { return T("tray_run_tip") })
	go func() {
		for range u.menuRun.ClickedCh {
			if u.onRunNow != nil {
				u.onRunNow()
			}
		}
	}()

	// Веб-дашборд — если сервер поднялся, открываем в браузере.
	if u.dashURL != "" {
		dash := systray.AddMenuItem(T("tray_open_dash"), u.dashURL)
		u.reg(dash, func() string { return T("tray_open_dash") }, nil)
		go func() {
			for range dash.ClickedCh {
				openBrowser(u.dashURL)
			}
		}()
	}
	systray.AddSeparator()

	// Информационные строки намеренно НЕ блокируем: Windows рисует
	// заблокированные пункты бледно-серым, что плохо читается.
	// Блоки разделены заголовками-эмодзи: другого «цвета» в меню Windows нет.
	u.line("hdrTest", "tray_hdr_test")
	u.line("down", "tray_down")
	u.line("up", "tray_up")
	u.line("ping", "tray_ping")
	u.line("jitter", "tray_jitter")
	u.line("loss", "tray_loss")
	u.line("ip", "tray_ip")
	u.line("net", "tray_net")
	u.line("server", "tray_server")
	u.line("time", "tray_time")
	systray.AddSeparator()

	// Постоянный монитор трафика.
	u.line("hdrNet", "tray_hdr_traffic")
	u.line("netNow", "tray_net_now")
	u.line("netDay", "tray_net_day")
	u.line("netMon", "tray_net_mon")
	systray.AddSeparator()

	sub := systray.AddMenuItem(T("tray_interval"), T("tray_interval_tip"))
	u.reg(sub, func() string { return T("tray_interval") }, func() string { return T("tray_interval_tip") })
	for _, m := range intervalChoices {
		choice := m
		item := sub.AddSubMenuItemCheckbox(
			T("tray_every_min", choice), "", choice == u.cfg.IntervalMin)
		u.reg(item, func() string { return T("tray_every_min", choice) }, nil)
		u.menuInt[choice] = item
		go func() {
			for range item.ClickedCh {
				if u.onInterval != nil {
					u.onInterval(choice)
				}
			}
		}()
	}

	u.buildSettings()

	systray.AddSeparator()

	quit := systray.AddMenuItem(T("tray_quit"), "")
	u.reg(quit, func() string { return T("tray_quit") }, nil)
	go func() {
		for range quit.ClickedCh {
			systray.Quit()
		}
	}()
}

// line — информационная строка меню по ключу таблицы локализации.
func (u *TrayUI) line(key, tkey string) *systray.MenuItem {
	// Не Disable(): серый «недоступный» текст плохо читается. Пункт
	// остаётся обычным, но клики по нему ни к чему не приводят —
	// обработчик ClickedCh не регистрируется.
	item := systray.AddMenuItem(T(tkey), "")
	u.items[key] = item
	u.reg(item, func() string { return T(tkey) }, nil)
	return item
}

// buildSettings создаёт подменю «Настройки»: объёмы теста, зоны оценки,
// язык интерфейса и уведомления.
func (u *TrayUI) buildSettings() {
	u.menuDL = map[int]*systray.MenuItem{}
	u.menuUL = map[int]*systray.MenuItem{}
	u.menuBad = map[float64]*systray.MenuItem{}
	u.menuGood = map[float64]*systray.MenuItem{}

	setRoot := systray.AddMenuItem(T("tray_settings"), T("tray_settings_tip"))
	u.reg(setRoot, func() string { return T("tray_settings") }, func() string { return T("tray_settings_tip") })

	u.mTraffic = setRoot.AddSubMenuItem(u.trafficTitle(), "")
	u.reg(u.mTraffic, u.trafficTitle, nil)

	u.menuDLP = setRoot.AddSubMenuItem(T("tray_dl_vol", u.cfg.DownloadMB), "")
	u.reg(u.menuDLP, func() string { return T("tray_dl_vol", u.cfg.DownloadMB) }, nil)
	for _, v := range downloadChoices {
		choice := v
		item := u.menuDLP.AddSubMenuItemCheckbox(T("tray_mb", choice), "", choice == u.cfg.DownloadMB)
		u.reg(item, func() string { return T("tray_mb", choice) }, nil)
		u.menuDL[choice] = item
		go func() {
			for range item.ClickedCh {
				if u.onDownloadMB != nil {
					u.onDownloadMB(choice)
				}
			}
		}()
	}

	u.menuULP = setRoot.AddSubMenuItem(T("tray_ul_vol", u.cfg.UploadMB), "")
	u.reg(u.menuULP, func() string { return T("tray_ul_vol", u.cfg.UploadMB) }, nil)
	for _, v := range uploadChoices {
		choice := v
		item := u.menuULP.AddSubMenuItemCheckbox(T("tray_mb", choice), "", choice == u.cfg.UploadMB)
		u.reg(item, func() string { return T("tray_mb", choice) }, nil)
		u.menuUL[choice] = item
		go func() {
			for range item.ClickedCh {
				if u.onUploadMB != nil {
					u.onUploadMB(choice)
				}
			}
		}()
	}

	// Зоны оценки: красный ниже bad, жёлтый между bad и good, зелёный ≥ good.
	u.mZones = setRoot.AddSubMenuItem(u.zonesTitle(), "")
	u.reg(u.mZones, u.zonesTitle, nil)

	u.menuBadP = setRoot.AddSubMenuItem(T("tray_bad_below", u.cfg.BadMbps), "")
	u.reg(u.menuBadP, func() string { return T("tray_bad_below", u.cfg.BadMbps) }, nil)
	for _, v := range badChoices {
		choice := v
		item := u.menuBadP.AddSubMenuItemCheckbox(T("tray_mbps", choice), "", choice == u.cfg.BadMbps)
		u.reg(item, func() string { return T("tray_mbps", choice) }, nil)
		u.menuBad[choice] = item
		go func() {
			for range item.ClickedCh {
				if u.onLimits != nil {
					u.onLimits(choice, u.cfg.GoodMbps)
				}
			}
		}()
	}

	u.menuGoodP = setRoot.AddSubMenuItem(T("tray_good_above", u.cfg.GoodMbps), "")
	u.reg(u.menuGoodP, func() string { return T("tray_good_above", u.cfg.GoodMbps) }, nil)
	for _, v := range goodChoices {
		choice := v
		item := u.menuGoodP.AddSubMenuItemCheckbox(T("tray_mbps", choice), "", choice == u.cfg.GoodMbps)
		u.reg(item, func() string { return T("tray_mbps", choice) }, nil)
		u.menuGood[choice] = item
		go func() {
			for range item.ClickedCh {
				if u.onLimits != nil {
					u.onLimits(u.cfg.BadMbps, choice)
				}
			}
		}()
	}

	// Язык интерфейса: ярлык подменю двуязычный, пункты — имена собственные,
	// поэтому при смене языка их пересобирать не нужно.
	u.menuLang = map[string]*systray.MenuItem{}
	langRoot := setRoot.AddSubMenuItem(T("tray_lang"), "")
	for _, l := range []string{LangRu, LangEn} {
		choice := l
		label := "English"
		if choice == LangRu {
			label = "Русский"
		}
		item := langRoot.AddSubMenuItemCheckbox(label, "", choice == Lang())
		u.menuLang[choice] = item
		go func() {
			for range item.ClickedCh {
				if u.onLang != nil {
					u.onLang(choice)
				}
			}
		}()
	}

	// Режим уведомлений о завершении проверок.
	u.menuNotif = map[string]*systray.MenuItem{}
	u.menuNotifP = setRoot.AddSubMenuItem(T("tray_notif", notifyLabel(u.cfg.NotifyMode)), "")
	u.reg(u.menuNotifP, func() string { return T("tray_notif", notifyLabel(u.cfg.NotifyMode)) }, nil)
	for _, mode := range []string{"all", "problems", "off"} {
		choice := mode
		item := u.menuNotifP.AddSubMenuItemCheckbox(notifyLabel(choice), "", choice == u.cfg.NotifyMode)
		u.reg(item, func() string { return notifyLabel(choice) }, nil)
		u.menuNotif[choice] = item
		go func() {
			for range item.ClickedCh {
				if u.onNotifyMode != nil {
					u.onNotifyMode(choice)
				}
			}
		}()
	}
}

func notifyLabel(mode string) string {
	switch mode {
	case "problems":
		return T("notif_problems")
	case "off":
		return T("notif_off")
	default:
		return T("notif_all")
	}
}

// notifyModeChanged обновляет меню после смены режима уведомлений.
func (u *TrayUI) notifyModeChanged(mode string) {
	for m, item := range u.menuNotif {
		if m == mode {
			item.Check()
		} else {
			item.Uncheck()
		}
	}
	u.menuNotifP.SetTitle(T("tray_notif", notifyLabel(mode)))
}

// trafficTitle — примерный трафик одной проверки (объёмы + пробы).
func (u *TrayUI) trafficTitle() string {
	return T("tray_traffic_per_check", u.cfg.DownloadMB+u.cfg.UploadMB+1)
}

func (u *TrayUI) updateTraffic() {
	u.mTraffic.SetTitle(u.trafficTitle())
}

// downloadMBChanged обновляет меню после смены объёма загрузки.
func (u *TrayUI) downloadMBChanged(mb int) {
	for v, item := range u.menuDL {
		if v == mb {
			item.Check()
		} else {
			item.Uncheck()
		}
	}
	u.menuDLP.SetTitle(T("tray_dl_vol", mb))
	u.updateTraffic()
}

// uploadMBChanged обновляет меню после смены объёма отдачи.
func (u *TrayUI) uploadMBChanged(mb int) {
	for v, item := range u.menuUL {
		if v == mb {
			item.Check()
		} else {
			item.Uncheck()
		}
	}
	u.menuULP.SetTitle(T("tray_ul_vol", mb))
	u.updateTraffic()
}

// zonesTitle — строка-подсказка с текущими зонами оценки.
func (u *TrayUI) zonesTitle() string {
	return T("tray_zones", u.cfg.BadMbps, u.cfg.BadMbps, u.cfg.GoodMbps, u.cfg.GoodMbps)
}

// limitsChanged обновляет меню зон и перекрашивает иконку по новым границам.
func (u *TrayUI) limitsChanged(bad, good float64) {
	for v, item := range u.menuBad {
		if v == bad {
			item.Check()
		} else {
			item.Uncheck()
		}
	}
	for v, item := range u.menuGood {
		if v == good {
			item.Check()
		} else {
			item.Uncheck()
		}
	}
	u.menuBadP.SetTitle(T("tray_bad_below", bad))
	u.menuGoodP.SetTitle(T("tray_good_above", good))
	u.mZones.SetTitle(u.zonesTitle())
	if u.last != nil {
		u.apply(u.last)
	}
}

func (u *TrayUI) setLine(key, format string, a ...any) {
	if item, ok := u.items[key]; ok {
		item.SetTitle(fmt.Sprintf(format, a...))
	}
}

// setChecking включает/выключает анимацию «идёт проверка».
func (u *TrayUI) setChecking(v bool) {
	u.stateMu.Lock()
	u.checking = v
	u.stateMu.Unlock()
	if v {
		u.menuRun.Disable()
		systray.SetTooltip(T("tray_checking"))
		go u.animate()
		return
	}
	u.menuRun.Enable()
	u.stopAnim()
}

// updateNet выводит текущие скорости и накопленные объёмы в меню и tooltip.
func (u *TrayUI) updateNet(s NetSnapshot) {
	u.stateMu.Lock()
	u.net = s
	checking := u.checking
	u.stateMu.Unlock()

	u.setLine("netNow", T("tray_net_now_f"), s.DownMbps, s.UpMbps)
	u.setLine("netDay", T("tray_net_day_f"), fmtBytes(s.DayDown), fmtBytes(s.DayUp))
	u.setLine("netMon", T("tray_net_mon_f"), monthName(time.Now()), fmtBytes(s.MonDown), fmtBytes(s.MonUp))
	if !checking {
		systray.SetTooltip(u.tooltipText())
	}
}

// tooltipText — сводный tooltip: живые скорости + последний тест.
func (u *TrayUI) tooltipText() string {
	u.stateMu.Lock()
	s := u.net
	u.stateMu.Unlock()
	tip := fmt.Sprintf(T("tray_tip_base"),
		s.DownMbps, s.UpMbps, fmtBytes(s.DayDown), fmtBytes(s.DayUp))
	if u.last == nil {
		return tip
	}
	ts := u.last.Timestamp.Format("15:04")
	if u.last.Err != "" {
		return tip + fmt.Sprintf(T("tray_tip_err"), ts)
	}
	return tip + fmt.Sprintf(T("tray_tip_ok"),
		u.last.DownloadMbps, u.last.UploadMbps, u.last.PingMs, ts)
}

// animate гоняет кадры свипа стрелки, пока не закроют animStop.
func (u *TrayUI) animate() {
	u.animMu.Lock()
	if u.animStop != nil { // уже работает
		u.animMu.Unlock()
		return
	}
	stop := make(chan struct{})
	u.animStop = stop
	u.animMu.Unlock()

	defer u.stopAnim()
	if len(workingFrames) < 2 {
		return
	}
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for i := 0; ; i++ {
		select {
		case <-stop:
			return
		case <-t.C:
			systray.SetIcon(workingFrames[workingSeq[i%len(workingSeq)]])
		}
	}
}

func (u *TrayUI) stopAnim() {
	u.animMu.Lock()
	defer u.animMu.Unlock()
	if u.animStop != nil {
		close(u.animStop)
		u.animStop = nil
	}
}

// updateProgress показывает ход проверки в tooltip (троттлинг ~4 раза/с).
func (u *TrayUI) updateProgress(phase string, frac float64) {
	u.pgMu.Lock()
	if frac < 1 && time.Since(u.pgLast) < 250*time.Millisecond {
		u.pgMu.Unlock()
		return
	}
	u.pgLast = time.Now()
	u.pgMu.Unlock()

	pct := int(frac * 100)
	if pct > 100 {
		pct = 100
	}
	var label string
	switch phase {
	case "meta":
		label = T("prog_meta")
	case "ping":
		label = T("prog_ping", pct)
	case "download":
		label = T("prog_download", pct)
	case "upload":
		label = T("prog_upload", pct)
	case "loss":
		label = T("prog_loss", pct)
	default:
		label = phase
	}
	systray.SetTooltip("Speed Test Tray — " + label)
}

// intervalChanged обновляет галочки меню интервалов.
func (u *TrayUI) intervalChanged(min int) {
	for m, item := range u.menuInt {
		if m == min {
			item.Check()
		} else {
			item.Uncheck()
		}
	}
}

// apply выводит результат проверки в тултип, меню и цвет иконки.
func (u *TrayUI) apply(res *Result) {
	u.last = res
	ts := res.Timestamp.Format("15:04:05")

	if res.Err != "" {
		systray.SetIcon(iconBytes("bad"))
		systray.SetTooltip(u.tooltipText())
		u.setLine("down", T("tray_line_err"), truncate(res.Err, 56))
		for _, k := range []string{"up", "ping", "jitter", "loss", "ip", "net", "server"} {
			u.setLine(k, "—")
		}
		u.setLine("time", T("tray_line_time"), ts)
		return
	}

	// Цвет иконки по зонам: зелёный ≥ good, жёлтый ≥ bad, красный ниже bad.
	icon := "ok"
	switch {
	case res.DownloadMbps >= u.cfg.GoodMbps:
		icon = "ok"
	case res.DownloadMbps >= u.cfg.BadMbps:
		icon = "warn"
	default:
		icon = "bad"
	}
	systray.SetIcon(iconBytes(icon))
	systray.SetTooltip(u.tooltipText())

	u.setLine("down", T("tray_line_down"), res.DownloadMbps, truncMark(res))
	u.setLine("up", T("tray_line_up"), res.UploadMbps)
	u.setLine("ping", T("tray_line_ping"), res.PingMs, res.PingDownMs, res.PingUpMs)
	u.setLine("jitter", T("tray_line_jitter"), res.JitterMs)
	u.setLine("loss", T("tray_line_loss"), res.LossPct)
	u.setLine("ip", T("tray_line_ip"), res.ClientIP, res.IPVersion)
	u.setLine("net", T("tray_line_net"), res.Network, res.ASN)
	u.setLine("server", T("tray_line_server"), res.ServerCity, res.Colo)
	u.setLine("time", T("tray_line_time"), ts)
}

func truncMark(res *Result) string {
	if res.Truncated {
		return T("tray_trunc")
	}
	return ""
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// notifier решает, какие тосты показывать, по режиму cfg.NotifyMode:
// "all" — после каждой проверки; "problems" — только ошибки и «плохая»
// зона, разово до восстановления; "off" — ничего.
type notifier struct {
	lowNotified bool
}

func (n *notifier) apply(res *Result, cfg *Config) {
	switch cfg.NotifyMode {
	case "off":
		return
	case "all":
		n.toastAlways(res, cfg)
	default:
		n.toastProblems(res, cfg)
	}
}

// toastAlways: уведомление после каждой проверки, с цветной иконкой зоны.
func (n *notifier) toastAlways(res *Result, cfg *Config) {
	if res.Err != "" {
		pushToast(T("toast_fail"), truncate(res.Err, 200), "bad")
		return
	}
	title, icon := T("toast_good"), "ok"
	switch {
	case res.DownloadMbps < cfg.BadMbps:
		title, icon = T("toast_bad"), "bad"
	case res.DownloadMbps < cfg.GoodMbps:
		title, icon = T("toast_norm"), "warn"
	}
	pushToast(title, fmt.Sprintf(T("toast_body"),
		res.DownloadMbps, res.UploadMbps, res.PingMs, res.JitterMs, res.LossPct), icon)
}

// toastProblems: только ошибки и «плохая» зона, повтор после восстановления.
func (n *notifier) toastProblems(res *Result, cfg *Config) {
	if res.Err != "" {
		if !n.lowNotified {
			n.lowNotified = true
			pushToast(T("toast_fail"), truncate(res.Err, 200), "bad")
		}
		return
	}
	if res.DownloadMbps < cfg.BadMbps {
		if !n.lowNotified {
			n.lowNotified = true
			pushToast(T("toast_bad"),
				fmt.Sprintf(T("toast_bad_body"), res.DownloadMbps, cfg.BadMbps), "bad")
		}
		return
	}
	n.lowNotified = false
}

// toastIconPath распаковывает встроенный PNG во временный каталог и возвращает
// путь для тоста (пустая строка — показать без иконки).
func toastIconPath(name string) string {
	dir := filepath.Join(os.TempDir(), "SpeedTestTray")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	p := filepath.Join(dir, name+".png")
	if _, err := os.Stat(p); err != nil {
		b, err := iconFS.ReadFile("icons/" + name + ".png")
		if err != nil {
			return ""
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return ""
		}
	}
	return p
}
