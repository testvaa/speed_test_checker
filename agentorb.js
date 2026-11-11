/* Орб-«человечек» ИИ-агента: порт движка LiveOrb (21st.dev) на vanilla JS.
   Чёрный вариант (тёмное тело, светлые глаза). Глаза следят за курсором,
   в покое косятся, иногда моргает. Без WebGL — страница показывает CSS-фолбэк. */
"use strict";

const ORB_BODY = "#18181b";
const ORB_EYE = "#f4f4f5";

function orbHexToRgb(hex) {
  const h = hex.replace("#", "").trim();
  const n = Number.parseInt(h.length === 3 ? h.split("").map(c => c + c).join("") : h.padEnd(6, "0").slice(0, 6), 16);
  if (Number.isNaN(n)) return [0.96, 0.96, 0.96];
  return [((n >> 16) & 255) / 255, ((n >> 8) & 255) / 255, (n & 255) / 255];
}

function orbCompile(gl, type, source) {
  const sh = gl.createShader(type);
  gl.shaderSource(sh, source);
  gl.compileShader(sh);
  if (!gl.getShaderParameter(sh, gl.COMPILE_STATUS)) {
    console.warn("orb: shader", gl.getShaderInfoLog(sh));
    gl.deleteShader(sh);
    return null;
  }
  return sh;
}

const ORB_VERT = `
attribute vec2 a_position;
void main() {
  gl_Position = vec4(a_position, 0.0, 1.0);
}
`;

const ORB_FRAG = `
precision highp float;

uniform vec2 u_resolution;
uniform float u_time;
uniform vec2 u_look;
uniform float u_blink;
uniform vec3 u_body;
uniform vec3 u_eye;

float hash(vec2 p) {
  return fract(sin(dot(p, vec2(127.1, 311.7))) * 43758.5453123);
}

float noise(vec2 p) {
  vec2 i = floor(p);
  vec2 f = fract(p);
  float a = hash(i);
  float b = hash(i + vec2(1.0, 0.0));
  float c = hash(i + vec2(0.0, 1.0));
  float d = hash(i + vec2(1.0, 1.0));
  vec2 u = f * f * (3.0 - 2.0 * f);
  return mix(a, b, u.x) + (c - a) * u.y * (1.0 - u.x) + (d - b) * u.x * u.y;
}

float fbm(vec2 p) {
  float v = 0.0;
  float a = 0.5;
  mat2 m = mat2(1.6, 1.2, -1.2, 1.6);
  for (int i = 0; i < 5; i++) {
    v += a * noise(p);
    p = m * p;
    a *= 0.5;
  }
  return v;
}

vec3 orient(vec3 p, vec2 look) {
  float yaw = look.x * 0.92;
  float pitch = -look.y * 0.78;
  float cy = cos(yaw);
  float sy = sin(yaw);
  float cp = cos(pitch);
  float sp = sin(pitch);
  vec3 q = vec3(p.x, p.y * cp - p.z * sp, p.y * sp + p.z * cp);
  return vec3(q.x * cy + q.z * sy, q.y, -q.x * sy + q.z * cy);
}

float eyeMask(vec3 n, vec3 e, vec3 right, vec3 up, float halfH, float rad) {
  float facing = dot(n, e);
  float x = dot(n, right) - dot(e, right);
  float y = dot(n, up) - dot(e, up);
  y -= clamp(y, -halfH, halfH);
  float d = length(vec2(x, y)) - rad;
  float fill = 1.0 - smoothstep(-0.01, 0.01, d);
  return fill * smoothstep(0.12, 0.32, facing);
}

void main() {
  vec2 uv = (gl_FragCoord.xy / u_resolution.xy) * 2.0 - 1.0;
  float aspect = u_resolution.x / max(u_resolution.y, 1.0);
  uv.x *= aspect;

  float radius = 0.94;
  vec2 p = uv / radius;
  float r2 = dot(p, p);
  float edge = 1.0 - smoothstep(0.985, 1.012, sqrt(max(r2, 0.0)));
  if (edge <= 0.001) {
    gl_FragColor = vec4(0.0);
    return;
  }

  float z = sqrt(max(1.0 - r2, 0.0));
  vec3 n = normalize(vec3(p, z));

  vec2 look = u_look;
  float lm = length(look);
  if (lm > 1.0) look /= lm;

  vec3 right = orient(vec3(1.0, 0.0, 0.0), look);
  vec3 up = orient(vec3(0.0, 1.0, 0.0), look);
  vec3 eL = orient(normalize(vec3(-0.32, 0.08, 1.0)), look);
  vec3 eR = orient(normalize(vec3(0.32, 0.08, 1.0)), look);

  float halfH = mix(0.128, 0.012, u_blink);
  float rad = mix(0.054, 0.062, u_blink);
  float eyes = max(
    eyeMask(n, eL, right, up, halfH, rad),
    eyeMask(n, eR, right, up, halfH, rad)
  );

  vec3 col = mix(u_body, u_eye, clamp(eyes, 0.0, 1.0));

  gl_FragColor = vec4(clamp(col, 0.0, 1.0), edge);
}
`;

const ORB_IDLE_MS = 3000;
const ORB_REST = { x: 0, y: 0.08 };

/**
 * createOrb(canvas) — возвращает {destroy} или null, если WebGL недоступен
 * (тогда страница оставляет CSS-фолбэк с двумя глазами).
 */
function createOrb(canvas) {
  const gl = canvas.getContext("webgl", {
    alpha: true, antialias: false, depth: false, stencil: false,
    premultipliedAlpha: false, powerPreference: "high-performance",
  });
  if (!gl) return null;

  const look = { x: 0, y: 0.08 };
  const targetLook = { x: 0, y: 0.08 };
  const glance = { x: 0, y: 0.08 };

  let vs = orbCompile(gl, gl.VERTEX_SHADER, ORB_VERT);
  let fs = orbCompile(gl, gl.FRAGMENT_SHADER, ORB_FRAG);
  if (!vs || !fs) return null;
  const prog = gl.createProgram();
  gl.attachShader(prog, vs); gl.attachShader(prog, fs);
  gl.linkProgram(prog);
  if (!gl.getProgramParameter(prog, gl.LINK_STATUS)) return null;
  gl.useProgram(prog);

  const buf = gl.createBuffer();
  gl.bindBuffer(gl.ARRAY_BUFFER, buf);
  gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 1, -1, -1, 1, -1, 1, 1, -1, 1, 1]), gl.STATIC_DRAW);
  const loc = gl.getAttribLocation(prog, "a_position");
  gl.enableVertexAttribArray(loc);
  gl.vertexAttribPointer(loc, 2, gl.FLOAT, false, 0, 0);

  const u = (name) => gl.getUniformLocation(prog, name);
  const uResolution = u("u_resolution"), uTime = u("u_time"), uLook = u("u_look"),
    uBlink = u("u_blink"), uBody = u("u_body"), uEye = u("u_eye");

  gl.enable(gl.BLEND);
  gl.blendFunc(gl.SRC_ALPHA, gl.ONE_MINUS_SRC_ALPHA);

  let raf = 0, destroyed = false, lost = false, onScreen = true;
  let last = 0, clock = 0;
  let nextBlink = 1800 + Math.random() * 2400;
  let blinkAt = -10000;
  let nextGlance = 600 + Math.random() * 900;
  let pointerAt = -Infinity;
  const bodyRGB = orbHexToRgb(ORB_BODY);
  const eyeRGB = orbHexToRgb(ORB_EYE);

  const resize = () => {
    const parent = canvas.parentElement;
    if (!parent) return false;
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    const w = parent.clientWidth, h = parent.clientHeight;
    if (w <= 0 || h <= 0) return false;
    const width = Math.max(1, Math.floor(w * dpr));
    const height = Math.max(1, Math.floor(h * dpr));
    canvas.style.width = w + "px";
    canvas.style.height = h + "px";
    if (width === canvas.width && height === canvas.height) return false;
    canvas.width = width;
    canvas.height = height;
    gl.viewport(0, 0, canvas.width, canvas.height);
    return true;
  };

  const mqReduce = window.matchMedia("(prefers-reduced-motion: reduce)");
  const moving = () => onScreen && !mqReduce.matches && document.visibilityState !== "hidden";

  function wake() {
    if (raf || destroyed || lost) return;
    last = performance.now();
    raf = requestAnimationFrame(tick);
  }

  function redraw() {
    if (destroyed || lost) return;
    cancelAnimationFrame(raf);
    raf = 0;
    last = performance.now();
    tick(last);
  }

  const onMove = (e) => {
    const parent = canvas.parentElement;
    if (!parent) return;
    const rect = parent.getBoundingClientRect();
    if (rect.width <= 0 || rect.height <= 0) return;
    const dx = (e.clientX - (rect.left + rect.width / 2)) / (rect.width / 2);
    const dy = (rect.top + rect.height / 2 - e.clientY) / (rect.height / 2);
    targetLook.x = Math.min(1, Math.max(-1, dx));
    targetLook.y = Math.min(1, Math.max(-1, dy));
    pointerAt = performance.now();
    if (onScreen) wake();
  };
  window.addEventListener("pointermove", onMove, { passive: true });

  const tick = (now) => {
    raf = 0;
    if (lost) return;
    const dt = Math.min(50, Math.max(0, now - last));
    last = now;
    const live = moving();
    if (live) clock += dt;
    const reduce = mqReduce.matches;

    const steering = now - pointerAt < ORB_IDLE_MS;
    if (!steering && live && clock >= nextGlance) {
      const back = Math.random() < 0.3;
      glance.x = back ? 0 : (Math.random() * 2 - 1) * 0.8;
      glance.y = back ? 0.08 : (Math.random() * 2 - 1) * 0.5;
      nextGlance = clock + 900 + Math.random() * 2100;
    }
    const goal = steering ? targetLook : (reduce ? ORB_REST : glance);
    const ease = live ? (steering ? 0.16 : 0.1) : 1;
    look.x += (goal.x - look.x) * ease;
    look.y += (goal.y - look.y) * ease;

    if (!reduce && clock >= nextBlink) {
      blinkAt = clock;
      nextBlink = clock + 2200 + Math.random() * 3800;
    }
    const bt = (clock - blinkAt) / 1000;
    let b = 0;
    if (bt < 0.055) b = bt / 0.055;
    else if (bt < 0.1) b = 1;
    else if (bt < 0.18) b = 1 - (bt - 0.1) / 0.08;

    gl.clearColor(0, 0, 0, 0);
    gl.clear(gl.COLOR_BUFFER_BIT);
    gl.uniform2f(uResolution, canvas.width, canvas.height);
    gl.uniform1f(uTime, reduce ? 0 : clock / 1000);
    gl.uniform2f(uLook, look.x, look.y);
    gl.uniform1f(uBlink, b);
    gl.uniform3f(uBody, bodyRGB[0], bodyRGB[1], bodyRGB[2]);
    gl.uniform3f(uEye, eyeRGB[0], eyeRGB[1], eyeRGB[2]);
    gl.drawArrays(gl.TRIANGLES, 0, 6);
    if (live && !destroyed) raf = requestAnimationFrame(tick);
  };

  const ro = new ResizeObserver(() => { if (resize()) redraw(); });
  if (canvas.parentElement) ro.observe(canvas.parentElement);
  const io = new IntersectionObserver((entries) => {
    onScreen = entries[0] ? entries[0].isIntersecting : true;
    wake();
  });
  io.observe(canvas);
  const onVis = () => wake();
  document.addEventListener("visibilitychange", onVis);
  const onReduce = () => wake();
  mqReduce.addEventListener("change", onReduce);

  const onLost = (e) => {
    e.preventDefault();
    lost = true;
    cancelAnimationFrame(raf);
    raf = 0;
    canvas.style.visibility = "hidden";
  };
  const onRestored = () => {
    if (destroyed) return;
    canvas.style.visibility = "";
    lost = false;
    wake();
  };
  canvas.addEventListener("webglcontextlost", onLost);
  canvas.addEventListener("webglcontextrestored", onRestored);

  resize();
  wake();

  return {
    destroy() {
      destroyed = true;
      cancelAnimationFrame(raf);
      ro.disconnect();
      io.disconnect();
      document.removeEventListener("visibilitychange", onVis);
      mqReduce.removeEventListener("change", onReduce);
      window.removeEventListener("pointermove", onMove);
      canvas.removeEventListener("webglcontextlost", onLost);
      canvas.removeEventListener("webglcontextrestored", onRestored);
    },
  };
}
