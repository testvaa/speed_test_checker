//go:build windows

package main

// pingOnce под Windows: один ICMP-пинг через Windows API (IcmpSendEcho из
// iphlpapi.dll) — не требует прав администратора и не зависит от локали,
// в отличие от разбора вывода ping.exe.

import (
	"encoding/binary"
	"fmt"
	"net"
	"syscall"
	"time"
	"unsafe"
)

var (
	modIphlpapi         = syscall.NewLazyDLL("iphlpapi.dll")
	procIcmpCreateFile  = modIphlpapi.NewProc("IcmpCreateFile")
	procIcmpCloseHandle = modIphlpapi.NewProc("IcmpCloseHandle")
	procIcmpSendEcho    = modIphlpapi.NewProc("IcmpSendEcho")
)

// icmpEchoReply — layout ICMP_ECHO_REPLY (x64), читаем только статус и RTT.
type icmpEchoReply struct {
	Address  uint32
	Status   uint32
	RTT      uint32
	DataSize uint16
	Reserved uint16
	Data     uintptr
	Ttl      uint8
	Tos      uint8
	Flags    uint8
	OptSize  uint8
	_        uint32
	OptData  uintptr
}

var icmpStatuses = map[uint32]string{
	11003: "хост недоступен",
	11004: "хост недоступен",
	11010: "таймаут",
	11050: "общая ошибка ICMP",
}

func pingOnce(host string, timeout time.Duration) (int, error) {
	ip := net.ParseIP(host)
	if ip == nil {
		addrs, err := net.LookupIP(host)
		if err != nil {
			return -1, fmt.Errorf("не удалось разрешить %s: %w", host, err)
		}
		for _, a := range addrs {
			if v4 := a.To4(); v4 != nil {
				ip = v4
				break
			}
		}
		if ip == nil {
			return -1, fmt.Errorf("%s: нет IPv4-адреса", host)
		}
	}
	b4 := ip.To4()
	if b4 == nil {
		return -1, fmt.Errorf("%s: нужен IPv4-адрес", host)
	}

	handle, _, _ := procIcmpCreateFile.Call()
	if handle == 0 || handle == ^uintptr(0) {
		return -1, fmt.Errorf("IcmpCreateFile: отказано")
	}
	defer procIcmpCloseHandle.Call(handle)

	payload := []byte("speedtesttrayping")
	req := make([]byte, len(payload))
	copy(req, payload)
	buf := make([]byte, int(unsafe.Sizeof(icmpEchoReply{}))+len(payload)+64)
	r, _, _ := procIcmpSendEcho.Call(
		handle,
		uintptr(binary.LittleEndian.Uint32(b4)),
		uintptr(unsafe.Pointer(&req[0])),
		uintptr(len(req)),
		0, // RequestOptions = NULL
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
		uintptr(timeout.Milliseconds()),
	)
	if r == 0 {
		return -1, fmt.Errorf("нет ответа")
	}
	reply := (*icmpEchoReply)(unsafe.Pointer(&buf[0]))
	if reply.Status != 0 {
		if s, ok := icmpStatuses[reply.Status]; ok {
			return -1, fmt.Errorf("%s", s)
		}
		return -1, fmt.Errorf("нет ответа (ICMP-статус %d)", reply.Status)
	}
	return int(reply.RTT), nil
}
