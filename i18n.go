package main

// Локализация: русский/английский. Язык хранится в config.json (lang),
// переключается с дашборда (кнопка EN/RU) и из трея (Настройки → Язык).
// T() — плоские строки с форматами printf; для вложенных структур
// (промпт агента, описания инструментов) — пары L(ru, en) по месту.

import (
	"fmt"
	"sync/atomic"
	"time"
)

const (
	LangRu = "ru"
	LangEn = "en"
)

var curLang atomic.Value // string: "ru" | "en"

// SetLang устанавливает текущий язык интерфейса.
func SetLang(lang string) {
	if lang != LangRu && lang != LangEn {
		return
	}
	curLang.Store(lang)
}

// Lang возвращает текущий язык ("ru" по умолчанию).
func Lang() string {
	if v, ok := curLang.Load().(string); ok && v != "" {
		return v
	}
	return LangRu
}

// L выбирает одну из двух строк по текущему языку — для мест, где
// заводить ключ в таблице неудобно (вложенные структуры, редкие строки).
func L(ru, en string) string {
	if Lang() == LangEn {
		return en
	}
	return ru
}

// T возвращает локализованную строку по ключу; args — аргументы printf.
// Нет ключа — возвращаем ключ (сразу видно при разработке); нет варианта
// языка — берём другой.
func T(key string, args ...any) string {
	e, ok := i18nTable[key]
	if !ok {
		return key
	}
	s := e[0]
	if Lang() == LangEn && e[1] != "" {
		s = e[1]
	}
	if len(args) == 0 {
		return s
	}
	return fmt.Sprintf(s, args...)
}

// i18nTable — форматные строки [ru, en]; ключи сгруппированы по файлам.
var i18nTable = map[string][2]string{
	// ── config.go ────────────────────────────────────────────────────
	"cfg_parse_err": {"config: не разобран %s (%v), использую значения по умолчанию", "config: failed to parse %s (%v), using defaults"},
	"cfg_read_err":  {"config: не прочитан %s (%v)", "config: cannot read %s (%v)"},
	"cfg_write_err": {"config: не записан %s (%v)", "config: cannot write %s (%v)"},
	"cfg_save_err":  {"config: не удалось сохранить %s: %v", "config: failed to save %s: %v"},

	// ── dashboard.go (HTTP) ──────────────────────────────────────────
	"err_post_only":         {"только POST", "POST only"},
	"err_get_only":          {"только GET", "GET only"},
	"err_check_running":     {"проверка уже идёт", "a check is already running"},
	"err_unavailable":       {"недоступно", "unavailable"},
	"err_bad_json":          {"плохой json", "bad json"},
	"err_agent_unavailable": {"агент недоступен", "agent unavailable"},
	"err_local_only":        {"доступ только с этого компьютера", "local access only"},
	"dash_ports_busy":       {"dashboard: порты %d–%d заняты, дашборд выключен", "dashboard: ports %d-%d are busy, dashboard disabled"},
	"err_bad_lang":          {"lang: ru|en", "lang: ru|en"},

	// ── trayui.go: статичное меню ────────────────────────────────────
	"tray_preparing":    {"Speed Test Tray — подготовка…", "Speed Test Tray — preparing…"},
	"tray_run":          {"▶  Проверить сейчас", "▶  Check now"},
	"tray_run_tip":      {"Запустить проверку немедленно", "Run a check immediately"},
	"tray_open_dash":    {"🖥 Открыть дашборд", "🖥 Open dashboard"},
	"tray_hdr_test":     {"📊 ПОСЛЕДНИЙ ТЕСТ", "📊 LAST TEST"},
	"tray_down":         {"↓ Загрузка: —", "↓ Download: —"},
	"tray_up":           {"↑ Отдача: —", "↑ Upload: —"},
	"tray_ping":         {"⏱ Пинг: —", "⏱ Ping: —"},
	"tray_jitter":       {"〰 Джиттер: —", "〰 Jitter: —"},
	"tray_loss":         {"📉 Потери: —", "📉 Loss: —"},
	"tray_ip":           {"🌐 IP: —", "🌐 IP: —"},
	"tray_net":          {"🏢 Сеть: —", "🏢 Network: —"},
	"tray_server":       {"📍 Сервер: —", "📍 Server: —"},
	"tray_time":         {"🕐 Проверено: —", "🕐 Checked: —"},
	"tray_hdr_traffic":  {"📶 ТРАФИК", "📶 TRAFFIC"},
	"tray_net_now":      {"⚡ Сейчас: ↓ — ↑ — Мбит/с", "⚡ Now: ↓ — ↑ — Mbps"},
	"tray_net_day":      {"📅 Сегодня: ↓ — ↑ —", "📅 Today: ↓ — ↑ —"},
	"tray_net_mon":      {"🗓 %s: ↓ — ↑ —", "🗓 %s: ↓ — ↑ —"},
	"tray_interval":     {"⏳ Интервал", "⏳ Interval"},
	"tray_interval_tip": {"Периодичность проверок", "Check frequency"},
	"tray_every_min":    {"Каждые %d мин", "Every %d min"},
	"tray_settings":     {"Настройки", "Settings"},
	"tray_settings_tip": {"Объём теста, трафик и зоны оценки", "Test size, traffic and speed zones"},
	"tray_lang":         {"🌐 Язык · Language", "🌐 Язык · Language"},

	// ── trayui.go: настройки ─────────────────────────────────────────
	"tray_traffic_per_check": {"Трафик за проверку: ≈%d МБ", "Data per check: ≈%d MB"},
	"tray_dl_vol":            {"Объём загрузки: %d МБ", "Download size: %d MB"},
	"tray_ul_vol":            {"Объём отдачи: %d МБ", "Upload size: %d MB"},
	"tray_mb":                {"%d МБ", "%d MB"},
	"tray_zones":             {"Красный < %.0f • жёлтый %.0f–%.0f • зелёный ≥ %.0f Мбит/с", "Red < %.0f • yellow %.0f–%.0f • green ≥ %.0f Mbps"},
	"tray_bad_below":         {"Плохо, если ниже: %.0f Мбит/с", "Bad if below: %.0f Mbps"},
	"tray_good_above":        {"Хорошо, если выше: %.0f Мбит/с", "Good if above: %.0f Mbps"},
	"tray_mbps":              {"%.0f Мбит/с", "%.0f Mbps"},
	"tray_notif":             {"Уведомления: %s", "Notifications: %s"},
	"notif_all":              {"все проверки", "all checks"},
	"notif_problems":         {"только проблемы", "problems only"},
	"notif_off":              {"выключены", "off"},
	"tray_quit":              {"🚪 Выход", "🚪 Quit"},

	// ── trayui.go: динамика ──────────────────────────────────────────
	"tray_line_down":   {"↓ Загрузка: %.1f Мбит/с%s", "↓ Download: %.1f Mbps%s"},
	"tray_line_up":     {"↑ Отдача: %.1f Мбит/с", "↑ Upload: %.1f Mbps"},
	"tray_line_ping":   {"⏱ Пинг: %.1f мс (нагр.: %.1f/%.1f)", "⏱ Ping: %.1f ms (loaded: %.1f/%.1f)"},
	"tray_line_jitter": {"〰 Джиттер: %.1f мс", "〰 Jitter: %.1f ms"},
	"tray_line_loss":   {"📉 Потери: %.1f%%", "📉 Loss: %.1f%%"},
	"tray_line_ip":     {"🌐 IP: %s (%s)", "🌐 IP: %s (%s)"},
	"tray_line_net":    {"🏢 Сеть: %s (AS%d)", "🏢 Network: %s (AS%d)"},
	"tray_line_server": {"📍 Сервер: %s (%s)", "📍 Server: %s (%s)"},
	"tray_line_time":   {"🕐 Проверено: %s", "🕐 Checked: %s"},
	"tray_line_err":    {"❌ Ошибка: %s", "❌ Error: %s"},
	"tray_trunc":       {" (обрезан)", " (truncated)"},
	"tray_net_now_f":   {"⚡ Сейчас: ↓ %.1f ↑ %.1f Мбит/с", "⚡ Now: ↓ %.1f ↑ %.1f Mbps"},
	"tray_net_day_f":   {"📅 Сегодня: ↓ %s  ↑ %s", "📅 Today: ↓ %s  ↑ %s"},
	"tray_net_mon_f":   {"🗓 %s: ↓ %s  ↑ %s", "🗓 %s: ↓ %s  ↑ %s"},
	"tray_checking":    {"Speed Test Tray — проверка…", "Speed Test Tray — checking…"},
	"tray_tip_base":    {"↓ %.1f  ↑ %.1f Мбит/с • сегодня ↓%s ↑%s", "↓ %.1f  ↑ %.1f Mbps • today ↓%s ↑%s"},
	"tray_tip_err":     {"\nТест: ошибка (%s)", "\nTest: error (%s)"},
	"tray_tip_ok":      {"\nТест: ↓ %.1f ↑ %.1f Мбит/с • %.0f мс • %s", "\nTest: ↓ %.1f ↑ %.1f Mbps • %.0f ms • %s"},
	"prog_meta":        {"соединение…", "connecting…"},
	"prog_ping":        {"пинг %d%%", "ping %d%%"},
	"prog_download":    {"загрузка %d%%", "download %d%%"},
	"prog_upload":      {"отдача %d%%", "upload %d%%"},
	"prog_loss":        {"потери %d%%", "loss %d%%"},

	// ── trayui.go: уведомления ───────────────────────────────────────
	"toast_fail":     {"❌ Проверка не удалась", "❌ Check failed"},
	"toast_good":     {"✅ Хорошая скорость", "✅ Good speed"},
	"toast_norm":     {"⚠️ Нормальная скорость", "⚠️ Fair speed"},
	"toast_bad":      {"🛑 Плохая скорость", "🛑 Poor speed"},
	"toast_body":     {"↓ %.1f Мбит/с • ↑ %.1f Мбит/с\nпинг %.0f мс • джиттер %.0f мс • потери %.1f%%", "↓ %.1f Mbps • ↑ %.1f Mbps\nping %.0f ms • jitter %.0f ms • loss %.1f%%"},
	"toast_bad_body": {"↓ %.1f Мбит/с — плохо, если ниже %.0f", "↓ %.1f Mbps — bad if below %.0f"},

	// ── netmon.go: единицы ───────────────────────────────────────────
	"byte_tb": {"%.2f ТБ", "%.2f TB"},
	"byte_gb": {"%.2f ГБ", "%.2f GB"},
	"byte_mb": {"%.1f МБ", "%.1f MB"},
	"byte_kb": {"%.1f КБ", "%.1f KB"},
	"byte_b":  {"%d Б", "%d B"},

	// ── measure.go ───────────────────────────────────────────────────
	"err_no_conn":    {"нет соединения с speed.cloudflare.com: %v", "cannot reach speed.cloudflare.com: %v"},
	"err_meta_empty": {"/meta вернул пустой ответ", "/meta returned an empty response"},
	"err_ping":       {"пинг: %w", "ping: %w"},
	"err_ping_all":   {"пинг: все пробы не удались", "ping: all probes failed"},
	"err_dl":         {"загрузка: %w", "download: %w"},
	"err_dl_http":    {"загрузка: HTTP %d — Cloudflare отклонил запрос; уменьшите объём теста или проверяйте реже", "download: HTTP %d — Cloudflare rejected the request; reduce the test size or check less often"},
	"err_dl_short":   {"загрузка: получено всего %d байт: %w", "download: only %d bytes received: %w"},
	"err_dl_short2":  {"загрузка: получено всего %d байт", "download: only %d bytes received"},
	"err_ul":         {"отдача: %w", "upload: %w"},
	"err_ul_http":    {"отдача: HTTP %d — Cloudflare отклонил запрос; уменьшите объём теста", "upload: HTTP %d — Cloudflare rejected the request; reduce the test size"},
	"err_ul_short":   {"отдача: отправлено только %d из %d байт", "upload: only %d of %d bytes sent"},

	// ── watch.go / ping ──────────────────────────────────────────────
	"err_bad_host":      {"некорректный хост %q", "invalid host %q"},
	"ping_summary_ok":   {"%s: отклик %d/%d, среднее %d мс (мин %d, макс %d)", "%s: replied %d/%d, avg %d ms (min %d, max %d)"},
	"ping_summary_lost": {", потеряно %d", ", lost %d"},
	"ping_summary_fail": {"%s: нет ответа (%d из %d попыток)%s", "%s: no reply (%d of %d attempts)%s"},
	"watch_started":     {"Наблюдаю за %s каждые %d мин.", "Watching %s every %d min."},
	"watch_not_found":   {"хост %s не наблюдается", "host %s is not being watched"},
	"watch_stopped":     {"Наблюдение за %s остановлено.", "Stopped watching %s."},
	"watch_none":        {"Активных наблюдений не было.", "There were no active watches."},
	"watch_stopped_all": {"Остановлены все наблюдения (%d).", "Stopped all watches (%d)."},
	"icmp_unreachable":  {"хост недоступен", "host unreachable"},
	"icmp_timeout":      {"таймаут", "timeout"},
	"icmp_general":      {"общая ошибка ICMP", "general ICMP error"},
	"ping_resolve_err":  {"не удалось разрешить %s: %w", "cannot resolve %s: %w"},
	"ping_no_ipv4":      {"%s: нет IPv4-адреса", "%s: no IPv4 address"},
	"ping_need_ipv4":    {"%s: нужен IPv4-адрес", "%s: an IPv4 address is required"},
	"ping_icmp_denied":  {"IcmpCreateFile: отказано", "IcmpCreateFile: denied"},
	"ping_no_reply":     {"нет ответа", "no reply"},
	"ping_icmp_status":  {"нет ответа (ICMP-статус %d)", "no reply (ICMP status %d)"},

	// ── archive.go ───────────────────────────────────────────────────
	"arch_collect":     {"собираю данные", "collecting data"},
	"arch_write_file":  {"записываю файл", "writing file"},
	"arch_export_err":  {"экспорт: %s", "export: %s"},
	"arch_saved":       {"Архив сохранён: %s — %d замеров, трафик за %d дн.", "Archive saved: %s — %d checks, traffic for %d days"},
	"arch_too_big":     {"архив больше %d МБ", "archive larger than %d MB"},
	"arch_not_ours":    {"это не архив SpeedTestTray (строка %d)", "this is not a SpeedTestTray archive (line %d)"},
	"arch_bad_ver":     {"версия архива %d не поддерживается", "archive version %d is not supported"},
	"arch_read_lines":  {"читаю строки", "reading lines"},
	"arch_write_lines": {"записываю строки", "writing lines"},
	"arch_empty":       {"в архиве нет данных (битых строк: %d)", "archive has no data (bad lines: %d)"},
	"arch_check_msg":   {"В архиве %d замеров (%s – %s), пересекается с историей: %d. Спросите пользователя: перезаписать совпавшие или пропустить их?", "The archive has %d checks (%s – %s), overlapping history: %d. Ask the user: overwrite the matching ones or skip them?"},
	"arch_loaded":      {"Загружено: +%d, перезаписано %d, пропущено %d, битых %d", "Imported: +%d, overwritten %d, skipped %d, bad %d"},
	"arch_loaded_full": {"Загружено: добавлено %d, перезаписано %d, пропущено %d, битых строк %d. Трафик: новых дней %d, обновлено %d.", "Imported: added %d, overwritten %d, skipped %d, bad lines %d. Traffic: new days %d, updated %d."},
	"arch_bad_mode":    {"режим %q не поддерживается (check/overwrite/skip)", "mode %q is not supported (check/overwrite/skip)"},
	"arch_no_file":     {"файл не найден: %s", "file not found: %s"},
	"arch_file_big":    {"файл больше %d МБ", "file larger than %d MB"},
	"arch_done":        {"Готово: %d замеров, %d дн. трафика", "Done: %d checks, %d days of traffic"},

	// ── agent.go: инициализация и скачивание ─────────────────────────
	"ag_check_server": {"проверяю llama-server", "checking llama-server"},
	"ag_connect":      {"подключаюсь к llama-server", "connecting to llama-server"},
	"ag_attached":     {"подхвачен работающий llama-server", "attached to a running llama-server"},
	"ag_check_files":  {"проверяю файлы агента", "checking agent files"},
	"ag_check_model":  {"проверяю модель", "checking the model"},
	"ag_start_server": {"запускаю llama-server (%s)", "starting llama-server (%s)"},
	"ag_load_model":   {"загружаю модель, это займёт до минуты", "loading the model, this takes up to a minute"},
	"ag_get_server":   {"не удалось получить llama-server: ", "failed to get llama-server: "},
	"ag_get_model":    {"не удалось получить модель: ", "failed to get the model: "},
	"ag_start_err":    {"не удалось запустить llama-server: ", "failed to start llama-server: "},
	"ag_died":         {"llama-server завершился раньше готовности — проверьте пути и порт %s", "llama-server exited before becoming ready — check the paths and port %s"},
	"ag_timeout":      {"llama-server не ответил за 3 минуты", "llama-server did not respond within 3 minutes"},
	"ag_dl_server":    {"скачиваю llama.cpp", "downloading llama.cpp"},
	"ag_unpack":       {"распаковываю llama.cpp", "unpacking llama.cpp"},
	"ag_dl_model":     {"скачиваю модель", "downloading the model"},
	"ag_dl_prog":      {"%s: %d из %d МБ", "%s: %d of %d MB"},
	"ag_no_exe":       {"в архиве нет %s", "the archive does not contain %s"},
	"ag_dl_short":     {"недокачано: %d из %d МБ", "incomplete download: %d of %d MB"},

	// ── agent.go: чат, стадии, ошибки LLM ────────────────────────────
	"ag_not_ready":       {"агент не готов (%s)", "agent is not ready (%s)"},
	"ag_dead":            {"llama-server не отвечает — нажмите «Повторить»", "llama-server is not responding — press Retry"},
	"st_read_digest":     {"читаю сводку замеров", "reading the measurement digest"},
	"st_answer":          {"формулирую ответ", "composing the answer"},
	"st_think":           {"думаю над ответом", "thinking about the answer"},
	"st_clarify":         {"уточняю действие", "clarifying the action"},
	"st_tool":            {"инструмент: %s", "tool: %s"},
	"st_done":            {"готово", "done"},
	"st_need_tool":       {"нужен инструмент", "a tool is needed"},
	"ag_refusal":         {"Команда похожа на действие, но вызвать инструмент не получилось. Попробуйте переформулировать: «пингуй <хост>», «следи за <хост> каждые N минут», «останови наблюдение за <хост>», «останови все наблюдения».", "The command looks like an action, but the tool could not be called. Try rephrasing: “ping <host>”, “watch <host> every N minutes”, “stop watching <host>”, “stop all watches”."},
	"ag_honest_nudge":    {"Инструмент вызвать не удалось. Ответь на вопрос пользователя честно и кратко, строго по данным сводки измерений: если данных не хватает, так и скажи — измерений пока мало — и посоветуй измерять регулярно: включить автопроверку хотя бы на час, например каждые 15–30 минут, тогда можно будет ответить точнее. Не выдумывай числа и не упоминай инструменты и внутреннее устройство приложения.", "The tool call failed. Answer the user honestly and briefly, strictly from the measurement digest: if the data is insufficient, say so directly — there are not enough measurements yet — and advise measuring regularly: enable auto-checks for at least an hour, e.g. every 15–30 minutes, after which you can answer more precisely. Do not invent numbers and do not mention tools or app internals."},
	"dg_hours_intro":     {"Средняя ↓ по часам суток за неделю (Мбит/с, замеров): ", "Average ↓ by hour of day over the week (Mbps, checks): "},
	"opt_uac_cancelled":  {"Запуск отменён — не подтверждён запрос Windows (UAC).", "Launch cancelled — the Windows (UAC) prompt was not confirmed."},
	"opt_timeout":        {"Помощник оптимизации не ответил слишком долго — наблюдение остановлено.", "The optimization helper did not respond in time — watching stopped."},
	"opt_cancelled_user": {"Оптимизация отменена пользователем.", "Optimization cancelled by user."},
	"ag_done_fallback":   {"Готово — действие выполнено.", "Done — action completed."},
	"ag_llm_unreachable": {"llama-server недоступен: %v", "llama-server unreachable: %v"},
	"ag_llm_code":        {"llama-server вернул %d: %s", "llama-server returned %d: %s"},
	"ag_llm_unexpected":  {"неожиданный ответ llama-server", "unexpected response from llama-server"},
	"ag_empty_reply":     {"(пустой ответ модели)", "(empty model reply)"},

	// ── agent.go: наблюдения инструментов ────────────────────────────
	"obs_unavailable":  {"действие недоступно", "action unavailable"},
	"obs_refused":      {"отказ: ", "refused: "},
	"obs_check_err":    {"Проверка завершилась ошибкой: %s", "The check failed: %s"},
	"obs_check_ok":     {"Проверка завершена в %s: ↓ %.1f ↑ %.1f Мбит/с, пинг %.0f мс, сервер %s.", "Check finished at %s: ↓ %.1f ↑ %.1f Mbps, ping %.0f ms, server %s."},
	"obs_check_late":   {"Проверка запущена, но не завершилась за 90 секунд — результат появится в истории позже.", "The check started but did not finish within 90 seconds — the result will appear in the history later."},
	"obs_wait":         {"жду замер… %d с", "waiting for the check… %d s"},
	"obs_bad_interval": {"отказ: период автопроверок должен быть 5–180 минут (пришло %d)", "refused: the check interval must be 5–180 minutes (got %d)"},
	"obs_interval_ok":  {"Период автопроверок изменён на %d минут.", "Check interval changed to %d minutes."},
	"obs_bad_dl":       {"отказ: объём теста загрузки должен быть 5–200 МБ (пришло %d)", "refused: download size must be 5–200 MB (got %d)"},
	"obs_dl_ok":        {"Объём теста загрузки: %d МБ.", "Download size: %d MB."},
	"obs_bad_ul":       {"отказ: объём теста отдачи должен быть 5–200 МБ (пришло %d)", "refused: upload size must be 5–200 MB (got %d)"},
	"obs_ul_ok":        {"Объём теста отдачи: %d МБ.", "Upload size: %d MB."},
	"obs_bad_limits":   {"отказ: нужны корректные границы, bad < good (пришло bad=%.1f good=%.1f)", "refused: valid limits are needed, bad < good (got bad=%.1f good=%.1f)"},
	"obs_limits_ok":    {"Границы зон обновлены: «плохо» < %.0f Мбит/с, «хорошо» ≥ %.0f Мбит/с.", "Speed zones updated: “bad” < %.0f Mbps, “good” ≥ %.0f Mbps."},
	"obs_bad_rate":     {"отказ: частота обновления — только 1000, 2000, 5000 или 10000 мс", "refused: refresh rate must be 1000, 2000, 5000 or 10000 ms"},
	"obs_rate_ok":      {"Частота обновления живого графика: %d мс.", "Live chart refresh rate: %d ms."},
	"obs_need_path":    {"отказ: укажите полный путь к архивному файлу", "refused: provide the full path to the archive file"},
	"obs_mode":         {"режим %s", "mode %s"},
	"obs_need_host":    {"отказ: укажите хост (имя или IP)", "refused: provide a host (name or IP)"},
	"obs_need_host2":   {"отказ: укажите хост", "refused: provide a host"},
	"obs_unknown":      {"неизвестное действие: %s", "unknown action: %s"},

	// ── agent.go: названия инструментов ──────────────────────────────
	"tt_run_check":    {"замер скорости", "speed check"},
	"tt_set_interval": {"период автопроверок", "check interval"},
	"tt_set_download": {"объём теста загрузки", "download size"},
	"tt_set_upload":   {"объём теста отдачи", "upload size"},
	"tt_set_limits":   {"границы зон", "speed zones"},
	"tt_set_net_rate": {"частота обновления", "refresh rate"},
	"tt_export":       {"экспорт архива", "archive export"},
	"tt_import":       {"загрузка архива", "archive import"},
	"tt_ping":         {"пинг хоста", "host ping"},
	"tt_watch":        {"наблюдение за хостом", "host watch"},
	"tt_unwatch":      {"остановка наблюдения", "stopping the watch"},
	"tt_unwatch_all":  {"остановка всех наблюдений", "stopping all watches"},

	// ── agent.go: сводка (digest + «Выводы») ─────────────────────────
	"dg_no_data":     {"Измерений пока нет.", "No measurements yet."},
	"dg_no_data_ins": {"Пока нет измерений — нажмите «Проверить сейчас», чтобы собрать первые данные.", "No measurements yet — press “Check now” to collect the first data."},
	"dg_day":         {"За последние 24 ч: %d измерений, средняя ↓ %.1f Мбит/с (мин %.1f, макс %.1f), средняя ↑ %.1f, средний пинг %.0f мс.", "Last 24 h: %d measurements, avg ↓ %.1f Mbps (min %.1f, max %.1f), avg ↑ %.1f, avg ping %.0f ms."},
	"dg_day_ins":     {"За 24 ч: средняя ↓ %.1f Мбит/с (от %.1f до %.1f), проверок %d.", "24 h: avg ↓ %.1f Mbps (from %.1f to %.1f), checks %d."},
	"dg_unchanged":   {"без изменений", "no change"},
	"dg_better":      {"на %.0f%% лучше", "%.0f%% better"},
	"dg_worse":       {"на %.0f%% хуже", "%.0f%% worse"},
	"dg_trend":       {"Тренд: средняя за сутки %.1f против %.1f Мбит/с сутками ранее (%s).", "Trend: today's average %.1f vs %.1f Mbps a day earlier (%s)."},
	"dg_trend_ins":   {"Тренд: по сравнению с предыдущими сутками скорость %s.", "Trend: compared to the previous day the speed is %s."},
	"dg_week":        {"За 7 дней: %d успешных измерений (%.0f%% в зелёной зоне ≥%.0f, %d ниже плохой границы %.0f), ошибок %d, обрезанных тестов %d. Средний пинг %.0f мс, средние потери %.1f%%.", "Over 7 days: %d successful measurements (%.0f%% in the green zone ≥%.0f, %d below the bad limit of %.0f), %d errors, %d truncated tests. Avg ping %.0f ms, avg loss %.1f%%."},
	"dg_anomalies":   {"Аномалии: %d измерений за неделю ниже «плохой» границы %.0f Мбит/с.", "Anomalies: %d measurements this week below the “bad” limit of %.0f Mbps."},
	"dg_errors":      {"Ошибок проверок за неделю: %d.", "Failed checks this week: %d."},
	"dg_locs":        {"Локации за 7 дней: ", "Locations over 7 days: "},
	"dg_loc_item":    {"%s — средняя %.1f Мбит/с (%d замеров); ", "%s — avg %.1f Mbps (%d checks); "},
	"dg_best_loc":    {"Лучшая локация за неделю: %s — %.1f Мбит/с (%d замеров).", "Best location this week: %s — %.1f Mbps (%d checks)."},
	"dg_hours":       {"По часам за 7 дней: лучший %02d:00 — %.1f Мбит/с, худший %02d:00 — %.1f Мбит/с.", "By hour over 7 days: best %02d:00 — %.1f Mbps, worst %02d:00 — %.1f Mbps."},
	"dg_hours_ins":   {"Лучший час за неделю: %02d:00 (↓ %.1f Мбит/с), худший: %02d:00 (↓ %.1f).", "Best hour this week: %02d:00 (↓ %.1f Mbps), worst: %02d:00 (↓ %.1f)."},
	"dg_traffic":     {"Трафик: сегодня ↓ %s ↑ %s, за месяц ↓ %s ↑ %s.", "Traffic: today ↓ %s ↑ %s, this month ↓ %s ↑ %s."},
	"dg_settings":    {"Настройки: автопроверка каждые %d мин; тест ↓%d/↑%d МБ; зоны: плохо < %.0f, хорошо ≥ %.0f Мбит/с; обновление графика %d мс.", "Settings: auto-check every %d min; test ↓%d/↑%d MB; zones: bad < %.0f, good ≥ %.0f Mbps; chart refresh %d ms."},
	"dg_last":        {"Последняя проверка: %s — ↓ %.1f ↑ %.1f Мбит/с, пинг %.0f мс (%s).", "Last check: %s — ↓ %.1f ↑ %.1f Mbps, ping %.0f ms (%s)."},
	"dg_unknown_loc": {"неизвестно", "unknown"},
	"dg_watch_intro": {"Наблюдения за хостами (остановить можно инструментом unwatch_host/unwatch_all): ", "Host watches (stop with the unwatch_host/unwatch_all tool): "},
	"dg_watch_item":  {"%s (каждые %d мин)", "%s (every %d min)"},
	"dg_seed_user":   {"Числовая сводка замеров (единственный источник фактов):", "Numeric measurement digest (the only source of facts):"},
	"dg_seed_ack":    {"Принял сводку, работаю с этими данными.", "Digest received, working with this data."},

	// ── main.go: консоль (-once, -netmon) ────────────────────────────
	"flag_once":    {"одна проверка с выводом в консоль, без трея", "run a single check and print it to the console, without the tray"},
	"flag_netmon":  {"диагностика монитора трафика: 8 опросов по 2 сек в консоль", "traffic monitor diagnostics: 8 polls of 2 s each to the console"},
	"netmon_intro": {"Мониторинг на 16 секунд (попробуйте что-нибудь открыть в интернете)…", "Monitoring for 16 seconds (try opening something on the internet)…"},
	"netmon_line":  {"  %2d с: ↓ %.1f ↑ %.1f Мбит/с | сегодня ↓%s ↑%s", "  %2d s: ↓ %.1f ↑ %.1f Mbps | today ↓%s ↑%s"},
	"once_intro":   {"Идёт проверка (Cloudflare speed test)…", "Running the check (Cloudflare speed test)…"},
	"once_err":     {"Ошибка: %s", "Error: %s"},
	"ph_meta":      {"соединение", "connecting"},
	"ph_ping":      {"пинг", "ping"},
	"ph_download":  {"загрузка", "download"},
	"ph_upload":    {"отдача", "upload"},
	"ph_loss":      {"потери", "loss"},
	"rep_down":     {"Загрузка:    %.1f Мбит/с%s", "Download:    %.1f Mbps%s"},
	"rep_up":       {"Отдача:      %.1f Мбит/с", "Upload:      %.1f Mbps"},
	"rep_ping":     {"Пинг:        %.1f мс (под нагрузкой: %.1f / %.1f мс)", "Ping:        %.1f ms (under load: %.1f / %.1f ms)"},
	"rep_jitter":   {"Джиттер:     %.1f мс", "Jitter:     %.1f ms"},
	"rep_loss":     {"Потери:      %.1f%%", "Loss:       %.1f%%"},
	"rep_net":      {"Сеть:        %s (AS%d)", "Network:    %s (AS%d)"},
	"rep_server":   {"Сервер:      %s (%s)", "Server:     %s (%s)"},
	"rep_proto":    {"Протокол:    %s", "Protocol:   %s"},
	"rep_time":     {"Время теста: %s", "Test time:  %s"},
}

// monthNames — названия месяцев для трея (строка «Месяц: ↓ … ↑ …»).
var monthNames = map[string][12]string{
	LangRu: {"Январь", "Февраль", "Март", "Апрель", "Май", "Июнь",
		"Июль", "Август", "Сентябрь", "Октябрь", "Ноябрь", "Декабрь"},
	LangEn: {"January", "February", "March", "April", "May", "June",
		"July", "August", "September", "October", "November", "December"},
}

// monthName — название месяца на текущем языке.
func monthName(t time.Time) string {
	return monthNames[Lang()][int(t.Month())-1]
}
