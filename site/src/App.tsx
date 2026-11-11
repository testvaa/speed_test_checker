import { useState } from "react";
import { NetBackground } from "@/components/NetBackground";
import { LiveChart } from "@/components/LiveChart";
import { DashboardDemo, Benefits, Badges } from "@/components/DashboardDemo";
import { Card } from "@/components/ui/card";
import { Button as Btn } from "@/components/ui/button";
import { TypingAnimation } from "@/components/ui/typing-animation";
import {
  Activity, Bot, ChevronDown, Clapperboard, Download, Gamepad2, Globe,
  MapPin, Package, Palette, Star, Tags, Terminal, TrendingDown, Zap,
  type LucideIcon,
} from "lucide-react";

/** Современный контейнер под линейную иконку. */
function IconTile({ icon: Icon, tone }: { icon: LucideIcon; tone: "ok" | "bad" }) {
  return (
    <span
      className={
        "mb-3 flex h-10 w-10 items-center justify-center rounded-xl border " +
        (tone === "ok"
          ? "border-ok/20 bg-ok/10 text-ok"
          : "border-bad/20 bg-bad/10 text-bad")
      }
    >
      <Icon className="h-5 w-5" strokeWidth={1.75} />
    </span>
  );
}

const GH = "https://github.com/testvaa/speed_test_cheeker";
const DL_WIN = GH + "/releases/latest/download/SpeedTestTray.exe";
const DL_MAC = GH + "/releases/latest/download/SpeedTestTray-macos-arm64";
const DL_LIN = GH + "/releases/latest/download/SpeedTestTray-linux-x64";

function Header() {
  return (
    <header className="sticky top-0 z-50 border-b border-line bg-[rgba(11,11,12,.86)] backdrop-blur-[8px]">
      <div className="mx-auto max-w-[960px] px-5">
        <nav className="flex items-center gap-[22px] py-3.5">
          <a className="flex items-center gap-2.5 text-[15px] font-semibold tracking-[.2px] no-underline" href="#">
            <span className="h-[9px] w-[9px] rounded-full bg-ok shadow-[0_0_10px_var(--color-ok)] animate-glow-pulse" />
            Speed Test Tray
          </a>
          <div className="ml-auto hidden gap-5 min-[640px]:flex">
            <a className="text-[13.5px] text-dim no-underline hover:text-txt" href="#dashboard">Статистика</a>
            <a className="text-[13.5px] text-dim no-underline hover:text-txt" href="#pains">Знакомо?</a>
            <a className="text-[13.5px] text-dim no-underline hover:text-txt" href="#solution">Решение</a>
            <a className="text-[13.5px] text-dim no-underline hover:text-txt" href="#start">Быстрый старт</a>
            <a className="text-[13.5px] text-dim no-underline hover:text-txt" href={GH}>GitHub</a>
          </div>
        </nav>
      </div>
    </header>
  );
}

function Hero() {
  const [headlineDone, setHeadlineDone] = useState(false);
  const reveal = headlineDone
    ? "opacity-100 translate-y-0"
    : "opacity-0 translate-y-2";
  return (
    <div className="pb-10 pt-20">
      <span className="mb-[18px] inline-flex items-center gap-2 font-mono text-[11px] font-semibold uppercase tracking-[2px] text-dimmer before:inline-block before:h-px before:w-[26px] before:bg-dimmer">
        вопрос на засыпку
      </span>
      <TypingAnimation
        className="mb-[18px] min-h-[3.4em] max-w-[760px] text-left whitespace-pre-line text-[clamp(28px,4.6vw,44px)] font-semibold leading-[1.14] tracking-[-.5px] drop-shadow-none"
        duration={42}
        segments={[
          { text: "Технологии растут с каждым годом.\nА насколько вы " },
          { text: "уверены", className: "text-ok [text-shadow:0_0_26px_rgba(52,211,153,.45)]" },
          { text: " в интернете, которым пользуетесь?" },
        ]}
        onComplete={() => setHeadlineDone(true)}
      />
      <div className={"transition-all duration-700 " + reveal}>
        <p className="mb-3.5 max-w-[660px] text-[clamp(15px,2vw,17px)] leading-[1.7] text-dim">
          Стриминг в 4K, облачные игры, видеозвонки, гигабайты обновлений — требования к каналу всё выше. Но действительно
          ли ваш интернет <b className="font-medium text-[#c9c9cf]">достаточно качественный</b> и соответствует тому, что вы
          от него ждёте — и за что платите провайдеру?
        </p>
        <p className="mb-[30px] font-mono text-[15px] font-medium text-[#b9b9c0]">
          Скорость в тарифе — это обещание. Реальность можно только <i className="not-italic text-ok">измерить</i>.
        </p>
      </div>
      <div className="mb-6 flex flex-wrap gap-3">
        <Btn variant="primary" href={DL_WIN}><Download className="h-4 w-4" /> Измерить свой канал</Btn>
        <Btn href={GH}><Star className="h-4 w-4" /> Репозиторий на GitHub</Btn>
      </div>
      <Badges />
      <LiveChart />
    </div>
  );
}

function Section({ id, tag, title, sub, children }: {
  id: string; tag: string; title: string; sub?: string; children: React.ReactNode;
}) {
  return (
    <section id={id} className="py-16">
      <span className="mb-2.5 block font-mono text-[11px] font-semibold uppercase tracking-[1.6px] text-dimmer">{tag}</span>
      <h2 className="mb-2 text-2xl font-semibold tracking-[-.3px]">{title}</h2>
      {sub && <p className="mb-7 max-w-[660px] leading-[1.65] text-dim">{sub}</p>}
      {children}
    </section>
  );
}

const pains: [LucideIcon, string, string][] = [
  [Clapperboard, "Видео буксует в самый интересный момент", "Кружок буферизации посреди фильма — классика. Но так ли плох канал в этот момент? Никто не знает: замера нет."],
  [Gamepad2, "Пинг скакнул — и катка проиграна", "В играх важна не только скорость, но и задержка и её стабильность. Узнаёте о проблеме постфактум, по своим фрагам."],
  [TrendingDown, "«До 100 Мбит/с» — только в тарифе", "Провайдер обещает максимум. Сколько реально приходит вечером, в час пик — проверить нечем, если только не вручную гонять спидтест."],
  [Globe, "VPN утром летает, к вечеру еле дышит", "Разные локации ведут себя по-разному в разное время суток. Какую включить сейчас — гадание на кофейной гуще."],
];

function Pains() {
  return (
    <Section id="pains" tag="Знакомо?" title="Когда интернет подводит, это всегда заметно"
      sub="Проблема в том, что «кажется медленным» нельзя принести провайдеру — да и самому себе не докажешь.">
      <div className="grid gap-4 min-[560px]:grid-cols-2">
        {pains.map(([Icon, t, d]) => (
          <Card key={t}>
            <IconTile icon={Icon} tone="bad" />
            <div className="mb-[7px] text-[15px] font-semibold text-[#f3d9d9]">{t}</div>
            <div className="text-[13.5px] leading-[1.62] text-dim">{d}</div>
          </Card>
        ))}
      </div>
      <div className="mt-[22px] rounded-r-[10px] border-l-[3px] border-ok bg-panel px-4 py-2.5 font-mono text-[14.5px] font-medium text-[#b9b9c0]">
        Ощущения — не аргумент. Аргумент — <i className="not-italic text-ok">регулярные замеры и статистика</i>.
      </div>
    </Section>
  );
}

const checks = [
  ["Регулярно проверяет канал", "через публичный API спидтеста Cloudflare: загрузка, отдача, пинг, джиттер, потери — без ключей и регистрации."],
  ["Копит статистику", "история проверок, трафик за сегодня и за месяц — всё хранится локально в json-файлах рядом с программой."],
  ["Подсказывает лучшую локацию VPN", "рейтинг локаций по скорости и график по часам суток — видно, какая страна быстрее именно в ваше время."],
  ["Сообщает о проблемах", "цветная иконка в трее и системные уведомления, когда канал падает ниже заданной границы."],
];

const features: [LucideIcon, string, string][] = [
  [Zap, "Замеры через Cloudflare", "Тот же API, что у speed.cloudflare.net: пинг без нагрузки и под нагрузкой, джиттер, потери пакетов, IP, провайдер и сервер."],
  [Palette, "Цветная иконка в трее", "Зелёная — хорошо, жёлтая — норма, красная — плохо. Границы настраиваются. Во время проверки иконка «оживает» свипом спидометра."],
  [Activity, "Монитор трафика", "Текущие скорости каждые 2 секунды, итоги за день и месяц, история ~3 месяца дней и ~2 года месяцев."],
  [MapPin, "Аналитика локаций VPN", "Каждый замер запоминает город сервера и AS-сеть — видно, через какой VPN шёл трафик и какая локация быстрее."],
  [Bot, "ИИ-агент на локальной модели", "Чат на дашборде: «сделай замер», «следи за router.local каждые 5 минут», архивы и настройки — модель крутится на вашей машине, ничего не уходит наружу."],
  [Package, "Портативность", "Один бинарь (~10 МБ), без установки. Все данные — config.json, traffic.json, history.json — рядом с программой."],
  [Terminal, "Консольные режимы", "-once — полный отчёт в консоль без трея, -netmon — диагностика монитора трафика."],
];

function Solution() {
  return (
    <Section id="solution" tag="Наш ответ" title="Speed Test Tray — контроль канала на автопилоте"
      sub="Маленькая утилита для Windows, macOS и Linux живёт в трее и сама, по расписанию, отвечает на все вопросы выше — цифрами.">
      <ul className="mb-7 grid list-none gap-3 p-0">
        {checks.map(([b, rest]) => (
          <li key={b} className="flex items-baseline gap-[11px] text-[14.5px] leading-[1.55] text-dim
            before:font-bold before:text-ok before:content-['✓'] before:[text-shadow:0_0_10px_rgba(52,211,153,.6)]">
            <span><b className="font-medium text-[#dcdcdf]">{b}</b> {rest}</span>
          </li>
        ))}
      </ul>
      <div className="grid gap-4 min-[820px]:grid-cols-3 min-[560px]:grid-cols-2">
        {features.map(([Icon, t, d]) => (
          <Card key={t}>
            <IconTile icon={Icon} tone="ok" />
            <div className="mb-[7px] text-[15px] font-semibold">{t}</div>
            <div className="text-[13.5px] leading-[1.62] text-dim">{d}</div>
          </Card>
        ))}
      </div>
    </Section>
  );
}

const steps = [
  ["Скачайте и запустите", <>Возьмите бинарь под свою ОС из последнего релиза: <code className="rounded-md border border-[#292930] bg-[#1c1c20] px-1.5 py-0.5 font-mono text-[12.5px] text-[#cfcfd4]">SpeedTestTray.exe</code> (Windows), <code className="rounded-md border border-[#292930] bg-[#1c1c20] px-1.5 py-0.5 font-mono text-[12.5px] text-[#cfcfd4]">SpeedTestTray-macos-arm64</code> (Apple Silicon) или <code className="rounded-md border border-[#292930] bg-[#1c1c20] px-1.5 py-0.5 font-mono text-[12.5px] text-[#cfcfd4]">SpeedTestTray-linux-x64</code> — установки нет. На Linux сделайте файл исполняемым; если SmartScreen предупредит о неизвестном издателе: «Подробнее → Выполнить в любом случае».</>],
  ["Первая проверка — сразу", <>Иконка появится в трее, проверка выполнится автоматически. Цвет иконки — вердикт: зелёный / жёлтый / красный. Правый клик — полное меню: результаты, трафик, интервалы, настройки.</>],
  ["Откройте дашборд", <>Меню трея → «🖥 Открыть дашборд» — или зайдите на <code className="rounded-md border border-[#292930] bg-[#1c1c20] px-1.5 py-0.5 font-mono text-[12.5px] text-[#cfcfd4]">127.0.0.1:8787</code>. Живой график, история, аналитика локаций VPN и чат с ИИ-агентом уже собираются.</>],
];

function StartSteps() {
  return (
    <Section id="start" tag="Действие" title="Три шага до первого замера">
      <Card>
        {steps.map(([t, d], i) => (
          <div key={t as string} className={"flex gap-4 py-[18px]" + (i < steps.length - 1 ? " border-b border-[#1c1c20]" : "")}>
            <div className="flex h-[34px] w-[34px] flex-none items-center justify-center rounded-[9px] border border-line bg-panel font-mono text-[15px] font-semibold text-dimmer">
              {i + 1}
            </div>
            <div>
              <h3 className="mb-1.5 mt-0.5 text-[15px] font-semibold">{t}</h3>
              <p className="m-0 text-[13.5px] leading-[1.62] text-dim">{d}</p>
            </div>
          </div>
        ))}
      </Card>
    </Section>
  );
}

const details: [string, React.ReactNode][] = [
  ["Платформы", "Windows x64 · Linux x64/arm64 · macOS arm64/x64 — готовые бинари в релизах"],
  ["Замеры", "speed.cloudflare.com: /meta, TTFB-пробы, __down/__up, оценка потерь"],
  ["Интервал проверок", "1 · 5 · 10 · 15 · 30 · 60 минут — из меню трея"],
  ["Объём теста", "загрузка 5–100 МБ, отдача 2–50 МБ — на ваш трафик"],
  ["Зоны и уведомления", "границы «плохо/хорошо»; тосты: все / только проблемы / выкл"],
  ["Монитор трафика", "счётчики сетевых интерфейсов (gopsutil), опрос раз в 2 с"],
  ["ИИ-агент", "локальная LFM3-1.7B через llama.cpp (Metal/Vulkan/CPU), инструменты: замер, наблюдение за хостами (ICMP), архив, настройки"],
  ["Дашборд", "встроенная страница (go:embed), HTTP только на 127.0.0.1:8787+"],
  ["Данные", "config.json · traffic.json · history.json — рядом с exe, переносятся копированием; история до 200 проверок"],
  ["Сборка из исходников", 'go build -trimpath -ldflags "-s -w -H=windowsgui" -o SpeedTestTray.exe . (Windows; Linux/macOS собирает CI)'],
  ["Лицензия", <a key="lic" className="text-[#7ce3b8] no-underline" href="https://creativecommons.org/licenses/by-nc/4.0/deed.ru">CC BY-NC 4.0</a>],
];

function Details() {
  return (
    <Section id="details" tag="Под капотом" title="Технические детали и настройки">
      <Card>
        <table className="w-full border-collapse text-[13.5px]">
          <tbody>
            {details.map(([k, v]) => (
              <tr key={k as string}>
                <td className="whitespace-nowrap border-b border-[#1c1c20] py-2.5 pl-0 pr-3 align-top text-dim">{k}</td>
                <td className="border-b border-[#1c1c20] py-2.5 align-top font-mono text-[12.5px] text-[#cfcfd4]">{v}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Card>
    </Section>
  );
}

function FinalCTA() {
  return (
    <section id="download" className="py-16">
      <div className="rounded-[18px] border border-[#263038] bg-gradient-to-b from-[#12151a] to-[#101318] px-7 py-12 text-center max-[560px]:px-4">
        <h2 className="mb-2.5 text-2xl font-semibold tracking-[-.3px]">Узнайте, чему верить: тарифу или факту</h2>
        <p className="mx-auto mb-6 max-w-[520px] leading-[1.65] text-dim">
          Скачайте Speed Test Tray — и уже через минуту у вас будет первый честный замер, а через неделю — статистика,
          с которой можно разговаривать с провайдером.
        </p>
        <div className="flex flex-wrap justify-center gap-3">
          <Btn variant="primary" href={DL_WIN}><Download className="h-4 w-4" /> Windows · exe (~10 МБ)</Btn>
          <Btn href={DL_MAC}><Download className="h-4 w-4" /> macOS · arm64</Btn>
          <Btn href={DL_LIN}><Download className="h-4 w-4" /> Linux · x64</Btn>
          <Btn href={GH + "/releases"}><Tags className="h-4 w-4" /> Все релизы</Btn>
        </div>
      </div>
    </section>
  );
}

function Footer() {
  const a = "text-dim no-underline hover:text-txt";
  return (
    <footer className="mt-5 border-t border-line pb-11 pt-7">
      <div className="mx-auto flex max-w-[960px] flex-wrap items-center gap-[18px] px-5 font-mono text-[12.5px] text-dimmer">
        <span>Speed Test Tray · v1.0.0 · Windows / macOS / Linux</span>
        <a className={a} href={GH}>GitHub</a>
        <a className={a} href={GH + "/releases"}>Релизы</a>
        <a className={a} href={GH + "/issues"}>Issues</a>
        <a className={a} href="https://creativecommons.org/licenses/by-nc/4.0/deed.ru">CC BY-NC 4.0</a>
        <span className="ml-auto">написано на Go · React + Tailwind · графики — canvas</span>
      </div>
    </footer>
  );
}

export default function App() {
  return (
    <>
      <NetBackground />
      <Header />
      <main className="relative mx-auto max-w-[960px] px-5">
        <Hero />
        <section id="dashboard" className="py-16">
          <span className="mb-2.5 block font-mono text-[11px] font-semibold uppercase tracking-[1.6px] text-dimmer">
            Наш ответ — сразу к цифрам
          </span>
          <h2 className="mb-2 text-2xl font-semibold tracking-[-.3px]">Статистика вместо ощущений</h2>
          <p className="mb-7 max-w-[660px] leading-[1.65] text-dim">
            Speed Test Tray живёт в трее и собирает всё это сам. Дашборд поднимается на{" "}
            <code className="rounded-md border border-[#292930] bg-[#1c1c20] px-1.5 py-0.5 font-mono text-[12.5px] text-[#cfcfd4]">127.0.0.1:8787</code>{" "}
            и открывается из меню трея — только с вашего компьютера, наружу ничего не торчит. Ниже — как он выглядит
            (демо-данные).
          </p>
          <DashboardDemo />
          <Benefits />
        </section>
        <Pains />
        <Solution />
        <StartSteps />
        <FinalCTA />
        <Details />
      </main>
      <Footer />
    </>
  );
}
