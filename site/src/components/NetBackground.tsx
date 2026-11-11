import { useEffect, useRef } from "react";

type Node = { x: number; y: number; vx: number; vy: number; r: number; anchor?: "A" | "B" };
type Packet = { from: Node; to: Node; t: number; speed: number; color: string; back: boolean; dead?: boolean };
type Ripple = { x: number; y: number; r: number; a: number; dead?: boolean };

/** Фоновая сеть: дрейфующие узлы, блуждающие пакеты и «пинг» A↔B с рябью отклика. */
export function NetBackground() {
  const ref = useRef<HTMLCanvasElement>(null);

  useEffect(() => {
    const cv = ref.current!;
    const ctx = cv.getContext("2d")!;
    let W = 0, H = 0, frame = 0;
    let nodes: Node[] = [], packets: Packet[] = [], ripples: Ripple[] = [];
    const LINK = 190;

    const size = () => {
      const dpr = Math.min(window.devicePixelRatio || 1, 2);
      W = innerWidth; H = innerHeight;
      cv.width = Math.round(W * dpr); cv.height = Math.round(H * dpr);
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    };
    const seed = () => {
      nodes = [];
      const n = Math.max(26, Math.min(60, Math.round((W * H) / 26000)));
      for (let i = 0; i < n; i++)
        nodes.push({
          x: Math.random() * W, y: Math.random() * H,
          vx: (Math.random() - 0.5) * 0.18, vy: (Math.random() - 0.5) * 0.18,
          r: Math.random() * 1.3 + 0.9,
        });
      nodes.push({ x: W * 0.16, y: H * 0.24, vx: 0, vy: 0, r: 3.4, anchor: "A" });
      nodes.push({ x: W * 0.84, y: H * 0.68, vx: 0, vy: 0, r: 3.4, anchor: "B" });
    };
    const near = (a: Node, b: Node) => {
      const d = Math.hypot(a.x - b.x, a.y - b.y);
      return d < LINK ? d : Infinity;
    };
    const spawnPing = () => {
      const a = nodes.find((n) => n.anchor === "A")!;
      const b = nodes.find((n) => n.anchor === "B")!;
      packets.push({ from: a, to: b, t: 0, speed: 0.006 + Math.random() * 0.004, color: "#7ce3b8", back: true });
    };
    const spawnWander = () => {
      const from = nodes[Math.floor(Math.random() * nodes.length)];
      const nbrs = nodes.filter((n) => n !== from && near(from, n) < Infinity);
      if (!nbrs.length) return;
      const to = nbrs[Math.floor(Math.random() * nbrs.length)];
      packets.push({ from, to, t: 0, speed: 0.01 + Math.random() * 0.012, color: "rgba(190,205,230,.9)", back: false });
    };
    const draw = () => {
      ctx.clearRect(0, 0, W, H);
      for (const n of nodes) {
        if (n.anchor) continue;
        n.x += n.vx; n.y += n.vy;
        if (n.x < -20) n.x = W + 20; if (n.x > W + 20) n.x = -20;
        if (n.y < -20) n.y = H + 20; if (n.y > H + 20) n.y = -20;
      }
      ctx.lineWidth = 1;
      for (let i = 0; i < nodes.length; i++)
        for (let j = i + 1; j < nodes.length; j++) {
          const d = near(nodes[i], nodes[j]);
          if (d === Infinity) continue;
          ctx.strokeStyle = "rgba(120,140,175," + (0.1 * (1 - d / LINK)).toFixed(3) + ")";
          ctx.beginPath();
          ctx.moveTo(nodes[i].x, nodes[i].y); ctx.lineTo(nodes[j].x, nodes[j].y);
          ctx.stroke();
        }
      for (const n of nodes) {
        if (n.anchor) {
          ctx.save();
          ctx.shadowColor = "#7ce3b8"; ctx.shadowBlur = 12;
          ctx.fillStyle = "rgba(124,227,184,.85)";
          ctx.beginPath(); ctx.arc(n.x, n.y, n.r, 0, 7); ctx.fill();
          ctx.restore();
          ctx.fillStyle = "rgba(160,180,205,.55)";
          ctx.font = "10px ui-monospace,Consolas,monospace";
          ctx.fillText(n.anchor, n.x + 7, n.y - 6);
        } else {
          ctx.fillStyle = "rgba(150,165,195,.30)";
          ctx.beginPath(); ctx.arc(n.x, n.y, n.r, 0, 7); ctx.fill();
        }
      }
      for (const p of packets) {
        p.t += p.speed;
        if (p.t >= 1) {
          ripples.push({ x: p.to.x, y: p.to.y, r: 2, a: 0.5 });
          if (p.back) {
            p.back = false;
            const f = p.from; p.from = p.to; p.to = f; p.t = 0;
            p.color = "rgba(255,255,255,.95)";
          } else p.dead = true;
        }
        const t = p.t < 0.5 ? 2 * p.t * p.t : 1 - Math.pow(-2 * p.t + 2, 2) / 2;
        const x = p.from.x + (p.to.x - p.from.x) * t;
        const y = p.from.y + (p.to.y - p.from.y) * t;
        ctx.save();
        ctx.shadowColor = p.color; ctx.shadowBlur = 9;
        ctx.fillStyle = p.color;
        ctx.beginPath(); ctx.arc(x, y, 1.9, 0, 7); ctx.fill();
        ctx.restore();
      }
      packets = packets.filter((p) => !p.dead);
      for (const r of ripples) {
        r.r += 0.9; r.a -= 0.016;
        if (r.a <= 0) { r.dead = true; continue; }
        ctx.strokeStyle = "rgba(124,227,184," + r.a.toFixed(3) + ")";
        ctx.beginPath(); ctx.arc(r.x, r.y, r.r, 0, 7); ctx.stroke();
      }
      ripples = ripples.filter((r) => !r.dead);
    };
    const loop = () => {
      draw();
      if (++frame % 46 === 0) spawnWander();
      if (frame % 300 === 1) spawnPing();
      timer = window.setTimeout(loop, 33);
    };
    let timer = 0;

    size(); seed();
    addEventListener("resize", () => { size(); seed(); });
    if (matchMedia("(prefers-reduced-motion: reduce)").matches) draw();
    else loop();
    return () => { clearTimeout(timer); removeEventListener("resize", size); };
  }, []);

  return <canvas id="netbg" ref={ref} aria-hidden className="pointer-events-none fixed inset-0 -z-10 h-full w-full" />;
}
