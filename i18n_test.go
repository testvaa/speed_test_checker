package main

import (
	"regexp"
	"strings"
	"testing"
)

// TestI18nTableComplete — у каждого ключа есть оба варианта и они различаются.
func TestI18nTableComplete(t *testing.T) {
	for key, e := range i18nTable {
		if strings.TrimSpace(e[0]) == "" {
			t.Errorf("ключ %s: пустой русский вариант", key)
		}
		if strings.TrimSpace(e[1]) == "" {
			t.Errorf("ключ %s: пустой английский вариант", key)
		}
	}
}

// TestTFormats — форматные аргументы подставляются в обоих языках.
func TestTFormats(t *testing.T) {
	// ключ с одним аргументом: число плейсхолдеров обязано совпадать
	ph := func(s string) int { return strings.Count(s, "%") - 2*strings.Count(s, "%%") }
	for _, key := range []string{"tray_every_min", "arch_saved", "obs_interval_ok"} {
		if ph(i18nTable[key][0]) != ph(i18nTable[key][1]) {
			t.Errorf("ключ %s: разное число плейсхолдеров ru/en", key)
		}
	}

	SetLang(LangRu)
	if got := T("tray_every_min", 5); got != "Каждые 5 мин" {
		t.Errorf("T(ru) = %q", got)
	}
	SetLang(LangEn)
	if got := T("tray_every_min", 5); got != "Every 5 min" {
		t.Errorf("T(en) = %q", got)
	}
	SetLang(LangRu) // вернуть состояние тестов
}

// TestDetectLangFallback — DetectLang всегда возвращает допустимый язык.
func TestDetectLangFallback(t *testing.T) {
	if got := DetectLang(); got != LangRu && got != LangEn {
		t.Errorf("DetectLang() = %q, хочу ru|en", got)
	}
}

// TestAgentRegexesBothLanguages — определение «просьбы о действии» работает
// на русском и английском вводе в любом языке интерфейса.
func TestAgentRegexesBothLanguages(t *testing.T) {
	yes := []string{
		"пингуй ya.ru", "следи за router.local", "сделай замер", "останови наблюдение",
		"ping ya.ru", "run a speed test", "check now", "watch router.local every 5 minutes",
		"stop watching ya.ru", "cancel all watches", "save the archive", "import archive C:/a.jsonl",
	}
	no := []string{
		"какая скорость сегодня?", "почему интернет тормозит?",
		"what is my speed?", "why is the internet slow?", "расскажи про сети",
	}
	for _, s := range yes {
		if !actionIntent.MatchString(s) {
			t.Errorf("actionIntent не распознал %q", s)
		}
	}
	for _, s := range no {
		if actionIntent.MatchString(s) {
			t.Errorf("actionIntent ложно сработал на %q", s)
		}
	}
	// actionWords ловит словесные отчёты модели на обоих языках
	for _, s := range []string{"Проверка запущено", "инструмент вызван", "The check is done", "Watch started", "Archive saved", "Interval changed"} {
		if !actionWords.MatchString(s) {
			t.Errorf("actionWords не увидел %q", s)
		}
	}
}

// TestAgentToolsForLang — в каждом языке все 12 инструментов с описаниями.
func TestAgentToolsForLang(t *testing.T) {
	names := regexp.MustCompile(`^[a-z_]+$`)
	for _, lang := range []string{LangRu, LangEn} {
		tools := agentToolsFor(lang)
		if len(tools) != 12 {
			t.Errorf("%s: инструментов %d, хочу 12", lang, len(tools))
		}
		for _, tool := range tools {
			fn := tool["function"].(map[string]any)
			name := fn["name"].(string)
			if !names.MatchString(name) {
				t.Errorf("%s: странное имя инструмента %q", lang, name)
			}
			desc := fn["description"].(string)
			if len(desc) < 10 {
				t.Errorf("%s: пустое описание %s", lang, name)
			}
		}
	}
}
