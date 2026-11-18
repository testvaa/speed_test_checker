//go:build windows

package main

// Маршрут по умолчанию и Wi-Fi на Windows: GetBestRoute из iphlpapi.dll —
// тот же приём, что IcmpSendEcho в ping_windows.go (без прав админа и без
// разбора локального ping.exe). Wi-Fi — разбор netsh (parseNetshWlan).

import (
	"encoding/binary"
	"net"
	"strings"
	"syscall"
	"unsafe"
)

var procGetBestRoute = modIphlpapi.NewProc("GetBestRoute")

// mibIPForwardRow — layout MIB_IPFORWARDROW; нужны NextHop и IfIndex.
type mibIPForwardRow struct {
	Dest    uint32
	Mask    uint32
	Policy  uint32
	NextHop uint32
	IfIndex uint32
	_       [10]uint32
}

// ndRouteInfo — интерфейс/MTU/шлюз маршрута по умолчанию (куда пойдёт
// трафик до 1.1.1.1).
func ndRouteInfo() (ifaceName, gw string, mtu int, err error) {
	ip := net.ParseIP("1.1.1.1").To4()
	if ip == nil {
		return "", "", 0, syscall.EINVAL
	}
	var row mibIPForwardRow
	r1, _, _ := procGetBestRoute.Call(
		uintptr(binary.LittleEndian.Uint32(ip)), 0, uintptr(unsafe.Pointer(&row)))
	if r1 != 0 {
		return "", "", 0, syscall.Errno(r1)
	}
	gwIP := make(net.IP, 4)
	binary.LittleEndian.PutUint32(gwIP, row.NextHop)
	gw = gwIP.String()
	if it, e := net.InterfaceByIndex(int(row.IfIndex)); e == nil {
		ifaceName = it.Name
		if strings.HasPrefix(ifaceName, "{") {
			ifaceName = "LAN" // внутреннее GUID-имя адаптера — так понятнее
		}
		mtu = it.MTU
	}
	return ifaceName, gw, mtu, nil
}

// ndWifiInfo — SSID и сигнал % подключённой Wi-Fi сети (не Wi-Fi — false).
func ndWifiInfo() (ssid string, signalPct int, ok bool) {
	return parseNetshWlan(ndNetshWlanOut())
}
