package main

// Правила анализа сети и композитный балл. Пороги отдельных метрик — из
// отраслевых источников (см. комментарий к ndT); веса балла — наша
// взвешенная свёртка, как у коммерческих тест-сервисов. Всё — чистые
// функции, покрыты тестами (netdiag_test.go).

import (
	"math"
	"sort"
)

// ndT — пороги правил в одном месте. Источники:
//   - потери, задержка, джиттер: ITU-T Y.1541 (классы 0/1: RTT и джиттер
//     для интерактивного трафика) и ITU-T G.1010 (цели для голоса/игр);
//     джиттер как средний |Δ| соседних проб — RFC 3550 §6.4.1;
//   - прирост задержки под нагрузкой (bufferbloat): практика сообщества
//     bufferbloat.net (формат шкалы A–F тестов DSLReports/Waveform);
//   - Wi-Fi: дизайн-гайды Cisco VoWLAN (граница voice-grade −67 dBm),
//     пересчитано в проценты качества сигнала netsh (~50–60%);
//   - DNS и падение скорости против недавнего максимума — операционная
//     практика, отдельного стандарта нет.
//
// Менять порог — только с обновлением комментария об источнике.
var ndT = struct {
	GwLossWarn, GwLossBad   float64 // потери до шлюза, %: 2 / 10
	NetLossWarn, NetLossBad float64 // потери до публичных хостов, %: 2 / 10
	LatWarnMs, LatBadMs     float64 // средний RTT наружу, мс: 100 / 200
	JitWarnMs, JitBadMs     float64 // джиттер, мс: 15 / 30
	BloatWarnMs, BloatBadMs float64 // прирост задержки под нагрузкой, мс: 50 / 150
	WifiWarnPct, WifiBadPct int     // сигнал Wi-Fi, % качества (меньше — хуже): 50 / 30
	DNSSlowMs, DNSRatio     float64 // системный DNS медленнее: 50 мс / в 2 раза
	SpeedDropRatio          float64 // последний замер ниже доли от недавнего максимума: 0.5
	DownMinMbps             float64 // ниже этой скорости «падение» не трактуем: 5
}{
	GwLossWarn: 2, GwLossBad: 10,
	NetLossWarn: 2, NetLossBad: 10,
	LatWarnMs: 100, LatBadMs: 200,
	JitWarnMs: 15, JitBadMs: 30,
	BloatWarnMs: 50, BloatBadMs: 150,
	WifiWarnPct: 50, WifiBadPct: 30,
	DNSSlowMs: 50, DNSRatio: 2,
	SpeedDropRatio: 0.5,
	DownMinMbps: 5,
}

// ndMetrics — вход правил; *Done-флаги отличают «не меряли» от нулей,
// чтобы частичный результат после остановки не трактовался как идеал.
type ndMetrics struct {
	GwDone bool // шлюз пинговался
	GwOK   bool // шлюз ответил хоть раз
	GwLossPct float64

	NetDone    bool // публичные хосты пинговались
	NetOK      bool
	NetLossPct float64
	NetAvgMs   float64
	NetJitterMs float64

	DNSDone     bool
	DNSSysOK    bool // системный резолвер ответил
	DNSSysAvgMs float64
	DNSAltOK    bool // хоть одна альтернатива (1.1.1.1/8.8.8.8) ответила
	DNSAltBestMs float64

	BloatDone bool // нагрузка мерялась
	BloatOK   bool
	BloatAddMs float64 // max прироста под загрузкой и отдачей

	WifiDone     bool // подключение — Wi-Fi и сигнал известен
	WifiSignalPct int

	DownKnown     bool // есть успешные замеры скорости за 7 дней
	DownMbps      float64
	RecentMaxMbps float64
}

// bloatGrade — шкала A–F по приросту задержки под нагрузкой; формат шкалы
// тестов сообщества bufferbloat.net, пороги фиксированы открыто.
func bloatGrade(add float64) string {
	switch {
	case add <= 30:
		return "A"
	case add <= 60:
		return "B"
	case add <= 120:
		return "C"
	case add <= 250:
		return "D"
	case add <= 400:
		return "E"
	}
	return "F"
}

// ndQuality — линейное качество метрики: 1 при значении good (и лучше),
// 0 при bad (и хуже), между — линейно.
func ndQuality(v, good, bad float64) float64 {
	if v <= good {
		return 1
	}
	if v >= bad {
		return 0
	}
	return (bad - v) / (bad - good)
}

// computeScore — композитный балл 0..100: взвешенное среднее качеств
// присутствующих компонент (потери 25, bufferbloat 25, задержка 20,
// джиттер 15, DNS 15). Отсутствующие компоненты исключаются из свёртки —
// частичный результат оценивается по тому, что успели измерить. Слабый
// Wi-Fi дополнительно ограничивает балл сверху: радиоканал узкое место
// независимо от провайдера.
func computeScore(m ndMetrics) int {
	type comp struct{ w, q float64 }
	var cs []comp
	if m.NetDone || m.GwDone {
		loss := m.NetLossPct
		if m.GwDone && m.GwLossPct > loss {
			loss = m.GwLossPct
		}
		cs = append(cs, comp{25, ndQuality(loss, 0, 10)}) // 10% потерь → 0 (Y.1541 IPLR хуже некуда)
	}
	if m.BloatDone {
		cs = append(cs, comp{25, ndQuality(m.BloatAddMs, 30, 300)})
	}
	if m.NetDone && m.NetOK {
		cs = append(cs, comp{20, ndQuality(m.NetAvgMs, 50, 250)})
		cs = append(cs, comp{15, ndQuality(m.NetJitterMs, 5, 50)})
	}
	if m.DNSDone {
		q := 1.0
		if !m.DNSSysOK {
			q = 0
		} else if m.DNSAltOK && m.DNSSysAvgMs > ndT.DNSSlowMs &&
			m.DNSSysAvgMs > ndT.DNSRatio*m.DNSAltBestMs {
			q = 0.4
		}
		cs = append(cs, comp{15, q})
	}
	if len(cs) == 0 {
		return 0
	}
	var wsum, qsum float64
	for _, c := range cs {
		wsum += c.w
		qsum += c.w * c.q
	}
	score := int(math.Round(100 * qsum / wsum))
	if m.WifiDone {
		if m.WifiSignalPct <= ndT.WifiBadPct && score > 45 {
			score = 45
		} else if m.WifiSignalPct <= ndT.WifiWarnPct && score > 65 {
			score = 65
		}
	}
	return score
}

// recommendations — правила анализа; sev: 3 — плохо, 2 — внимание,
// 1 — норма. Возвращается отсортированным: серьёзное сверху.
func recommendations(m ndMetrics) []ndRec {
	var out []ndRec
	add := func(sev int, code string, args map[string]any) {
		out = append(out, ndRec{Code: code, Sev: sev, Args: args})
	}
	r1 := func(v float64) float64 { return math.Round(v*10) / 10 }

	gwClean := !m.GwDone || m.GwLossPct < ndT.GwLossWarn

	// Локальная сеть: потери до шлюза — кабель/роутер/Wi-Fi.
	if m.GwDone && m.GwLossPct >= ndT.GwLossWarn {
		sev := 2
		if m.GwLossPct >= ndT.GwLossBad {
			sev = 3
		}
		add(sev, "gw_loss", map[string]any{"loss": r1(m.GwLossPct)})
	}
	// Канал провайдера: шлюз чистый, а наружу теряется.
	if m.NetDone && m.NetLossPct >= ndT.NetLossWarn && gwClean {
		sev := 2
		if m.NetLossPct >= ndT.NetLossBad {
			sev = 3
		}
		add(sev, "net_loss", map[string]any{"loss": r1(m.NetLossPct)})
	}
	if m.NetDone && m.NetOK {
		if m.NetAvgMs >= ndT.LatWarnMs {
			sev := 2
			if m.NetAvgMs >= ndT.LatBadMs {
				sev = 3
			}
			add(sev, "latency", map[string]any{"ms": math.Round(m.NetAvgMs)})
		}
		if m.NetJitterMs >= ndT.JitWarnMs {
			sev := 2
			if m.NetJitterMs >= ndT.JitBadMs {
				sev = 3
			}
			add(sev, "jitter", map[string]any{"j": r1(m.NetJitterMs)})
		}
	}
	if m.BloatDone && m.BloatOK {
		if m.BloatAddMs >= ndT.BloatBadMs {
			add(3, "bloat_bad", map[string]any{"add": math.Round(m.BloatAddMs)})
		} else if m.BloatAddMs >= ndT.BloatWarnMs {
			add(2, "bloat_warn", map[string]any{"add": math.Round(m.BloatAddMs)})
		}
	}
	if m.DNSDone {
		dnsSlow := m.DNSSysOK && m.DNSAltOK && m.DNSSysAvgMs > ndT.DNSSlowMs &&
			m.DNSSysAvgMs > ndT.DNSRatio*m.DNSAltBestMs
		if !m.DNSSysOK && m.DNSAltOK {
			add(3, "dns_fail", nil)
		} else if dnsSlow {
			add(2, "dns_slow", map[string]any{
				"sys": math.Round(m.DNSSysAvgMs), "alt": math.Round(m.DNSAltBestMs)})
		}
	}
	if m.WifiDone && m.WifiSignalPct > 0 && m.WifiSignalPct <= ndT.WifiWarnPct {
		sev := 2
		if m.WifiSignalPct <= ndT.WifiBadPct {
			sev = 3
		}
		add(sev, "wifi_weak", map[string]any{"pct": m.WifiSignalPct})
	}
	if m.DownKnown && m.RecentMaxMbps >= ndT.DownMinMbps &&
		m.DownMbps < ndT.SpeedDropRatio*m.RecentMaxMbps {
		add(2, "speed_drop", map[string]any{"cur": r1(m.DownMbps), "max": r1(m.RecentMaxMbps)})
	}

	// Всё измеренное в порядке — так и говорим.
	if len(out) == 0 && m.NetDone && m.NetOK && m.NetLossPct < ndT.NetLossWarn &&
		m.NetAvgMs < ndT.LatWarnMs && m.NetJitterMs < ndT.JitWarnMs &&
		(!m.BloatDone || m.BloatAddMs < ndT.BloatWarnMs) {
		add(1, "ok", nil)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Sev != out[j].Sev {
			return out[i].Sev > out[j].Sev
		}
		return out[i].Code < out[j].Code
	})
	return out
}

// analyze — балл + рекомендации одним вызовом.
func analyze(m ndMetrics) (int, []ndRec) {
	return computeScore(m), recommendations(m)
}
