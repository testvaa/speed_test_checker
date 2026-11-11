import { useEffect, useRef, useState } from "react";
import { drawSeries } from "./charts";

/** Hero-график: живая симуляция «простой → проверка → пик» с панелью прогресса. */
export function LiveChart() {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const [probe, setProbe] = useState<{ show: boolean; text: string; frac: number }>({
    show: false, text: "", frac: 0,
  });
  const peakRef = useRef(0);
  const [peak, setPeak] = useState("…");

  useEffect(() => {
    const c = canvasRef.current!;
    const N = 140;
    let data: number[] = [], v = 22, phase: "idle" | "check" = "check", phaseT = 0;
    let peakText = 0;
    for (let i = 0; i < N; i++) { v = Math.max(1, v + (Math.random() - 0.5) * 6); data.push(v); }

    const next = () => {
      phaseT++;
      if (phase === "idle") {
        v = Math.max(0.8, Math.min(30, v + (Math.random() - 0.5) * 7));
        data.push(+v.toFixed(1)); data.shift();
        if (phaseT > 170) { phase = "check"; phaseT = 0; }
        setProbe({ show: false, text: "", frac: 0 });
      } else {
        const frac = Math.min(1, phaseT / 55);
        const ramp = frac < 0.15 ? frac / 0.15 : 1 - Math.max(0, (frac - 0.8) / 0.2) * 0.9;
        v = 8 + 105 * ramp * (0.85 + 0.15 * Math.sin(phaseT / 6));
        data.push(+v.toFixed(1)); data.shift();
        setProbe({ show: true, text: "Проверка · загрузка " + Math.round(frac * 100) + "%", frac });
        if (frac >= 1) { phase = "idle"; phaseT = 0; }
      }
      const peak = drawSeries(c, data, { height: 190, bad: 10 });
      if (peak > peakText) { peakText = peak; setPeak(String(Math.round(peak))); }
    };

    if (matchMedia("(prefers-reduced-motion: reduce)").matches) {
      drawSeries(c, data, { height: 190, bad: 10 });
      setPeak(String(Math.round(Math.max(...data))));
      return;
    }
    const t = setInterval(next, 480);
    return () => clearInterval(t);
  }, []);

  return (
    <div className="relative mt-11 rounded-[14px] border border-line bg-panel px-[18px] pb-[10px] pt-4">
      <div className="mb-2 flex items-baseline gap-2.5">
        <span className="text-[13px] text-dim">Так это выглядит: скорость за сессию</span>
        <span className="ml-auto font-mono text-[12.5px] text-dimmer">
          пик <i className="font-serif text-sm italic text-[#c9c9cf]">{peak}</i> Мбит/с
        </span>
      </div>
      <canvas ref={canvasRef} className="block h-[190px] w-full" />
      {probe.show && (
        <div
          className="absolute right-3.5 top-3 min-w-[180px] rounded-[10px] border border-dashed border-[#3a3a41] px-3 py-[9px] font-mono text-xs text-[#cfcfd4]"
          style={{
            backgroundColor: "rgba(15,15,17,.94)",
            backgroundImage: "radial-gradient(rgba(255,255,255,.10) 1px, transparent 1.4px)",
            backgroundSize: "7px 7px",
          }}
        >
          <div className="mb-[7px] flex items-center gap-[7px]">
            <span>✎</span>
            <span>{probe.text}</span>
          </div>
          <div className="h-[3px] overflow-hidden rounded-[2px] bg-[#26262c]">
            <i
              className="block h-full bg-white shadow-[0_0_8px_rgba(255,255,255,.7)] transition-[width] duration-[400ms]"
              style={{ width: Math.round(probe.frac * 100) + "%" }}
            />
          </div>
        </div>
      )}
    </div>
  );
}

/** Статичный демо-график «сессии с двумя проверками». */
export function SessionChart() {
  const ref = useRef<HTMLCanvasElement>(null);
  useEffect(() => {
    const c = ref.current!;
    const data: number[] = [];
    let v = 14;
    for (let i = 0; i < 120; i++) {
      if (i > 18 && i < 34) v = 92 + 18 * Math.sin((i - 18) / 4);
      else if (i > 70 && i < 86) v = 76 + 22 * Math.sin((i - 70) / 4);
      else v = Math.max(1, Math.min(24, v + (Math.random() - 0.5) * 8));
      data.push(+v.toFixed(1));
    }
    const render = () => drawSeries(c, data, { height: 170, bad: 10 });
    render();
    addEventListener("resize", render);
    return () => removeEventListener("resize", render);
  }, []);
  return <canvas ref={ref} className="block h-[170px] w-full" />;
}

/** График «скорость по часам суток»: X — час 0..24, Y — Мбит/с (как в дашборде). */
export function HoursChart({ avg, cnt, bad }: { avg: number[]; cnt: number[]; bad: number }) {
  const ref = useRef<HTMLCanvasElement>(null);
  useEffect(() => {
    const c = ref.current!;
    const dpr = window.devicePixelRatio || 1;
    const W = c.parentElement?.clientWidth ?? 600, H = 230;
    c.width = Math.round(W * dpr); c.height = Math.round(H * dpr);
    c.style.height = H + "px";
    const ctx = c.getContext("2d")!;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, W, H);
    const padL = 40, padR = 12, padT = 14, padB = 26;
    const iw = W - padL - padR, ih = H - padT - padB;
    const mono = "ui-monospace,Consolas,monospace";

    let maxA = 10;
    for (const a of avg) if (a > maxA) maxA = a;
    if (bad) maxA = Math.max(maxA, bad * 1.08);
    maxA *= 1.15;
    const pow = Math.pow(10, Math.floor(Math.log10(maxA)));
    for (const m of [1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10]) if (m * pow >= maxA) { maxA = m * pow; break; }

    const X = (h: number) => padL + ((h + 0.5) / 24) * iw;
    const Y = (v: number) => padT + ih - (v / maxA) * ih;

    const nowH = new Date().getHours();
    ctx.fillStyle = "rgba(255,255,255,.04)";
    ctx.fillRect(padL + (nowH / 24) * iw, padT, iw / 24, ih);

    ctx.font = "10px " + mono;
    for (let h = 0; h <= 24; h++) {
      const x = padL + (h / 24) * iw;
      ctx.strokeStyle = h % 3 === 0 ? "#1f1f24" : "#17171b";
      ctx.beginPath(); ctx.moveTo(x, padT); ctx.lineTo(x, padT + ih); ctx.stroke();
      if (h % 3 === 0 && h < 24) {
        ctx.fillStyle = "#67676d"; ctx.textAlign = "center"; ctx.textBaseline = "alphabetic";
        ctx.fillText(String(h).padStart(2, "0"), X(h), H - 8);
      }
    }
    ctx.textAlign = "right"; ctx.textBaseline = "middle";
    for (let i = 0; i <= 4; i++) {
      const v = (maxA * i) / 4, y = Y(v);
      ctx.strokeStyle = "#1e1e23"; ctx.beginPath();
      ctx.moveTo(padL, y); ctx.lineTo(W - padR, y); ctx.stroke();
      ctx.fillStyle = "#67676d";
      const r = Math.round(v * 10) / 10;
      ctx.fillText(r % 1 === 0 ? String(r) : r.toFixed(1), padL - 6, y);
    }
    if (bad && bad < maxA) {
      ctx.save(); ctx.setLineDash([4, 4]); ctx.strokeStyle = "rgba(248,113,113,.5)";
      ctx.beginPath(); ctx.moveTo(padL, Y(bad)); ctx.lineTo(W - padR, Y(bad)); ctx.stroke();
      ctx.restore();
    }
    ctx.save();
    ctx.strokeStyle = "#fff"; ctx.lineWidth = 1.8; ctx.lineJoin = "round";
    ctx.shadowColor = "rgba(255,255,255,.7)"; ctx.shadowBlur = 8;
    ctx.beginPath();
    let pen = false;
    for (let h = 0; h < 24; h++) {
      if (!cnt[h]) { pen = false; continue; }
      const x = X(h), y = Y(avg[h]);
      if (pen) ctx.lineTo(x, y); else { ctx.moveTo(x, y); pen = true; }
    }
    ctx.stroke();
    ctx.restore();
    for (let h = 0; h < 24; h++) {
      if (!cnt[h]) continue;
      const v = avg[h];
      const col = v >= 50 ? "#34d399" : v >= 25 ? "#fbbf24" : "#f87171";
      ctx.save();
      ctx.shadowColor = col; ctx.shadowBlur = 7;
      ctx.fillStyle = col;
      ctx.beginPath(); ctx.arc(X(h), Y(v), 3.2, 0, Math.PI * 2); ctx.fill();
      ctx.restore();
    }
  }, [avg, cnt, bad]);
  return <canvas ref={ref} className="block h-[230px] w-full" />;
}
