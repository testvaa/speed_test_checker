package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

// speed.cloudflare.com — тот же публичный API, которым пользуется
// сайт speed.cloudflare.net. Ключи не нужны.
const baseURL = "https://speed.cloudflare.com"

// Без браузерных заголовков /meta отвечает 403.
const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 SpeedTestTray/1.0"

// Result — итог одной проверки (аналог сводки на speed.cloudflare.net).
type Result struct {
	Timestamp    time.Time
	DownloadMbps float64
	UploadMbps   float64
	PingMs       float64 // пинг без нагрузки (TTFB)
	PingDownMs   float64 // пинг во время загрузки
	PingUpMs     float64 // пинг во время отдачи
	JitterMs     float64 // вариация задержки
	LossPct      float64 // доля неудачных проб, %
	ClientIP     string
	IPVersion    string // IPv4 / IPv6
	Network      string // провайдер (asOrganization)
	ASN          int
	ServerCity   string // город сервера Cloudflare
	Colo         string // IATA-код точки присутствия
	HTTPProtocol string // например HTTP/2
	Truncated    bool   // тест обрезан по таймауту, скорость посчитана по факту
	Err          string // непусто — проверка не удалась
}

// ProgressFn сообщает о ходе проверки: phase (meta/ping/download/upload/loss)
// и доля выполнения 0..1. Может вызываться из горутин — реализация должна
// быть потокобезопасной.
type ProgressFn func(phase string, frac float64)

// meta — поля ответа GET /meta.
type meta struct {
	ClientIP     string `json:"clientIp"`
	HTTPProtocol string `json:"httpProtocol"`
	ASN          int    `json:"asn"`
	ASOrg        string `json:"asOrganization"`
	Colo         struct {
		IATA string `json:"iata"`
		City string `json:"city"`
	} `json:"colo"`
}

func newHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        32,
			MaxIdleConnsPerHost: 32,
			IdleConnTimeout:     30 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
			ForceAttemptHTTP2:   true,
		},
	}
}

// RunCheck выполняет полную проверку в порядке: meta → пинг/джиттер →
// загрузка (с пингом под нагрузкой) → отдача → потери.
func RunCheck(cfg *Config, progress ProgressFn) *Result {
	res := &Result{Timestamp: time.Now()}
	client := newHTTPClient()
	if tr, ok := client.Transport.(*http.Transport); ok {
		defer tr.CloseIdleConnections()
	}
	ctx := context.Background()
	pg := func(phase string, frac float64) {
		if progress != nil {
			progress(phase, frac)
		}
	}

	pg("meta", 0)
	m, err := fetchMeta(ctx, client)
	if err != nil {
		res.Err = fmt.Sprintf(T("err_no_conn"), err)
		return res
	}
	pg("meta", 1)
	res.ClientIP = m.ClientIP
	res.Network = m.ASOrg
	res.ASN = m.ASN
	res.Colo = m.Colo.IATA
	res.ServerCity = m.Colo.City
	if res.ServerCity == "" {
		res.ServerCity = coloCity(m.Colo.IATA)
	}
	res.HTTPProtocol = m.HTTPProtocol
	if ip := net.ParseIP(m.ClientIP); ip != nil {
		if ip.To4() != nil {
			res.IPVersion = "IPv4"
		} else {
			res.IPVersion = "IPv6"
		}
	}

	// Пинг без нагрузки и джиттер (первая проба — прогрев, не считаем).
	if err := measureLatency(ctx, client, cfg.LatencyProbes, res, pg); err != nil {
		res.Err = err.Error()
		return res
	}

	if err := measureDownload(ctx, client, cfg, res, pg); err != nil {
		res.Err = err.Error()
		return res
	}
	if err := measureUpload(ctx, client, cfg, res, pg); err != nil {
		res.Err = err.Error()
		return res
	}
	res.LossPct = measureLoss(ctx, client, cfg.LossProbes, pg)
	return res
}

func fetchMeta(ctx context.Context, c *http.Client) (*meta, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/meta", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", "https://speed.cloudflare.net/")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var m meta
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("/meta: HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&m); err != nil {
		return nil, err
	}
	if m.ClientIP == "" {
		return nil, errors.New(T("err_meta_empty"))
	}
	return &m, nil
}

// probeTTFB возвращает время до первого байта ответа на запрос ?bytes=0, мс.
func probeTTFB(ctx context.Context, c *http.Client) (float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/__down?bytes=0", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", userAgent)
	start := time.Now()
	var first time.Time
	trace := &httptrace.ClientTrace{
		GotFirstResponseByte: func() { first = time.Now() },
	}
	*req = *req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if first.IsZero() {
		first = time.Now()
	}
	return float64(first.Sub(start).Microseconds()) / 1000, nil
}

func measureLatency(ctx context.Context, c *http.Client, probes int, res *Result, pg ProgressFn) error {
	if _, err := probeTTFB(ctx, c); err != nil { // прогрев соединения
		return fmt.Errorf(T("err_ping"), err)
	}
	samples := make([]float64, 0, probes)
	for i := 0; i < probes; i++ {
		v, err := probeTTFB(ctx, c)
		if err == nil {
			samples = append(samples, v)
		}
		pg("ping", float64(i+1)/float64(probes))
	}
	if len(samples) < 2 {
		return errors.New(T("err_ping_all"))
	}
	var sum, jitter float64
	for i, v := range samples {
		sum += v
		if i > 0 {
			d := v - samples[i-1]
			if d < 0 {
				d = -d
			}
			jitter += d
		}
	}
	res.PingMs = sum / float64(len(samples))
	res.JitterMs = jitter / float64(len(samples)-1)
	return nil
}

// probeWhileRunning шлёт TTFB-пробы, пока не закроют stop; сам закрывает out.
func probeWhileRunning(ctx context.Context, c *http.Client, stop <-chan struct{}, out chan<- float64) {
	defer close(out)
	for {
		select {
		case <-stop:
			return
		default:
		}
		if v, err := probeTTFB(ctx, c); err == nil {
			select {
			case out <- v:
			default:
			}
		}
	}
}

func avgChan(ch chan float64) float64 {
	var sum float64
	n := 0
	for v := range ch {
		sum += v
		n++
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// downloadChunk — размер одного запроса __down. Cloudflare охотно отдаёт
// небольшие запросы, но одиночные на 100 МБ начинает отклонять (HTTP 403,
// «1 байт»), поэтому большой объём качается последовательными кусками —
// так же устроен и сам speed.cloudflare.net.
const downloadChunk = int64(25) << 20

// measureDownload качает download_mb из __down и меряет скорость; заодно
// параллельные пробы дают пинг под нагрузкой. Если этап сорвался по
// таймауту, почти ничего не получив (медленный старт), — одна повторная
// попытка с новым запасом времени.
func measureDownload(ctx context.Context, c *http.Client, cfg *Config, res *Result, pg ProgressFn) error {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		err = downloadAttempt(ctx, c, cfg, res, pg)
		if err == nil || !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
	}
	return err
}

func downloadAttempt(ctx context.Context, c *http.Client, cfg *Config, res *Result, pg ProgressFn) error {
	n := int64(cfg.DownloadMB) << 20
	ctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutSec)*time.Second)
	defer cancel()

	stop := make(chan struct{})
	loaded := make(chan float64, 64)
	go probeWhileRunning(ctx, c, stop, loaded)
	defer func() {
		close(stop)
		res.PingDownMs = avgChan(loaded)
	}()

	var total, lastMark int64
	var first time.Time
	var readErr error
	buf := make([]byte, 64*1024)
	for total < n && readErr == nil {
		want := n - total
		if want > downloadChunk {
			want = downloadChunk
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			baseURL+"/__down?bytes="+fmt.Sprint(want), nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", userAgent)
		trace := &httptrace.ClientTrace{
			GotFirstResponseByte: func() {
				if first.IsZero() {
					first = time.Now()
				}
			},
		}
		*req = *req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
		resp, err := c.Do(req)
		if err != nil {
			return fmt.Errorf(T("err_dl"), err)
		}
		if resp.StatusCode >= 400 {
			code := resp.StatusCode
			resp.Body.Close()
			return fmt.Errorf(T("err_dl_http"), code)
		}
		pg("download", float64(total)/float64(n))
		for {
			k, rerr := resp.Body.Read(buf)
			total += int64(k)
			if total-lastMark >= 256<<10 || rerr != nil { // прогресс каждые ~256 КБ
				lastMark = total
				pg("download", float64(total)/float64(n))
			}
			if rerr != nil {
				if rerr != io.EOF { // EOF — нормальное завершение куска
					readErr = rerr
				}
				break
			}
		}
		resp.Body.Close()
	}
	pg("download", 1)

	if total < 1<<20 { // меньше мегабайта — это не измерение
		if readErr != nil {
			return fmt.Errorf(T("err_dl_short"), total, readErr)
		}
		return fmt.Errorf(T("err_dl_short2"), total)
	}
	if total < n {
		res.Truncated = true
	}
	if first.IsZero() {
		first = time.Now()
	}
	res.DownloadMbps = mbps(total, time.Since(first))
	return nil
}

// countReader считает прочитанные из него байты (для замера отдачи).
type countReader struct {
	r    io.Reader
	read atomic.Int64
}

func (cr *countReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	cr.read.Add(int64(n))
	return n, err
}

// measureUpload отправляет upload_mb случайных байт в __up; параллельные
// пробы дают пинг под нагрузкой отдачи.
func measureUpload(ctx context.Context, c *http.Client, cfg *Config, res *Result, pg ProgressFn) error {
	n := int64(cfg.UploadMB) << 20
	ctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutSec)*time.Second)
	defer cancel()

	payload := make([]byte, n)
	if _, err := rand.Read(payload); err != nil {
		return fmt.Errorf(T("err_ul"), err)
	}

	stop := make(chan struct{})
	loaded := make(chan float64, 64)
	go probeWhileRunning(ctx, c, stop, loaded)

	cr := &countReader{r: bytes.NewReader(payload)}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/__up", cr)
	if err != nil {
		close(stop)
		avgChan(loaded)
		return err
	}
	req.ContentLength = n
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("User-Agent", userAgent)

	pg("upload", 0)
	pollStop := make(chan struct{})
	go func() { // прогресс отдаи — опрос счётчика прочитанных байт
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-pollStop:
				return
			case <-t.C:
				pg("upload", float64(cr.read.Load())/float64(n))
			}
		}
	}()

	start := time.Now()
	resp, err := c.Do(req) // возвращается, когда тело отправлено и пришёл ответ
	elapsed := time.Since(start)
	close(pollStop)
	pg("upload", 1)
	close(stop)
	res.PingUpMs = avgChan(loaded)
	if err != nil {
		// Таймаут при большом объёме — не ошибка: считаем скорость по
		// фактически отправленному и помечаем тест как обрезанный
		// (так же, как у загрузки). Порог 5 МБ отсекает «ничего не успели».
		if errors.Is(err, context.DeadlineExceeded) && cr.read.Load() >= 5<<20 {
			res.Truncated = true
			res.UploadMbps = mbps(cr.read.Load(), elapsed)
			return nil
		}
		return fmt.Errorf(T("err_ul"), err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		code := resp.StatusCode
		resp.Body.Close()
		return fmt.Errorf(T("err_ul_http"), code)
	}
	resp.Body.Close()

	if cr.read.Load() < n/2 { // отправили меньше половины — не измерение
		return fmt.Errorf(T("err_ul_short"), cr.read.Load(), n)
	}
	res.UploadMbps = mbps(cr.read.Load(), elapsed)
	return nil
}

// measureLoss оценивает долю потерь: probes мелких параллельных запросов
// (пачками по 10), ошибка/HTTP>=400 считается потерей. 0 — выключено.
func measureLoss(ctx context.Context, c *http.Client, probes int, pg ProgressFn) float64 {
	if probes <= 0 {
		return 0
	}
	const parallel = 10
	var ok, fail, done atomic.Int64
	var wg sync.WaitGroup
	sem := make(chan struct{}, parallel)
	for i := 0; i < probes; i++ {
		wg.Add(1)
		go func() {
			defer func() {
				done.Add(1)
				pg("loss", float64(done.Load())/float64(probes))
				wg.Done()
			}()
			sem <- struct{}{}
			defer func() { <-sem }()
			pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(pctx, http.MethodGet,
				baseURL+"/__down?bytes=10&r="+randomSuffix(), nil)
			if err != nil {
				fail.Add(1)
				return
			}
			req.Header.Set("User-Agent", userAgent)
			resp, err := c.Do(req)
			if err != nil {
				fail.Add(1)
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode >= 400 {
				fail.Add(1)
			} else {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	total := ok.Load() + fail.Load()
	if total == 0 {
		return 0
	}
	return float64(fail.Load()) / float64(total) * 100
}

func mbps(bytes int64, d time.Duration) float64 {
	sec := d.Seconds()
	if sec <= 0 {
		return 0
	}
	return float64(bytes) * 8 / sec / 1e6
}

func randomSuffix() string {
	var b [6]byte
	rand.Read(b[:])
	return url.QueryEscape(fmt.Sprintf("%x", b[:]))
}

// coloCity переводит IATA-код точки присутствия Cloudflare в город.
var coloCities = map[string]string{
	"AMS": "Амстердам", "ARN": "Стокгольм", "ATH": "Афины", "ATL": "Атланта",
	"BCN": "Барселона", "BER": "Берлин", "BRU": "Брюссель", "BUD": "Будапешт",
	"BUH": "Бухарест", "CPH": "Копенгаген", "DME": "Москва", "DUS": "Дюссельдорф",
	"DXB": "Дубай", "EWR": "Нью-Йорк", "FRA": "Франкфурт", "HAM": "Гамбург",
	"HEL": "Хельсинки", "IST": "Стамбул", "KBP": "Киев", "KIV": "Кишинёв",
	"LCA": "Ларнака", "LED": "Санкт-Петербург", "LHR": "Лондон", "LIS": "Лиссабон",
	"LUX": "Люксембург", "LYS": "Лион", "MAD": "Мадрид", "MAN": "Манчестер",
	"MIL": "Милан", "MRS": "Марсель", "MUC": "Мюнхен", "OSL": "Осло",
	"OTP": "Бухарест", "PRG": "Прага", "RIX": "Рига", "SOF": "София",
	"TLL": "Таллин", "TLV": "Тель-Авив", "VIE": "Вена", "VNO": "Вильнюс",
	"WAW": "Варшава", "ZAG": "Загреб", "YYZ": "Торонто", "ORD": "Чикаго",
	"IAD": "Ашберн", "DFW": "Даллас", "LAX": "Лос-Анджелес", "SJC": "Сан-Хосе",
	"SEA": "Сиэтл", "MIA": "Майами", "SIN": "Сингапур", "HKG": "Гонконг",
	"NRT": "Токио",
}

// coloCitiesEn — английские названия тех же городов.
var coloCitiesEn = map[string]string{
	"AMS": "Amsterdam", "ARN": "Stockholm", "ATH": "Athens", "ATL": "Atlanta",
	"BCN": "Barcelona", "BER": "Berlin", "BRU": "Brussels", "BUD": "Budapest",
	"BUH": "Bucharest", "CPH": "Copenhagen", "DME": "Moscow", "DUS": "Düsseldorf",
	"DXB": "Dubai", "EWR": "New York", "FRA": "Frankfurt", "HAM": "Hamburg",
	"HEL": "Helsinki", "IST": "Istanbul", "KBP": "Kyiv", "KIV": "Chișinău",
	"LCA": "Larnaca", "LED": "Saint Petersburg", "LHR": "London", "LIS": "Lisbon",
	"LUX": "Luxembourg", "LYS": "Lyon", "MAD": "Madrid", "MAN": "Manchester",
	"MIL": "Milan", "MRS": "Marseille", "MUC": "Munich", "OSL": "Oslo",
	"OTP": "Bucharest", "PRG": "Prague", "RIX": "Riga", "SOF": "Sofia",
	"TLL": "Tallinn", "TLV": "Tel Aviv", "VIE": "Vienna", "VNO": "Vilnius",
	"WAW": "Warsaw", "ZAG": "Zagreb", "YYZ": "Toronto", "ORD": "Chicago",
	"IAD": "Ashburn", "DFW": "Dallas", "LAX": "Los Angeles", "SJC": "San Jose",
	"SEA": "Seattle", "MIA": "Miami", "SIN": "Singapore", "HKG": "Hong Kong",
	"NRT": "Tokyo",
}

func coloCity(iata string) string {
	table := coloCities
	if Lang() == LangEn {
		table = coloCitiesEn
	}
	if city, ok := table[iata]; ok {
		return city
	}
	return iata
}
