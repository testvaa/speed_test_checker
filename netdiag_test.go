package main

// Тесты анализатора сети: правила (балл/рекомендации), парсеры вывода
// системных утилит и расчёт джиттера. Только чистые функции — гонки и
// сетевое окружение не тестируем.

import "testing"

func TestBloatGrade(t *testing.T) {
	cases := []struct {
		add  float64
		want string
	}{
		{0, "A"}, {29.9, "A"}, {30, "A"}, {31, "B"}, {60, "B"},
		{90, "C"}, {120, "C"}, {200, "D"}, {250, "D"}, {300, "E"},
		{400, "E"}, {500, "F"},
	}
	for _, c := range cases {
		if got := bloatGrade(c.add); got != c.want {
			t.Errorf("bloatGrade(%v) = %q, хочу %q", c.add, got, c.want)
		}
	}
}

func TestNDQuality(t *testing.T) {
	cases := []struct {
		v, good, bad, want float64
	}{
		{0, 0, 10, 1}, {5, 0, 10, .5}, {10, 0, 10, 0}, {15, 0, 10, 0},
		{50, 50, 250, 1}, {150, 50, 250, .5}, {250, 50, 250, 0},
	}
	for _, c := range cases {
		if got := ndQuality(c.v, c.good, c.bad); got != c.want {
			t.Errorf("ndQuality(%v,%v,%v) = %v, хочу %v", c.v, c.good, c.bad, got, c.want)
		}
	}
}

func TestNDJitter(t *testing.T) {
	cases := []struct {
		samples []float64
		want    float64
	}{
		{[]float64{}, 0},
		{[]float64{10}, 0},
		{[]float64{10, 20}, 10},
		{[]float64{10, 20, 10}, 10}, // mean(10, 10)
		{[]float64{10, 10, 10}, 0},
	}
	for _, c := range cases {
		if got := ndJitter(c.samples); got != c.want {
			t.Errorf("ndJitter(%v) = %v, хочу %v", c.samples, got, c.want)
		}
	}
}

func TestComputeScore(t *testing.T) {
	allGood := ndMetrics{
		GwDone: true, GwOK: true,
		NetDone: true, NetOK: true, NetAvgMs: 20, NetJitterMs: 3,
		DNSDone: true, DNSSysOK: true, DNSAltOK: true, DNSSysAvgMs: 12, DNSAltBestMs: 15,
		BloatDone: true, BloatOK: true, BloatAddMs: 10,
	}
	if got := computeScore(allGood); got != 100 {
		t.Errorf("computeScore(всё отлично) = %d, хочу 100", got)
	}

	allBad := ndMetrics{
		GwDone: true, GwOK: true, GwLossPct: 20,
		NetDone: true, NetOK: true, NetLossPct: 20, NetAvgMs: 300, NetJitterMs: 80,
		DNSDone: true, DNSSysOK: false, DNSAltOK: true,
		BloatDone: true, BloatOK: true, BloatAddMs: 500,
	}
	if got := computeScore(allBad); got > 15 {
		t.Errorf("computeScore(всё плохо) = %d, хочу ≤ 15", got)
	}

	// частичный результат: только пинг, идеальный — свёртка перенормируется
	pingOnly := ndMetrics{NetDone: true, NetOK: true, NetAvgMs: 20, NetJitterMs: 3}
	if got := computeScore(pingOnly); got != 100 {
		t.Errorf("computeScore(только пинг, отлично) = %d, хочу 100", got)
	}

	// ничего не измерено
	if got := computeScore(ndMetrics{}); got != 0 {
		t.Errorf("computeScore(пусто) = %d, хочу 0", got)
	}

	// слабый Wi-Fi ограничивает балл сверху
	weakWifi := allGood
	weakWifi.WifiDone, weakWifi.WifiSignalPct = true, 25
	if got := computeScore(weakWifi); got != 45 {
		t.Errorf("computeScore(слабый Wi-Fi) = %d, хочу 45 (потолок)", got)
	}
}

func TestRecommendations(t *testing.T) {
	has := func(recs []ndRec, code string) bool {
		for _, r := range recs {
			if r.Code == code {
				return true
			}
		}
		return false
	}
	cases := []struct {
		name string
		m    ndMetrics
		want []string
		not  []string
	}{
		{
			name: "потери на шлюзе — локальная сеть",
			m:    ndMetrics{GwDone: true, GwOK: true, GwLossPct: 12, NetDone: true, NetOK: true, NetLossPct: 12, NetAvgMs: 30, NetJitterMs: 3},
			want: []string{"gw_loss"},
			not:  []string{"net_loss"}, // канал провайдера не виним
		},
		{
			name: "чистый шлюз, потери наружу — провайдер",
			m:    ndMetrics{GwDone: true, GwOK: true, NetDone: true, NetOK: true, NetLossPct: 8, NetAvgMs: 30, NetJitterMs: 3},
			want: []string{"net_loss"},
		},
		{
			name: "высокая задержка и джиттер",
			m:    ndMetrics{NetDone: true, NetOK: true, NetAvgMs: 220, NetJitterMs: 40, NetLossPct: 0},
			want: []string{"latency", "jitter"},
		},
		{
			name: "сильный bufferbloat",
			m:    ndMetrics{NetDone: true, NetOK: true, NetAvgMs: 20, NetJitterMs: 3, NetLossPct: 0, BloatDone: true, BloatOK: true, BloatAddMs: 250},
			want: []string{"bloat_bad"},
		},
		{
			name: "умеренный bufferbloat",
			m:    ndMetrics{NetDone: true, NetOK: true, NetAvgMs: 20, NetJitterMs: 3, NetLossPct: 0, BloatDone: true, BloatOK: true, BloatAddMs: 70},
			want: []string{"bloat_warn"},
		},
		{
			name: "системный DNS лёг",
			m:    ndMetrics{NetDone: true, NetOK: true, NetAvgMs: 20, NetJitterMs: 3, NetLossPct: 0, DNSDone: true, DNSSysOK: false, DNSAltOK: true},
			want: []string{"dns_fail"},
		},
		{
			name: "системный DNS медленный",
			m:    ndMetrics{NetDone: true, NetOK: true, NetAvgMs: 20, NetJitterMs: 3, NetLossPct: 0, DNSDone: true, DNSSysOK: true, DNSSysAvgMs: 120, DNSAltOK: true, DNSAltBestMs: 15},
			want: []string{"dns_slow"},
		},
		{
			name: "слабый Wi-Fi",
			m:    ndMetrics{NetDone: true, NetOK: true, NetAvgMs: 20, NetJitterMs: 3, NetLossPct: 0, WifiDone: true, WifiSignalPct: 40},
			want: []string{"wifi_weak"},
		},
		{
			name: "скорость упала против максимума",
			m:    ndMetrics{NetDone: true, NetOK: true, NetAvgMs: 20, NetJitterMs: 3, NetLossPct: 0, DownKnown: true, DownMbps: 20, RecentMaxMbps: 90},
			want: []string{"speed_drop"},
		},
		{
			name: "всё в порядке",
			m:    ndMetrics{GwDone: true, GwOK: true, NetDone: true, NetOK: true, NetAvgMs: 20, NetJitterMs: 3, NetLossPct: 0, BloatDone: true, BloatOK: true, BloatAddMs: 10},
			want: []string{"ok"},
		},
		{
			name: "частичный прогон без пинга — «ok» не выдаём",
			m:    ndMetrics{BloatDone: true, BloatOK: true, BloatAddMs: 10},
			want: nil,
			not:  []string{"ok"},
		},
	}
	for _, c := range cases {
		recs := recommendations(c.m)
		for _, w := range c.want {
			if !has(recs, w) {
				t.Errorf("%s: рекомендации %v без %q", c.name, recs, w)
			}
		}
		for _, n := range c.not {
			if has(recs, n) {
				t.Errorf("%s: рекомендации %v содержат лишнее %q", c.name, recs, n)
			}
		}
		// сортировка: серьёзное сверху
		for i := 1; i < len(recs); i++ {
			if recs[i-1].Sev < recs[i].Sev {
				t.Errorf("%s: рекомендации не отсортированы по sev: %v", c.name, recs)
			}
		}
	}
}

func TestParseNetshWlan(t *testing.T) {
	en := `
There are 1 wireless interfaces on the system:

Name                   : Wi-Fi
Description            : Intel(R) Wi-Fi 6 AX201 160MHz
State                  : connected
SSID                   : HomeNet
BSSID                  : aa:bb:cc:dd:ee:ff
Signal                 : 86%
Profile                : HomeNet
`
	ssid, pct, ok := parseNetshWlan(en)
	if !ok || ssid != "HomeNet" || pct != 86 {
		t.Errorf("parseNetshWlan(en) = (%q, %d, %v), хочу (HomeNet, 86, true)", ssid, pct, ok)
	}

	ru := `
В системе имеется 1 беспроводной интерфейс:

Имя                   : Wi-Fi
Описание              : MediaTek Wi-Fi 6 MT7921
Состояние             : подключен
SSID                   : МояСеть
BSSID                  : 11:22:33:44:55:66
Сигнал                : 42%
Профиль               : МояСеть
`
	ssid, pct, ok = parseNetshWlan(ru)
	if !ok || ssid != "МояСеть" || pct != 42 {
		t.Errorf("parseNetshWlan(ru) = (%q, %d, %v), хочу (МояСеть, 42, true)", ssid, pct, ok)
	}

	// отключённый интерфейс + "disconnected" не должен попасть в connected
	off := `
Name                   : Wi-Fi
State                  : disconnected
SSID                   : Something
Signal                 : 0%
`
	if _, _, ok := parseNetshWlan(off); ok {
		t.Errorf("parseNetshWlan(отключен) = ok, хочу false")
	}

	// два интерфейса: первый отключён, второй подключён
	two := `
Name                   : Wi-Fi 2
State                  : disconnected
Name                   : Wi-Fi
State                  : connected
SSID                   : Second
Signal                 : 71%
`
	ssid, pct, ok = parseNetshWlan(two)
	if !ok || ssid != "Second" || pct != 71 {
		t.Errorf("parseNetshWlan(два) = (%q, %d, %v), хочу (Second, 71, true)", ssid, pct, ok)
	}
}

func TestParseRouteLinux(t *testing.T) {
	gw, dev, ok := parseRouteLinux("default via 192.168.1.1 dev wlp3s0 proto dhcp metric 600\n192.168.1.0/24 dev wlp3s0 ...\n")
	if !ok || gw != "192.168.1.1" || dev != "wlp3s0" {
		t.Errorf("parseRouteLinux = (%q, %q, %v), хочу (192.168.1.1, wlp3s0, true)", gw, dev, ok)
	}
	if _, _, ok := parseRouteLinux("192.168.1.0/24 dev eth0 ...\n"); ok {
		t.Errorf("parseRouteLinux без default = ok, хочу false")
	}
}

func TestParseRouteDarwin(t *testing.T) {
	out := `
   route to: 1.1.1.1
destination: default
    gateway: 192.168.0.1
  interface: en0
`
	gw, iface, ok := parseRouteDarwin(out)
	if !ok || gw != "192.168.0.1" || iface != "en0" {
		t.Errorf("parseRouteDarwin = (%q, %q, %v), хочу (192.168.0.1, en0, true)", gw, iface, ok)
	}
}
