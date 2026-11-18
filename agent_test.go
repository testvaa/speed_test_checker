package main

// Тест сводки ИИ-агента: диагностика сети попадает в Digest (агент знает
// о ней в каждом ответе, без действий пользователя).

import (
	"strings"
	"testing"
	"time"
)

func TestAgentDigestIncludesNetDiag(t *testing.T) {
	nd := NewNetDiag()
	// завершённая диагностика с одной рекомендацией — без реальных пингов:
	// кладём данные напрямую, как это сделал бы finish()
	nd.mu.Lock()
	nd.state = ndDone
	nd.score = 96
	nd.lastRun = time.Now()
	nd.env = &ndEnv{Gateway: "192.0.2.1"}
	nd.ping = []ndPingHost{{Host: "1.1.1.1", Scope: "net", Sent: 8, Recv: 8, AvgMs: 12, JitterMs: 2}}
	nd.recs = []ndRec{{Code: "bloat_warn", Sev: 2, Args: map[string]any{"add": 52.0}}}
	nd.mu.Unlock()

	a := &Agent{
		history: func() []dashEntry { return nil },
		traffic: func() TrafficStore { return TrafficStore{} },
		ndProv:  nd.DigestText,
	}
	d := a.Digest()
	if d == "" {
		t.Fatalf("Digest пуст")
	}
	for _, want := range []string{"96/100", "192.0.2.1", "1.1.1.1", "bufferbloat"} {
		if !strings.Contains(d, want) {
			t.Errorf("Digest без %q:\n%s", want, d)
		}
	}

	// без диагностики блока нет
	a2 := &Agent{
		history: func() []dashEntry { return nil },
		traffic: func() TrafficStore { return TrafficStore{} },
		ndProv:  NewNetDiag().DigestText,
	}
	if d2 := a2.Digest(); strings.Contains(d2, "/100") {
		t.Errorf("Digest без диагностики содержит балл:\n%s", d2)
	}
}
