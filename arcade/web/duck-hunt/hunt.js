// Agent Hunt's phone game: a landscape canvas. Each flying agent is a real kagent session
// the server woke for this run (a Substrate Resume); tapping one sends the hit, and the
// server suspends it (a real snapshot) and scores it. The sky, the agents and every effect
// are drawn here; the score, the combo and the timings come from the server.
"use strict";

export const KINDS = {
  agent: { body: "#7c5cff", belly: "#b9a8ff", wing: "#5a3fe0", speed: 1, size: 1, label: "" },
  swift: { body: "#22d3ee", belly: "#a5f3fc", wing: "#0e9fb8", speed: 1.5, size: 0.85, label: "SWIFT +150" },
  golden: { body: "#ffd23d", belly: "#fff2b0", wing: "#e0a800", speed: 1.8, size: 1.05, label: "GOLDEN +500" },
};
export const mult = (c) => 1 + Math.min(4, Math.max(0, c - 1) >> 1);
export const perRun = (wave) => Math.min(3, 1 + ((wave - 1) >> 1));

const rnd = (a, b) => a + Math.random() * (b - a);
const clamp = (v, a, b) => Math.max(a, Math.min(b, v));

// startHunt runs the game on canvas. api: { launch(), hit(flight), escape(flight) } promise
// calls to the server; hooks: { onScore(state), onEnd(), sfx(name), buzz(pattern) }.
export function startHunt(canvas, api, hooks) {
  const cx = canvas.getContext("2d");
  let W = 0, H = 0, dpr = 1, S = 1; // S: agent size in px
  const stars = [], towers = [], city = [];
  function resize() {
    dpr = Math.min(2, window.devicePixelRatio || 1);
    W = canvas.clientWidth; H = canvas.clientHeight;
    canvas.width = Math.round(W * dpr); canvas.height = Math.round(H * dpr);
    S = Math.max(34, Math.min(72, H * 0.13));
    stars.length = 0;
    for (let i = 0; i < 90; i++) stars.push({ x: Math.random() * W, y: Math.random() * H * 0.62, r: rnd(0.5, 1.6), p: Math.random() * 6.3 });
    city.length = 0;
    for (let x = -20; x < W + 40; ) { const w = rnd(30, 70); city.push({ x, w, h: rnd(H * 0.1, H * 0.26), lit: Math.random() }); x += w + rnd(2, 10); }
    towers.length = 0;
    const n = Math.max(4, Math.round(W / 170));
    for (let i = 0; i < n; i++) towers.push({ x: (i + 0.5) * (W / n) + rnd(-30, 30), w: rnd(26, 40), h: rnd(H * 0.18, H * 0.32), leds: Math.round(rnd(5, 9)), p: Math.random() * 10 });
  }
  addEventListener("resize", resize);
  resize();

  const ground = () => H - Math.max(26, H * 0.09);
  const bin = () => ({ x: W / 2, y: ground() + 2, w: Math.max(110, W * 0.15), h: Math.max(40, H * 0.11) });

  // ---- run state ----
  const g = { playing: false, t0: 0, ends: 0, wave: 1, score: 0, combo: 0, lastHit: 0, hits: 0, shots: 0, escapes: 0, golden: 0, bestCombo: 0, binCount: 0, binFlash: 0,
    pending: false, nextLaunch: 0, waking: null };
  const agents = [], parts = [], texts = [], rings = [];
  let shake = 0, flash = 0;

  function start(seconds) {
    agents.length = 0; parts.length = 0; texts.length = 0; rings.length = 0;
    Object.assign(g, { playing: true, t0: performance.now(), ends: performance.now() + seconds * 1000, wave: 1, score: 0, combo: 0, lastHit: 0,
      hits: 0, shots: 0, escapes: 0, golden: 0, bestCombo: 0, binCount: 0, pending: false, nextLaunch: performance.now() + 600, waking: null });
    hooks.onScore(g);
  }
  function stop() { g.playing = false; }

  // ---- launching: one request at a time, as many in the air as the wave allows ----
  function launchTick(now) {
    if (!g.playing || g.pending || now < g.nextLaunch) return;
    const air = agents.filter((a) => a.state === "flying").length;
    if (air >= perRun(g.wave)) return;
    g.pending = true;
    const at = { x: rnd(W * 0.12, W * 0.88), y: ground() - 8 };
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
  function spawn(r, at) {
    const k = KINDS[r.kind] || KINDS.agent;
    const sp = (W * 0.11 + 40) * (1 + 0.16 * (g.wave - 1)) * k.speed;
    const ang = rnd(-Math.PI * 0.82, -Math.PI * 0.18);
    agents.push({ flight: r.flight, kind: r.kind, k, x: at.x, y: at.y, vx: Math.cos(ang) * sp, vy: Math.sin(ang) * sp, sp, size: S * k.size,
      t: 0, turn: rnd(0.6, 1.3), life: Math.max(3.8, 8.2 - g.wave * 0.75) * (r.kind === "golden" ? 0.75 : 1), state: "flying", flap: Math.random() * 6,
      woke: r.wokeMs, say: "", sayUntil: 0, rot: 0 });
    float(`woke in ${r.wokeMs} ms`, at.x, at.y - S * 0.8, "#9fffb0", 1.2, 12);
    if (r.kind === "golden") hooks.sfx("golden");
  }

  // ---- shooting ----
  function shoot(x, y) {
    if (!g.playing) return;
    g.shots++;
    rings.push({ x, y, t: 0 });
    hooks.sfx("shot"); hooks.buzz(12);
    let best = null, bd = 1e9;
    for (const a of agents) {
      if (a.state !== "flying") continue;
      const d = Math.hypot(a.x - x, a.y - y);
      if (d < a.size * 0.78 && d < bd) { best = a; bd = d; }
    }
    if (!best) { puff(x, y); return; }
    const a = best, now = performance.now();
    a.state = "falling"; a.vy = -S * 3; a.vx *= 0.25; a.rot = 0;
    // Predict the combo the server will give (it confirms on the reply).
    g.combo = now - g.lastHit <= 2200 ? g.combo + 1 : 1;
    g.lastHit = now;
    const m = mult(g.combo), pts = (a.kind === "golden" ? 500 : a.kind === "swift" ? 150 : 100) * m;
    sparks(a.x, a.y, a.k.body, 26);
    flash = 0.9; shake = Math.max(shake, a.kind === "golden" ? 9 : 5);
    float(`+${pts}`, a.x, a.y - a.size * 0.6, a.kind === "golden" ? "#ffd23d" : "#ffffff", 1.1, 22);
    if (m > 1) float(`×${m} COMBO`, a.x, a.y - a.size * 1.1, "#ff7ad9", 1.2, 16);
    hooks.sfx(a.kind === "golden" ? "goldhit" : "hit"); hooks.buzz(a.kind === "golden" ? [30, 40, 30] : 35);
    api.hit(a.flight).then((h) => {
      if (!h || h.error) return;
      g.score = h.score; g.combo = h.combo; g.hits++; g.bestCombo = Math.max(g.bestCombo, h.combo);
      if (h.kind === "golden") g.golden++;
      a.suspendMs = h.asleep ? h.ms : 0;
      hooks.onScore(g);
    }).catch(() => {});
  }

  // ---- effects ----
  function sparks(x, y, color, n) {
    for (let i = 0; i < n; i++) {
      const an = Math.random() * 6.283, s = rnd(60, 280);
      parts.push({ x, y, vx: Math.cos(an) * s, vy: Math.sin(an) * s - 60, t: 0, life: rnd(0.35, 0.8), color, r: rnd(1.5, 3.5) });
    }
  }
  function puff(x, y) {
    for (let i = 0; i < 8; i++) parts.push({ x, y, vx: rnd(-50, 50), vy: rnd(-60, -10), t: 0, life: 0.4, color: "#9aa3b8", r: rnd(2, 4) });
    float("miss", x, y - 14, "#9aa3b8", 0.6, 12);
  }
  function float(text, x, y, color, life, size) { texts.push({ text, x, y, color, t: 0, life, size }); if (texts.length > 40) texts.shift(); }
  function say(flight, text) {
    const a = agents.find((x) => x.flight === flight && x.state === "flying");
    if (a) { a.say = text; a.sayUntil = performance.now() + 3200; }
  }

  // ---- update ----
  function update(dt, now) {
    if (g.playing) {
      g.wave = Math.max(g.wave, 1 + Math.floor((now - g.t0) / 15000));
      if (now >= g.ends) { g.playing = false; hooks.onEnd(); }
      if (now - g.lastHit > 2200 && g.combo) { g.combo = 0; hooks.onScore(g); }
      launchTick(now);
    }
    const gy = ground(), b = bin();
    for (let i = agents.length - 1; i >= 0; i--) {
      const a = agents[i];
      a.t += dt; a.flap += dt * (a.state === "flying" ? 16 : 30);
      if (a.state === "flying") {
        a.turn -= dt;
        if (a.turn <= 0) {
          a.turn = rnd(0.5, 1.3);
          const ang = Math.atan2(a.vy, a.vx) + rnd(-1.1, 1.1);
          a.vx = Math.cos(ang) * a.sp; a.vy = Math.sin(ang) * a.sp;
          if (a.vy > 0 && a.y > H * 0.55) a.vy = -Math.abs(a.vy);
        }
        if (a.t > a.life) { a.state = "escaping"; a.vx *= 0.4; a.vy = -a.sp * 1.6; }
        a.x += a.vx * dt; a.y += a.vy * dt;
        if (a.x < a.size * 0.6 && a.vx < 0) a.vx = -a.vx;
        if (a.x > W - a.size * 0.6 && a.vx > 0) a.vx = -a.vx;
        if (a.y < H * 0.1 && a.vy < 0) a.vy = Math.abs(a.vy) * 0.8;
        if (a.y > gy - a.size * 0.9 && a.vy > 0) a.vy = -Math.abs(a.vy);
      } else if (a.state === "escaping") {
        a.x += a.vx * dt; a.y += a.vy * dt;
        if (a.y < -a.size * 2) {
          agents.splice(i, 1);
          if (g.playing) { g.escapes++; g.combo = 0; hooks.onScore(g); float("escaped!", clamp(a.x, 50, W - 50), 40, "#ff8a8a", 1, 14); hooks.sfx("escape"); }
          api.escape(a.flight).catch(() => {});
        }
      } else if (a.state === "falling") {
        // Spin, then arc into Snapshot Storage.
        a.rot += dt * 9;
        a.vy += S * 12 * dt;
        a.x += (b.x - a.x) * Math.min(1, dt * 2.6);
        a.y += a.vy * dt;
        if (a.y >= b.y - b.h * 0.2) {
          agents.splice(i, 1);
          g.binCount++; g.binFlash = 1;
          sparks(b.x, b.y - b.h * 0.4, "#5fd068", 14);
          const ms = a.suspendMs;
          float(ms ? `snapshotted in ${ms} ms` : "snapshotted", b.x, b.y - b.h - 10, "#9fffb0", 1.6, 13);
          hooks.sfx("bin");
        }
      }
    }
    for (let i = parts.length - 1; i >= 0; i--) { const p = parts[i]; p.t += dt; p.vy += 420 * dt; p.x += p.vx * dt; p.y += p.vy * dt; if (p.t > p.life) parts.splice(i, 1); }
    for (let i = texts.length - 1; i >= 0; i--) { const t = texts[i]; t.t += dt; t.y -= 26 * dt; if (t.t > t.life) texts.splice(i, 1); }
    for (let i = rings.length - 1; i >= 0; i--) { rings[i].t += dt; if (rings[i].t > 0.3) rings.splice(i, 1); }
    shake = Math.max(0, shake - dt * 30); flash = Math.max(0, flash - dt * 4); g.binFlash = Math.max(0, g.binFlash - dt * 2.5);
  }

  // ---- drawing ----
  function drawSky(now) {
    const gr = cx.createLinearGradient(0, 0, 0, H);
    gr.addColorStop(0, "#0a0b2a"); gr.addColorStop(0.55, "#2a1650"); gr.addColorStop(0.82, "#7a2d6b"); gr.addColorStop(1, "#ff8a5c");
    cx.fillStyle = gr; cx.fillRect(0, 0, W, H);
    for (const s of stars) { cx.globalAlpha = 0.45 + 0.45 * Math.sin(now / 700 + s.p); cx.fillStyle = "#fff"; cx.fillRect(s.x, s.y, s.r, s.r); }
    cx.globalAlpha = 1;
    // Moon with the kagent glow.
    const mx = W * 0.82, my = H * 0.2, mr = H * 0.075;
    const mg = cx.createRadialGradient(mx, my, mr * 0.2, mx, my, mr * 3.2);
    mg.addColorStop(0, "#ffffff22"); mg.addColorStop(1, "#ffffff00");
    cx.fillStyle = mg; cx.beginPath(); cx.arc(mx, my, mr * 3.2, 0, 7); cx.fill();
    cx.fillStyle = "#fff5d6"; cx.beginPath(); cx.arc(mx, my, mr, 0, 7); cx.fill();
    cx.fillStyle = "#ead9a8"; cx.beginPath(); cx.arc(mx - mr * 0.3, my - mr * 0.2, mr * 0.18, 0, 7); cx.arc(mx + mr * 0.25, my + mr * 0.3, mr * 0.12, 0, 7); cx.fill();
  }
  function drawCity(now) {
    const gy = ground();
    // Far city.
    for (const c of city) {
      cx.fillStyle = "#1a1036"; cx.fillRect(c.x, gy - c.h, c.w, c.h);
      cx.fillStyle = "#ffcf6b";
      for (let y = gy - c.h + 8; y < gy - 6; y += 10) for (let x = c.x + 5; x < c.x + c.w - 5; x += 9)
        if (((x * 7 + y * 13) % 11) / 11 < c.lit * 0.5) { cx.globalAlpha = 0.55; cx.fillRect(x, y, 3, 4); }
      cx.globalAlpha = 1;
    }
    // Cluster racks: tall server towers with blinking LEDs (the Substrate workers).
    for (const t of towers) {
      const x = t.x - t.w / 2, y = gy - t.h;
      cx.fillStyle = "#0e1424"; cx.fillRect(x, y, t.w, t.h);
      cx.strokeStyle = "#2c3a63"; cx.lineWidth = 1.5; cx.strokeRect(x + 0.5, y + 0.5, t.w - 1, t.h - 1);
      for (let i = 0; i < t.leds; i++) {
        const ly = y + 10 + i * ((t.h - 20) / t.leds);
        cx.fillStyle = "#1c2742"; cx.fillRect(x + 4, ly, t.w - 8, 5);
        const on = Math.sin(now / 260 + t.p + i * 1.7) > 0.2;
        cx.fillStyle = on ? "#5fd068" : "#24402c"; cx.fillRect(x + t.w - 10, ly + 1, 3, 3);
      }
      cx.fillStyle = "#5fd068"; cx.globalAlpha = 0.5 + 0.5 * Math.sin(now / 400 + t.p);
      cx.beginPath(); cx.arc(t.x, y - 6, 2.5, 0, 7); cx.fill(); cx.globalAlpha = 1;
      cx.strokeStyle = "#2c3a63"; cx.beginPath(); cx.moveTo(t.x, y); cx.lineTo(t.x, y - 6); cx.stroke();
    }
    // Ground.
    cx.fillStyle = "#0b0f1c"; cx.fillRect(0, gy, W, H - gy);
    cx.fillStyle = "#5fd06855"; cx.fillRect(0, gy, W, 2);
  }
  function drawBin() {
    const b = bin(), x = b.x - b.w / 2, y = b.y - b.h;
    const glow = 0.35 + g.binFlash * 0.65;
    cx.save();
    cx.shadowColor = "#5fd068"; cx.shadowBlur = 18 * glow;
    cx.fillStyle = "#111a18"; roundRect(x, y, b.w, b.h, 8); cx.fill();
    cx.restore();
    cx.strokeStyle = `rgba(95,208,104,${0.5 + glow * 0.5})`; cx.lineWidth = 2; roundRect(x, y, b.w, b.h, 8); cx.stroke();
    // Drawers.
    for (let i = 1; i < 3; i++) { cx.fillStyle = "#1b2a24"; cx.fillRect(x + 8, y + i * (b.h / 3) - 2, b.w - 16, 2); }
    cx.fillStyle = "#5fd068"; cx.font = `700 ${Math.max(9, b.h * 0.2)}px Inter, system-ui, sans-serif`; cx.textAlign = "center";
    cx.fillText("SNAPSHOT STORAGE", b.x, y + b.h * 0.42);
    cx.fillStyle = "#cfe9d2"; cx.font = `800 ${Math.max(11, b.h * 0.32)}px Inter, system-ui, sans-serif`;
    cx.fillText(String(g.binCount), b.x, y + b.h * 0.86);
  }
  function roundRect(x, y, w, h, r) { cx.beginPath(); cx.moveTo(x + r, y); cx.arcTo(x + w, y, x + w, y + h, r); cx.arcTo(x + w, y + h, x, y + h, r); cx.arcTo(x, y + h, x, y, r); cx.arcTo(x, y, x + w, y, r); cx.closePath(); }

  // drawAgent: a round agent-bird with the kagent screen face, antenna and flapping wings.
  function drawAgent(a, now) {
    const s = a.size, k = a.k;
    cx.save();
    cx.translate(a.x, a.y);
    const face = a.vx >= 0 ? 1 : -1;
    if (a.state === "falling") cx.rotate(a.rot); else cx.rotate(clamp(a.vy / a.sp, -0.6, 0.6) * 0.35 * face);
    cx.scale(face, 1);
    if (a.kind === "golden") { cx.shadowColor = "#ffd23d"; cx.shadowBlur = 18; }
    // Back wing.
    const fl = Math.sin(a.flap);
    cx.fillStyle = k.wing;
    cx.beginPath(); cx.ellipse(-s * 0.12, -s * 0.12, s * 0.34, s * 0.16 * (0.4 + Math.abs(fl)), -0.5 - fl * 0.6, 0, 7); cx.fill();
    // Body.
    cx.fillStyle = k.body; cx.beginPath(); cx.ellipse(0, 0, s * 0.42, s * 0.32, 0, 0, 7); cx.fill();
    cx.shadowBlur = 0;
    cx.fillStyle = k.belly; cx.beginPath(); cx.ellipse(s * 0.05, s * 0.1, s * 0.26, s * 0.17, 0, 0, 7); cx.fill();
    // Tail.
    cx.fillStyle = k.wing; cx.beginPath(); cx.moveTo(-s * 0.38, -s * 0.05); cx.lineTo(-s * 0.58, -s * 0.2); cx.lineTo(-s * 0.55, s * 0.08); cx.closePath(); cx.fill();
    // Head: the kagent screen face.
    const hx = s * 0.3, hy = -s * 0.24, hw = s * 0.42, hh = s * 0.32;
    cx.fillStyle = k.body; roundRect(hx - hw / 2 - s * 0.03, hy - hh / 2 - s * 0.03, hw + s * 0.06, hh + s * 0.06, s * 0.1); cx.fill();
    cx.fillStyle = "#140c2e"; roundRect(hx - hw / 2, hy - hh / 2, hw, hh, s * 0.08); cx.fill();
    const blink = a.state === "falling" || (Math.sin(now / 900 + a.flap) > 0.97);
    cx.fillStyle = a.state === "falling" ? "#9fffff" : "#7dffa0";
    if (blink) { cx.fillRect(hx - hw * 0.28, hy, hw * 0.18, s * 0.03); cx.fillRect(hx + hw * 0.1, hy, hw * 0.18, s * 0.03); }
    else { cx.fillRect(hx - hw * 0.26, hy - hh * 0.18, hw * 0.14, hh * 0.36); cx.fillRect(hx + hw * 0.12, hy - hh * 0.18, hw * 0.14, hh * 0.36); }
    // Beak.
    cx.fillStyle = "#ffb13d"; cx.beginPath(); cx.moveTo(hx + hw / 2 + s * 0.01, hy + hh * 0.05); cx.lineTo(hx + hw / 2 + s * 0.17, hy + hh * 0.2); cx.lineTo(hx + hw / 2 + s * 0.01, hy + hh * 0.35); cx.closePath(); cx.fill();
    // Antenna.
    cx.strokeStyle = k.body; cx.lineWidth = Math.max(1.5, s * 0.04); cx.beginPath(); cx.moveTo(hx, hy - hh / 2); cx.lineTo(hx - s * 0.04, hy - hh / 2 - s * 0.16); cx.stroke();
    cx.fillStyle = a.kind === "golden" ? "#fff" : "#5fd068"; cx.beginPath(); cx.arc(hx - s * 0.04, hy - hh / 2 - s * 0.18, s * 0.05, 0, 7); cx.fill();
    // Front wing.
    cx.fillStyle = k.wing; cx.globalAlpha = 0.95;
    cx.beginPath(); cx.ellipse(-s * 0.02, -s * 0.06, s * 0.3, s * 0.14 * (0.35 + Math.abs(fl)), -0.35 - fl * 0.8, 0, 7); cx.fill();
    cx.globalAlpha = 1;
    // Snapshot freeze: an ice box while falling.
    if (a.state === "falling") { cx.strokeStyle = "#9fffff"; cx.lineWidth = 2; cx.globalAlpha = 0.8; roundRect(-s * 0.55, -s * 0.55, s * 1.1, s * 0.95, 6); cx.stroke(); cx.globalAlpha = 1; }
    cx.restore();
    // Labels (not rotated).
    if (a.state === "flying" && k.label) {
      cx.font = `800 ${Math.max(9, s * 0.2)}px Inter, system-ui, sans-serif`; cx.textAlign = "center";
      cx.fillStyle = a.kind === "golden" ? "#ffd23d" : "#7df3ff"; cx.fillText(k.label, a.x, a.y - s * 0.62);
    }
    if (a.say && now < a.sayUntil && a.state === "flying") bubble(a.x, a.y - s * (k.label ? 0.85 : 0.65), a.say);
  }
  function bubble(x, y, text) {
    cx.font = `600 ${Math.max(11, S * 0.24)}px Inter, system-ui, sans-serif`;
    const w = Math.min(W * 0.5, cx.measureText(text).width + 16), h = Math.max(20, S * 0.42);
    const bx = clamp(x - w / 2, 6, W - w - 6), by = y - h;
    cx.fillStyle = "#ffffffee"; roundRect(bx, by, w, h, 8); cx.fill();
    cx.beginPath(); cx.moveTo(clamp(x, bx + 10, bx + w - 10) - 5, by + h); cx.lineTo(clamp(x, bx + 10, bx + w - 10), by + h + 7); cx.lineTo(clamp(x, bx + 10, bx + w - 10) + 5, by + h); cx.fill();
    cx.fillStyle = "#1b1440"; cx.textAlign = "center"; cx.fillText(text.length > 44 ? text.slice(0, 43) + "…" : text, bx + w / 2, by + h * 0.68, w - 10);
  }

  function draw(now) {
    cx.setTransform(dpr, 0, 0, dpr, 0, 0);
    const sx = shake ? rnd(-shake, shake) : 0, sy = shake ? rnd(-shake, shake) : 0;
    cx.save(); cx.translate(sx, sy);
    drawSky(now);
    drawCity(now);
    drawBin();
    if (g.waking) {
      const p = 0.4 + 0.4 * Math.sin(now / 120);
      cx.globalAlpha = p; cx.fillStyle = "#9fffb0"; cx.font = `600 ${Math.max(10, S * 0.22)}px Inter, system-ui, sans-serif`; cx.textAlign = "center";
      cx.fillText("waking an agent…", g.waking.x, g.waking.y - 6); cx.globalAlpha = 1;
    }
    for (const a of agents) drawAgent(a, now);
    for (const p of parts) { cx.globalAlpha = 1 - p.t / p.life; cx.fillStyle = p.color; cx.fillRect(p.x, p.y, p.r, p.r); }
    cx.globalAlpha = 1;
    for (const r of rings) {
      const k = r.t / 0.3;
      cx.strokeStyle = `rgba(255,255,255,${1 - k})`; cx.lineWidth = 3; cx.beginPath(); cx.arc(r.x, r.y, 8 + k * 26, 0, 7); cx.stroke();
      cx.strokeStyle = `rgba(255,90,90,${1 - k})`; cx.lineWidth = 2;
      cx.beginPath(); cx.moveTo(r.x - 18, r.y); cx.lineTo(r.x - 6, r.y); cx.moveTo(r.x + 6, r.y); cx.lineTo(r.x + 18, r.y); cx.moveTo(r.x, r.y - 18); cx.lineTo(r.x, r.y - 6); cx.moveTo(r.x, r.y + 6); cx.lineTo(r.x, r.y + 18); cx.stroke();
    }
    for (const t of texts) {
      cx.globalAlpha = Math.min(1, 2 * (1 - t.t / t.life));
      cx.font = `800 ${t.size}px Inter, system-ui, sans-serif`; cx.textAlign = "center";
      cx.lineWidth = 3; cx.strokeStyle = "#0a0b2acc"; cx.strokeText(t.text, t.x, t.y); cx.fillStyle = t.color; cx.fillText(t.text, t.x, t.y);
    }
    cx.globalAlpha = 1;
    cx.restore();
    if (flash) { cx.fillStyle = `rgba(160,255,255,${flash * 0.18})`; cx.fillRect(0, 0, W, H); }
  }

  let last = performance.now();
  function frame(now) {
    const dt = Math.min(0.05, (now - last) / 1000);
    last = now;
    update(dt, now);
    draw(now);
    requestAnimationFrame(frame);
  }
  requestAnimationFrame(frame);

  canvas.addEventListener("pointerdown", (e) => { e.preventDefault(); const r = canvas.getBoundingClientRect(); shoot(e.clientX - r.left, e.clientY - r.top); });
  return { start, stop, say, state: g, agents, timeLeft: () => Math.max(0, (g.ends - performance.now()) / 1000) };
}
