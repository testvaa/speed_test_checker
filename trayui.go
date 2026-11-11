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
	menuDLP    *systray.MenuItem
	menuULP    *systray.MenuItem
	menuBadP   *systray.MenuItem
	menuGoodP  *systray.MenuItem
	menuNotifP *systray.MenuItem
	mTraffic   *systray.MenuItem
	mZones     *systray.MenuItem
	last       *Result

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
}

func newTrayUI(cfg *Config, cfgPath string,
	onRunNow func(), onInterval func(int),
	onDownloadMB func(int), onUploadMB func(int), onLimits func(bad, good float64),
	onNotifyMode func(string),
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
	systray.SetTooltip("Speed Test Tray — подготовка…")

	// Главная кнопка — первой строкой меню, с маркером, чтобы сразу бросалась в глаза.
	u.menuRun = systray.AddMenuItem("▶  Проверить сейчас", "Запустить проверку немедленно")
	go func() {
		for range u.menuRun.ClickedCh {
			if u.onRunNow != nil {
				u.onRunNow()
			}
		}
	}()

	// Веб-дашборд — если сервер поднялся, открываем в браузере.
	if u.dashURL != "" {
		dash := systray.AddMenuItem("🖥 Открыть дашборд", u.dashURL)
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
	u.line("hdrTest", "📊 ПОСЛЕДНИЙ ТЕСТ")
	u.line("down", "↓ Загрузка: —")
	u.line("up", "↑ Отдача: —")
	u.line("ping", "⏱ Пинг: —")
	u.line("jitter", "〰 Джиттер: —")
	u.line("loss", "📉 Потери: —")
	u.line("ip", "🌐 IP: —")
	u.line("net", "🏢 Сеть: —")
	u.line("server", "📍 Сервер: —")
	u.line("time", "🕐 Проверено: —")
	systray.AddSeparator()

	// Постоянный монитор трафика.
	u.line("hdrNet", "📶 ТРАФИК")
	u.line("netNow", "⚡ Сейчас: ↓ — ↑ — Мбит/с")
	u.line("netDay", "📅 Сегодня: ↓ — ↑ —")
	u.line("netMon", "🗓 Месяц: ↓ — ↑ —")
	systray.AddSeparator()

	sub := systray.AddMenuItem("⏳ Интервал", "Периодичность проверок")
	for _, m := range intervalChoices {
		choice := m
		item := sub.AddSubMenuItemCheckbox(
			fmt.Sprintf("Каждые %d мин", choice), "", choice == u.cfg.IntervalMin)
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

	quit := systray.AddMenuItem("🚪 Выход", "")
	go func() {
		for range quit.ClickedCh {
			systray.Quit()
		}
	}()
}

func (u *TrayUI) line(key, title string) *systray.MenuItem {
	// Не Disable(): серый «недоступный» текст плохо читается. Пункт
	// остаётся обычным, но клики по нему ни к чему не приводят —
	// обработчик ClickedCh не регистрируется.
	item := systray.AddMenuItem(title, "")
	u.items[key] = item
	return item
}

// buildSettings создаёт подменю «Настройки»: объёмы теста и зоны оценки.
func (u *TrayUI) buildSettings() {
	u.menuDL = map[int]*systray.MenuItem{}
	u.menuUL = map[int]*systray.MenuItem{}
	u.menuBad = map[float64]*systray.MenuItem{}
	u.menuGood = map[float64]*systray.MenuItem{}

	setRoot := systray.AddMenuItem("Настройки", "Объём теста, трафик и зоны оценки")

	u.mTraffic = setRoot.AddSubMenuItem(u.trafficTitle(), "")

	u.menuDLP = setRoot.AddSubMenuItem(fmt.Sprintf("Объём загрузки: %d МБ", u.cfg.DownloadMB), "")
	for _, v := range downloadChoices {
		choice := v
		item := u.menuDLP.AddSubMenuItemCheckbox(fmt.Sprintf("%d МБ", choice), "", choice == u.cfg.DownloadMB)
		u.menuDL[choice] = item
		go func() {
			for range item.ClickedCh {
				if u.onDownloadMB != nil {
					u.onDownloadMB(choice)
				}
			}
		}()
	}

	u.menuULP = setRoot.AddSubMenuItem(fmt.Sprintf("Объём отдачи: %d МБ", u.cfg.UploadMB), "")
	for _, v := range uploadChoices {
		choice := v
		item := u.menuULP.AddSubMenuItemCheckbox(fmt.Sprintf("%d МБ", choice), "", choice == u.cfg.UploadMB)
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

	u.menuBadP = setRoot.AddSubMenuItem(fmt.Sprintf("Плохо, если ниже: %.0f Мбит/с", u.cfg.BadMbps), "")
	for _, v := range badChoices {
		choice := v
		item := u.menuBadP.AddSubMenuItemCheckbox(fmt.Sprintf("%.0f Мбит/с", choice), "", choice == u.cfg.BadMbps)
		u.menuBad[choice] = item
		go func() {
			for range item.ClickedCh {
				if u.onLimits != nil {
					u.onLimits(choice, u.cfg.GoodMbps)
				}
			}
		}()
	}

	u.menuGoodP = setRoot.AddSubMenuItem(fmt.Sprintf("Хорошо, если выше: %.0f Мбит/с", u.cfg.GoodMbps), "")
	for _, v := range goodChoices {
		choice := v
		item := u.menuGoodP.AddSubMenuItemCheckbox(fmt.Sprintf("%.0f Мбит/с", choice), "", choice == u.cfg.GoodMbps)
		u.menuGood[choice] = item
		go func() {
			for range item.ClickedCh {
				if u.onLimits != nil {
					u.onLimits(u.cfg.BadMbps, choice)
				}
			}
		}()
	}

	// Режим уведомлений о завершении проверок.
	u.menuNotif = map[string]*systray.MenuItem{}
	u.menuNotifP = setRoot.AddSubMenuItem(fmt.Sprintf("Уведомления: %s", notifyLabel(u.cfg.NotifyMode)), "")
	for _, mode := range []string{"all", "problems", "off"} {
		choice := mode
		item := u.menuNotifP.AddSubMenuItemCheckbox(notifyLabel(choice), "", choice == u.cfg.NotifyMode)
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
		return "только проблемы"
	case "off":
		return "выключены"
	default:
		return "все проверки"
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
	u.menuNotifP.SetTitle(fmt.Sprintf("Уведомления: %s", notifyLabel(mode)))
}

// trafficTitle — примерный трафик одной проверки (объёмы + пробы).
func (u *TrayUI) trafficTitle() string {
	return fmt.Sprintf("Трафик за проверку: ≈%d МБ", u.cfg.DownloadMB+u.cfg.UploadMB+1)
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
	u.menuDLP.SetTitle(fmt.Sprintf("Объём загрузки: %d МБ", mb))
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
	u.menuULP.SetTitle(fmt.Sprintf("Объём отдачи: %d МБ", mb))
	u.updateTraffic()
}

// zonesTitle — строка-подсказка с текущими зонами оценки.
func (u *TrayUI) zonesTitle() string {
	return fmt.Sprintf("Красный < %.0f • жёлтый %.0f–%.0f • зелёный ≥ %.0f Мбит/с",
		u.cfg.BadMbps, u.cfg.BadMbps, u.cfg.GoodMbps, u.cfg.GoodMbps)
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
	u.menuBadP.SetTitle(fmt.Sprintf("Плохо, если ниже: %.0f Мбит/с", bad))
	u.menuGoodP.SetTitle(fmt.Sprintf("Хорошо, если выше: %.0f Мбит/с", good))
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
		systray.SetTooltip("Speed Test Tray — проверка…")
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

	u.setLine("netNow", "⚡ Сейчас: ↓ %.1f ↑ %.1f Мбит/с", s.DownMbps, s.UpMbps)
	u.setLine("netDay", "📅 Сегодня: ↓ %s  ↑ %s", fmtBytes(s.DayDown), fmtBytes(s.DayUp))
	u.setLine("netMon", "🗓 %s: ↓ %s  ↑ %s", monthNameRU(time.Now()), fmtBytes(s.MonDown), fmtBytes(s.MonUp))
	if !checking {
		systray.SetTooltip(u.tooltipText())
	}
}

// tooltipText — сводный tooltip: живые скорости + последний тест.
func (u *TrayUI) tooltipText() string {
	u.stateMu.Lock()
	s := u.net
	u.stateMu.Unlock()
	tip := fmt.Sprintf("↓ %.1f  ↑ %.1f Мбит/с • сегодня ↓%s ↑%s",
		s.DownMbps, s.UpMbps, fmtBytes(s.DayDown), fmtBytes(s.DayUp))
	if u.last == nil {
		return tip
	}
	ts := u.last.Timestamp.Format("15:04")
	if u.last.Err != "" {
		return tip + fmt.Sprintf("\nТест: ошибка (%s)", ts)
	}
	return tip + fmt.Sprintf("\nТест: ↓ %.1f ↑ %.1f Мбит/с • %.0f мс • %s",
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
		label = "соединение…"
	case "ping":
		label = fmt.Sprintf("пинг %d%%", pct)
	case "download":
		label = fmt.Sprintf("загрузка %d%%", pct)
	case "upload":
		label = fmt.Sprintf("отдача %d%%", pct)
	case "loss":
		label = fmt.Sprintf("потери %d%%", pct)
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
		u.setLine("down", "❌ Ошибка: %s", truncate(res.Err, 56))
		for _, k := range []string{"up", "ping", "jitter", "loss", "ip", "net", "server"} {
			u.setLine(k, "—")
		}
		u.setLine("time", "🕐 Проверено: %s", ts)
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

	u.setLine("down", "↓ Загрузка: %.1f Мбит/с%s", res.DownloadMbps, truncMark(res))
	u.setLine("up", "↑ Отдача: %.1f Мбит/с", res.UploadMbps)
	u.setLine("ping", "⏱ Пинг: %.1f мс (нагр.: %.1f/%.1f)", res.PingMs, res.PingDownMs, res.PingUpMs)
	u.setLine("jitter", "〰 Джиттер: %.1f мс", res.JitterMs)
	u.setLine("loss", "📉 Потери: %.1f%%", res.LossPct)
	u.setLine("ip", "🌐 IP: %s (%s)", res.ClientIP, res.IPVersion)
	u.setLine("net", "🏢 Сеть: %s (AS%d)", res.Network, res.ASN)
	u.setLine("server", "📍 Сервер: %s (%s)", res.ServerCity, res.Colo)
	u.setLine("time", "🕐 Проверено: %s", ts)
}

func truncMark(res *Result) string {
	if res.Truncated {
		return " (обрезан)"
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
		pushToast("❌ Проверка не удалась", truncate(res.Err, 200), "bad")
		return
	}
	title, icon := "✅ Хорошая скорость", "ok"
	switch {
	case res.DownloadMbps < cfg.BadMbps:
		title, icon = "🛑 Плохая скорость", "bad"
	case res.DownloadMbps < cfg.GoodMbps:
		title, icon = "⚠️ Нормальная скорость", "warn"
	}
	pushToast(title, fmt.Sprintf("↓ %.1f Мбит/с • ↑ %.1f Мбит/с\nпинг %.0f мс • джиттер %.0f мс • потери %.1f%%",
		res.DownloadMbps, res.UploadMbps, res.PingMs, res.JitterMs, res.LossPct), icon)
}

// toastProblems: только ошибки и «плохая» зона, повтор после восстановления.
func (n *notifier) toastProblems(res *Result, cfg *Config) {
	if res.Err != "" {
		if !n.lowNotified {
			n.lowNotified = true
			pushToast("❌ Проверка не удалась", truncate(res.Err, 200), "bad")
		}
		return
	}
	if res.DownloadMbps < cfg.BadMbps {
		if !n.lowNotified {
			n.lowNotified = true
			pushToast("🛑 Плохая скорость",
				fmt.Sprintf("↓ %.1f Мбит/с — плохо, если ниже %.0f", res.DownloadMbps, cfg.BadMbps), "bad")
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
