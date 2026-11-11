import { useEffect, useMemo, useRef, useState } from "react";

import { cn } from "@/lib/utils";

export interface TypingSegment {
  text: string;
  className?: string;
}

interface TypingAnimationProps {
  text?: string;
  /** Раскрашенные части одного текста: печатаются как единое целое. */
  segments?: TypingSegment[];
  /** Задержка между символами, мс. */
  duration?: number;
  className?: string;
  /** Вызывается один раз, когда текст напечатан полностью. */
  onComplete?: () => void;
}

/**
 * Эффект печати для заголовков. Базовый API — `text`;
 * `segments` позволяет красить части текста (акценты), не ломая посимвольную печать.
 * При prefers-reduced-motion текст показывается сразу.
 */
export function TypingAnimation({
  text,
  segments,
  duration = 200,
  className,
  onComplete,
}: TypingAnimationProps) {
  const parts: TypingSegment[] = useMemo(
    () => segments ?? (text !== undefined ? [{ text }] : []),
    [segments, text]
  );
  const full = useMemo(() => parts.map((p) => p.text).join(""), [parts]);
  const [shown, setShown] = useState(0);

  const completeRef = useRef(onComplete);
  completeRef.current = onComplete;

  useEffect(() => {
    if (matchMedia("(prefers-reduced-motion: reduce)").matches) {
      setShown(full.length);
      completeRef.current?.();
      return;
    }
    setShown(0);
    let i = 0;
    const typingEffect = setInterval(() => {
      i += 1;
      setShown(i);
      if (i >= full.length) {
        clearInterval(typingEffect);
        completeRef.current?.();
      }
    }, duration);
    return () => clearInterval(typingEffect);
  }, [full, duration]);

  const done = shown >= full.length;
  let rest = shown;

  return (
    <h1
      className={cn(
        "font-display text-center text-4xl font-bold leading-[5rem] tracking-[-0.02em] drop-shadow-sm",
        className
      )}
    >
      {parts.map((p, idx) => {
        const take = Math.max(0, Math.min(p.text.length, rest));
        rest -= take;
        return (
          <span key={idx} className={p.className}>
            {p.text.substring(0, take)}
          </span>
        );
      })}
      {!done && (
        <span
          aria-hidden
          className="ml-0.5 inline-block h-[0.85em] w-[3px] translate-y-[0.1em] rounded-sm bg-ok align-baseline animate-caret"
        />
      )}
    </h1>
  );
}
