// netdiag — пробник сетевых счётчиков gopsutil: три снимка с шагом 3 сек
// с печатью дельт по каждому интерфейсу. Запуск: go run ./scripts/netdiag
package main

import (
	"fmt"
	"time"

	"github.com/shirou/gopsutil/v4/net"
)

func snap(label string) map[string][2]uint64 {
	stats, err := net.IOCounters(true)
	if err != nil {
		fmt.Println(label, "ОШИБКА:", err)
		return nil
	}
	m := make(map[string][2]uint64, len(stats))
	for _, s := range stats {
		fmt.Printf("%s  %-34s recv=%14d sent=%14d\n", label, s.Name, s.BytesRecv, s.BytesSent)
		m[s.Name] = [2]uint64{s.BytesRecv, s.BytesSent}
	}
	return m
}

func main() {
	prev := snap("t0")
	for i := 1; i <= 3; i++ {
		time.Sleep(3 * time.Second)
		cur := snap(fmt.Sprintf("t%d", i*3))
		fmt.Println("---- дельты ----")
		if cur == nil {
			continue
		}
		for name, c := range cur {
			if p, ok := prev[name]; ok && (c[0] != p[0] || c[1] != p[1]) {
				fmt.Printf("  %-32s dRecv=%d dSent=%d\n", name, c[0]-p[0], c[1]-p[1])
			}
		}
		prev = cur
	}
}
