import { SessionChart, HoursChart } from "@/components/LiveChart";
import { demoRating, demoHours, demoHist, zoneColor } from "./demoData";
import {
  ArrowDownToLine, Gamepad2, MonitorPlay, Waves, type LucideIcon,
} from "lucide-react";

const TICKS = 22, MAXD = 100;
const hh = (i: number) => String(i).padStart(2, "0") + ":00";

/** Мок веб-дашборда Speed Test Tray (демо-данные). */
export function DashboardDemo() {
  const avg = demoHours.map((x) => x.avg);
  const cnt = demoHours.map((x) => x.cnt);

  return (
    <div className="flex flex-col gap-3 rounded-2xl border border-line bg-panel2 p-[18px]">
      <div className="text-center font-mono text-[11.5px] text-dimmer">
        // демо дашборда v1.5 — данные сгенерированы для примера
      </div>

      {/* шапка */}
      <div className="flex items-center gap-3 rounded-xl border border-line bg-panel px-4 py-3.5">
        <div className="flex h-[34px] w-[34px] items-center justify-center rounded-[9px] bg-[#1d1d21]">
          <span className="h-[9px] w-[9px] rounded-full bg-ok shadow-[0_0_10px_var(--color-ok)] animate-glow-pulse" />
        </div>
        <div>
          <div className="text-base font-semibold">Мониторинг сети</div>
          <div className="mt-0.5 font-mono text-xs text-dim">
            SpeedTestTray · <b className="font-medium text-[#cfcfd4]">Cloudflare</b> · каждые 10 мин
          </div>
        </div>
        <div className="ml-auto text-right">
          <div className="font-mono text-[30px] font-semibold leading-none text-shadow-[0_0_16px_rgba(255,255,255,.25)]">
            87.4<span className="text-[13px] font-normal text-dim"> ↓ Мбит/с</span>
          </div>
          <div className="mt-1.5 font-mono text-xs text-dim">↑ 62.1 · сегодня ↓ 3.1 ГБ</div>
        </div>
      </div>

      {/* чип последней проверки */}
      <div className="flex flex-wrap gap-2.5 rounded-[9px] border border-line border-l-[3px] border-l-ok bg-panel px-3 py-2 font-mono text-xs text-dim">
        <span>✓ последняя проверка 22:41:47</span>
        <span><b className="font-medium text-[#cfcfd4]">Амстердам · Datacamp Limited</b></span>
        <span>↓ 87.4 ↑ 62.8 Мбит/с</span>
        <span>пинг 34 мс · джиттер 6 мс</span>
      </div>

      {/* трафик */}
      <div className="flex flex-wrap gap-x-7 gap-y-2 rounded-xl border border-line bg-panel px-4 py-3 font-mono text-[13px] text-dim">
        <span><span className="text-dimmer">Сегодня:</span> ↓ <b className="font-medium text-[#dcdcdf]">2.33 ГБ</b> · ↑ <b className="font-medium text-[#dcdcdf]">1.83 ГБ</b></span>
        <span><span className="text-dimmer">За месяц:</span> ↓ <b className="font-medium text-[#dcdcdf]">6.61 ГБ</b> · ↑ <b className="font-medium text-[#dcdcdf]">5.69 ГБ</b></span>
      </div>

      {/* график сессии */}
      <div className="rounded-xl border border-line bg-panel px-3.5 pb-2 pt-3">
        <SessionChart />
      </div>

      {/* рейтинг + часы */}
      <div className="grid grid-cols-1 gap-3 min-[700px]:grid-cols-2">
        <div className="rounded-xl border border-line bg-panel px-4 py-3.5">
          <div className="mb-2 text-xs text-dim">Рейтинг локаций — куда подключаться</div>
          {demoRating.map((r) => (
            <div
              key={r.loc}
              className="flex items-center gap-2.5 border-b border-[#1c1c20] py-[7px] font-mono text-[12.5px] last:border-0"
            >
              <span className="min-w-0 flex-1 truncate text-[#e3e3e7]">
                {r.loc}<span className="ml-1.5 text-[10.5px] text-dimmer">{r.colo}</span>
              </span>
              <span className="w-[34px] text-right text-dimmer">×{r.n}</span>
              <span className="w-[74px] text-right" style={{ color: zoneColor[r.zone] }}>
                ↓ {r.avg.toFixed(1)}
              </span>
              <span className="w-[52px] text-right text-[11.5px] text-dimmer">{r.ping} мс</span>
            </div>
          ))}
        </div>
        <div className="rounded-xl border border-line bg-panel px-4 py-3.5">
          <div className="mb-2 text-xs text-dim">Скорость по часам суток — когда именно</div>
          <HoursChart avg={avg} cnt={cnt} bad={25} />
        </div>
      </div>

      {/* история */}
      <div className="rounded-xl border border-line bg-panel px-4 py-3.5">
        <div className="mb-2 text-xs text-dim">История проверок — локация · скорость · длительность</div>
        {demoHist.map((h) => {
          const fill = Math.max(1, Math.round((h.down / MAXD) * TICKS));
          return (
            <div key={h.tm} className="flex items-center gap-3 border-b border-[#1c1c20] py-2 font-mono text-[12.5px] last:border-0">
              <span className="w-[58px] text-dimmer">{h.tm}</span>
              <span className="w-[86px] overflow-hidden text-ellipsis whitespace-nowrap text-[#a9a9b0]">{h.loc}</span>
              <div className="flex h-3.5 min-w-10 flex-1 items-center gap-[2px]">
                {Array.from({ length: TICKS }, (_, i) => (
                  <i
                    key={i}
                    className={
                      "min-w-[2px] flex-1 rounded-[1px] " +
                      (i < fill
                        ? h.zone === "ok"
                          ? "bg-ok"
                          : h.zone === "warn"
                            ? "bg-warn"
                            : "bg-bad"
                        : "bg-[#1e1e23]")
                    }
                  />
                ))}
              </div>
              <span className="w-[66px] text-right" style={{ color: zoneColor[h.zone] }}>↓ {h.down.toFixed(1)}</span>
              <span className="w-[34px] text-right text-[11.5px] text-dimmer">{h.dur} с</span>
            </div>
          );
        })}
      </div>
    </div>
  );
}

/** Блок бенефитов после демо. */
export function Benefits() {
  const items: [LucideIcon, string][] = [
    [MonitorPlay, "видео без буферизации"],
    [Gamepad2, "игры без лагов"],
    [ArrowDownToLine, "загрузки на максимуме"],
    [Waves, "спокойный сёрфинг"],
  ];
  return (
    <div className="mt-6 grid grid-cols-2 gap-3.5 min-[760px]:grid-cols-4">
      {items.map(([Icon, t]) => (
        <div key={t} className="flex flex-col items-center gap-2 rounded-xl border border-line bg-panel p-4 text-center">
          <span className="flex h-9 w-9 items-center justify-center rounded-lg border border-ok/20 bg-ok/10 text-ok">
            <Icon className="h-[18px] w-[18px]" strokeWidth={1.75} />
          </span>
          <span className="text-[13.5px] font-medium text-[#cfcfd4]">{t}</span>
        </div>
      ))}
    </div>
  );
}

/** Кодовые бейджи под hero. */
export function Badges() {
  const b: [string, string][] = [
    ["Go", " · один бинарник"],
    ["Windows", " 10 / 11"],
    ["Cloudflare", " speed test API"],
    ["v1.5.0", ""],
    ["127.0.0.1:8787", " — дашборд"],
    ["CC BY-NC 4.0", ""],
  ];
  return (
    <div className="flex flex-wrap gap-2">
      {b.map(([k, v]) => (
        <span key={k} className="rounded-md border border-line bg-panel px-[9px] py-1 font-mono text-[11.5px] font-medium text-dim">
          <i className="not-italic text-[#c5c5cb]">{k}</i>{v}
        </span>
      ))}
    </div>
  );
}
