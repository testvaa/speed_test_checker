//go:build linux || darwin

package main

// pingOnce под Linux/macOS: системный ping (на macOS он setuid, на Linux
// обычно есть у всех). Разбираем RTT из вывода «time=NN ms» — эта часть
// вывода iputils/Apple ping не локализуется.

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"time"
)

var pingRTTRe = regexp.MustCompile(`time[=<]\s*([\d.]+)\s*ms`)

func pingOnce(host string, timeout time.Duration) (int, error) {
	secs := int(timeout.Seconds())
	if secs < 1 {
		secs = 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout+2*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		// macOS: -t — таймаут в секундах на весь пинг
		cmd = exec.CommandContext(ctx, "ping", "-c", "1", "-t", strconv.Itoa(secs), host)
	} else {
		// iputils: -W — таймаут ответа в секундах
		cmd = exec.CommandContext(ctx, "ping", "-c", "1", "-W", strconv.Itoa(secs), host)
	}

	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return -1, fmt.Errorf("таймаут")
	}
	if err != nil {
		// ping возвращает ошибку и при потере пакета — смотрим, есть ли RTT
		if m := pingRTTRe.FindSubmatch(out); m != nil {
			// пакет ушёл и вернулся, несмотря на код возврата
		} else {
			return -1, fmt.Errorf("нет ответа")
		}
	}
	m := pingRTTRe.FindSubmatch(out)
	if m == nil {
		return -1, fmt.Errorf("нет ответа")
	}
	f, err := strconv.ParseFloat(string(m[1]), 64)
	if err != nil {
		return -1, fmt.Errorf("нет ответа")
	}
	return int(f + 0.5), nil
}
