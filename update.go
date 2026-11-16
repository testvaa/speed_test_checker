package main

// Обновления: проверка последнего релиза на GitHub, скачивание нового
// бинарника и перезапуск приложения на него. Версия текущего бинарника —
// main.version (прошивается ldflags в CI; локальные сборки — "dev").

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// updateAPI — GitHub API «последний релиз» (без пререлизов и черновиков).
	updateAPI = "https://api.github.com/repos/testvaa/speed_test_checker/releases/latest"
	// updFirstDelay — пауза перед первой фоновой проверкой после старта.
	updFirstDelay = 15 * time.Second
	// updInterval — период фоновой проверки при включённом автообновлении.
	updInterval = 24 * time.Hour
	// updMinAssetSize — меньше точно не бинарник (защита от страницы ошибки).
	updMinAssetSize = 1 << 20
)

// Состояния обновления (updateState.State).
const (
	updIdle        = "idle"        // ещё не проверяли
	updChecking    = "checking"    // идёт запрос к GitHub
	updAvailable   = "available"   // есть новая версия (скачиваем/можно качать)
	updUpToDate    = "uptodate"    // установлена последняя версия
	updDownloading = "downloading" // качаем <exe>.new
	updReady       = "ready"       // скачано и проверено, ждём перезапуска
	updApplying    = "applying"    // подменяем файл и перезапускаемся
	updError       = "error"       // ошибка последней операции
)

// updateState — снимок состояния апдейтера для /api/state и трея.
type updateState struct {
	Version    string  `json:"version"`             // версия текущего бинарника
	State      string  `json:"state"`               // upd* из набора выше
	Latest     string  `json:"latest,omitempty"`    // тег последнего релиза
	ReleaseURL string  `json:"releaseUrl,omitempty"`
	Notes      string  `json:"notes,omitempty"`     // примечания релиза (трей не показывает)
	Frac       float64 `json:"frac,omitempty"`      // прогресс скачивания 0..1
	Recv       int64   `json:"recv,omitempty"`      // скачано байт
	Total      int64   `json:"total,omitempty"`     // размер ассета
	Err        string  `json:"err,omitempty"`       // текст последней ошибки
	Auto       bool    `json:"auto"`                // включено ли автообновление
}

// ghRelease / ghAsset — нужные поля ответа GitHub API releases/latest.
type ghRelease struct {
	TagName string    `json:"tag_name"`
	HTMLURL string    `json:"html_url"`
	Body    string    `json:"body"`
	Assets  []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// Updater — проверка и установка обновлений с GitHub Releases.
type Updater struct {
	mu     sync.Mutex
	state  string // upd*
	latest string // тег последнего релиза с GitHub
	relURL string
	notes  string
	frac   float64
	recv   int64
	total  int64
	errMsg string

	assetURL  string // бинарник под текущую ОС
	assetSHA  string // ожидаемый sha256 (пусто — релиз без контрольной суммы)
	assetSize int64

	dlCancel  context.CancelFunc
	readyVer  string // версия скачанного <exe>.new (пусто — не скачан)
	exeNew    string // путь к скачанному файлу

	auto     bool
	stop     chan struct{}
	poke     chan struct{}
	stopOnce sync.Once

	// notify — тосты о событиях (kind: available/uptodate/ready/error).
	notify func(kind, ver string)
	// onChanged — реакция UI трея на смену состояния.
	onChanged func(updateState)
	// onRestart — «перезапусти приложение» (systray.Quit; вызывается,
	// когда новый бинарник уже подменён).
	onRestart func()
}

// NewUpdater создаёт апдейтер; обратные вызовы можно подсоединить позже.
func NewUpdater(auto bool) *Updater {
	return &Updater{
		state: updIdle,
		auto:  auto,
		stop:  make(chan struct{}),
		poke:  make(chan struct{}, 1),
	}
}

// OnNotify/OnChanged/OnRestart подсоединяют обратные вызовы (до Start).
func (u *Updater) OnNotify(fn func(kind, ver string))       { u.notify = fn }
func (u *Updater) OnChanged(fn func(updateState))           { u.onChanged = fn }
func (u *Updater) OnRestart(fn func())                      { u.onRestart = fn }

// Auto сообщает, включено ли автообновление.
func (u *Updater) Auto() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.auto
}

// SetAuto переключает автообновление; при включении сразу будит цикл.
func (u *Updater) SetAuto(on bool) {
	u.mu.Lock()
	if u.auto == on {
		u.mu.Unlock()
		return
	}
	u.auto = on
	u.mu.Unlock()
	u.changed()
	if on {
		select {
		case u.poke <- struct{}{}:
		default:
		}
	}
}

// Snapshot возвращает текущее состояние для UI.
func (u *Updater) Snapshot() updateState {
	u.mu.Lock()
	defer u.mu.Unlock()
	return updateState{
		Version:    version,
		State:      u.state,
		Latest:     u.latest,
		ReleaseURL: u.relURL,
		Notes:      u.notes,
		Frac:       u.frac,
		Recv:       u.recv,
		Total:      u.total,
		Err:        u.errMsg,
		Auto:       u.auto,
	}
}

// State возвращает текущее состояние строкой.
func (u *Updater) State() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.state
}

// Busy — идёт проверка, скачивание или применение обновления.
func (u *Updater) Busy() bool {
	switch u.State() {
	case updChecking, updDownloading, updApplying:
		return true
	}
	return false
}

// changed рассылает новый снимок подписчикам (трей).
func (u *Updater) changed() {
	if u.onChanged != nil {
		u.onChanged(u.Snapshot())
	}
}

// setState под замком + рассылка.
func (u *Updater) setState(s string) {
	u.mu.Lock()
	u.state = s
	u.mu.Unlock()
	u.changed()
}

// fail переводит в состояние ошибки с текстом.
func (u *Updater) fail(err error) {
	u.mu.Lock()
	u.state = updError
	u.errMsg = err.Error()
	u.frac, u.recv, u.total = 0, 0, 0
	u.mu.Unlock()
	u.changed()
}

// fire — тост, если подписчик есть.
func (u *Updater) fire(kind, ver string) {
	if u.notify != nil {
		u.notify(kind, ver)
	}
}

// Check запускает проверку обновлений (manual=true — с тостом о результате;
// при найденной версии сразу начинает скачивание).
func (u *Updater) Check(manual bool) {
	go func() {
		if !u.checkSync(manual) {
			return
		}
		// Нашли новую версию — качаем сразу: и трап-only пользователь,
		// и дашборд ждут одного и того же файла.
		if u.State() == updAvailable {
			u.downloadSync(manual)
		}
	}()
}

// checkSync опрашивает GitHub и сравнивает версии; false — занято или ошибка
// старта. Ошибки и результаты записывает в состояние, тосты — при manual.
func (u *Updater) checkSync(manual bool) bool {
	u.mu.Lock()
	switch u.state {
	case updChecking, updDownloading, updApplying:
		u.mu.Unlock()
		return false
	case updReady: // уже скачано — заново не проверяем
		u.mu.Unlock()
		return false
	}
	u.state = updChecking
	u.errMsg = ""
	u.mu.Unlock()
	u.changed()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rel, err := fetchRelease(ctx)
	if err != nil {
		u.fail(err)
		if manual {
			u.fire("error", "")
		}
		return false
	}

	asset, sha := pickAssets(rel, assetName())
	if asset == nil {
		u.fail(errors.New(T("err_upd_no_asset", rel.TagName)))
		if manual {
			u.fire("error", "")
		}
		return false
	}

	u.mu.Lock()
	u.latest = rel.TagName
	u.relURL = rel.HTMLURL
	u.notes = rel.Body
	u.assetURL = asset.BrowserDownloadURL
	u.assetSize = asset.Size
	u.assetSHA = sha
	newer := semverNewer(version, rel.TagName)
	if newer {
		u.state = updAvailable
		// прежняя закачка (если была) больше не актуальна
		u.readyVer = ""
		u.exeNew = ""
	} else {
		u.state = updUpToDate
	}
	u.mu.Unlock()
	u.changed()

	if manual && !newer {
		u.fire("uptodate", "")
	}
	return true
}

// downloadSync качает бинарник в <exe>.new с проверкой размера и sha256.
// По готовности — состояние ready; отмена — через Cancel/Stop.
func (u *Updater) downloadSync(manual bool) {
	u.mu.Lock()
	switch u.state {
	case updAvailable, updError: // ошибка могла остаться после неудачной закачки
		if u.assetURL == "" {
			u.mu.Unlock()
			return
		}
	default:
		u.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	u.dlCancel = cancel
	u.state = updDownloading
	u.frac, u.recv, u.total = 0, 0, 0
	url := u.assetURL
	u.mu.Unlock()
	u.changed()

	exe, err := os.Executable()
	if err != nil {
		cancel()
		u.fail(err)
		return
	}
	dst := exe + ".new"
	prog := func(frac float64, recv, total int64) {
		u.mu.Lock()
		u.frac, u.recv, u.total = frac, recv, total
		u.mu.Unlock()
		u.changed()
	}
	dst, err = downloadFile(ctx, url, dst, prog)
	cancel()
	u.mu.Lock()
	u.dlCancel = nil
	cancelled := ctx.Err() != nil
	shaWant, ver := u.assetSHA, u.latest
	u.mu.Unlock()
	if err != nil {
		if cancelled {
			// пользователь отменил — возвращаемся к «доступна»;
			// недокачанный .new.part уберём при следующей попытке (O_TRUNC)
			u.setState(updAvailable)
			return
		}
		u.fail(err)
		if manual {
			u.fire("error", "")
		}
		return
	}

	// Проверки: размер и (если релиз публикует) контрольная сумма.
	if err := verifyDownload(dst, shaWant); err != nil {
		_ = os.Remove(dst)
		u.fail(err)
		if manual {
			u.fire("error", "")
		}
		return
	}

	u.mu.Lock()
	u.state = updReady
	u.readyVer = ver
	u.exeNew = dst
	u.frac = 1
	u.mu.Unlock()
	u.changed()
	u.fire("ready", ver)
}

// Cancel прерывает скачивание (состояние возвращается к «доступна»).
func (u *Updater) Cancel() {
	u.mu.Lock()
	cancel := u.dlCancel
	u.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Apply подменяет бинарник и перезапускает приложение. Отвечает мгновенно:
// реальная работа — с короткой паузой, чтобы HTTP-ответ успел уйти.
func (u *Updater) Apply() {
	u.mu.Lock()
	if u.state != updReady || u.exeNew == "" {
		u.mu.Unlock()
		return
	}
	u.state = updApplying
	u.mu.Unlock()
	u.changed()

	go func() {
		time.Sleep(300 * time.Millisecond) // отдать HTTP-ответ до остановки сервера
		if err := swapBinary(u.exeNew); err != nil {
			u.fail(err)
			return
		}
		if u.onRestart != nil {
			u.onRestart() // корректный выход через onExit приложения
		}
	}()
}

// Start запускает фоновый цикл проверок (работает всегда, но проверяет
// только при включённом автообновлении — чтобы переключатель действовал
// без перезапуска приложения).
func (u *Updater) Start() { go u.Loop() }

// Loop — сам цикл; отдельная функция ради safeLoop-обёртки в main.
func (u *Updater) Loop() {
	first := time.NewTimer(updFirstDelay)
	daily := time.NewTicker(updInterval)
	defer first.Stop()
	defer daily.Stop()
	for {
		select {
		case <-u.stop:
			return
		case <-u.poke:
			if u.Auto() {
				u.autoPass()
			}
		case <-first.C:
			if u.Auto() {
				u.autoPass()
			}
		case <-daily.C:
			if u.Auto() {
				u.autoPass()
			}
		}
	}
}

// autoPass — фоновая проверка + скачивание; ошибки не тостим, они видны
// в состоянии на дашборде (не будим пользователя из-за сети раз в сутки).
func (u *Updater) autoPass() {
	if u.checkSync(false) && u.State() == updAvailable {
		u.downloadSync(false)
	}
}

// Stop останавливает цикл и прерывает скачивание (выход/перезапуск).
func (u *Updater) Stop() {
	u.stopOnce.Do(func() {
		close(u.stop)
	})
	u.Cancel()
}

// ── вспомогательные функции ──────────────────────────────────────────

// fetchRelease запрашивает последний релиз с GitHub API.
func fetchRelease(ctx context.Context) (*ghRelease, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, updateAPI, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "SpeedTestTray/"+version)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(T("err_upd_bad_resp", resp.StatusCode))
	}
	var rel ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return nil, err
	}
	if rel.TagName == "" {
		return nil, errors.New(T("err_upd_bad_resp", resp.StatusCode))
	}
	return &rel, nil
}

// assetName — имя бинарника текущей ОС/архитектуры в релизах (release.yml).
func assetName() string {
	switch {
	case runtime.GOOS == "windows":
		return "SpeedTestTray.exe"
	case runtime.GOOS == "linux":
		return "SpeedTestTray-linux-x64"
	case runtime.GOOS == "darwin" && runtime.GOARCH == "arm64":
		return "SpeedTestTray-macos-arm64"
	case runtime.GOOS == "darwin":
		return "SpeedTestTray-macos-x64"
	}
	return ""
}

// pickAssets находит в релизе бинарник name и его контрольную сумму
// name+".sha256" (может не быть — старые релизы; тогда sha = "").
func pickAssets(rel *ghRelease, name string) (asset *ghAsset, sha string) {
	var shaURL string
	for i := range rel.Assets {
		a := &rel.Assets[i]
		if a.Name == name {
			asset = a
		}
		if a.Name == name+".sha256" {
			shaURL = a.BrowserDownloadURL
		}
	}
	if asset == nil || shaURL == "" {
		return asset, ""
	}
	// контрольная сумма — крошечный файл, качаем тут же
	cli := &http.Client{Timeout: 15 * time.Second}
	resp, err := cli.Get(shaURL)
	if err != nil {
		return asset, ""
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil || resp.StatusCode != http.StatusOK {
		return asset, ""
	}
	fields := strings.Fields(string(body))
	if len(fields) > 0 {
		return asset, fields[0]
	}
	return asset, ""
}

// downloadFile качает url в dst: стримит в dst+".part", по завершении
// переименовывает (Windows не переименует открытый файл). Прогресс —
// колбэк с долей 0..1 и байтами; может быть nil.
func downloadFile(ctx context.Context, url, dst string, prog func(frac float64, recv, total int64)) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errors.New(T("err_upd_http", resp.StatusCode))
	}
	// Редирект GitHub отдаёт Content-Length; -1 (chunked) — покажем байты.
	total := resp.ContentLength

	f, err := os.OpenFile(dst+".part", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()

	buf := make([]byte, 512<<10)
	var got int64
	last := time.Now()
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return "", werr
			}
			got += int64(n)
			if prog != nil && time.Since(last) >= 300*time.Millisecond {
				last = time.Now()
				frac := 0.0
				if total > 0 {
					frac = float64(got) / float64(total)
				}
				prog(frac, got, total)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", rerr
		}
	}
	if cerr := ctx.Err(); cerr != nil {
		return "", cerr
	}
	// Windows не переименует открытый файл — закрываем явно
	if err := f.Close(); err != nil {
		return "", err
	}
	return dst, os.Rename(dst+".part", dst)
}

// verifyDownload проверяет размер и sha256 скачанного файла.
func verifyDownload(path, shaWant string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st.Size() < updMinAssetSize {
		return errors.New(T("err_upd_small", st.Size()))
	}
	if shaWant == "" {
		return nil // релиз без контрольной суммы — пропускаем проверку
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, shaWant) {
		return errors.New(T("err_upd_checksum"))
	}
	return nil
}

// swapBinary подменяет текущий exe на новый файл (dst = <exe>.new):
// Windows позволяет переименовать работающий exe, но не заменить —
// поэтому exe уезжает в .old, новый встаёт на его место.
func swapBinary(dst string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if abs, err := filepath.Abs(exe); err == nil {
		exe = abs
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dst, 0o755); err != nil {
			return err
		}
		if err := os.Rename(dst, exe); err != nil {
			return err
		}
	} else {
		old := exe + ".old"
		_ = os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			return err
		}
		if err := os.Rename(dst, exe); err != nil {
			_ = os.Rename(old, exe) // откат на место
			return err
		}
	}
	return spawnRestarter(exe)
}

// spawnRestarter запускает отсоединённый «перезапускатель»: он ждёт пару
// секунд (пока старый процесс освободит порт 8787) и стартует новый exe.
// Windows: сценарий пишется во временный .cmd — вариант
// exec.Command("cmd", "/c", script) не годится: Go экранирует кавычки
// сценария как \", а cmd.exe так их не понимает.
func spawnRestarter(exe string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		bat := exe + ".restart.cmd"
		script := "@echo off\r\n" +
			"ping -n 4 127.0.0.1 >nul\r\n" + // ~3 с: старый процесс освобождает порт
			"start \"\" \"" + exe + "\"\r\n" +
			"del \"%~f0\"\r\n" // самоудаление; на случай сбоя — cleanupUpdateFiles
		if err := os.WriteFile(bat, []byte(script), 0o644); err != nil {
			return err
		}
		cmd = exec.Command(bat) // CreateProcess сам запускает .cmd через cmd.exe
	} else {
		script := fmt.Sprintf(`sleep 3; exec "%s"`, exe)
		cmd = exec.Command("sh", "-c", script)
	}
	cmd.SysProcAttr = hideConsoleSysProcAttr()
	if err := cmd.Start(); err != nil {
		return errors.New(T("upd_restart_err", err))
	}
	go func() { _ = cmd.Wait() }() // переиспользуем зомби-процесс cmd
	return nil
}

// cleanupUpdateFiles удаляет за собой прошлые файлы обновления:
// .old (предыдущий бинарник), .new и .new.part (недокачанное),
// .restart.cmd (если самоудаление рестартера не сработало).
func cleanupUpdateFiles() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	for _, suffix := range []string{".old", ".new", ".new.part", ".restart.cmd"} {
		_ = os.Remove(exe + suffix)
	}
}

// parseSemver разбирает «v1.2.3» / «1.2.3-rc1» → {1,2,3}.
func parseSemver(s string) ([3]int, bool) {
	var v [3]int
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i] // суффиксы -rc1/-beta и метабилд не сравниваем
	}
	parts := strings.SplitN(s, ".", 3)
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p == "" {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

// semverNewer: b строго больше a (непарсящиеся версии — никогда не «новее»,
// поэтому dev-сборка не предлагает саму себя обновить).
func semverNewer(a, b string) bool {
	va, okA := parseSemver(a)
	vb, okB := parseSemver(b)
	if !okA || !okB {
		return false
	}
	for i := 0; i < 3; i++ {
		if vb[i] != va[i] {
			return vb[i] > va[i]
		}
	}
	return false
}
