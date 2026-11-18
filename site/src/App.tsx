import { NetBackground } from "@/components/NetBackground";
import { DashboardDemo, Benefits, Badges } from "@/components/DashboardDemo";
import { ScrollGlobe, type SGSection } from "@/components/ScrollGlobe";
import { Card } from "@/components/ui/card";

const GH = "https://github.com/testvaa/speed_test_checker";
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

const pains = [
  { title: "Видео буксует в самый интересный момент", description: "Кружок буферизации посреди фильма — классика. Но так ли плох канал в этот момент? Никто не знает: замера нет." },
  { title: "Пинг скакнул — и катка проиграна", description: "В играх важна не только скорость, но и задержка и её стабильность. Узнаёте о проблеме постфактум, по своим фрагам." },
  { title: "«До 100 Мбит/с» — только в тарифе", description: "Провайдер обещает максимум. Сколько реально приходит вечером, в час пик — проверить нечем, если только не вручную гонять спидтест." },
  { title: "VPN утром летает, к вечеру еле дышит", description: "Разные локации ведут себя по-разному в разное время суток. Какую включить сейчас — гадание на кофейной гуще." },
];

const solutionFeatures = [
  { title: "Замеры через Cloudflare", description: "Тот же API, что у speed.cloudflare.net: пинг без нагрузки и под нагрузкой, джиттер, потери пакетов, IP, провайдер и сервер." },
  { title: "Анализ сети с рекомендациями", description: "Диагностика за ~30 секунд: локальная сеть (шлюз, Wi-Fi, MTU), канал провайдера (пинг-профиль, потери), DNS против 1.1.1.1 / 8.8.8.8 / Яндекса / AliDNS и рост задержки под нагрузкой (bufferbloat). Итог — балл здоровья 0–100 и список конкретных рекомендаций; пороги метрик — по ITU-T Y.1541/G.1010 и практике bufferbloat.net." },
  { title: "Цветная иконка в трее", description: "Зелёная — хорошо, жёлтая — норма, красная — плохо. Границы настраиваются. Во время проверки иконка «оживает» свипом спидометра." },
  { title: "Монитор трафика", description: "Текущие скорости каждые 2 секунды, итоги за день и месяц, история ~3 месяца дней и ~2 года месяцев." },
  { title: "Аналитика локаций VPN", description: "Каждый замер запоминает город сервера и AS-сеть — видно, через какой VPN шёл трафик и какая локация быстрее." },
  { title: "ИИ-агент на локальной модели", description: "Чат на дашборде: «сделай замер», «следи за router.local каждые 5 минут», архивы и настройки — модель крутится на вашей машине, ничего не уходит наружу. Каждый запрос показывается карточкой «Ход запроса»: шаги, таймер, длительности." },
  { title: "Автообновление", description: "Приложение само проверяет новый релиз (при запуске и раз в сутки), скачивает бинарник с проверкой контрольной суммы и по кнопке перезапускается — без установщиков и ручных скачиваний. Переключается в трее и на дашборде." },
  { title: "Русский / английский", description: "Кнопка EN/RU на дашборде или пункт в трее — весь интерфейс, уведомления и даже ИИ-агент переключаются на лету. При первой установке язык берётся из системы." },
  { title: "Портативность", description: "Один бинарь (~10 МБ), без установки. Все данные — config.json, traffic.json, history.json — рядом с программой." },
  { title: "Консольные режимы", description: "-once — полный отчёт в консоль без трея, -netmon — диагностика монитора трафика." },
];

const Code = ({ children }: { children: React.ReactNode }) => (
  <code className="rounded-md border border-[#292930] bg-[#1c1c20] px-1.5 py-0.5 font-mono text-[12.5px] text-[#cfcfd4]">
    {children}
  </code>
);

const startFeatures = [
  {
    title: "1 · Скачайте и запустите",
    description: (
      <>Возьмите бинарь под свою ОС из последнего релиза: <Code>SpeedTestTray.exe</Code> (Windows),{" "}
        <Code>SpeedTestTray-macos-arm64</Code> (Apple Silicon) или <Code>SpeedTestTray-linux-x64</Code> — установки нет.
        На Linux сделайте файл исполняемым; если SmartScreen предупредит о неизвестном издателе: «Подробнее → Выполнить в любом случае».</>
    ),
  },
  {
    title: "2 · Первая проверка — сразу",
    description: "Иконка появится в трее, проверка выполнится автоматически. Цвет иконки — вердикт: зелёный / жёлтый / красный. Правый клик — полное меню: результаты, трафик, интервалы, настройки.",
  },
  {
    title: "3 · Откройте дашборд",
    description: (
      <>Меню трея → «🖥 Открыть дашборд» — или зайдите на <Code>127.0.0.1:8787</Code>. Живой график, история,
        аналитика локаций VPN и чат с ИИ-агентом уже собираются.</>
    ),
  },
];

const details: [string, React.ReactNode][] = [
  ["Платформы", "Windows x64 · Linux x64/arm64 · macOS arm64/x64 — готовые бинари в релизах"],
  ["Замеры", "speed.cloudflare.com: /meta, TTFB-пробы, __down/__up, оценка потерь; при ответе 429 автопроверки уходят в паузу с нарастающим интервалом (×3…×9, до 4 ч)"],
  ["Анализ сети", "шлюз и маршрут по умолчанию (GetBestRoute / ip route), Wi-Fi сигнал (netsh), пинг-профиль (RFC 3550 джиттер), системный DNS против 1.1.1.1 / 8.8.8.8 / 77.88.8.8 (Яндекс) / 223.5.5.5 (AliDNS), задержка под нагрузкой (шкала A–F)"],
  ["Кто потребляет интернет", "процессы и соединения с обратным DNS; поиск, сортировка и фильтры (активные, без loopback)"],
  ["Интервал проверок", "1 · 5 · 10 · 15 · 30 · 60 минут — из меню трея"],
  ["Объём теста", "загрузка 5–100 МБ, отдача 2–50 МБ — на ваш трафик"],
  ["Зоны и уведомления", "границы «плохо/хорошо»; тосты: все / только проблемы / выкл"],
  ["Монитор трафика", "счётчики сетевых интерфейсов (gopsutil), опрос раз в 2 с"],
  ["ИИ-агент", "локальная LFM3-1.7B через llama.cpp (Metal/Vulkan/CPU), инструменты: замер, наблюдение за хостами (ICMP), архив, настройки; ход запроса — карточкой со шагами"],
  ["Автообновление", "сверка с релизами GitHub (запуск + раз в сутки), скачивание с проверкой .sha256, подмена exe и перезапуск; тумблер в трее и на дашборде"],
  ["Язык интерфейса", "русский / английский — кнопка EN/RU на дашборде, Настройки → Язык в трее; по умолчанию — язык системы"],
  ["Дашборд", "встроенная страница (go:embed), HTTP только на 127.0.0.1:8787+"],
  ["Данные", "config.json · traffic.json · history.json — рядом с exe, переносятся копированием; история до 200 проверок"],
  ["Сборка из исходников", 'go build -trimpath -ldflags "-s -w -H=windowsgui" -o SpeedTestTray.exe . (Windows; Linux/macOS собирает CI)'],
  ["Лицензия", <a key="lic" className="text-[#7ce3b8] no-underline" href="https://creativecommons.org/licenses/by-nc/4.0/deed.ru">CC BY-NC 4.0</a>],
];

const DetailsTable = () => (
  <Card className="w-full text-left">
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
);

const sections: SGSection[] = [
  {
    id: "hero",
    badge: "вопрос на засыпку",
    title: "Насколько вы уверены",
    subtitle: "в интернете, которым пользуетесь?",
    align: "left",
    description: (
      <>Стриминг в 4K, облачные игры, видеозвонки, гигабайты обновлений — требования к каналу всё выше. Но действительно
        ли ваш интернет <b className="font-medium text-[#c9c9cf]">достаточно качественный</b> и соответствует тому, что вы
        от него ждёте — и за что платите провайдеру? Скорость в тарифе — это обещание. Реальность можно только{" "}
        <i className="not-italic text-ok">измерить</i>.</>
    ),
    actions: [
      { label: "Измерить свой канал", href: DL_WIN, variant: "primary" },
      { label: "Репозиторий на GitHub", href: GH },
    ],
    children: <Badges />,
  },
  {
    id: "dashboard",
    badge: "статистика",
    title: "Статистика вместо ощущений",
    align: "center",
    description: (
      <>Speed Test Tray живёт в трее и собирает всё это сам. Дашборд поднимается на <Code>127.0.0.1:8787</Code> и
        открывается из меню трея — только с вашего компьютера, наружу ничего не торчит. Ниже — как он выглядит
        (демо-данные).</>
    ),
    children: (
      <div className="space-y-8">
        <DashboardDemo />
        <Benefits />
      </div>
    ),
  },
  {
    id: "pains",
    badge: "знакомо?",
    title: "Когда интернет подводит, это всегда заметно",
    align: "right",
    description:
      "Проблема в том, что «кажется медленным» нельзя принести провайдеру — да и самому себе не докажешь. Ощущения — не аргумент. Аргумент — регулярные замеры и статистика.",
    features: pains,
  },
  {
    id: "solution",
    badge: "решение",
    title: "Speed Test Tray",
    subtitle: "контроль канала на автопилоте",
    align: "left",
    description:
      "Маленькая утилита для Windows, macOS и Linux живёт в трее и сама, по расписанию, отвечает на все вопросы выше — цифрами: регулярно проверяет канал через API Cloudflare, копит статистику локально, подсказывает лучшую локацию VPN и сообщает о проблемах уведомлениями.",
    features: solutionFeatures,
  },
  {
    id: "start",
    badge: "быстрый старт",
    title: "Три шага до первого замера",
    align: "center",
    features: startFeatures,
  },
  {
    id: "details",
    badge: "под капотом",
    title: "Технические детали и настройки",
    align: "left",
    children: <DetailsTable />,
  },
  {
    id: "download",
    badge: "скачать",
    title: "Чему верить: тарифу или факту",
    align: "center",
    description:
      "Скачайте Speed Test Tray — и уже через минуту у вас будет первый честный замер, а через неделю — статистика, с которой можно разговаривать с провайдером.",
    actions: [
      { label: "Windows · exe (~10 МБ)", href: DL_WIN, variant: "primary" },
      { label: "macOS · arm64", href: DL_MAC },
      { label: "Linux · x64", href: DL_LIN },
      { label: "Все релизы", href: GH + "/releases" },
    ],
  },
];

function Footer() {
  const a = "text-dim no-underline hover:text-txt";
  return (
    <footer className="relative z-20 mt-5 border-t border-line bg-base pb-11 pt-7">
      <div className="mx-auto flex max-w-[960px] flex-wrap items-center gap-[18px] px-5 font-mono text-[12.5px] text-dimmer">
        <span>Speed Test Tray · v1.5.0 · Windows / macOS / Linux · RU / EN</span>
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
      <main className="relative">
        <ScrollGlobe sections={sections} />
      </main>
      <Footer />
    </>
  );
}
