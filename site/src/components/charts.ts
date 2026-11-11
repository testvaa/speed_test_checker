/** Общая отрисовка «светящегося» временного ряда в стиле дашборда. */
export function dotPattern(ctx: CanvasRenderingContext2D) {
  const p = document.createElement("canvas");
  p.width = p.height = 7;
  const g = p.getContext("2d")!;
  g.fillStyle = "rgba(255,255,255,.22)";
  g.beginPath(); g.arc(1.5, 1.5, 1, 0, 7); g.fill();
  return ctx.createPattern(p, "repeat")!;
}

export function drawSeries(canvas: HTMLCanvasElement, data: number[], opts: { height: number; bad?: number }): number {
  const dpr = window.devicePixelRatio || 1;
  const W = canvas.parentElement?.clientWidth ?? 600;
  const h = opts.height;
  if (canvas.width !== Math.round(W * dpr) || canvas.height !== Math.round(h * dpr)) {
    canvas.width = Math.round(W * dpr); canvas.height = Math.round(h * dpr);
  }
  const ctx = canvas.getContext("2d")!;
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, W, h);
  const padL = 38, padR = 8, padT = 12, padB = 18;

  let maxV = 10;
  for (const v of data) if (v > maxV) maxV = v;
  if (opts.bad) maxV = Math.max(maxV, opts.bad * 1.1);
  maxV *= 1.15;
  const pow = Math.pow(10, Math.floor(Math.log10(maxV)));
  for (const m of [1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10]) if (m * pow >= maxV) { maxV = m * pow; break; }

  const X = (i: number) => padL + (i / (data.length - 1)) * (W - padL - padR);
  const Y = (v: number) => h - padB - (v / maxV) * (h - padT - padB);
  const mono = "10px ui-monospace,Consolas,monospace";
  const fmt = (v: number) => { const r = Math.round(v * 10) / 10; return r % 1 === 0 ? String(r) : r.toFixed(1); };

  ctx.font = mono;
  ctx.textAlign = "right"; ctx.textBaseline = "middle";
  for (let i = 0; i <= 4; i++) {
    const v = (maxV * i) / 4, y = Y(v);
    ctx.strokeStyle = "#1e1e23"; ctx.beginPath();
    ctx.moveTo(padL, y); ctx.lineTo(W - padR, y); ctx.stroke();
    ctx.fillStyle = "#67676d"; ctx.fillText(fmt(v), padL - 6, y);
  }
  if (opts.bad && opts.bad < maxV) {
    ctx.save(); ctx.setLineDash([4, 4]); ctx.strokeStyle = "rgba(248,113,113,.5)";
    ctx.beginPath(); ctx.moveTo(padL, Y(opts.bad)); ctx.lineTo(W - padR, Y(opts.bad)); ctx.stroke();
    ctx.restore();
  }
  ctx.save();
  ctx.beginPath();
  data.forEach((v, i) => (i ? ctx.lineTo(X(i), Y(v)) : ctx.moveTo(X(i), Y(v))));
  ctx.lineTo(X(data.length - 1), h - padB); ctx.lineTo(X(0), h - padB); ctx.closePath();
  ctx.fillStyle = dotPattern(ctx); ctx.fill();
  ctx.restore();
  ctx.save();
  ctx.shadowColor = "rgba(255,255,255,.8)"; ctx.shadowBlur = 9;
  ctx.strokeStyle = "#fff"; ctx.lineWidth = 1.7; ctx.lineJoin = "round";
  ctx.beginPath();
  data.forEach((v, i) => (i ? ctx.lineTo(X(i), Y(v)) : ctx.moveTo(X(i), Y(v))));
  ctx.stroke();
  ctx.restore();
  const lx = X(data.length - 1), ly = Y(data[data.length - 1]);
  ctx.save(); ctx.shadowColor = "#fff"; ctx.shadowBlur = 14; ctx.fillStyle = "#fff";
  ctx.beginPath(); ctx.arc(lx, ly, 3.2, 0, 7); ctx.fill(); ctx.restore();
  return Math.max(...data);
}
