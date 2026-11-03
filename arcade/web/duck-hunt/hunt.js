// Duck Hunt's phone game: a landscape canvas in an 8-bit style. Each flying duck is a real
// kagent session the server woke for this run (a Substrate Resume); tapping one sends the
// hit, and the server suspends it (a real snapshot) and scores it. Everything is drawn here
// as original pixel art, generated from simple shapes at a low resolution and scaled up by a
// whole number so the pixels stay crisp; the score, the combo and the timings come from the
// server.
"use strict";

// ---- palettes and kinds ----
const PAL = {
  green: { head: "#1fa838", body: "#3a3a3a", wing: "#161616", belly: "#c8975e", ring: "#ffffff" },
  blue: { head: "#3a6cff", body: "#6a3ad0", wing: "#33178a", belly: "#cdbcff", ring: "#ffffff" },
  red: { head: "#e83a3a", body: "#8a2a1a", wing: "#4a1208", belly: "#f4a46c", ring: "#ffffff" },
  gold: { head: "#ffe04a", body: "#e8a800", wing: "#9c6a00", belly: "#fff3b8", ring: "#ffffff" },
};
export const KINDS = {
  agent: { pals: ["green", "blue"], speed: 1, size: 1, label: "", color: "#ffffff" },
  swift: { pals: ["red"], speed: 1.5, size: 0.9, label: "SWIFT +150", color: "#fc9838" },
  golden: { pals: ["gold"], speed: 1.8, size: 1, label: "GOLDEN +500", color: "#ffe04a" },
};
export const mult = (c) => 1 + Math.min(4, Math.max(0, c - 1) >> 1);
export const perRun = (wave) => Math.min(3, 1 + ((wave - 1) >> 1));
export const SKY = "#3cbcfc", SKY_PINK = "#fcb8b0";
export const FONT = '"Press Start 2P", ui-monospace, monospace';

const rnd = (a, b) => a + Math.random() * (b - a);
const clamp = (v, a, b) => Math.max(a, Math.min(b, v));

// ---- pixel art: paint a grid from shapes, outline it in black, turn it into a canvas ----
function grid(w, h) {
  const g = { w, h, c: new Array(w * h).fill(null), bare: new Set() };
  g.px = (x, y, col) => { x |= 0; y |= 0; if (x >= 0 && y >= 0 && x < w && y < h) g.c[y * w + x] = col; };
  g.at = (x, y) => (x >= 0 && y >= 0 && x < w && y < h ? g.c[y * w + x] : null);
  g.rect = (x0, y0, x1, y1, col) => { for (let y = y0; y <= y1; y++) for (let x = x0; x <= x1; x++) g.px(x, y, col); };
  g.ellipse = (cx, cy, rx, ry, col, keep) => {
    for (let y = Math.floor(cy - ry); y <= Math.ceil(cy + ry); y++)
      for (let x = Math.floor(cx - rx); x <= Math.ceil(cx + rx); x++) {
        const dx = (x + 0.5 - cx) / rx, dy = (y + 0.5 - cy) / ry;
        if (dx * dx + dy * dy <= 1 && (!keep || keep(x, y))) g.px(x, y, typeof col === "function" ? col(x, y) : col);
      }
  };
  g.poly = (pts, col) => {
    const xs = pts.map((p) => p[0]), ys = pts.map((p) => p[1]);
    for (let y = Math.floor(Math.min(...ys)); y <= Math.ceil(Math.max(...ys)); y++)
      for (let x = Math.floor(Math.min(...xs)); x <= Math.ceil(Math.max(...xs)); x++) {
        let inside = false;
        for (let i = 0, j = pts.length - 1; i < pts.length; j = i++) {
          const [xi, yi] = pts[i], [xj, yj] = pts[j], px = x + 0.5, py = y + 0.5;
          if (yi > py !== yj > py && px < ((xj - xi) * (py - yi)) / (yj - yi) + xi) inside = !inside;
        }
        if (inside) g.px(x, y, col);
      }
  };
  // outline: black around everything painted (but not inside it).
  g.outline = (col = "#000000") => {
    const add = [];
    for (let y = 0; y < h; y++) for (let x = 0; x < w; x++) {
      if (g.at(x, y)) continue;
      if (g.at(x - 1, y) || g.at(x + 1, y) || g.at(x, y - 1) || g.at(x, y + 1)) add.push([x, y]);
    }
    for (const [x, y] of add) g.px(x, y, col);
  };
  g.canvas = () => {
    const c = document.createElement("canvas");
    c.width = w; c.height = h;
    const x = c.getContext("2d"), img = x.createImageData(w, h);
    for (let i = 0; i < w * h; i++) {
      const col = g.c[i];
      if (!col) continue;
      const n = parseInt(col.slice(1), 16);
      img.data[i * 4] = n >> 16; img.data[i * 4 + 1] = (n >> 8) & 255; img.data[i * 4 + 2] = n & 255; img.data[i * 4 + 3] = 255;
    }
    x.putImageData(img, 0, 0);
    return c;
  };
  return g;
}

// A duck facing right, 26x20, with a tiny kagent-face badge on its side: up / mid / down
// wing frames and a "shot" pose.
const DW = 26, DH = 20;
const duckCache = new Map();
export function duckFrames(pal) {
  if (duckCache.has(pal)) return duckCache.get(pal);
  const p = PAL[pal] || PAL.green;
  const make = (wing) => {
    const g = grid(DW, DH);
    g.poly([[1, 9], [6, 9], [5, 13]], p.wing); // tail
    g.ellipse(11, 12, 7.5, 4.6, (x, y) => (y >= 13 && x >= 8 ? p.belly : p.body));
    g.rect(15, 7, 17, 11, p.head); // neck
    g.ellipse(18.5, 6.5, 3.6, 3.4, p.head); // head
    for (let x = 14; x <= 18; x++) if (g.at(x, 10)) g.px(x, 10, p.ring); // neck ring
    g.rect(22, 6, 24, 7, "#ffa040"); g.px(24, 8, "#e07010"); // beak
    if (wing === "shot") { g.rect(18, 4, 19, 5, "#ffffff"); g.px(19, 5, "#000000"); }
    else { g.px(19, 5, "#ffffff"); g.px(20, 5, "#000000"); }
    g.rect(8, 10, 11, 12, "#942de7"); g.px(9, 11, "#5fd068"); g.px(10, 11, "#5fd068"); // kagent badge
    const W = p.wing;
    if (wing === "up") g.poly([[7, 10], [14, 10], [10, 1], [7, 0]], W);
    if (wing === "mid") g.poly([[6, 10], [14, 10], [3, 5], [1, 6]], W);
    if (wing === "down") g.poly([[7, 11], [14, 11], [11, 19], [7, 19]], W);
    if (wing === "shot") { g.poly([[6, 10], [12, 10], [8, 1], [5, 0]], W); g.poly([[10, 10], [15, 10], [15, 1], [12, 0]], W); g.rect(10, 16, 10, 18, "#ffa040"); g.rect(13, 16, 13, 18, "#ffa040"); }
    g.outline();
    return g.canvas();
  };
  const f = { up: make("up"), mid: make("mid"), down: make("down"), shot: make("shot") };
  duckCache.set(pal, f);
  return f;
}
// duckIcon: a small duck silhouette for the HUD's HIT row (white, or red once hit).
export function duckIcon(col) {
  const g = grid(12, 10);
  g.ellipse(5, 6, 4, 2.6, col); g.ellipse(9, 3.5, 1.9, 1.8, col); g.rect(7, 3, 8, 6, col); g.px(11, 3, col);
  return g.canvas().toDataURL();
}

// The kagent bot mascot, redrawn as pixel art: arms up holding a catch, or shaking its head.
let mascotCache = null;
export function mascotFrames() {
  if (mascotCache) return mascotCache;
  const make = (mode) => {
    const g = grid(24, 22);
    const P = "#942de7", D = "#1a0b2e";
    if (mode === "hold") { g.rect(0, 2, 2, 11, P); g.rect(21, 2, 23, 11, P); }
    else { g.rect(0, 12, 2, 19, P); g.rect(21, 12, 23, 19, P); }
    g.rect(3, 7, 20, 21, P);
    g.px(3, 7, null); g.px(20, 7, null); g.rect(4, 6, 19, 6, P); // rounded top
    g.rect(6, 10, 17, 17, D); // screen
    if (mode === "hold") { g.rect(8, 12, 9, 14, "#5fd068"); g.rect(14, 12, 15, 14, "#5fd068"); }
    else { g.rect(8, 14, 10, 14, "#ff8a8a"); g.rect(13, 14, 15, 14, "#ff8a8a"); g.px(9, 13, "#ff8a8a"); g.px(14, 13, "#ff8a8a"); }
    g.outline();
    return g.canvas();
  };
  mascotCache = { hold: make("hold"), sad: make("sad") };
  return mascotCache;
}

// scenery draws the background (sky, tree, bush) and the foreground (grass, dirt) once for a
// stage size. grassTop is where the grass starts; ducks rise out of it and fall into it.
function hash(n) { n = (n ^ 61) ^ (n >>> 16); n = n + (n << 3); n ^= n >>> 4; n = Math.imul(n, 0x27d4eb2d); return (n ^ (n >>> 15)) >>> 0; }
export function scenery(GW, GH, opts = {}) {
  const dirt = opts.dirt ?? 30, grassTop = GH - dirt - 8;
  const back = grid(GW, GH);
  // Tree: a chunky trunk with two branches and a round crown, on the left.
  const tx = Math.round(opts.treeX ?? GW * 0.11), top = grassTop - Math.round(GH * 0.42);
  const bark = (x, y) => ((x + (y >> 2)) % 4 === 0 ? "#4a2a0a" : "#8a5420");
  for (let y = top + 10; y < grassTop + 6; y++) for (let x = tx - 5; x <= tx + 5; x++) back.px(x, y, bark(x, y));
  back.poly([[tx - 2, top + 34], [tx + 1, top + 31], [tx - 14, top + 16], [tx - 17, top + 18]], "#8a5420");
  back.poly([[tx + 1, top + 30], [tx + 4, top + 33], [tx + 18, top + 18], [tx + 15, top + 16]], "#8a5420");
  const leaf = (x, y) => ((x * 7 + y * 3) % 5 === 0 ? "#007000" : (x + y) % 9 === 0 ? "#3cd040" : "#00a800");
  for (const [cx, cy, r] of [[tx, top + 2, 15], [tx - 15, top + 12, 12], [tx + 15, top + 12, 12], [tx - 6, top + 18, 11], [tx + 8, top + 20, 10]]) back.ellipse(cx, cy, r, r * 0.86, leaf);
  // A bush on the right.
  const bx = Math.round(GW * 0.8);
  for (const [cx, r] of [[bx - 14, 9], [bx, 12], [bx + 13, 8]]) back.ellipse(cx, grassTop + 2, r * 1.4, r, (x, y) => ((x + y) % 7 === 0 ? "#007000" : "#00a800"));
  back.outline();
  const front = grid(GW, GH);
  // Tall grass: a column per pixel with a jittered height, in tufts.
  for (let x = 0; x < GW; x++) {
    const h = 6 + (hash(x * 3 + 1) % 5) + Math.round(5 * Math.max(0, Math.sin(x / 9)) * Math.max(0, Math.sin(x / 23 + 1)));
    for (let y = grassTop - h; y < GH - dirt + 2; y++) {
      const d = y - (grassTop - h);
      front.px(x, y, d < 2 ? (x % 3 === 0 ? "#b8f818" : "#58d854") : x % 4 === 0 ? "#007000" : "#00a800");
    }
  }
  for (let y = GH - dirt + 2; y < GH; y++) for (let x = 0; x < GW; x++) front.px(x, y, hash(x * 31 + y * 17) % 13 === 0 ? "#6a3a0c" : "#a45c1c");
  return { back: back.canvas(), front: front.canvas(), grassTop, dirt };
}

// ---- the pixel stage: a low-res canvas scaled up by a whole number, letterboxed ----
export function pixelStage(canvas, baseH = 180) {
  const cx = canvas.getContext("2d");
  const off = document.createElement("canvas"), o = off.getContext("2d");
  const st = { k: 1, GW: 320, GH: baseH, ox: 0, oy: 0, dpr: 1, off, o, cx };
  st.resize = () => {
    st.dpr = Math.min(3, window.devicePixelRatio || 1);
    const dw = Math.round(canvas.clientWidth * st.dpr), dh = Math.round(canvas.clientHeight * st.dpr);
    canvas.width = dw; canvas.height = dh;
    st.k = Math.max(1, Math.floor(dh / baseH));
    st.GH = Math.floor(dh / st.k); st.GW = Math.floor(dw / st.k);
    st.ox = Math.floor((dw - st.GW * st.k) / 2); st.oy = Math.floor((dh - st.GH * st.k) / 2);
    off.width = st.GW; off.height = st.GH;
    o.imageSmoothingEnabled = false;
  };
  st.blit = (sx = 0, sy = 0) => {
    cx.setTransform(1, 0, 0, 1, 0, 0);
    cx.fillStyle = "#000"; cx.fillRect(0, 0, canvas.width, canvas.height);
    cx.imageSmoothingEnabled = false;
    cx.drawImage(off, st.ox + sx * st.k, st.oy + sy * st.k, st.GW * st.k, st.GH * st.k);
  };
  // toGame: a CSS-pixel point on the canvas to stage pixels; toScreen: back again.
  st.toGame = (x, y) => [(x * st.dpr - st.ox) / st.k, (y * st.dpr - st.oy) / st.k];
  st.toScreen = (gx, gy) => [(st.ox + gx * st.k) / st.dpr, (st.oy + gy * st.k) / st.dpr];
  // text: pixel-font text at stage coordinates, drawn at full resolution over the blit.
  st.text = (s, gx, gy, size, col, align = "center", shadow = true) => {
    cx.font = `${Math.max(6, Math.round(size * st.k))}px ${FONT}`;
    cx.textAlign = align; cx.textBaseline = "alphabetic";
    const x = Math.round(st.ox + gx * st.k), y = Math.round(st.oy + gy * st.k);
    if (shadow) { cx.fillStyle = "#000"; cx.fillText(s, x + st.k, y + st.k); }
    cx.fillStyle = col; cx.fillText(s, x, y);
    return cx.measureText(s).width / st.k;
  };
  // box: an 8-bit panel (black with a white border) at stage coordinates.
  st.box = (gx, gy, gw, gh, border = "#ffffff", fill = "#000000") => {
    const k = st.k, x = Math.round(st.ox + gx * k), y = Math.round(st.oy + gy * k), w = Math.round(gw * k), h = Math.round(gh * k);
    cx.fillStyle = border; cx.fillRect(x - k, y - k, w + 2 * k, h + 2 * k);
    cx.fillStyle = fill; cx.fillRect(x, y, w, h);
  };
  return st;
}

// startHunt runs the game on canvas. api: { launch(), hit(flight), escape(flight) } promise
// calls to the server; hooks: { onScore(state), onEnd(), sfx(name), buzz(pattern) }.
export function startHunt(canvas, api, hooks) {
  const st = pixelStage(canvas, 180);
  let sc = null;
  function resize() {
    st.resize();
    sc = scenery(st.GW, st.GH);
    // The HTML HUD sits on the dirt strip.
    document.documentElement.style.setProperty("--hud", `${(sc.dirt * st.k) / st.dpr}px`);
    document.documentElement.style.setProperty("--px", `${st.k / st.dpr}px`);
  }
  addEventListener("resize", resize);
  resize();
  const top = () => 10, low = () => sc.grassTop - 16;

  // ---- run state ----
  const g = { playing: false, t0: 0, ends: 0, wave: 1, shownWave: 0, score: 0, combo: 0, lastHit: 0, hits: 0, shots: 0, escapes: 0, golden: 0, bestCombo: 0,
    binCount: 0, pending: false, nextLaunch: 0, waking: null, ammo: 3, reloadAt: 0, row: [], pinkUntil: 0, banner: null };
  const agents = [], parts = [], texts = [], marks = [];
  let shake = 0, flash = 0, mascot = null;

  function start(seconds) {
    agents.length = 0; parts.length = 0; texts.length = 0; marks.length = 0; mascot = null;
    Object.assign(g, { playing: true, t0: performance.now(), ends: performance.now() + seconds * 1000, wave: 1, shownWave: 0, score: 0, combo: 0, lastHit: 0,
      hits: 0, shots: 0, escapes: 0, golden: 0, bestCombo: 0, binCount: 0, pending: false, nextLaunch: performance.now() + 1500, waking: null,
      ammo: 3, reloadAt: 0, row: [], pinkUntil: 0, banner: null });
    hooks.onScore(g);
  }
  function stop() { g.playing = false; }
  function banner(text, ms, color = "#ffffff") { g.banner = { text, until: performance.now() + ms, color }; }

  // ---- launching: one request at a time, as many in the air as the wave allows ----
  function launchTick(now) {
    if (!g.playing || g.pending || now < g.nextLaunch) return;
    const air = agents.filter((a) => a.state === "flying").length;
    if (air >= perRun(g.wave)) return;
    g.pending = true;
    const at = { x: rnd(st.GW * 0.18, st.GW * 0.85), y: sc.grassTop + 6 };
    g.waking = { ...at, t: now };
    api.launch().then((r) => {
      g.pending = false; g.waking = null;
      if (!g.playing) return;
      if (!r || r.busy) { g.nextLaunch = performance.now() + (r && r.retryMs ? r.retryMs : 600); return; }
      g.wave = r.wave || g.wave;
      spawn(r, at);
      g.nextLaunch = performance.now() + Math.max(250, 1100 - g.wave * 140);
    }).catch(() => { g.pending = false; g.waking = null; g.nextLaunch = performance.now() + 1200; });
  }
  // NES-style flight: straight diagonals at a few fixed angles, turning now and then.
  const ANGLES = [-0.42, -0.79, -1.15, -1.99, -2.36, -2.72];
  function heading(a, up) {
    let ang = ANGLES[(Math.random() * ANGLES.length) | 0];
    if (!up && Math.random() < 0.45) ang = -ang;
    a.vx = Math.cos(ang) * a.sp; a.vy = Math.sin(ang) * a.sp;
  }
  function spawn(r, at) {
    const k = KINDS[r.kind] || KINDS.agent;
    const sp = st.GW * 0.17 * (1 + 0.16 * (g.wave - 1)) * k.speed;
    const a = { flight: r.flight, kind: r.kind, k, pal: k.pals[(Math.random() * k.pals.length) | 0], x: at.x, y: at.y, vx: 0, vy: 0, sp, size: k.size,
      t: 0, turn: rnd(0.5, 1.1), life: Math.max(3.8, 8.2 - g.wave * 0.75) * (r.kind === "golden" ? 0.75 : 1), state: "flying", flap: Math.random() * 3,
      woke: r.wokeMs, say: "", sayUntil: 0, spin: 0, shotAt: 0 };
    heading(a, true);
    agents.push(a);
    g.ammo = 3;
    float(`woke in ${r.wokeMs} ms`, at.x, sc.grassTop - 2, "#ffffff", 1.3, 5);
    hooks.sfx(r.kind === "golden" ? "golden" : "quack");
    hooks.onScore(g);
  }

  // ---- shooting: three shots a duck, NES style; out of shots, a short reload ----
  function shoot(gx, gy) {
    if (!g.playing) return;
    const now = performance.now();
    if (g.ammo <= 0) {
      if (now >= g.reloadAt) { g.ammo = 3; } else { hooks.sfx("reload"); return; }
    }
    g.ammo--; g.shots++;
    if (g.ammo <= 0) g.reloadAt = now + 700;
    marks.push({ x: gx, y: gy, t: 0 });
    flash = 1; hooks.sfx("shot"); hooks.buzz(12);
    let best = null, bd = 1e9;
    for (const a of agents) {
      if (a.state !== "flying") continue;
      const d = Math.hypot(a.x - gx, a.y - gy);
      if (d < 15 * a.size && d < bd) { best = a; bd = d; }
    }
    hooks.onScore(g);
    if (!best) { puff(gx, gy); return; }
    const a = best;
    a.state = "shot"; a.shotAt = now;
    g.ammo = 3;
    // Predict the combo the server will give (it confirms on the reply).
    g.combo = now - g.lastHit <= 2200 ? g.combo + 1 : 1;
    g.lastHit = now;
    const m = mult(g.combo), pts = (a.kind === "golden" ? 500 : a.kind === "swift" ? 150 : 100) * m;
    sparks(a.x, a.y, PAL[a.pal].head, 14);
    shake = Math.max(shake, a.kind === "golden" ? 3 : 2);
    float(`${pts}`, a.x, a.y - 12, a.kind === "golden" ? "#ffe04a" : "#ffffff", 1.1, 7);
    if (m > 1) float(`x${m} COMBO`, a.x, a.y - 22, "#fc74b4", 1.2, 6);
    hooks.sfx(a.kind === "golden" ? "goldhit" : "hit"); hooks.buzz(a.kind === "golden" ? [30, 40, 30] : 35);
    api.hit(a.flight).then((h) => {
      if (!h || h.error) return;
      g.score = h.score; g.combo = h.combo; g.hits++; g.bestCombo = Math.max(g.bestCombo, h.combo);
      if (h.kind === "golden") g.golden++;
      a.suspendMs = h.asleep ? h.ms : 0;
      if (a.landed && a.suspendMs) snapText(a.suspendMs);
      hooks.onScore(g);
    }).catch(() => {});
  }

  // ---- results row: ten ducks a round, red when hit; all ten is PERFECT ----
  function record(hit) {
    g.row.push(hit);
    if (g.row.length >= 10) {
      if (g.row.every(Boolean)) { banner("PERFECT!!", 1800, "#ffe04a"); hooks.sfx("perfect"); }
      setTimeout(() => { g.row = []; hooks.onScore(g); }, 1400);
    }
    hooks.onScore(g);
  }

  // ---- effects ----
  function sparks(x, y, color, n) {
    for (let i = 0; i < n; i++) { const an = Math.random() * 6.283, s = rnd(20, 80); parts.push({ x, y, vx: Math.cos(an) * s, vy: Math.sin(an) * s - 20, t: 0, life: rnd(0.3, 0.6), color }); }
  }
  function puff(x, y) {
    for (let i = 0; i < 6; i++) parts.push({ x, y, vx: rnd(-20, 20), vy: rnd(-25, -5), t: 0, life: 0.35, color: "#ffffff" });
  }
  // snapText: the real snapshot time, over the crate, one line per recent snapshot.
  function snapText(ms) {
    const busy = texts.filter((t) => t.crate && t.t < 1.2).length;
    float(`snapshotted in ${ms} ms`, st.GW / 2, sc.grassTop - 18 - busy * 8, "#5fd068", 1.8, 5);
    texts[texts.length - 1].crate = true;
  }
  function float(text, x, y, color, life, size) { texts.push({ text, x, y, color, t: 0, life, size }); if (texts.length > 30) texts.shift(); }
  function say(flight, text) {
    const a = agents.find((x) => x.flight === flight && x.state === "flying");
    if (a) { a.say = text; a.sayUntil = performance.now() + 3200; }
  }
  function pop(mode, x, catchPal) {
    mascot = { mode, x: clamp(x, 24, st.GW - 24), t: 0, life: mode === "hold" ? 1.5 : 1.6, pal: catchPal };
  }

  // ---- update ----
  function update(dt, now) {
    if (g.playing) {
      g.wave = Math.max(g.wave, 1 + Math.floor((now - g.t0) / 15000));
      if (g.wave !== g.shownWave) { g.shownWave = g.wave; banner(`ROUND ${g.wave}`, 1500); hooks.sfx("round"); hooks.onScore(g); }
      if (now >= g.ends) { g.playing = false; hooks.onEnd(); }
      if (now - g.lastHit > 2200 && g.combo) { g.combo = 0; hooks.onScore(g); }
      if (g.ammo <= 0 && now >= g.reloadAt) { g.ammo = 3; hooks.onScore(g); }
      launchTick(now);
    }
    for (let i = agents.length - 1; i >= 0; i--) {
      const a = agents[i];
      a.t += dt;
      if (a.state === "flying") {
        a.flap += dt * 10;
        a.turn -= dt;
        if (a.turn <= 0) { a.turn = rnd(0.5, 1.2); heading(a, a.y > low() - 10); }
        if (a.t > a.life) { a.state = "escaping"; a.vx = 0; a.vy = -a.sp * 1.4; }
        a.x += a.vx * dt; a.y += a.vy * dt;
        if (a.x < 12 && a.vx < 0) a.vx = -a.vx;
        if (a.x > st.GW - 12 && a.vx > 0) a.vx = -a.vx;
        if (a.y < top() + 6 && a.vy < 0) a.vy = -a.vy;
        if (a.y > low() && a.vy > 0) a.vy = -a.vy;
      } else if (a.state === "escaping") {
        a.flap += dt * 14;
        a.y += a.vy * dt;
        if (a.y < -20) {
          agents.splice(i, 1);
          if (g.playing) {
            g.escapes++; g.combo = 0; g.pinkUntil = now + 1200; banner("FLY AWAY", 1200);
            pop("sad", st.GW / 2); record(false); hooks.sfx("flyaway");
          }
          api.escape(a.flight).catch(() => {});
        }
      } else if (a.state === "shot") {
        if (now - a.shotAt > 380) { a.state = "falling"; a.vy = 20; hooks.sfx("fall"); }
      } else if (a.state === "falling") {
        a.spin += dt * 12;
        a.vy = Math.min(a.vy + 160 * dt, 120);
        a.y += a.vy * dt;
        if (a.y >= sc.grassTop + 8) {
          agents.splice(i, 1);
          a.landed = true;
          g.binCount++;
          pop("hold", a.x, a.pal);
          record(true);
          if (a.suspendMs) snapText(a.suspendMs);
          hooks.sfx("bin"); // a late hit reply still finds a through its closure
        }
      }
    }
    for (let i = parts.length - 1; i >= 0; i--) { const p = parts[i]; p.t += dt; p.vy += 140 * dt; p.x += p.vx * dt; p.y += p.vy * dt; if (p.t > p.life) parts.splice(i, 1); }
    for (let i = texts.length - 1; i >= 0; i--) { const t = texts[i]; t.t += dt; t.y -= 10 * dt; if (t.t > t.life) texts.splice(i, 1); }
    for (let i = marks.length - 1; i >= 0; i--) { marks[i].t += dt; if (marks[i].t > 0.25) marks.splice(i, 1); }
    if (mascot) { mascot.t += dt; if (mascot.t > mascot.life) mascot = null; }
    shake = Math.max(0, shake - dt * 12); flash = Math.max(0, flash - dt * 14);
  }

  // ---- drawing ----
  function drawDuck(o, a, now) {
    const f = duckFrames(a.pal);
    let img = f.mid;
    if (a.state === "flying" || a.state === "escaping") img = [f.up, f.mid, f.down, f.mid][Math.floor(a.flap) % 4];
    else img = f.shot;
    const x = Math.round(a.x), y = Math.round(a.y);
    o.save();
    o.translate(x, y);
    if (a.state === "falling") { o.rotate((Math.floor(a.spin) % 4) * Math.PI / 2); }
    else if (a.vx < 0 && a.state !== "shot") o.scale(-1, 1);
    if (a.kind === "golden" && a.state === "flying" && Math.floor(now / 120) % 2) { o.globalAlpha = 0.85; }
    o.drawImage(img, -DW / 2, -DH / 2);
    o.restore();
  }
  function drawMascot(o) {
    if (!mascot) return;
    const m = mascot, f = mascotFrames(), rise = 0.25, fall = 0.25;
    const k = m.t < rise ? m.t / rise : m.t > m.life - fall ? (m.life - m.t) / fall : 1;
    const y = Math.round(sc.grassTop + 12 - 30 * k);
    const x = Math.round(m.x + (m.mode === "sad" ? (Math.floor(m.t * 12) % 2 ? 1 : -1) : 0));
    o.drawImage(m.mode === "hold" ? f.hold : f.sad, x - 12, y - 11);
    if (m.mode === "hold") o.drawImage(duckFrames(m.pal || "green").shot, x - DW / 2, y - 11 - DH + 1);
  }
  function draw(now) {
    const o = st.o, GW = st.GW, GH = st.GH;
    o.fillStyle = now < g.pinkUntil ? SKY_PINK : SKY; o.fillRect(0, 0, GW, GH);
    o.drawImage(sc.back, 0, 0);
    if (g.waking) { // grass rustles where a duck is about to come out
      const p = Math.floor(now / 90) % 2;
      o.fillStyle = "#b8f818"; o.fillRect(Math.round(g.waking.x) - 6 + p, sc.grassTop - 3, 2, 3); o.fillRect(Math.round(g.waking.x) + 4 - p, sc.grassTop - 4, 2, 4);
    }
    for (const a of agents) drawDuck(o, a, now);
    drawMascot(o);
    o.drawImage(sc.front, 0, 0);
    // Snapshot Storage: a crate half hidden in the grass.
    const cw = 46, ch = 18, crx = Math.round(GW / 2 - cw / 2), cry = sc.grassTop - 12;
    o.fillStyle = "#000"; o.fillRect(crx - 1, cry - 1, cw + 2, ch + 2);
    o.fillStyle = "#1a2a20"; o.fillRect(crx, cry, cw, ch);
    o.fillStyle = "#5fd068"; o.fillRect(crx, cry, cw, 1); o.fillRect(crx, cry + 4, cw, 1);
    for (const p of parts) { o.fillStyle = p.color; o.fillRect(Math.round(p.x), Math.round(p.y), 1, 1); }
    for (const m of marks) { // the shot: a small crosshair where the trigger was pulled
      o.fillStyle = "#ffffff"; const x = Math.round(m.x), y = Math.round(m.y);
      o.fillRect(x - 4, y, 3, 1); o.fillRect(x + 2, y, 3, 1); o.fillRect(x, y - 4, 1, 3); o.fillRect(x, y + 2, 1, 3);
    }
    const sx = shake ? Math.round(rnd(-shake, shake)) : 0, sy = shake ? Math.round(rnd(-shake, shake)) : 0;
    st.blit(sx, sy);
    // Text over the pixels.
    st.text("SNAPSHOT", GW / 2, cry + 9, 4, "#5fd068", "center", false);
    st.text(String(g.binCount), GW / 2, cry + 16, 5, "#ffffff", "center", false);
    for (const a of agents) {
      if (a.state !== "flying") continue;
      if (a.k.label) st.text(a.k.label, a.x, a.y - 13, 4, a.k.color);
      if (a.say && now < a.sayUntil) bubble(a.x, a.y - (a.k.label ? 20 : 14), a.say);
    }
    for (const t of texts) { if (Math.floor(t.t * 10) % 2 || t.t > 0.3) st.text(t.text, t.x, t.y, t.size, t.color); }
    if (g.banner && now < g.banner.until) {
      const w = g.banner.text.length * 8 + 16;
      st.box(GW / 2 - w / 2, GH * 0.32, w, 16);
      st.text(g.banner.text, GW / 2, GH * 0.32 + 12, 8, g.banner.color, "center", false);
    }
    if (flash) { st.cx.setTransform(1, 0, 0, 1, 0, 0); st.cx.fillStyle = `rgba(255,255,255,${0.75 * flash})`; st.cx.fillRect(0, 0, canvas.width, canvas.height); }
  }
  function bubble(x, y, text) {
    const s = text.length > 34 ? text.slice(0, 33) + "…" : text;
    const w = Math.min(st.GW * 0.6, s.length * 4 + 8), h = 10;
    const bx = clamp(x - w / 2, 4, st.GW - w - 4), by = y - h;
    st.box(bx, by, w, h, "#000000", "#ffffff");
    st.text(s, bx + w / 2, by + 7, 4, "#000000", "center", false);
  }

  let last = performance.now();
  function frame(now) {
    const dt = Math.max(0, Math.min(0.05, (now - last) / 1000));
    last = now;
    update(dt, now);
    draw(now);
    requestAnimationFrame(frame);
  }
  requestAnimationFrame(frame);

  canvas.addEventListener("pointerdown", (e) => {
    e.preventDefault();
    const r = canvas.getBoundingClientRect();
    const [gx, gy] = st.toGame(e.clientX - r.left, e.clientY - r.top);
    shoot(gx, gy);
  });
  return { start, stop, say, state: g, agents, toScreen: st.toScreen, mascot: () => !!mascot, timeLeft: () => Math.max(0, (g.ends - performance.now()) / 1000) };
}
