package main

import "testing"

func TestValidHost(t *testing.T) {
	cases := map[string]bool{
		"ya.ru":            true,
		"1.1.1.1":          true,
		"router.local":     true,
		"2001:db8::1":      true,
		"":                 false,
		"-f":               false, // флаг-инъекция в системный ping
		"--flood":          false,
		"host; rm -rf /":   false,
		"host `id`":        false,
		"example.com/path": false,
	}
	for host, want := range cases {
		if got := validHost(host); got != want {
			t.Errorf("validHost(%q) = %v, хочу %v", host, got, want)
		}
	}
}
