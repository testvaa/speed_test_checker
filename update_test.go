package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// testServer — мини-аналог httptest.NewServer: в урезанном тулчейне
// (GOROOT без net/http/httptest) собираем сервер вручную на 127.0.0.1:0.
type testServer struct {
	URL string
	srv *http.Server
	ln  net.Listener
}

func newTestServer(h http.Handler) *testServer {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	s := &testServer{URL: "http://" + ln.Addr().String(), srv: &http.Server{Handler: h}, ln: ln}
	go func() { _ = s.srv.Serve(ln) }()
	return s
}

func (s *testServer) Close() { _ = s.srv.Close() }

func TestParseSemver(t *testing.T) {
	ok := map[string][3]int{
		"v1.2.3":        {1, 2, 3},
		"1.2.3":         {1, 2, 3},
		"v10.20.30":     {10, 20, 30},
		"v1.2.3-rc1":    {1, 2, 3},
		"v1.2.3+build5": {1, 2, 3},
		" v2.0.0 ":      {2, 0, 0},
	}
	for s, want := range ok {
		got, valid := parseSemver(s)
		if !valid {
			t.Errorf("parseSemver(%q): ожидал валидность", s)
			continue
		}
		if got != want {
			t.Errorf("parseSemver(%q) = %v, хочу %v", s, got, want)
		}
	}
	bad := []string{"", "dev", "v1.2", "1.2.3.4", "v1.x.3", "v-1.2.3", "v1..3"}
	for _, s := range bad {
		if _, valid := parseSemver(s); valid {
			t.Errorf("parseSemver(%q): ожидал невалидность", s)
		}
	}
}

func TestSemverNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v1.2.3", "v1.2.4", true},
		{"v1.2.3", "v1.3.0", true},
		{"v1.9.0", "v2.0.0", true},
		{"v1.2.3", "v1.2.3", false},
		{"v1.2.4", "v1.2.3", false},
		{"v2.0.0", "v1.9.9", false},
		// dev-сборка и мусор никогда не «старее» релиза
		{"dev", "v9.9.9", false},
		{"v1.2.3", "nightly", false},
		{"dev", "dev", false},
		// суффиксы отбрасываются: rc считается той же тройкой
		{"v1.2.3", "v1.2.3-rc1", false},
		{"v1.2.3-rc1", "v1.2.4", true},
	}
	for _, c := range cases {
		if got := semverNewer(c.a, c.b); got != c.want {
			t.Errorf("semverNewer(%q, %q) = %v, хочу %v", c.a, c.b, got, c.want)
		}
	}
}

func TestAssetNamePerOS(t *testing.T) {
	got := assetName()
	var want string
	switch runtime.GOOS {
	case "windows":
		want = "SpeedTestTray.exe"
	case "linux":
		want = "SpeedTestTray-linux-x64"
	case "darwin":
		if runtime.GOARCH == "arm64" {
			want = "SpeedTestTray-macos-arm64"
		} else {
			want = "SpeedTestTray-macos-x64"
		}
	default:
		want = ""
	}
	if got != want {
		t.Errorf("assetName() = %q, хочу %q", got, want)
	}
}

func TestPickAssets(t *testing.T) {
	shaBody := shaHex("binary-bytes")
	srv := newTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			_, _ = w.Write([]byte(shaBody + "  SpeedTestTray.exe\n"))
			return
		}
		_, _ = w.Write([]byte("binary-bytes"))
	}))
	defer srv.Close()

	rel := &ghRelease{TagName: "v1.2.0", Assets: []ghAsset{
		{Name: "SpeedTestTray.exe", BrowserDownloadURL: srv.URL + "/exe"},
		{Name: "SpeedTestTray.exe.sha256", BrowserDownloadURL: srv.URL + "/exe.sha256"},
		{Name: "SpeedTestTray-linux-x64", BrowserDownloadURL: srv.URL + "/linux"},
	}}
	asset, sha := pickAssets(rel, "SpeedTestTray.exe")
	if asset == nil {
		t.Fatal("pickAssets: бинарник не найден")
	}
	if asset.BrowserDownloadURL != srv.URL+"/exe" {
		t.Errorf("pickAssets: неверный URL ассета: %s", asset.BrowserDownloadURL)
	}
	if sha != shaBody {
		t.Errorf("pickAssets: sha256 = %q, хочу %q", sha, shaBody)
	}

	// релиз без контрольной суммы (старые) — sha пустой, бинарник найден
	relOld := &ghRelease{TagName: "v1.1.0", Assets: []ghAsset{
		{Name: "SpeedTestTray.exe", BrowserDownloadURL: srv.URL + "/exe"},
	}}
	if a, sha := pickAssets(relOld, "SpeedTestTray.exe"); a == nil || sha != "" {
		t.Errorf("pickAssets(старый релиз) = ассет:%v sha:%q; хочу ассет и пустой sha", a, sha)
	}

	// сборки под платформу нет — nil
	if a, _ := pickAssets(rel, "SpeedTestTray-macos-x64"); a != nil {
		t.Errorf("pickAssets(нет ассета) = %v, хочу nil", a)
	}
}

func TestVerifyDownload(t *testing.T) {
	dir := t.TempDir()
	data := strings.Repeat("speedtest-tray-binary ", 100000) // > 1 МиБ
	p := filepath.Join(dir, "SpeedTestTray.exe.new")
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	good := shaHex(data)

	if err := verifyDownload(p, good); err != nil {
		t.Errorf("verifyDownload(верная сумма) = %v, хочу nil", err)
	}
	if err := verifyDownload(p, strings.ToUpper(good)); err != nil {
		t.Errorf("verifyDownload(верхний регистр) = %v, хочу nil (регистронезависимо)", err)
	}
	if err := verifyDownload(p, ""); err != nil {
		t.Errorf("verifyDownload(без суммы) = %v, хочу nil (проверка пропускается)", err)
	}
	if err := verifyDownload(p, "deadbeef"); err == nil {
		t.Error("verifyDownload(чужая сумма) = nil, хочу ошибку")
	}

	// подозрительно малый файл
	small := filepath.Join(dir, "small.exe")
	_ = os.WriteFile(small, []byte("tiny"), 0o644)
	if err := verifyDownload(small, ""); err == nil {
		t.Error("verifyDownload(маленький файл) = nil, хочу ошибку")
	}
}

func TestDownloadFile(t *testing.T) {
	payload := strings.Repeat("x", 300000)
	srv := newTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	dst := filepath.Join(t.TempDir(), "app.new")
	got, err := downloadFile(context.Background(), srv.URL+"/f", dst, nil)
	if err != nil {
		t.Fatalf("downloadFile: %v", err)
	}
	if got != dst {
		t.Errorf("downloadFile вернул %q, хочу %q", got, dst)
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("прочитать скачанное: %v", err)
	}
	if string(b) != payload {
		t.Error("скачанный файл не совпадает с ответом сервера")
	}
	if _, err := os.Stat(dst + ".part"); !errors.Is(err, os.ErrNotExist) {
		t.Error(".part должен быть переименован, а не остаться рядом")
	}
}

func TestDownloadFileHTTPError(t *testing.T) {
	srv := newTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	dst := filepath.Join(t.TempDir(), "app.new")
	if _, err := downloadFile(context.Background(), srv.URL+"/f", dst, nil); err == nil {
		t.Error("downloadFile(HTTP 404) = nil, хочу ошибку")
	}
}

func TestDownloadFileCancel(t *testing.T) {
	release := make(chan struct{})
	srv := newTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		_, _ = w.Write([]byte("never-completes"))
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	dst := filepath.Join(t.TempDir(), "app.new")
	errCh := make(chan error, 1)
	go func() {
		_, err := downloadFile(ctx, srv.URL+"/f", dst, nil)
		errCh <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Error("downloadFile(отмена) = nil, хочу ошибку контекста")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("downloadFile не отреагировал на отмену за 5 с")
	}
}

func TestUpdaterLifecycle(t *testing.T) {
	u := NewUpdater(true)
	if u.State() != updIdle {
		t.Fatalf("новый апдейтер в состоянии %q, хочу idle", u.State())
	}
	if !u.Auto() {
		t.Error("Auto() = false, хочу true")
	}
	if u.Busy() {
		t.Error("Busy() = true в idle")
	}

	var changed int
	u.OnChanged(func(updateState) { changed++ })

	// fail переводит в error и рассылает состояние
	u.fail(errors.New("boom"))
	if u.State() != updError {
		t.Errorf("после fail состояние %q, хочу error", u.State())
	}
	if changed == 0 {
		t.Error("onChanged не вызван после fail")
	}

	// тумблер туда-обратно
	u.SetAuto(false)
	if u.Auto() {
		t.Error("SetAuto(false) не сработал")
	}
	u.SetAuto(true)
	if !u.Auto() {
		t.Error("SetAuto(true) не сработал")
	}

	// Stop идемпотентен и не блокирует
	u.Stop()
	u.Stop()
}

func TestSnapshotFields(t *testing.T) {
	u := NewUpdater(false)
	s := u.Snapshot()
	if s.Version != version {
		t.Errorf("Snapshot.Version = %q, хочу %q", s.Version, version)
	}
	if s.State != updIdle || s.Auto {
		t.Errorf("Snapshot: state=%q auto=%v, хочу idle/false", s.State, s.Auto)
	}
}

// shaHex — hex(sha256(s)), для контрольных сумм в тестах.
func shaHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
