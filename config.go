package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config — настройки утилиты. Хранятся в config.json рядом с exe
// (если каталог недоступен для записи — в %APPDATA%\SpeedTestTray).
type Config struct {
	IntervalMin   int     `json:"interval_min"`   // период проверок, минут
	DownloadMB    int     `json:"download_mb"`    // объём теста загрузки, МБ
	UploadMB      int     `json:"upload_mb"`      // объём теста отдачи, МБ
	LatencyProbes int     `json:"latency_probes"` // число проб для пинга/джиттера
	LossProbes    int     `json:"loss_probes"`    // число проб для оценки потерь (0 = выключено)
	BadMbps       float64 `json:"bad_mbps"`       // ниже — плохо: красная иконка + уведомление
	GoodMbps      float64 `json:"good_mbps"`      // выше — хорошо: зелёная; между — норма, жёлтая
	NotifyMode    string  `json:"notify_mode"`    // all / problems / off
	TimeoutSec    int     `json:"timeout_sec"`    // таймаут каждого этапа, секунд
	NetRateMs     int     `json:"net_rate_ms"`    // частота опроса сети/обновления живого графика, мс
	Lang          string  `json:"lang"`           // язык интерфейса: ru / en (нет поля — по языку ОС)

	// ИИ-агент: локальный llama-server + модель (запускается по кнопке на дашборде).
	AgentLlamaServer string `json:"agent_llama_server"` // путь до llama-server.exe (относительно exe или абсолютный)
	AgentModel       string `json:"agent_model"`        // путь до GGUF-модели
	AgentPort        int    `json:"agent_port"`         // порт llama-server на 127.0.0.1
	AgentDevice      string `json:"agent_device"`       // auto (NVIDIA по списку устройств) | cpu | имя устройства (Vulkan1)

	// Устаревший порог из старых config.json — только для миграции.
	ThresholdMbps float64 `json:"threshold_mbps,omitempty"`
}

func defaultConfig() Config {
	return Config{
		IntervalMin:   10,
		DownloadMB:    25,
		UploadMB:      10,
		LatencyProbes: 15,
		LossProbes:    100,
		BadMbps:       10,
		GoodMbps:      50,
		NotifyMode:    "all",
		TimeoutSec:    30,
		NetRateMs:     2000,

		AgentLlamaServer: filepath.Join("agent", serverExeName()),
		AgentModel:       filepath.Join("agent", "models", "lfm3-1.7b-q4_k_m.gguf"),
		AgentPort:        8090,
		AgentDevice:      "auto",
	}
}

// netRatesMs — допустимые частоты опроса сети (селектор «обновление» на дашборде).
var netRatesMs = []int{1000, 2000, 5000, 10000}

// nearestNetRateMs приводит частоту к ближайшему допустимому значению.
func nearestNetRateMs(ms int) int {
	if ms <= 0 {
		return netRatesMs[1] // 2 с
	}
	best := netRatesMs[0]
	for _, r := range netRatesMs {
		if absInt(ms-r) < absInt(ms-best) {
			best = r
		}
	}
	return best
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// normalize заполняет нулевые поля дефолтами и зажимает значения в разумные рамки.
func (c *Config) normalize() {
	d := defaultConfig()
	if c.IntervalMin <= 0 {
		c.IntervalMin = d.IntervalMin
	}
	c.IntervalMin = clampInt(c.IntervalMin, 1, 1440)
	if c.DownloadMB <= 0 {
		c.DownloadMB = d.DownloadMB
	}
	c.DownloadMB = clampInt(c.DownloadMB, 1, 1024)
	if c.UploadMB <= 0 {
		c.UploadMB = d.UploadMB
	}
	c.UploadMB = clampInt(c.UploadMB, 1, 1024)
	if c.LatencyProbes <= 0 {
		c.LatencyProbes = d.LatencyProbes
	}
	c.LatencyProbes = clampInt(c.LatencyProbes, 3, 50)
	c.LossProbes = clampInt(c.LossProbes, 0, 1000)
	// Миграция со старого одиночного порога: зелёный ≥ threshold,
	// красный < threshold/2 — так вела себя прежняя версия.
	if c.BadMbps <= 0 && c.GoodMbps <= 0 && c.ThresholdMbps > 0 {
		c.BadMbps = c.ThresholdMbps / 2
		c.GoodMbps = c.ThresholdMbps
	}
	if c.BadMbps <= 0 {
		c.BadMbps = d.BadMbps
	}
	if c.GoodMbps <= 0 {
		c.GoodMbps = d.GoodMbps
	}
	c.BadMbps = clampFloat(c.BadMbps, 1, 1000)
	c.GoodMbps = clampFloat(c.GoodMbps, 2, 10000)
	if c.BadMbps >= c.GoodMbps {
		c.BadMbps = c.GoodMbps / 2
	}
	switch c.NotifyMode {
	case "all", "problems", "off":
	default:
		c.NotifyMode = d.NotifyMode
	}
	if c.TimeoutSec <= 0 {
		c.TimeoutSec = d.TimeoutSec
	}
	c.TimeoutSec = clampInt(c.TimeoutSec, 5, 600)
	c.NetRateMs = nearestNetRateMs(c.NetRateMs)
	switch c.Lang {
	case LangRu, LangEn:
	default:
		c.Lang = DetectLang()
	}
	if c.AgentLlamaServer == "" {
		c.AgentLlamaServer = d.AgentLlamaServer
	}
	if c.AgentModel == "" {
		c.AgentModel = d.AgentModel
	}
	if c.AgentPort <= 0 {
		c.AgentPort = d.AgentPort
	}
	c.AgentPort = clampInt(c.AgentPort, 1024, 65535)
	if c.AgentDevice == "" {
		c.AgentDevice = d.AgentDevice
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// configPath возвращает путь к config.json: рядом с exe, а если каталог
// только для чтения — в профиле пользователя.
func configPath() string {
	name := "config.json"
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), name)
		if f, err := os.OpenFile(filepath.Join(filepath.Dir(exe), ".probe"), os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			f.Close()
			os.Remove(filepath.Join(filepath.Dir(exe), ".probe"))
			return p
		}
	}
	if dir, err := os.UserConfigDir(); err == nil {
		p := filepath.Join(dir, "SpeedTestTray")
		_ = os.MkdirAll(p, 0o755)
		return filepath.Join(p, name)
	}
	return name
}

// LoadConfig читает config.json (create=true — создаёт с дефолтами при отсутствии).
func LoadConfig(create bool) (*Config, string) {
	path := configPath()
	cfg := defaultConfig()
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			fmt.Fprintln(os.Stderr, T("cfg_parse_err", path, err))
			cfg = defaultConfig()
		}
	} else if !os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, T("cfg_read_err", path, err))
	}
	cfg.normalize()
	SetLang(cfg.Lang)
	if create && os.IsNotExist(err) {
		if err := cfg.Save(path); err != nil {
			fmt.Fprintln(os.Stderr, T("cfg_write_err", path, err))
		}
	}
	return &cfg, path
}

// Save записывает конфиг в файл.
func (c *Config) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
