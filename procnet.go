package main

import (
	"context"
	stdnet "net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	gnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

// procRec — процесс с активными сетевыми соединениями.
type procRec struct {
	Pid       int32    `json:"pid"`
	Name      string   `json:"name"`
	Conns     int      `json:"conns"`
	Bps       float64  `json:"bps"`   // ≈ байт/с: весь ввод-вывод процесса (диск+сеть)
	Hosts     []string `json:"hosts"` // уникальные хосты (первые несколько)
	HostsMore int      `json:"hostsMore"`
}

// connRec — одно соединение в представлении для страницы.
type connRec struct {
	Pid    int32  `json:"pid"`
	Name   string `json:"name"`
	Proto  string `json:"proto"`           // tcp / udp
	State  string `json:"state,omitempty"` // для tcp
	Remote string `json:"remote"`          // ip:порт
	Host   string `json:"host,omitempty"`  // reverse DNS, если определился
}

// procNetResp — ответ /api/procnet.
type procNetResp struct {
	Ts       string    `json:"ts"`
	SampleMs int64     `json:"sampleMs"`
	Approx   bool      `json:"approx"` // активность — оценка, не точный счётчик
	Procs    []procRec `json:"procs"`
	Conns    []connRec `json:"conns"`
}

// ProcNet — сборщик «кто потребляет интернет» для дашборда.
// Соединения — точные (таблица Windows, как netstat -ano), активность —
// оценка по счётчикам ввода-вывода процесса (точный сетевой счётчик в
// Windows требует прав администратора), имена хостов — reverse DNS с кэшем.
type ProcNet struct {
	mu      sync.Mutex
	names   map[int32]nameRec // pid → имя процесса (кэш)
	io      map[int32]ioRec   // pid → прошлое значение счётчиков I/O
	dns     map[string]dnsRec // ip → результат reverse-lookup
	pending map[string]bool   // ip → lookup уже запущен
	sem     chan struct{}     // ограничитель параллельных DNS-запросов

	last     time.Time
	lastResp procNetResp
	have     bool
}

type nameRec struct {
	name  string
	until time.Time
}

type ioRec struct {
	bytes uint64
	t     time.Time
	bps   float64
}

type dnsRec struct {
	host  string
	ok    bool
	until time.Time
}

const (
	procIODelta  = 700 * time.Millisecond // минимальный интервал для расчёта скорости
	procIOStale  = 90 * time.Second       // сколько помнить счётчики исчезнувшего процесса
	procIOCap    = 256                    // при превышении — чистим устаревшие записи
	procNameTTL  = 60 * time.Second
	dnsOKTTL     = 30 * time.Minute
	dnsFailTTL   = 3 * time.Minute
	dnsTimeout   = 900 * time.Millisecond
	procMaxDNS   = 12 // новых DNS-запросов за один снимок
	procMaxProcs = 25
	procMaxConns = 200
	procMaxHosts = 4
)

func NewProcNet() *ProcNet {
	return &ProcNet{
		names:   map[int32]nameRec{},
		io:      map[int32]ioRec{},
		dns:     map[string]dnsRec{},
		pending: map[string]bool{},
		sem:     make(chan struct{}, 8),
	}
}

// Collect снимает соединения и счётчики процессов. Частые вызовы
// (чаще раза в секунду) отдают предыдущий снимок без пересчёта.
func (p *ProcNet) Collect() procNetResp {
	now := time.Now()

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.have && now.Sub(p.last) < time.Second {
		return p.lastResp
	}

	conns, err := gnet.Connections("inet")
	if err != nil {
		if p.have {
			return p.lastResp
		}
		return procNetResp{Ts: now.Format(time.RFC3339), Approx: true,
			Procs: []procRec{}, Conns: []connRec{}}
	}
	dt := now.Sub(p.last)
	if p.last.IsZero() || dt <= 0 {
		dt = 0 // первый снимок — скорость ещё не из чего посчитать
	}
	p.last = now

	type acc struct {
		conns int
		hosts []string
	}
	procs := map[int32]*acc{}
	out := make([]connRec, 0, len(conns))
	unresolved := map[string]bool{}

	for _, c := range conns {
		ip := c.Raddr.IP
		if ip == "" || ip == "0.0.0.0" || ip == "::" {
			continue // слушающие сокеты и мусор
		}
		if c.Pid <= 4 {
			continue // 0 и 4 — системные/TIME_WAIT-записи без владельца
		}
		proto := "udp"
		if c.Status != "" {
			proto = "tcp"
		}
		if c.Status == "LISTEN" {
			continue // слушающий сокет — не соединение
		}
		host := p.hostLocked(ip, now)
		if !isLanIP(ip) && !p.resolvedLocked(ip, now) {
			unresolved[ip] = true
		}
		out = append(out, connRec{
			Pid: c.Pid, Name: p.nameLocked(c.Pid, now), Proto: proto,
			State: c.Status, Remote: ip + ":" + strconv.Itoa(int(c.Raddr.Port)),
			Host: host,
		})
		a := procs[c.Pid]
		if a == nil {
			a = &acc{}
			procs[c.Pid] = a
		}
		a.conns++
		h := host
		if h == "" {
			h = ip
		}
		if !containsStr(a.hosts, h) {
			a.hosts = append(a.hosts, h)
		}
	}
	// reverse DNS для новых адресов — по немного за снимок, чтобы не мешать
	n := 0
	for ip := range unresolved {
		if n >= procMaxDNS {
			break
		}
		p.startResolveLocked(ip)
		n++
	}

	// оценка активности: дельта счётчиков I/O (весь ввод-вывод: диск+сеть)
	list := make([]procRec, 0, len(procs))
	for pid, a := range procs {
		rec := procRec{Pid: pid, Name: p.nameLocked(pid, now), Conns: a.conns}
		if cur, ok := p.ioOf(pid, now); ok {
			rec.Bps = cur.bps
		}
		hn := len(a.hosts)
		sort.Strings(a.hosts)
		if hn > procMaxHosts {
			rec.Hosts = a.hosts[:procMaxHosts]
			rec.HostsMore = hn - procMaxHosts
		} else {
			rec.Hosts = a.hosts
		}
		list = append(list, rec)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Bps != list[j].Bps {
			return list[i].Bps > list[j].Bps
		}
		if list[i].Conns != list[j].Conns {
			return list[i].Conns > list[j].Conns
		}
		return list[i].Name < list[j].Name
	})
	if len(list) > procMaxProcs {
		list = list[:procMaxProcs]
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Remote < out[j].Remote
	})
	if len(out) > procMaxConns {
		out = out[:procMaxConns]
	}
	if len(p.io) > procIOCap {
		for pid, r := range p.io {
			if now.Sub(r.t) > procIOStale {
				delete(p.io, pid)
			}
		}
	}

	resp := procNetResp{
		Ts:       now.Format(time.RFC3339),
		SampleMs: dt.Milliseconds(),
		Approx:   true,
		Procs:    list,
		Conns:    out,
	}
	p.lastResp = resp
	p.have = true
	return resp
}

// ioOf снимает счётчики ввода-вывода процесса и считает среднюю скорость
// по дельте с прошлым опросом. ok=false — дельта ещё не набрана (первый
// снимок) либо счётчики недоступны (процессы других пользователей).
func (p *ProcNet) ioOf(pid int32, now time.Time) (ioRec, bool) {
	prev, hasPrev := p.io[pid]
	var total uint64
	if pr, err := process.NewProcess(pid); err == nil {
		if io, err := pr.IOCounters(); err == nil {
			total = io.ReadBytes + io.WriteBytes
		}
	}
	if hasPrev {
		rec := ioRec{bytes: total, t: now, bps: prev.bps}
		if total >= prev.bytes {
			bps := float64(total-prev.bytes) / now.Sub(prev.t).Seconds()
			if prev.bps > 0 { // сглаживание, чтобы значения не мигали
				bps = 0.5*bps + 0.5*prev.bps
			}
			rec.bps = bps
		}
		p.io[pid] = rec
		return rec, true
	}
	p.io[pid] = ioRec{bytes: total, t: now}
	return ioRec{}, false
}

// nameLocked возвращает имя процесса (в нижнем регистре, как в диспетчере задач).
func (p *ProcNet) nameLocked(pid int32, now time.Time) string {
	if r, ok := p.names[pid]; ok && now.Before(r.until) {
		return r.name
	}
	name := ""
	if pr, err := process.NewProcess(pid); err == nil {
		if nm, err := pr.Name(); err == nil {
			name = strings.ToLower(nm)
		}
	}
	p.names[pid] = nameRec{name: name, until: now.Add(procNameTTL)}
	return name
}

// hostLocked — сохранённое имя хоста для ip ("" — ещё не определился).
func (p *ProcNet) hostLocked(ip string, now time.Time) string {
	if r, ok := p.dns[ip]; ok && r.until.After(now) && r.ok {
		return r.host
	}
	return ""
}

// resolvedLocked — по ip уже есть свежий ответ DNS (в т.ч. отрицательный).
func (p *ProcNet) resolvedLocked(ip string, now time.Time) bool {
	if r, ok := p.dns[ip]; ok {
		return r.until.After(now)
	}
	return false
}

// startResolveLocked запускает (один раз) фоновый reverse-lookup для ip.
func (p *ProcNet) startResolveLocked(ip string) {
	if p.pending[ip] {
		return
	}
	p.pending[ip] = true
	go func() {
		p.sem <- struct{}{}
		defer func() { <-p.sem }()
		host, ok := reverseHost(ip)
		p.mu.Lock()
		defer p.mu.Unlock()
		ttl := dnsFailTTL
		if ok {
			ttl = dnsOKTTL
		}
		p.dns[ip] = dnsRec{host: host, ok: ok, until: time.Now().Add(ttl)}
		delete(p.pending, ip)
	}()
}

// reverseHost — reverse DNS с коротким таймаутом; ok=false, если не удалось.
func reverseHost(ip string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), dnsTimeout)
	defer cancel()
	names, err := stdnet.DefaultResolver.LookupAddr(ctx, ip)
	if err != nil || len(names) == 0 {
		return "", false
	}
	return strings.TrimSuffix(names[0], "."), true
}

// isLanIP — локальным и служебным адресам reverse DNS не нужен.
func isLanIP(ip string) bool {
	parts := strings.Split(ip, ".")
	if len(parts) == 4 {
		n0, err1 := strconv.Atoi(parts[0])
		n1, err2 := strconv.Atoi(parts[1])
		if err1 == nil && err2 == nil {
			switch {
			case n0 == 0, n0 == 10, n0 == 127, n0 == 169 && n1 == 254,
				n0 == 192 && n1 == 168, n0 == 172 && n1 >= 16 && n1 <= 31,
				n0 >= 224: // multicast/broadcast/reserved
				return true
			}
		}
		return false
	}
	low := strings.ToLower(ip)
	return strings.HasPrefix(low, "fe80:") || strings.HasPrefix(low, "fc") ||
		strings.HasPrefix(low, "fd") || low == "::1"
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
