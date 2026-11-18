//go:build !windows

package main

// Маршрут по умолчанию на macOS/Linux: разбор вывода route/ip (парсеры —
// чистые функции в netdiag.go, работают и под CI). Wi-Fi здесь не
// определяется — правило просто не срабатывает.

import (
	"context"
	"net"
	"os/exec"
	"runtime"
	"time"
)

// ndRouteInfo — интерфейс/MTU/шлюз маршрута по умолчанию.
func ndRouteInfo() (ifaceName, gw string, mtu int, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var out []byte
	if runtime.GOOS == "darwin" {
		out, err = exec.CommandContext(ctx, "route", "-n", "get", "1.1.1.1").Output()
		if err != nil {
			return "", "", 0, err
		}
		gw, ifaceName, _ = parseRouteDarwin(string(out))
	} else {
		out, err = exec.CommandContext(ctx, "ip", "route", "show", "default").Output()
		if err != nil {
			return "", "", 0, err
		}
		gw, ifaceName, _ = parseRouteLinux(string(out))
	}
	if gw == "" {
		return "", "", 0, exec.ErrNotFound
	}
	if ifaceName != "" {
		if it, e := net.InterfaceByName(ifaceName); e == nil {
			mtu = it.MTU
		}
	}
	return ifaceName, gw, mtu, nil
}

// ndWifiInfo — вне Windows сигнал Wi-Fi не снимаем.
func ndWifiInfo() (ssid string, signalPct int, ok bool) { return "", 0, false }
