// Gateway Defense on the phone: draws sim.js's game on a 2D canvas, neon-cyber style,
// and turns taps into builds. The page passes createSim in (so its import carries the
// deploy's cache-busting version) and gets kills, leaks and waves through hooks.
"use strict";

const TAU = Math.PI * 2;
const BADGE = { jailbreak: "💬", burner: "🔥", escaper: "📤", runaway: "💤", obfus: "🔒", burst: "💥", boss: "👑" };

// startGame runs a game on canvas. hooks: onHud(hud), onKill(kill), onLeak(creep, lost),
// onWave(n, cleared, wave), onEnd(result), onPad(padIndex), onTower(towerIndex),
// onBoss(), onCombo(n), sfx(kind).
export function startGame(canvas, rules, hooks, createSim) {
  const ctx = canvas.getContext("2d");
  const M = rules.map, W = M.w, H = M.h;
  const call = (name, ...a) => hooks[name] && hooks[name](...a);
  const fx = [], floats = [], pops = [];
  let shake = 0, hurt = 0, bossFlash = 0, combo = { n: 0, t: 0 }, sel = null;

  let sim = null; // set right below; the hooks only run once it is
  sim = createSim(rules, {
    onHud: hud,
    onKill(k) {
      burst(k.x, k.y, k.color, k.boss ? 90 : 18);
      ring(k.x, k.y, k.color, k.boss ? 2.2 : 0.7);
      pop(`+$${k.bounty}`, k.x, k.y - 0.15, "#fde68a");
      if (k.combo >= 3) { combo = { n: k.combo, t: 1 }; call("onCombo", k.combo); }
      if (k.boss) { shake = Math.max(shake, 0.9); bossFlash = 1; }
      call("onKill", k);
    },
    onLeak(k, lost, x, y) {
      shake = Math.max(shake, lost > 2 ? 1 : 0.55);
      hurt = 1;
      burst(x, y, "#ff4d5e", 26);
      call("onLeak", k, lost);
    },
    onWave: (n, cleared, w) => call("onWave", n, cleared, w),
    onEnd: (r) => call("onEnd", r),
    sfx: (k) => call("sfx", k),
    fx(kind, d) {
      switch (kind) {
        case "build": case "upgrade": {
          const [x, y] = M.pads[d.pad], col = sim.towerKind[d.id].color;
          burst(x + 0.5, y + 0.5, kind === "upgrade" ? "#fde68a" : col, kind === "upgrade" ? 26 : 16);
          ring(x + 0.5, y + 0.5, kind === "upgrade" ? "#fde68a" : col, 1);
          if (kind === "upgrade") pop(`LEVEL ${d.level}`, x + 0.5, y + 0.1, "#fde68a");
          break;
        }
        case "beam": fx.push({ kind: "beam", ...d, t: 0, life: 0.22, seed: Math.random() * 99 }); break;
        case "pulse": fx.push({ kind: "pulse", ...d, t: 0, life: 0.45 }); break;
        case "hit":
          if (d.tower === "guard") fx.push({ kind: "hex", x: d.x, y: d.y, t: 0, life: 0.3, big: d.weak });
          else if (d.tower === "egress") fx.push({ kind: "wall", x: d.x, y: d.y, t: 0, life: 0.4, ang: laneAngle(d.x, d.y) });
          else if (d.tower === "suspend") burst(d.x, d.y, "#c4b5fd", 8, 0.5);
          if (d.weak) sparks(d.x, d.y, sim.towerKind[d.tower].color, 5);
          break;
        case "deflect": fx.push({ kind: "deflect", x: d.x, y: d.y, t: 0, life: 0.35 }); break;
        case "split": burst(d.x, d.y, "#fb923c", 14); ring(d.x, d.y, "#fb923c", 0.9); pop("SPLIT!", d.x, d.y - 0.3, "#fdba74"); break;
        case "summon": ring(d.x, d.y, "#e879f9", 2.6); burst(d.x, d.y, "#e879f9", 30); pop(`SUMMONS ${d.n}!`, d.x, d.y - 0.4, "#f5d0fe"); shake = Math.max(shake, 0.4); break;
        case "bossIn": shake = Math.max(shake, 0.8); bossFlash = 1; call("onBoss"); break;
      }
    },
  });
  const st = () => sim.st;

  function hud() {
    if (!sim) return;
    const s = st(), w = waveName(s.phase === "build" ? s.wave + 1 : Math.max(1, s.wave));
    call("onHud", { money: s.money, lives: s.lives, maxLives: rules.lives, score: s.score, wave: s.wave, waves: rules.waves.length,
      phase: s.phase, auto: Math.ceil(s.auto), fast: s.fast, waveName: w, endless: s.wave > rules.waves.length, won: s.won });
  }
  function waveName(n) {
    if (n <= rules.waves.length) return rules.waves[n - 1].name;
    return "Endless " + (n - rules.waves.length);
  }
  function laneAngle(x, y) {
    let best = 0, bd = 1e9;
    for (let s = 0; s < sim.LANE; s += 0.2) {
      const [px, py, dx, dy] = sim.posAt(s), d = Math.hypot(px - x, py - y);
      if (d < bd) { bd = d; best = Math.atan2(dy, dx); }
    }
    return best;
  }

  // ---- effects ----
  function burst(x, y, color, n, speed = 1) {
    for (let i = 0; i < n && fx.length < 500; i++) {
      const a = Math.random() * TAU, v = (0.8 + Math.random() * 2.6) * speed;
      fx.push({ kind: "bit", x, y, vx: Math.cos(a) * v, vy: Math.sin(a) * v, t: 0, life: 0.4 + Math.random() * 0.45, color, sz: 1.5 + Math.random() * 2.5 });
    }
  }
  const sparks = (x, y, color, n) => burst(x, y, color, n, 1.6);
  function ring(x, y, color, r) { fx.push({ kind: "ring", x, y, t: 0, life: 0.45, color, r }); }
  function pop(text, x, y, color) {
    if (pops.length > 24) pops.shift();
    pops.push({ text, x, y, color, t: 0, life: 0.9 });
  }
  // floater shows a real call's result rising over a spot. Two at most share a
  // neighbourhood: a third pushes out the oldest, so they never pile up unreadably.
  function floater(text, x, y, color = "#fff", life = 1.8) {
    const near = (f) => Math.abs(f.x - x) < 3 && Math.abs(f.y - y) < 1.6;
    const close = floats.filter(near);
    if (close.length >= 2) floats.splice(floats.indexOf(close[0]), 1);
    if (floats.length > 10) floats.shift();
    const lift = floats.some((f) => near(f) && f.t < 0.9) ? 0.75 : 0;
    floats.push({ text, x, y: y - lift, color, t: 0, life });
  }

  // ---- layout and the cached background ----
  let ts = 32, ox = 0, oy = 0, dpr = 1, cw = 0, ch = 0;
  const bg = document.createElement("canvas"), bgc = bg.getContext("2d");
  function layout(top, bottom) {
    dpr = Math.min(window.devicePixelRatio || 1, 2);
    cw = canvas.clientWidth; ch = canvas.clientHeight;
    canvas.width = Math.round(cw * dpr); canvas.height = Math.round(ch * dpr);
    const availH = ch - top - bottom;
    ts = Math.floor(Math.min((cw - 12) / W, availH / (H + 0.9)));
    ox = Math.round((cw - ts * W) / 2);
    oy = Math.round(top + (availH - ts * H) / 2 + ts * 0.45);
    paintBackground();
  }
  const X = (x) => ox + x * ts, Y = (y) => oy + y * ts;

  function rr(c, x, y, w, h, r) {
    c.beginPath();
    c.moveTo(x + r, y);
    c.arcTo(x + w, y, x + w, y + h, r);
    c.arcTo(x + w, y + h, x, y + h, r);
    c.arcTo(x, y + h, x, y, r);
    c.arcTo(x, y, x + w, y, r);
    c.closePath();
  }
  function lanePath(c) {
    c.beginPath();
    sim.pts.forEach(([x, y], i) => (i ? c.lineTo(X(x), Y(y)) : c.moveTo(X(x), Y(y))));
  }

  // The static layers: night gradient, a near grid, the board's circuit tiles and the
  // lane's body with neon edges. Animated layers go on top each frame.
  function paintBackground() {
    bg.width = canvas.width; bg.height = canvas.height;
    const c = bgc;
    c.setTransform(dpr, 0, 0, dpr, 0, 0);
    const g = c.createLinearGradient(0, 0, 0, ch);
    g.addColorStop(0, "#0a0f24"); g.addColorStop(0.5, "#070b1c"); g.addColorStop(1, "#050813");
    c.fillStyle = g; c.fillRect(0, 0, cw, ch);
    const v = c.createRadialGradient(cw / 2, ch * 0.45, ch * 0.1, cw / 2, ch * 0.45, ch * 0.75);
    v.addColorStop(0, "rgba(40,60,140,0.25)"); v.addColorStop(1, "rgba(0,0,0,0)");
    c.fillStyle = v; c.fillRect(0, 0, cw, ch);
    // Board tiles: dark plates with a faint circuit trace each.
    for (let y = 0; y < H; y++) for (let x = 0; x < W; x++) {
      c.fillStyle = (x + y) % 2 ? "rgba(22,32,64,0.55)" : "rgba(18,26,54,0.55)";
      c.fillRect(X(x) + 1, Y(y) + 1, ts - 2, ts - 2);
      if ((x * 7 + y * 3) % 5 === 0) {
        c.strokeStyle = "rgba(90,140,255,0.08)"; c.lineWidth = 1;
        c.beginPath(); c.moveTo(X(x) + ts * 0.2, Y(y) + ts * 0.8); c.lineTo(X(x) + ts * 0.5, Y(y) + ts * 0.8); c.lineTo(X(x) + ts * 0.65, Y(y) + ts * 0.6);
        c.stroke();
        c.fillStyle = "rgba(90,140,255,0.12)"; c.beginPath(); c.arc(X(x) + ts * 0.65, Y(y) + ts * 0.6, 1.5, 0, TAU); c.fill();
      }
    }
    // The lane: soft outer glow, dark body, neon rails.
    c.lineJoin = "round"; c.lineCap = "round";
    for (const [w, col] of [[1.25, "rgba(56,189,248,0.05)"], [1.08, "rgba(56,189,248,0.09)"], [0.94, "#04060f"], [0.86, "#0b1430"], [0.7, "#0e1a3c"]]) {
      lanePath(c); c.strokeStyle = col; c.lineWidth = ts * w; c.stroke();
    }
    // Rails: a wide cyan stroke under a slightly narrower dark one leaves two neon edges.
    lanePath(c); c.strokeStyle = "rgba(34,211,238,0.7)"; c.lineWidth = ts * 0.74; c.stroke();
    lanePath(c); c.strokeStyle = "#0d1838"; c.lineWidth = ts * 0.68; c.stroke();
  }

  // glow is a cached soft round sprite in a colour, drawn additively for neon light.
  const glowCache = new Map();
  function glow(color) {
    let s = glowCache.get(color);
    if (!s) {
      s = document.createElement("canvas"); s.width = s.height = 64;
      const c = s.getContext("2d"), g = c.createRadialGradient(32, 32, 0, 32, 32, 32);
      g.addColorStop(0, color); g.addColorStop(0.35, color + "88"); g.addColorStop(1, color + "00");
      c.fillStyle = g; c.fillRect(0, 0, 64, 64);
      glowCache.set(color, s);
    }
    return s;
  }
  function light(x, y, r, color, a = 1) {
    ctx.globalCompositeOperation = "lighter";
    ctx.globalAlpha = a;
    ctx.drawImage(glow(color), x - r, y - r, r * 2, r * 2);
    ctx.globalAlpha = 1;
    ctx.globalCompositeOperation = "source-over";
  }

  // ---- animated layers ----
  const motes = Array.from({ length: 34 }, () => ({ x: Math.random(), y: Math.random(), v: 0.01 + Math.random() * 0.03, r: 0.5 + Math.random() * 1.5 }));
  function drawAmbient(now, dt) {
    // A far grid drifting slowly behind the board (parallax), and rising data motes.
    const sp = ts * 0.5, off = (now / 90) % sp;
    ctx.strokeStyle = "rgba(80,120,255,0.07)"; ctx.lineWidth = 1;
    ctx.beginPath();
    for (let x = -sp + (off % sp); x < cw; x += sp) { ctx.moveTo(x, 0); ctx.lineTo(x, ch); }
    for (let y = -sp + ((off * 0.6) % sp); y < ch; y += sp) { ctx.moveTo(0, y); ctx.lineTo(cw, y); }
    ctx.stroke();
    for (const m of motes) {
      m.y -= m.v * dt; if (m.y < -0.02) { m.y = 1.02; m.x = Math.random(); }
      ctx.fillStyle = "rgba(125,211,252,0.35)";
      ctx.fillRect(m.x * cw, m.y * ch, m.r, m.r * 3);
    }
  }

  function drawLane(now) {
    // Flowing data packets along the lane, and a scanning dash on the centre line.
    ctx.save();
    lanePath(ctx);
    ctx.setLineDash([ts * 0.12, ts * 0.42]);
    ctx.lineDashOffset = -now / 25;
    ctx.strokeStyle = "rgba(103,232,249,0.55)";
    ctx.lineWidth = Math.max(1.5, ts * 0.05);
    ctx.lineCap = "round";
    ctx.stroke();
    ctx.restore();
    const n = 14, speed = 1.6;
    for (let i = 0; i < n; i++) {
      const s = ((now / 1000) * speed + (i * sim.LANE) / n) % sim.LANE;
      const [x, y] = sim.posAt(s);
      light(X(x), Y(y), ts * 0.28, "#22d3ee", 0.55);
      ctx.fillStyle = "#cffafe";
      ctx.fillRect(X(x) - 1.5, Y(y) - 1.5, 3, 3);
    }
    // The entry portal: the internet, where the traffic comes from.
    const [ex, ey0] = sim.pts[0], ey = Math.max(ey0, -0.2) + 0.15;
    const px = X(ex), py = Y(ey);
    light(px, py, ts * 0.9, "#f472b6", 0.5 + Math.sin(now / 300) * 0.15);
    ctx.save(); ctx.translate(px, py);
    for (let i = 0; i < 3; i++) {
      ctx.rotate(now / (700 + i * 300) * (i % 2 ? -1 : 1));
      ctx.strokeStyle = ["#f472b6", "#a78bfa", "#38bdf8"][i]; ctx.lineWidth = 2;
      ctx.beginPath(); ctx.arc(0, 0, ts * (0.32 + i * 0.1), 0, TAU * 0.7); ctx.stroke();
    }
    ctx.restore();
    ctx.font = `800 ${Math.round(ts * 0.26)}px Inter, system-ui, sans-serif`;
    ctx.textAlign = "left"; ctx.textBaseline = "middle";
    const glitch = Math.random() < 0.04 ? (Math.random() - 0.5) * 4 : 0;
    ctx.fillStyle = "#f9a8d4"; ctx.fillText("THE INTERNET", px + ts * 0.7 + glitch, py);
  }

  // The cluster core: a hexagon reactor that pulses faster and cracks as lives drop.
  const cracks = Array.from({ length: 9 }, (_, i) => {
    const a = (i / 9) * TAU + Math.random() * 0.4, pts = [[0, 0]];
    let r = 0;
    for (let j = 0; j < 3; j++) { r += 0.12 + Math.random() * 0.12; const aa = a + (Math.random() - 0.5) * 0.7; pts.push([Math.cos(aa) * r, Math.sin(aa) * r]); }
    return pts;
  });
  function hex(c, x, y, r, rot = 0) {
    c.beginPath();
    for (let i = 0; i < 6; i++) { const a = rot + (i / 6) * TAU; c[i ? "lineTo" : "moveTo"](x + Math.cos(a) * r, y + Math.sin(a) * r); }
    c.closePath();
  }
  function drawCluster(now) {
    const s = st(), [cx, cy0] = sim.pts[sim.pts.length - 1], x = X(cx), y = Y(cy0 + 0.1);
    const f = Math.max(0, s.lives / rules.lives);
    const col = f > 0.6 ? "#4ade80" : f > 0.3 ? "#fbbf24" : "#f87171";
    const beat = 0.5 + 0.5 * Math.sin(now / (130 + 420 * f));
    light(x, y, ts * (1.3 + beat * 0.3 + hurt * 0.6), hurt > 0.05 ? "#ff4d5e" : col, 0.55 + beat * 0.25);
    const R = ts * 0.62;
    hex(ctx, x, y, R, now / 4000); ctx.fillStyle = "#081226"; ctx.fill();
    ctx.strokeStyle = col; ctx.lineWidth = 2.5; ctx.stroke();
    hex(ctx, x, y, R * 0.68, -now / 2500); ctx.strokeStyle = col + "aa"; ctx.lineWidth = 1.5; ctx.stroke();
    const core = ctx.createRadialGradient(x, y, 0, x, y, R * 0.5);
    core.addColorStop(0, "#ffffff"); core.addColorStop(0.4, col); core.addColorStop(1, col + "00");
    ctx.fillStyle = core; ctx.beginPath(); ctx.arc(x, y, R * (0.35 + beat * 0.12), 0, TAU); ctx.fill();
    // Cracks, one more per lost ninth of the lives.
    const n = Math.round((1 - f) * cracks.length);
    ctx.strokeStyle = "rgba(255,90,100,0.9)"; ctx.lineWidth = 1.5;
    for (let i = 0; i < n; i++) {
      ctx.beginPath();
      cracks[i].forEach(([a, b], j) => ctx[j ? "lineTo" : "moveTo"](x + a * ts, y + b * ts));
      ctx.stroke();
    }
    ctx.fillStyle = "#e2fbe8";
    ctx.font = `800 ${Math.round(ts * 0.22)}px Inter, system-ui, sans-serif`;
    ctx.textAlign = "center"; ctx.textBaseline = "middle";
    ctx.fillText(`YOUR CLUSTER · ${s.lives}`, x, y + R + ts * 0.26);
  }

  function drawPads(now) {
    M.pads.forEach(([px, py], i) => {
      if (sim.towerAt(i) >= 0) return;
      const on = sel && sel.pad === i, x = X(px + 0.5), y = Y(py + 0.5), r = ts * 0.36;
      hex(ctx, x, y, r, Math.PI / 6);
      ctx.fillStyle = on ? "rgba(56,189,248,0.25)" : "rgba(30,50,110,0.35)"; ctx.fill();
      ctx.setLineDash([3, 3]); ctx.lineDashOffset = now / 80;
      ctx.strokeStyle = on ? "#7dd3fc" : "rgba(96,165,250,0.5)"; ctx.lineWidth = 1.5; ctx.stroke(); ctx.setLineDash([]);
      ctx.fillStyle = on ? "#e0f2fe" : "rgba(147,197,253,0.7)";
      ctx.font = `700 ${Math.round(ts * 0.34)}px Inter, system-ui, sans-serif`;
      ctx.textAlign = "center"; ctx.textBaseline = "middle";
      ctx.fillText("+", x, y + 1);
    });
  }

  // ---- towers: each control has its own art; levels add rings, bricks or shards ----
  function drawTowers(now) {
    const s = st();
    s.towers.forEach((t, i) => {
      const k = sim.towerKind[t.id], [px, py] = M.pads[t.pad], x = X(px + 0.5), y = Y(py + 0.5), L = t.level;
      if (sel && sel.tower === i) {
        ctx.beginPath(); ctx.arc(x, y, sim.rangeOf(t) * ts, 0, TAU);
        ctx.fillStyle = k.color + "18"; ctx.fill();
        ctx.setLineDash([5, 4]); ctx.strokeStyle = k.color + "cc"; ctx.lineWidth = 1.5; ctx.stroke(); ctx.setLineDash([]);
      }
      light(x, y, ts * (0.62 + L * 0.08 + t.kick * 0.15), k.color, 0.35 + t.kick * 0.3);
      const sc = 1 + t.kick * 0.07;
      ctx.save(); ctx.translate(x, y); ctx.scale(sc, sc);
      // Base plate.
      rr(ctx, -ts * 0.42, -ts * 0.42, ts * 0.84, ts * 0.84, ts * 0.2);
      ctx.fillStyle = "#0a1230"; ctx.fill();
      ctx.lineWidth = L >= 3 ? 3 : 2; ctx.strokeStyle = k.color; ctx.stroke();
      if (L >= 2) { rr(ctx, -ts * 0.36, -ts * 0.36, ts * 0.72, ts * 0.72, ts * 0.16); ctx.strokeStyle = k.color + "66"; ctx.lineWidth = 1; ctx.stroke(); }
      t.spin += 0.016 * (1 + t.kick * 3);
      switch (t.id) {
        case "guard": drawGuard(t, k, L, now); break;
        case "limit": drawLimit(t, k, L, now); break;
        case "egress": drawEgress(t, k, L, now); break;
        case "suspend": drawSuspend(t, k, L, now); break;
      }
      // Level pips.
      for (let l = 0; l < L; l++) {
        ctx.fillStyle = "#fde68a";
        ctx.beginPath(); ctx.arc(-ts * 0.2 + l * ts * 0.2, ts * 0.34, ts * 0.05, 0, TAU); ctx.fill();
      }
      ctx.restore();
    });
  }
  function drawGuard(t, k, L, now) {
    // A hexagon shield emitter: one hex per level, the inner one pulsing; level 3 gets
    // orbiting nodes. A short emitter points at the target.
    for (let l = 0; l < L; l++) {
      hex(ctx, 0, 0, ts * (0.14 + l * 0.08), t.spin * (l % 2 ? -1 : 1) + l);
      ctx.strokeStyle = l ? k.color + "aa" : k.color; ctx.lineWidth = l ? 1.5 : 2.5; ctx.stroke();
    }
    hex(ctx, 0, 0, ts * (0.08 + 0.02 * Math.sin(now / 200)), 0); ctx.fillStyle = "#fdf2f8"; ctx.fill();
    if (L >= 3) for (let i = 0; i < 3; i++) {
      const a = t.spin * 2 + (i / 3) * TAU;
      ctx.fillStyle = k.color; ctx.beginPath(); ctx.arc(Math.cos(a) * ts * 0.32, Math.sin(a) * ts * 0.32, ts * 0.045, 0, TAU); ctx.fill();
    }
    ctx.save(); ctx.rotate(t.ang); ctx.fillStyle = k.color;
    rr(ctx, ts * 0.2, -ts * 0.045, ts * 0.2 - t.kick * ts * 0.05, ts * 0.09, ts * 0.04); ctx.fill(); ctx.restore();
  }
  function drawLimit(t, k, L, now) {
    // A throttle dial: tick marks, a ring per level, and a needle that swings when it fires.
    for (let l = 0; l < L; l++) {
      ctx.beginPath(); ctx.arc(0, 0, ts * (0.16 + l * 0.07), 0, TAU);
      ctx.strokeStyle = l ? k.color + "88" : k.color; ctx.lineWidth = l ? 1.2 : 2.5; ctx.stroke();
    }
    for (let i = 0; i < 8; i++) {
      const a = (i / 8) * TAU;
      ctx.strokeStyle = k.color + "aa"; ctx.lineWidth = 1.2;
      ctx.beginPath(); ctx.moveTo(Math.cos(a) * ts * 0.27, Math.sin(a) * ts * 0.27); ctx.lineTo(Math.cos(a) * ts * 0.32, Math.sin(a) * ts * 0.32); ctx.stroke();
    }
    const a = -Math.PI * 0.8 + (Math.sin(now / 400 + t.spin) * 0.3 + 0.5 + t.kick * 0.5) * Math.PI * 1.2;
    ctx.strokeStyle = "#fff7ed"; ctx.lineWidth = 2;
    ctx.beginPath(); ctx.moveTo(0, 0); ctx.lineTo(Math.cos(a) * ts * 0.22, Math.sin(a) * ts * 0.22); ctx.stroke();
    ctx.fillStyle = k.color; ctx.beginPath(); ctx.arc(0, 0, ts * 0.05, 0, TAU); ctx.fill();
  }
  function drawEgress(t, k, L, now) {
    // A firewall: rows of bricks (one more per level) with flames flickering on top.
    const rows = L + 1, bw = ts * 0.18, bh = ts * 0.1, top = ts * 0.2 - rows * bh;
    for (let r = 0; r < rows; r++) for (let c = 0; c < 3; c++) {
      const x = -bw * 1.5 + c * bw + (r % 2 ? bw / 2 : 0) - (r % 2 && c === 2 ? bw : 0), y = top + r * bh;
      ctx.fillStyle = r % 2 ? "#1e40af" : "#1d4ed8"; ctx.fillRect(x + 1, y + 1, bw - 2, bh - 2);
      ctx.strokeStyle = k.color; ctx.lineWidth = 1; ctx.strokeRect(x + 1, y + 1, bw - 2, bh - 2);
    }
    for (let i = 0; i < 3; i++) {
      const fx0 = -bw + i * bw, h = ts * (0.12 + 0.07 * Math.sin(now / 90 + i * 2 + t.spin) + t.kick * 0.06);
      ctx.fillStyle = i % 2 ? "#38bdf8" : "#7dd3fc";
      ctx.beginPath(); ctx.moveTo(fx0 - bw * 0.35, top); ctx.quadraticCurveTo(fx0, top - h * 1.3, fx0 + bw * 0.35, top); ctx.fill();
    }
  }
  function drawSuspend(t, k, L, now) {
    // A crystal emitter with ice shards orbiting it, two more per level.
    const r = ts * 0.2;
    ctx.save(); ctx.rotate(t.ang + Math.PI / 2);
    ctx.beginPath(); ctx.moveTo(0, -r * 1.25); ctx.lineTo(r * 0.7, 0); ctx.lineTo(0, r * 1.05); ctx.lineTo(-r * 0.7, 0); ctx.closePath();
    const g = ctx.createLinearGradient(0, -r, 0, r); g.addColorStop(0, "#f5f3ff"); g.addColorStop(1, k.color);
    ctx.fillStyle = g; ctx.fill(); ctx.strokeStyle = "#ede9fe"; ctx.lineWidth = 1.2; ctx.stroke();
    ctx.restore();
    const n = L * 2;
    for (let i = 0; i < n; i++) {
      const a = t.spin * 1.6 + (i / n) * TAU, rr2 = ts * 0.3;
      ctx.save(); ctx.translate(Math.cos(a) * rr2, Math.sin(a) * rr2); ctx.rotate(a);
      ctx.fillStyle = "#ddd6fe"; ctx.beginPath(); ctx.moveTo(0, -ts * 0.05); ctx.lineTo(ts * 0.025, 0); ctx.lineTo(0, ts * 0.05); ctx.lineTo(-ts * 0.025, 0); ctx.fill();
      ctx.restore();
    }
  }

  // ---- creeps ----
  function drawCreeps(now) {
    const s = st();
    for (const c of s.creeps) {
      const k = sim.creepKind[c.id], [x, y, dx, dy] = sim.posAt(c.s), boss = c.id === rules.boss.id;
      const r = ts * (boss ? 0.5 : c.id === "runaway" ? 0.3 : 0.26);
      const sx = X(x), sy = Y(y) + Math.sin(now / 160 + c.bob) * ts * 0.04;
      const stunned = s.t < c.stunUntil, slowed = s.t < c.slowUntil;
      light(sx, sy, r * 2.2, k.color, boss ? 0.8 : 0.45);
      ctx.fillStyle = "rgba(0,0,0,0.4)";
      ctx.beginPath(); ctx.ellipse(sx, Y(y) + r * 0.9, r * 0.85, r * 0.28, 0, 0, TAU); ctx.fill();
      // Body: a glossy blob; spiky for burst burners, plated for armour.
      const body = ctx.createRadialGradient(sx - r * 0.3, sy - r * 0.4, r * 0.1, sx, sy, r);
      body.addColorStop(0, c.flash > 0 ? "#ffffff" : "#ffffffaa"); body.addColorStop(0.35, c.flash > 0 ? "#ffffff" : stunned ? "#c4b5fd" : k.color); body.addColorStop(1, stunned ? "#6d28d9" : k.color);
      ctx.fillStyle = body;
      if (c.id === "burst") {
        ctx.beginPath();
        for (let i = 0; i < 16; i++) { const a = (i / 16) * TAU + now / 500, rr2 = i % 2 ? r : r * 1.22; ctx[i ? "lineTo" : "moveTo"](sx + Math.cos(a) * rr2, sy + Math.sin(a) * rr2); }
        ctx.closePath(); ctx.fill();
      } else { ctx.beginPath(); ctx.arc(sx, sy, r, 0, TAU); ctx.fill(); }
      if (k.armor) { ctx.strokeStyle = "#cbd5e1"; ctx.lineWidth = boss ? 4 : 2.5; ctx.setLineDash([r * 0.5, r * 0.25]); ctx.beginPath(); ctx.arc(sx, sy, r * 0.92, 0, TAU); ctx.stroke(); ctx.setLineDash([]); }
      if (k.shield) {
        hex(ctx, sx, sy, r * 1.35, now / 700);
        ctx.strokeStyle = c.deflect > 0 ? "#ffffff" : "rgba(232,121,249,0.85)"; ctx.lineWidth = c.deflect > 0 ? 3 : 1.8; ctx.stroke();
        ctx.fillStyle = "rgba(232,121,249,0.12)"; ctx.fill();
      }
      if (stunned) { ctx.strokeStyle = "#ede9fe"; ctx.lineWidth = 2; ctx.beginPath(); ctx.arc(sx, sy, r + 2, 0, TAU); ctx.stroke(); for (let i = 0; i < 3; i++) { const a = now / 300 + (i / 3) * TAU; ctx.fillStyle = "#e0e7ff"; ctx.fillRect(sx + Math.cos(a) * r * 1.2 - 2, sy + Math.sin(a) * r * 1.2 - 2, 4, 4); } }
      if (slowed) { ctx.strokeStyle = "#fb923c"; ctx.setLineDash([3, 3]); ctx.lineDashOffset = now / 60; ctx.lineWidth = 2; ctx.beginPath(); ctx.arc(sx, sy, r + 4, 0, TAU); ctx.stroke(); ctx.setLineDash([]); }
      // Eyes that look where it is going (angry ones for the boss).
      const n = Math.hypot(dx, dy) || 1, lx = (dx / n) * r * 0.12, ly = (dy / n) * r * 0.12;
      for (const side of [-1, 1]) {
        ctx.fillStyle = "#fff";
        ctx.beginPath(); ctx.arc(sx + side * r * 0.35, sy - r * 0.15, r * 0.24, 0, TAU); ctx.fill();
        ctx.fillStyle = "#0b1020";
        ctx.beginPath(); ctx.arc(sx + side * r * 0.35 + lx, sy - r * 0.15 + ly, r * 0.11, 0, TAU); ctx.fill();
        if (boss) { ctx.strokeStyle = "#0b1020"; ctx.lineWidth = 2.5; ctx.beginPath(); ctx.moveTo(sx + side * r * 0.12, sy - r * 0.45); ctx.lineTo(sx + side * r * 0.58, sy - r * 0.32); ctx.stroke(); }
      }
      ctx.font = `${Math.round(r * (boss ? 0.8 : 0.85))}px "Apple Color Emoji","Segoe UI Emoji","Noto Color Emoji",sans-serif`;
      ctx.textAlign = "center"; ctx.textBaseline = "middle";
      ctx.fillText(BADGE[c.id] || "", sx + r * 0.78, sy - r * 0.82);
      // Health.
      if (!boss) {
        const bw = r * 2, f = Math.max(0, c.hp / c.max);
        ctx.fillStyle = "#050814"; ctx.fillRect(sx - bw / 2, sy - r - 8, bw, 4);
        ctx.fillStyle = f > 0.5 ? "#4ade80" : f > 0.25 ? "#facc15" : "#f87171";
        ctx.fillRect(sx - bw / 2, sy - r - 8, bw * f, 4);
      }
    }
  }
  // The boss gets a health bar across the top of the board.
  function drawBossBar(now) {
    const b = st().creeps.find((c) => c.id === rules.boss.id);
    if (!b) return;
    const w = cw * 0.84, h = Math.max(16, ts * 0.42), x = (cw - w) / 2, y = oy - ts * 0.6, f = Math.max(0, b.hp / b.max);
    rr(ctx, x, y, w, h, h / 2); ctx.fillStyle = "rgba(20,4,10,0.9)"; ctx.fill();
    ctx.strokeStyle = "#ef4444"; ctx.lineWidth = 1.5; ctx.stroke();
    if (f > 0) {
      rr(ctx, x + 2, y + 2, Math.max(h - 4, (w - 4) * f), h - 4, (h - 4) / 2);
      const g = ctx.createLinearGradient(x, 0, x + w, 0); g.addColorStop(0, "#ef4444"); g.addColorStop(1, "#f97316");
      ctx.fillStyle = g; ctx.fill();
    }
    // The name rides inside the bar, so it never sits under the rank ticker above.
    ctx.font = `900 ${Math.round(h * 0.62)}px Inter, system-ui, sans-serif`;
    ctx.textAlign = "center"; ctx.textBaseline = "middle";
    ctx.lineWidth = 3; ctx.strokeStyle = "#1a0508";
    const label = `👑 ${rules.boss.name.toUpperCase()} · ${Math.ceil(f * 100)}%`;
    ctx.strokeText(label, cw / 2, y + h / 2 + 1);
    ctx.fillStyle = "#fff1f2"; ctx.fillText(label, cw / 2, y + h / 2 + 1 + Math.sin(now / 120) * 0.5);
  }

  // ---- shots and effects ----
  function drawShots(now) {
    for (const sh of st().shots) {
      const k = sim.towerKind[sh.kind], x = X(sh.x), y = Y(sh.y);
      light(x, y, ts * 0.32, k.color, 0.9);
      if (sh.kind === "egress") {
        // A flaming packet with a short tail.
        ctx.fillStyle = "#e0f2fe"; ctx.fillRect(x - ts * 0.07, y - ts * 0.05, ts * 0.14, ts * 0.1);
        ctx.fillStyle = "#38bdf8"; ctx.beginPath(); ctx.arc(x, y - ts * 0.07, ts * 0.05 + Math.sin(now / 50) * 1.5, 0, TAU); ctx.fill();
      } else {
        hex(ctx, x, y, ts * 0.08, now / 100); ctx.fillStyle = "#fdf2f8"; ctx.fill();
        ctx.strokeStyle = k.color; ctx.lineWidth = 1.5; ctx.stroke();
      }
    }
  }
  function drawFx(dt) {
    for (let i = fx.length - 1; i >= 0; i--) {
      const e = fx[i];
      e.t += dt;
      if (e.t >= e.life) { fx.splice(i, 1); continue; }
      const f = 1 - e.t / e.life;
      switch (e.kind) {
        case "bit":
          e.x += e.vx * dt; e.y += e.vy * dt; e.vx *= 0.9; e.vy *= 0.9;
          ctx.globalAlpha = f; ctx.fillStyle = e.color;
          ctx.fillRect(X(e.x) - e.sz / 2, Y(e.y) - e.sz / 2, e.sz, e.sz);
          break;
        case "ring":
          ctx.globalAlpha = f; ctx.strokeStyle = e.color; ctx.lineWidth = 2.5 * f + 0.5;
          ctx.beginPath(); ctx.arc(X(e.x), Y(e.y), ts * (0.2 + (1 - f) * e.r), 0, TAU); ctx.stroke();
          break;
        case "hex": // Prompt Guard's shield bubble zap
          ctx.globalAlpha = f; ctx.strokeStyle = "#f472b6"; ctx.lineWidth = e.big ? 3 : 2;
          hex(ctx, X(e.x), Y(e.y), ts * (0.3 + (1 - f) * (e.big ? 0.4 : 0.25)), e.t * 4); ctx.stroke();
          ctx.fillStyle = "rgba(244,114,182,0.15)"; ctx.fill();
          break;
        case "pulse": // Rate Limiter's throttle ring
          ctx.globalAlpha = f * 0.75; ctx.strokeStyle = "#fb923c"; ctx.lineWidth = 3;
          ctx.setLineDash([8, 6]); ctx.lineDashOffset = e.t * 60;
          ctx.beginPath(); ctx.arc(X(e.x), Y(e.y), ts * e.r * (1 - f * 0.75), 0, TAU); ctx.stroke(); ctx.setLineDash([]);
          break;
        case "wall": { // Egress Wall's firewall across the lane
          const w = ts * 0.55;
          ctx.save(); ctx.translate(X(e.x), Y(e.y)); ctx.rotate(e.ang + Math.PI / 2);
          for (let j = -2; j <= 2; j++) {
            const h = ts * (0.25 + 0.15 * Math.sin(e.t * 40 + j)) * f;
            ctx.globalAlpha = f; ctx.fillStyle = j % 2 ? "#38bdf8" : "#bae6fd";
            ctx.beginPath(); ctx.moveTo((j / 2) * w - 4, 0); ctx.quadraticCurveTo((j / 2) * w, -h, (j / 2) * w + 4, 0); ctx.fill();
          }
          ctx.fillStyle = "#1d4ed8"; ctx.fillRect(-w - 3, -2, w * 2 + 6, 4);
          ctx.restore();
          break;
        }
        case "beam": { // Suspend Beam: a jagged freeze bolt
          const x1 = X(e.x), y1 = Y(e.y), x2 = X(e.x2), y2 = Y(e.y2), segs = 7;
          ctx.globalAlpha = f;
          for (const [w, col] of [[ts * 0.16 * f + 2, "rgba(167,139,250,0.35)"], [2.5, "#ede9fe"]]) {
            ctx.strokeStyle = col; ctx.lineWidth = w; ctx.lineJoin = "round";
            ctx.beginPath(); ctx.moveTo(x1, y1);
            for (let j = 1; j < segs; j++) {
              const p = j / segs, jx = Math.sin(e.seed + j * 12.9 + e.t * 30) * ts * 0.12;
              ctx.lineTo(x1 + (x2 - x1) * p + jx, y1 + (y2 - y1) * p - jx);
            }
            ctx.lineTo(x2, y2); ctx.stroke();
          }
          light(x2, y2, ts * 0.5, "#a78bfa", f);
          break;
        }
        case "deflect":
          ctx.globalAlpha = f; ctx.strokeStyle = "#e2e8f0"; ctx.lineWidth = 2;
          ctx.beginPath(); ctx.arc(X(e.x), Y(e.y), ts * (0.4 + (1 - f) * 0.2), 0, TAU); ctx.stroke();
          ctx.font = `800 ${Math.round(ts * 0.2)}px Inter, sans-serif`; ctx.fillStyle = "#e2e8f0"; ctx.textAlign = "center";
          ctx.fillText("SHIELDED", X(e.x), Y(e.y) - ts * 0.55);
          break;
      }
      ctx.globalAlpha = 1;
    }
    // Money and point pops: small, quick, no box.
    ctx.textAlign = "center"; ctx.textBaseline = "middle";
    for (let i = pops.length - 1; i >= 0; i--) {
      const p = pops[i];
      p.t += dt;
      if (p.t >= p.life) { pops.splice(i, 1); continue; }
      ctx.globalAlpha = Math.min(1, 2.5 * (1 - p.t / p.life));
      ctx.font = `800 ${Math.round(Math.max(11, ts * 0.28))}px Inter, system-ui, sans-serif`;
      ctx.lineWidth = 3; ctx.strokeStyle = "#050814"; ctx.strokeText(p.text, X(p.x), Y(p.y) - p.t * ts * 0.9);
      ctx.fillStyle = p.color; ctx.fillText(p.text, X(p.x), Y(p.y) - p.t * ts * 0.9);
      ctx.globalAlpha = 1;
    }
    // Real-call results, boxed so they read as proof.
    for (let i = floats.length - 1; i >= 0; i--) {
      const fl = floats[i];
      fl.t += dt;
      if (fl.t >= fl.life) { floats.splice(i, 1); continue; }
      const y = Y(fl.y) - fl.t * ts * 0.7;
      ctx.globalAlpha = Math.min(1, 2 * (1 - fl.t / fl.life));
      ctx.font = `800 ${Math.round(Math.max(11, ts * 0.3))}px Inter, system-ui, sans-serif`;
      const w = ctx.measureText(fl.text).width + 14;
      const x = Math.min(cw - w / 2 - 4, Math.max(w / 2 + 4, X(fl.x)));
      rr(ctx, x - w / 2, y - ts * 0.25, w, ts * 0.5, ts * 0.14);
      ctx.fillStyle = "rgba(5,8,20,0.9)"; ctx.fill();
      ctx.strokeStyle = fl.color; ctx.lineWidth = 1.5; ctx.stroke();
      ctx.fillStyle = fl.color; ctx.fillText(fl.text, x, y + 1);
      ctx.globalAlpha = 1;
    }
  }
  function drawOverlay(dt) {
    // Combo, red vignette on a leak, white flash on the boss.
    if (combo.t > 0) {
      combo.t = Math.max(0, combo.t - dt * 0.9);
      const s = 1 + (1 - combo.t) * 0.15 + (combo.t > 0.85 ? (combo.t - 0.85) * 3 : 0);
      ctx.save(); ctx.translate(cw / 2, oy + ts * 0.9); ctx.scale(s, s);
      ctx.globalAlpha = Math.min(1, combo.t * 2);
      ctx.font = `900 ${Math.round(ts * 0.62)}px Inter, system-ui, sans-serif`; ctx.textAlign = "center"; ctx.textBaseline = "middle";
      ctx.lineWidth = 5; ctx.strokeStyle = "#1e1b4b"; ctx.strokeText(`×${combo.n} COMBO`, 0, 0);
      const g = ctx.createLinearGradient(-80, 0, 80, 0); g.addColorStop(0, "#f472b6"); g.addColorStop(1, "#fde68a");
      ctx.fillStyle = g; ctx.fillText(`×${combo.n} COMBO`, 0, 0);
      ctx.restore(); ctx.globalAlpha = 1;
    }
    if (hurt > 0.02) {
      const v = ctx.createRadialGradient(cw / 2, ch / 2, ch * 0.3, cw / 2, ch / 2, ch * 0.75);
      v.addColorStop(0, "rgba(255,0,40,0)"); v.addColorStop(1, `rgba(255,0,40,${0.45 * hurt})`);
      ctx.fillStyle = v; ctx.fillRect(0, 0, cw, ch);
    }
    if (bossFlash > 0.02) { ctx.fillStyle = `rgba(255,220,230,${0.35 * bossFlash})`; ctx.fillRect(0, 0, cw, ch); }
  }

  // ---- the frame loop ----
  let last = performance.now(), acc = 0, raf = 0;
  function frame(now) {
    const real = Math.min(0.1, (now - last) / 1000);
    last = now;
    const s = st();
    if (!s.paused && s.phase !== "over") {
      acc += real * (s.fast ? rules.fastSpeed : 1);
      while (acc >= 1 / 60) { sim.step(1 / 60); acc -= 1 / 60; if (st().phase === "over") break; }
    }
    hurt = Math.max(0, hurt - real * 1.6);
    bossFlash = Math.max(0, bossFlash - real * 2);
    shake = Math.max(0, shake - real * 2.2);
    const sx = shake ? (Math.random() - 0.5) * shake * ts * 0.35 : 0, sy = shake ? (Math.random() - 0.5) * shake * ts * 0.35 : 0;
    ctx.setTransform(1, 0, 0, 1, 0, 0);
    ctx.drawImage(bg, 0, 0);
    ctx.setTransform(dpr, 0, 0, dpr, sx * dpr, sy * dpr);
    drawAmbient(now, real);
    drawLane(now);
    drawPads(now);
    drawCluster(now);
    drawTowers(now);
    drawCreeps(now);
    drawShots(now);
    drawFx(real);
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    drawBossBar(now);
    drawOverlay(real);
    raf = requestAnimationFrame(frame);
  }

  // Taps pick a pad or a tower.
  canvas.addEventListener("pointerdown", (e) => {
    const r = canvas.getBoundingClientRect();
    const x = Math.floor((e.clientX - r.left - ox) / ts), y = Math.floor((e.clientY - r.top - oy) / ts);
    const pad = M.pads.findIndex(([px, py]) => px === x && py === y);
    if (pad < 0) { sel = null; call("onPad", -1); return; }
    const ti = sim.towerAt(pad);
    sel = ti >= 0 ? { tower: ti, pad } : { pad };
    if (ti >= 0) call("onTower", ti);
    else call("onPad", pad);
  });

  hud();
  raf = requestAnimationFrame(frame);
  return {
    layout,
    build: (pad, id) => sim.build(pad, id),
    upgrade: (i) => sim.upgrade(i),
    sell: (i) => { const ok = sim.sell(i); sel = null; return ok; },
    startWave: () => sim.startWave(),
    reset: () => { sim.reset(); fx.length = floats.length = pops.length = 0; sel = null; shake = hurt = bossFlash = 0; combo = { n: 0, t: 0 }; },
    quit: () => sim.quit(),
    floater,
    // padScreen is pad i's centre in CSS pixels on the canvas (for scripted taps).
    padScreen: (i) => [X(M.pads[i][0] + 0.5), Y(M.pads[i][1] + 0.5)],
    deselect: () => { sel = null; },
    setFast: (on) => { st().fast = on; hud(); },
    setPaused: (on) => { st().paused = on; },
    tower: (i) => {
      const t = st().towers[i];
      if (!t) return null;
      const k = sim.towerKind[t.id];
      return { ...k, level: t.level, upCost: t.level < k.maxLevel ? sim.upCost(t) : 0, sell: sim.sellValue(t), range: sim.rangeOf(t) };
    },
    money: () => st().money,
    score: () => st().score,
    phase: () => st().phase,
    wave: () => st().wave,
    sim,
    stop: () => cancelAnimationFrame(raf),
  };
}
