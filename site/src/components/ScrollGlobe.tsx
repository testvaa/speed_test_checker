import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { cn } from "@/lib/utils";
import { Globe } from "@/components/ui/globe";
import { Button } from "@/components/ui/button";

// ScrollGlobe: скролл-лендинг с «живым» глобусом на фоне. Глобус фиксирован
// и перелетает по точкам при смене активной секции; справа — точки-навигация,
// сверху — полоса прогресса. Порт 21st.dev ScrollGlobe на тему сайта.

export interface SGFeature {
  title: string;
  description: React.ReactNode;
}

export interface SGAction {
  label: string;
  href: string;
  variant?: "primary" | "default";
}

export interface SGSection {
  id: string;
  badge?: string;
  title: string;
  subtitle?: string;
  description?: React.ReactNode;
  align?: "left" | "center" | "right";
  features?: SGFeature[];
  actions?: SGAction[];
  children?: React.ReactNode;
}

export interface SGPosition {
  top: string;
  left: string;
  scale: number;
}

const defaultPositions: SGPosition[] = [
  { top: "42%", left: "76%", scale: 1.35 }, // герой: глобус справа от текста
  { top: "24%", left: "18%", scale: 0.9 }, // статистика: уходит за демо-дашборд
  { top: "50%", left: "16%", scale: 1.5 }, // боли: слева, текст справа
  { top: "28%", left: "84%", scale: 1.15 }, // решение: справа-сверху
  { top: "14%", left: "50%", scale: 0.85 }, // старт: сверху по центру
  { top: "72%", left: "14%", scale: 1.25 }, // детали: слева-снизу
  { top: "50%", left: "50%", scale: 1.8 }, // скачать: большой фон по центру
];

const parsePercent = (s: string) => parseFloat(s.replace("%", ""));

function transformFor(i: number, positions: { top: number; left: number; scale: number }[]) {
  const p = positions[Math.min(i, positions.length - 1)];
  return `translate3d(${p.left}vw, ${p.top}vh, 0) translate3d(-50%, -50%, 0) scale3d(${p.scale}, ${p.scale}, 1)`;
}

export function ScrollGlobe({
  sections,
  positions = defaultPositions,
  className,
}: {
  sections: SGSection[];
  positions?: SGPosition[];
  className?: string;
}) {
  const [activeSection, setActiveSection] = useState(0);
  const [scrollProgress, setScrollProgress] = useState(0);
  const [globeTransform, setGlobeTransform] = useState("");
  const sectionRefs = useRef<(HTMLElement | null)[]>([]);
  const animationFrameId = useRef(0);
  const navLabelTimeoutRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  const calculatedPositions = useMemo(
    () => positions.map((p) => ({ top: parsePercent(p.top), left: parsePercent(p.left), scale: p.scale })),
    [positions],
  );

  // активная секция = ближайшая к центру экрана; глобус летит к её точке
  const updateScrollPosition = useCallback(() => {
    const scrollTop = window.pageYOffset;
    const docHeight = document.documentElement.scrollHeight - window.innerHeight;
    setScrollProgress(Math.min(Math.max(docHeight > 0 ? scrollTop / docHeight : 0, 0), 1));

    const viewportCenter = window.innerHeight / 2;
    let newActiveSection = 0;
    let minDistance = Infinity;
    sectionRefs.current.forEach((ref, index) => {
      if (!ref) return;
      const rect = ref.getBoundingClientRect();
      // центр экрана внутри секции — она активна сразу (важно для секций
      // выше одного экрана: центр-в-центр там не доскроллить)
      const distance =
        rect.top <= viewportCenter && rect.bottom >= viewportCenter
          ? 0
          : Math.abs(rect.top + rect.height / 2 - viewportCenter);
      if (distance < minDistance) {
        minDistance = distance;
        newActiveSection = index;
      }
    });

    setGlobeTransform(transformFor(newActiveSection, calculatedPositions));
    setActiveSection(newActiveSection);
  }, [calculatedPositions]);

  // троттлинг скролла через requestAnimationFrame
  useEffect(() => {
    let ticking = false;
    const handleScroll = () => {
      if (!ticking) {
        animationFrameId.current = requestAnimationFrame(() => {
          updateScrollPosition();
          ticking = false;
        });
        ticking = true;
      }
    };
    window.addEventListener("scroll", handleScroll, { passive: true });
    updateScrollPosition();
    return () => {
      window.removeEventListener("scroll", handleScroll);
      cancelAnimationFrame(animationFrameId.current);
      if (navLabelTimeoutRef.current) clearTimeout(navLabelTimeoutRef.current);
    };
  }, [updateScrollPosition]);

  // стартовая позиция глобуса до первого скролла
  useEffect(() => {
    setGlobeTransform(transformFor(0, calculatedPositions));
  }, [calculatedPositions]);

  return (
    <div
      className={cn("relative w-full min-h-screen overflow-x-hidden bg-transparent text-txt", className)}
    >
      {/* полоса прогресса */}
      <div className="fixed top-0 left-0 z-[60] h-0.5 w-full bg-gradient-to-r from-line/20 via-line/40 to-line/20">
        <div
          className="h-full bg-gradient-to-r from-[#34d399] via-[#38bdf8] to-[#6b8aad] will-change-transform"
          style={{
            transform: `scaleX(${scrollProgress})`,
            transformOrigin: "left center",
            transition: "transform 0.15s ease-out",
            filter: "drop-shadow(0 0 2px rgba(56,189,248,0.35))",
          }}
        />
      </div>

      {/* точки-навигация справа с автоскрытием подписей (на узких экранах — нет) */}
      <div className="fixed top-1/2 right-2 z-40 hidden -translate-y-1/2 sm:right-4 sm:flex lg:right-8">
        <div className="space-y-3 sm:space-y-4 lg:space-y-6">
          {sections.map((section, index) => (
            <div key={section.id} className="group relative">
              <div
                className={cn(
                  "nav-label absolute top-1/2 right-5 -translate-y-1/2 rounded-md px-2 py-1 text-xs font-medium whitespace-nowrap",
                  "border border-line/60 bg-base/95 shadow-xl backdrop-blur-md sm:right-6 sm:px-3 sm:py-1.5 lg:right-8",
                  activeSection === index ? "animate-fade-out" : "opacity-0",
                )}
              >
                <div className="flex items-center gap-1.5">
                  <div className="h-1 w-1 animate-pulse rounded-full bg-[#34d399]" />
                  <span>{section.badge || `Секция ${index + 1}`}</span>
                </div>
              </div>
              <button
                onClick={() => {
                  sectionRefs.current[index]?.scrollIntoView({ behavior: "smooth", block: "center" });
                  // подпись активной точки не глушим раньше времени
                  if (navLabelTimeoutRef.current) clearTimeout(navLabelTimeoutRef.current);
                }}
                className={cn(
                  "relative h-2 w-2 rounded-full border-2 transition-all duration-300 hover:scale-125 sm:h-2.5 sm:w-2.5 lg:h-3 lg:w-3",
                  "before:absolute before:inset-0 before:rounded-full before:transition-all before:duration-300",
                  activeSection === index
                    ? "border-[#34d399] bg-[#34d399] shadow-lg before:animate-ping before:bg-[#34d399]/20"
                    : "border-dim/40 bg-transparent hover:border-[#34d399]/60 hover:bg-[#34d399]/10",
                )}
                aria-label={section.badge || `Секция ${index + 1}`}
              />
            </div>
          ))}
        </div>
        <div className="absolute top-0 bottom-0 left-1/2 -z-10 w-px -translate-x-1/2 bg-gradient-to-b from-transparent via-[#34d399]/20 to-transparent" />
      </div>

      {/* глобус: фиксирован, перелетает между секциями */}
      <div
        className="pointer-events-none fixed z-10 will-change-transform transition-all duration-[1400ms] ease-[cubic-bezier(0.23,1,0.32,1)] motion-reduce:transition-none"
        style={{
          transform: globeTransform,
          filter: `opacity(${activeSection === sections.length - 1 ? 0.4 : 0.85})`,
        }}
      >
        <div className="scale-75 sm:scale-90 lg:scale-100">
          <Globe />
        </div>
      </div>

      {/* секции */}
      {sections.map((section, index) => (
        <section
          key={section.id}
          id={section.id}
          ref={(el) => {
            sectionRefs.current[index] = el;
          }}
          className={cn(
            "relative z-20 flex min-h-screen w-full max-w-full flex-col justify-center overflow-hidden py-12 px-4 sm:py-16 sm:px-6 md:px-8 lg:px-12",
            section.align === "center" && "items-center text-center",
            section.align === "right" && "items-end text-right",
            (!section.align || section.align === "left") && "items-start text-left",
          )}
        >
          <div
            className={cn(
              "w-full max-w-sm will-change-transform sm:max-w-lg md:max-w-2xl lg:max-w-4xl xl:max-w-5xl",
            )}
          >
            <h1
              className={cn(
                "mb-6 font-bold leading-[1.1] tracking-tight sm:mb-8",
                index === 0
                  ? "text-3xl sm:text-4xl md:text-5xl lg:text-6xl xl:text-7xl"
                  : "text-2xl sm:text-3xl md:text-4xl lg:text-5xl",
              )}
            >
              {section.subtitle ? (
                <div className="space-y-1 sm:space-y-2">
                  <div className="bg-gradient-to-r from-txt to-txt/80 bg-clip-text text-transparent">
                    {section.title}
                  </div>
                  <div className="text-[0.6em] font-medium tracking-wider text-dim sm:text-[0.7em]">
                    {section.subtitle}
                  </div>
                </div>
              ) : (
                <div className="bg-gradient-to-r from-txt via-txt to-txt/80 bg-clip-text text-transparent">
                  {section.title}
                </div>
              )}
            </h1>

            {section.description && (
              <div
                className={cn(
                  "mb-8 text-[16px] leading-relaxed font-light text-dim sm:mb-10 sm:text-lg lg:text-xl",
                  section.align === "center" ? "mx-auto max-w-full text-center" : "max-w-full",
                )}
              >
                <p className="mb-3 sm:mb-4">{section.description}</p>
                {index === 0 && (
                  <div className="mt-4 flex flex-wrap items-center gap-3 text-xs text-dimmer sm:mt-6 sm:gap-4 sm:text-sm">
                    <div className="flex items-center gap-2">
                      <div className="h-1 w-1 animate-pulse rounded-full bg-[#34d399]" />
                      <span>живой глобус</span>
                    </div>
                    <div className="flex items-center gap-2">
                      <div
                        className="h-1 w-1 animate-pulse rounded-full bg-[#34d399]"
                        style={{ animationDelay: "0.5s" }}
                      />
                      <span>листайте вниз</span>
                    </div>
                  </div>
                )}
              </div>
            )}

            {section.features && section.features.length > 0 && (
              <div className="mb-8 grid gap-3 sm:mb-10 sm:gap-4">
                {section.features.map((feature) => (
                  <div
                    key={feature.title}
                    className={cn(
                      "group rounded-lg border border-line bg-panel/80 p-4 backdrop-blur-sm transition-all duration-300 sm:p-5 lg:p-6",
                      "hover:-translate-y-1 hover:border-[#34d399]/20 hover:bg-panel/80 hover:shadow-lg hover:shadow-[#34d399]/5",
                    )}
                  >
                    <div className="flex items-start gap-3 sm:gap-4">
                      <div className="mt-1.5 h-1.5 w-1.5 flex-none rounded-full bg-[#34d399]/60 transition-colors group-hover:bg-[#34d399] sm:mt-2 sm:h-2 sm:w-2" />
                      <div className="min-w-0 flex-1 space-y-1.5 sm:space-y-2">
                        <h3 className="text-[16px] font-semibold sm:text-lg">{feature.title}</h3>
                        <p className="text-sm leading-relaxed text-dim sm:text-[16px]">{feature.description}</p>
                      </div>
                    </div>
                  </div>
                ))}
              </div>
            )}

            {section.children}

            {section.actions && section.actions.length > 0 && (
              <div
                className={cn(
                  "flex flex-col flex-wrap gap-3 sm:flex-row sm:gap-4",
                  section.align === "center" && "justify-center",
                  section.align === "right" && "justify-end",
                  (!section.align || section.align === "left") && "justify-start",
                )}
              >
                {section.actions.map((action) => (
                  <Button key={action.label} href={action.href} variant={action.variant || "default"}>
                    {action.label}
                  </Button>
                ))}
              </div>
            )}
          </div>
        </section>
      ))}
    </div>
  );
}
