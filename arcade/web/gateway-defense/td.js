// Gateway Defense on the phone: the tower defense simulation and its 2D canvas. The rules
// (creeps, towers, waves, the map) come from the server so both sides agree; this file
// only plays them. Kills are reported through hooks.onKill so the page can fire the real
// call behind the tower that made it.
"use strict";

const TAU = Math.PI * 2;
const ICON = { guard: "🛡️", limit: "⏱️", egress: "🧱", suspend: "❄️" };
const BADGE = { jailbreak: "💬", burner: "🔥", escaper: "📤", runaway: "💤", boss: "👑" };

// startGame runs a game on canvas. hooks: onHud(hud), onKill(kill), onLeak(creep, lost),
// onWave(n, cleared), onEnd(result), onPad(padIndex), onTower(towerIndex), sfx(kind).
export function startGame(canvas, rules, hooks) {
  const ctx = canvas.getContext("2d");
  const M = rules.map, W = M.w, H = M.h;
  const creepKind = Object.fromEntries([...rules.creeps, rules.boss].map((c) => [c.id, c]));
  const towerKind = Object.fromEntries(rules.towers.map((t) => [t.id, t]));

  // The lane: corner points at tile centres, and the distance along it to each corner.
  const pts = M.path.map(([x, y]) => [x + 0.5, y + 0.5]);
  const acc = [0];
  for (let i = 1; i < pts.length; i++) acc.push(acc[i - 1] + Math.hypot(pts[i][0] - pts[i - 1][0], pts[i][1] - pts[i - 1][1]));
  const LANE = acc[acc.length - 1];
  function posAt(s) {
    let i = 1;
    while (i < pts.length - 1 && s > acc[i]) i++;
    const f = Math.min(1, Math.max(0, (s - acc[i - 1]) / (acc[i] - acc[i - 1] || 1)));
    const a = pts[i - 1], b = pts[i];
    return [a[0] + (b[0] - a[0]) * f, a[1] + (b[1] - a[1]) * f, b[0] - a[0], b[1] - a[1]];
  }

  let st, seq = 0;
  function reset() {
    st = {
      money: rules.money, lives: rules.lives, score: 0, wave: 0, phase: "build", t: 0, fast: false, paused: false,
      spawnQ: [], creeps: [], towers: [], shots: [], fx: [], floats: [], auto: 0, hurt: 0, sel: null, kills: 0, leaks: 0,
    };
    hud();
  }

  const towerAt = (pad) => st.towers.findIndex((t) => t.pad === pad);
  const level = (t, base, mult) => base * Math.pow(mult, t.level - 1);
  const rangeOf = (t) => level(t, towerKind[t.id].range, rules.upRange);
  const upCost = (t) => Math.round(towerKind[t.id].cost * rules.upCost * t.level);
  const sellValue = (t) => Math.round(t.spent * rules.sellBack);

  function hud() {
    hooks.onHud && hooks.onHud({ money: st.money, lives: st.lives, score: st.score, wave: st.wave, waves: rules.waves.length,
      phase: st.phase, auto: Math.ceil(st.auto), fast: st.fast, waveName: rules.waves[Math.max(0, st.wave - (st.phase === "wave" ? 1 : 0))]?.name });
  }

  // ---- actions from the page ----
  function build(pad, id) {
    const k = towerKind[id];
    if (!k || towerAt(pad) >= 0 || st.money < k.cost || st.phase === "over") return false;
    st.money -= k.cost;
    st.towers.push({ pad, id, level: 1, cd: 0, spent: k.cost, ang: -Math.PI / 2, kick: 0 });
    const [x, y] = M.pads[pad];
    burst(x + 0.5, y + 0.5, k.color, 14);
    hooks.sfx && hooks.sfx("build");
    hud();
    return true;
  }
  function upgrade(i) {
    const t = st.towers[i];
    if (!t || t.level >= towerKind[t.id].maxLevel) return false;
    const c = upCost(t);
    if (st.money < c) return false;
    st.money -= c;
    t.spent += c;
    t.level++;
    const [x, y] = M.pads[t.pad];
    burst(x + 0.5, y + 0.5, "#fde68a", 18);
    hooks.sfx && hooks.sfx("upgrade");
    hud();
    return true;
  }
  function sell(i) {
    const t = st.towers[i];
    if (!t) return;
    st.money += sellValue(t);
    st.towers.splice(i, 1);
    st.sel = null;
    hooks.sfx && hooks.sfx("sell");
    hud();
  }
  function startWave() {
    if (st.phase !== "build" || st.wave >= rules.waves.length) return;
    const w = rules.waves[st.wave];
    st.wave++;
    st.phase = "wave";
    st.auto = 0;
    st.spawnQ = [];
    for (let i = 0; i < w.count; i++) st.spawnQ.push({ at: st.t + 0.6 + i * w.gap, id: w.mix[i % w.mix.length], scale: w.hpScale });
    if (w.boss) st.spawnQ.push({ at: st.t + 0.6 + w.count * w.gap + 1.5, id: rules.boss.id, scale: w.hpScale });
    hooks.onWave && hooks.onWave(st.wave, false);
    hooks.sfx && hooks.sfx("wave");
    hud();
  }

  // ---- simulation ----
  function spawn(q) {
    const k = creepKind[q.id], hp = k.hp * q.scale;
    st.creeps.push({ uid: ++seq, id: q.id, hp, max: hp, s: 0, slowUntil: 0, slowF: 0, stunUntil: 0, by: null, bob: Math.random() * TAU });
  }

  function step(dt) {
    st.t += dt;
    while (st.spawnQ.length && st.spawnQ[0].at <= st.t) spawn(st.spawnQ.shift());
    // Creeps walk; the ones that reach the cluster cost lives.
    for (let i = st.creeps.length - 1; i >= 0; i--) {
      const c = st.creeps[i], k = creepKind[c.id];
      if (st.t < c.stunUntil) continue;
      const slow = st.t < c.slowUntil ? c.slowF : 0;
      c.s += k.speed * (1 - slow) * dt;
      if (c.s >= LANE) {
        st.creeps.splice(i, 1);
        const lost = c.id === rules.boss.id ? 10 : 1;
        st.lives = Math.max(0, st.lives - lost);
        st.leaks++;
        st.hurt = 1;
        hooks.onLeak && hooks.onLeak(k, lost);
        hooks.sfx && hooks.sfx("leak");
        hud();
        if (st.lives <= 0) return end(false);
      }
    }
    // Towers aim at the creep furthest along the lane within range.
    for (const t of st.towers) {
      t.cd -= dt;
      t.kick = Math.max(0, t.kick - dt * 6);
      const k = towerKind[t.id], [px, py] = M.pads[t.pad], tx = px + 0.5, ty = py + 0.5, r = rangeOf(t);
      let best = null, bestS = -1;
      for (const c of st.creeps) {
        const [cx, cy] = posAt(c.s);
        if (Math.hypot(cx - tx, cy - ty) <= r && c.s > bestS) { best = c; bestS = c.s; }
      }
      if (!best) continue;
      const [cx, cy] = posAt(best.s);
      t.ang = Math.atan2(cy - ty, cx - tx);
      if (t.cd > 0) continue;
      t.cd = 1 / k.rate;
      t.kick = 1;
      const dmg = level(t, k.damage, rules.upDamage);
      if (t.id === "suspend" || t.id === "limit") {
        // The beam and the limiter's pulse land at once.
        st.fx.push({ kind: t.id === "suspend" ? "beam" : "pulse", x: tx, y: ty, x2: cx, y2: cy, r, t: 0, life: t.id === "suspend" ? 0.18 : 0.35, color: k.color });
        hit(best, t, dmg);
      } else {
        st.shots.push({ x: tx, y: ty, target: best, tower: t, dmg, color: k.color, kind: t.id });
      }
      hooks.sfx && hooks.sfx("shot-" + t.id);
    }
    // Shots fly at their target.
    for (let i = st.shots.length - 1; i >= 0; i--) {
      const sh = st.shots[i];
      if (!st.creeps.includes(sh.target)) { st.shots.splice(i, 1); continue; }
      const [cx, cy] = posAt(sh.target.s), d = Math.hypot(cx - sh.x, cy - sh.y), v = 11 * dt;
      if (d <= v) {
        st.shots.splice(i, 1);
        hit(sh.target, sh.tower, sh.dmg);
      } else {
        sh.x += ((cx - sh.x) / d) * v;
        sh.y += ((cy - sh.y) / d) * v;
      }
    }
    // A wave is over once everything has spawned and nothing is left on the lane.
    if (st.phase === "wave" && !st.spawnQ.length && !st.creeps.length) {
      st.score += rules.clearBonus * st.wave;
      st.money += 20 + 5 * st.wave;
      hooks.onWave && hooks.onWave(st.wave, true);
      if (st.wave >= rules.waves.length) {
        st.score += rules.lifeBonus * st.lives;
        return end(true);
      }
      st.phase = "build";
      st.auto = 8;
      hooks.sfx && hooks.sfx("clear");
      hud();
    }
    if (st.phase === "build" && st.wave > 0 && st.auto > 0) {
      st.auto -= dt;
      if (st.auto <= 0) startWave();
      else if (Math.ceil(st.auto) !== Math.ceil(st.auto + dt)) hud();
    }
  }

  function hit(c, t, dmg) {
    if (!st.creeps.includes(c)) return;
    const k = towerKind[t.id], ck = creepKind[c.id];
    const mult = ck.weak === t.id ? rules.weakMult : 1;
    c.hp -= dmg * mult;
    c.by = t.id;
    c.flash = 0.12;
    if (k.slow) { c.slowUntil = st.t + 1; c.slowF = k.slow; }
    if (k.stun) c.stunUntil = st.t + (c.id === rules.boss.id ? k.stun / 3 : k.stun);
    const [x, y] = posAt(c.s);
    if (mult > 1) sparks(x, y, k.color, 4);
    if (c.hp > 0) return;
    st.creeps.splice(st.creeps.indexOf(c), 1);
    st.money += ck.bounty;
    st.score += ck.points;
    st.kills++;
    burst(x, y, ck.color, c.id === rules.boss.id ? 60 : 16);
    st.fx.push({ kind: "ring", x, y, t: 0, life: 0.4, color: k.color });
    hooks.onKill && hooks.onKill({ tower: t.id, creep: c.id, x, y, boss: c.id === rules.boss.id });
    hooks.sfx && hooks.sfx(c.id === rules.boss.id ? "boss" : "kill");
    hud();
  }

  function end(won) {
    st.phase = "over";
    st.shots = [];
    hud();
    hooks.onEnd && hooks.onEnd({ wave: won ? rules.waves.length + 1 : st.wave, score: st.score, lives: st.lives, won, kills: st.kills, leaks: st.leaks });
  }

  // ---- effects ----
  function burst(x, y, color, n) {
    for (let i = 0; i < n && st.fx.length < 400; i++) {
      const a = Math.random() * TAU, v = 0.8 + Math.random() * 2.4;
      st.fx.push({ kind: "bit", x, y, vx: Math.cos(a) * v, vy: Math.sin(a) * v, t: 0, life: 0.45 + Math.random() * 0.4, color });
    }
  }
  function sparks(x, y, color, n) { burst(x, y, color, n); }
  // floater shows a short line rising over a spot. Two at most share a neighbourhood: a
  // third pushes out the oldest, so real results never pile into an unreadable stack.
  function floater(text, x, y, color = "#fff", life = 1.8) {
    const near = (f) => Math.abs(f.x - x) < 3 && Math.abs(f.y - y) < 1.6;
    const close = st.floats.filter(near);
    if (close.length >= 2) st.floats.splice(st.floats.indexOf(close[0]), 1);
    if (st.floats.length > 10) st.floats.shift();
    const lift = st.floats.some((f) => near(f) && f.t < 0.9) ? 0.75 : 0;
    st.floats.push({ text, x, y: y - lift, color, t: 0, life });
  }

  // ---- drawing ----
  let ts = 32, ox = 0, oy = 0, dpr = 1, cw = 0, ch = 0;
  let padTop = 0, padBottom = 0;
  function layout(top, bottom) {
    padTop = top; padBottom = bottom;
    dpr = Math.min(window.devicePixelRatio || 1, 2);
    cw = canvas.clientWidth; ch = canvas.clientHeight;
    canvas.width = Math.round(cw * dpr); canvas.height = Math.round(ch * dpr);
    const availH = ch - top - bottom;
    ts = Math.floor(Math.min((cw - 12) / W, availH / (H + 0.9)));
    ox = Math.round((cw - ts * W) / 2);
    oy = Math.round(top + (availH - ts * H) / 2 + ts * 0.45);
  }
  const X = (x) => ox + x * ts, Y = (y) => oy + y * ts;

  function rr(x, y, w, h, r) {
    ctx.beginPath();
    ctx.moveTo(x + r, y);
    ctx.arcTo(x + w, y, x + w, y + h, r);
    ctx.arcTo(x + w, y + h, x, y + h, r);
    ctx.arcTo(x, y + h, x, y, r);
    ctx.arcTo(x, y, x + w, y, r);
    ctx.closePath();
  }

  function drawBoard(now) {
    const g = ctx.createLinearGradient(0, 0, 0, ch);
    g.addColorStop(0, "#14233b"); g.addColorStop(1, "#0d1626");
    ctx.fillStyle = g;
    ctx.fillRect(0, 0, cw, ch);
    // Soft checker tiles.
    for (let y = 0; y < H; y++) for (let x = 0; x < W; x++) {
      ctx.fillStyle = (x + y) % 2 ? "#17284388" : "#1a2d4a88";
      ctx.fillRect(X(x), Y(y), ts, ts);
    }
    // The lane: a wide rounded road with a flowing dashed centre.
    ctx.lineJoin = "round"; ctx.lineCap = "round";
    const lane = () => { ctx.beginPath(); pts.forEach(([x, y], i) => (i ? ctx.lineTo(X(x), Y(y)) : ctx.moveTo(X(x), Y(y)))); };
    lane(); ctx.strokeStyle = "#0a1220"; ctx.lineWidth = ts * 0.95; ctx.stroke();
    lane(); ctx.strokeStyle = "#2c3f61"; ctx.lineWidth = ts * 0.82; ctx.stroke();
    lane(); ctx.strokeStyle = "#344a70"; ctx.lineWidth = ts * 0.62; ctx.stroke();
    lane(); ctx.setLineDash([ts * 0.22, ts * 0.3]); ctx.lineDashOffset = -now / 40; ctx.strokeStyle = "#5a76a8"; ctx.lineWidth = Math.max(1.5, ts * 0.05); ctx.stroke();
    ctx.setLineDash([]);
    // The internet, where the traffic comes from.
    const [ex, ey] = pts[0];
    ctx.font = `700 ${Math.round(ts * 0.3)}px Inter, system-ui, sans-serif`;
    ctx.textAlign = "left"; ctx.textBaseline = "middle";
    ctx.fillStyle = "#9fb3d6";
    ctx.fillText("🌐 the internet", X(ex + 0.6), Y(Math.max(ey, -0.4) + 0.2));
    // Pads.
    M.pads.forEach(([x, y], i) => {
      if (towerAt(i) >= 0) return;
      const sel = st.sel && st.sel.pad === i;
      rr(X(x) + ts * 0.1, Y(y) + ts * 0.1, ts * 0.8, ts * 0.8, ts * 0.18);
      ctx.fillStyle = sel ? "#2a4470" : "#1e3150";
      ctx.fill();
      ctx.setLineDash([ts * 0.1, ts * 0.08]);
      ctx.strokeStyle = sel ? "#9fc4ff" : "#40598a";
      ctx.lineWidth = 1.5;
      ctx.stroke();
      ctx.setLineDash([]);
      ctx.fillStyle = sel ? "#cfe0ff" : "#4f6a9c";
      ctx.font = `700 ${Math.round(ts * 0.36)}px Inter, system-ui, sans-serif`;
      ctx.textAlign = "center";
      ctx.fillText("+", X(x + 0.5), Y(y + 0.52));
    });
  }

  function drawCluster(now) {
    const [cx, cy0] = pts[pts.length - 1], cy = cy0 + 0.15;
    const hurt = st.hurt;
    const glow = ctx.createRadialGradient(X(cx), Y(cy), 2, X(cx), Y(cy), ts * 1.3);
    glow.addColorStop(0, hurt > 0.05 ? `rgba(255,80,90,${0.55 * hurt + 0.15})` : "rgba(95,208,104,0.35)");
    glow.addColorStop(1, "rgba(0,0,0,0)");
    ctx.fillStyle = glow;
    ctx.beginPath(); ctx.arc(X(cx), Y(cy), ts * 1.3, 0, TAU); ctx.fill();
    // A friendly server rack with a Kubernetes-ish wheel.
    const w = ts * 1.3, h = ts * 0.85;
    rr(X(cx) - w / 2, Y(cy) - h / 2, w, h, ts * 0.16);
    ctx.fillStyle = "#1f3d2a"; ctx.fill();
    ctx.strokeStyle = hurt > 0.05 ? "#ff6b75" : "#5fd068"; ctx.lineWidth = 2; ctx.stroke();
    for (let i = 0; i < 3; i++) {
      const on = (Math.floor(now / 300) + i) % 3 !== 0;
      ctx.fillStyle = on ? "#7dff8a" : "#2c6b37";
      ctx.beginPath(); ctx.arc(X(cx) - w * 0.32 + i * ts * 0.14, Y(cy) + h * 0.18, ts * 0.045, 0, TAU); ctx.fill();
    }
    ctx.save();
    ctx.translate(X(cx) + w * 0.2, Y(cy) - h * 0.02);
    ctx.rotate(now / 2000);
    ctx.strokeStyle = "#9ff5a9"; ctx.lineWidth = Math.max(1.5, ts * 0.05);
    ctx.beginPath(); ctx.arc(0, 0, ts * 0.17, 0, TAU); ctx.stroke();
    for (let i = 0; i < 7; i++) {
      const a = (i / 7) * TAU;
      ctx.beginPath(); ctx.moveTo(0, 0); ctx.lineTo(Math.cos(a) * ts * 0.17, Math.sin(a) * ts * 0.17); ctx.stroke();
    }
    ctx.restore();
    ctx.fillStyle = "#d9f7dd";
    ctx.font = `800 ${Math.round(ts * 0.24)}px Inter, system-ui, sans-serif`;
    ctx.textAlign = "center";
    ctx.fillText("YOUR CLUSTER", X(cx), Y(cy) + h * 0.5 + ts * 0.22);
  }

  function drawTowers() {
    st.towers.forEach((t, i) => {
      const k = towerKind[t.id], [px, py] = M.pads[t.pad], x = X(px + 0.5), y = Y(py + 0.5);
      if (st.sel && st.sel.tower === i) {
        ctx.beginPath(); ctx.arc(x, y, rangeOf(t) * ts, 0, TAU);
        ctx.fillStyle = k.color + "1f"; ctx.fill();
        ctx.strokeStyle = k.color + "aa"; ctx.lineWidth = 1.5; ctx.stroke();
      }
      const s = 1 + t.kick * 0.08;
      ctx.save();
      ctx.translate(x, y);
      ctx.scale(s, s);
      rr(-ts * 0.42, -ts * 0.42, ts * 0.84, ts * 0.84, ts * 0.22);
      ctx.fillStyle = "#0e1a2c"; ctx.fill();
      ctx.lineWidth = 2.5; ctx.strokeStyle = k.color; ctx.stroke();
      // A little barrel pointing at the target.
      ctx.save();
      ctx.rotate(t.ang);
      ctx.fillStyle = k.color;
      rr(ts * 0.18, -ts * 0.07, ts * 0.3 - t.kick * ts * 0.06, ts * 0.14, ts * 0.05);
      ctx.fill();
      ctx.restore();
      ctx.font = `${Math.round(ts * 0.42)}px "Apple Color Emoji","Segoe UI Emoji","Noto Color Emoji",sans-serif`;
      ctx.textAlign = "center"; ctx.textBaseline = "middle";
      ctx.fillText(ICON[t.id], 0, ts * 0.02);
      for (let l = 0; l < t.level; l++) {
        ctx.fillStyle = "#fde68a";
        ctx.beginPath(); ctx.arc(-ts * 0.2 + l * ts * 0.2, ts * 0.33, ts * 0.055, 0, TAU); ctx.fill();
      }
      ctx.restore();
    });
  }

  function drawCreeps(now) {
    for (const c of st.creeps) {
      const k = creepKind[c.id], [x, y, dx, dy] = posAt(c.s), boss = c.id === rules.boss.id;
      const r = ts * (boss ? 0.48 : 0.27);
      const bob = Math.sin(now / 160 + c.bob) * ts * 0.04;
      const sx = X(x), sy = Y(y) + bob;
      const stunned = st.t < c.stunUntil, slowed = st.t < c.slowUntil;
      ctx.fillStyle = "rgba(0,0,0,0.35)";
      ctx.beginPath(); ctx.ellipse(sx, Y(y) + r * 0.85, r * 0.85, r * 0.3, 0, 0, TAU); ctx.fill();
      ctx.fillStyle = c.flash > 0 ? "#ffffff" : stunned ? "#c4b5fd" : k.color;
      ctx.beginPath(); ctx.arc(sx, sy, r, 0, TAU); ctx.fill();
      if (stunned) { ctx.strokeStyle = "#ede9fe"; ctx.lineWidth = 2; ctx.stroke(); }
      if (slowed) { ctx.strokeStyle = "#fb923c"; ctx.setLineDash([3, 3]); ctx.lineWidth = 2; ctx.beginPath(); ctx.arc(sx, sy, r + 3, 0, TAU); ctx.stroke(); ctx.setLineDash([]); }
      // Eyes that look where it is going.
      const n = Math.hypot(dx, dy) || 1, lx = (dx / n) * r * 0.12, ly = (dy / n) * r * 0.12;
      for (const side of [-1, 1]) {
        ctx.fillStyle = "#fff";
        ctx.beginPath(); ctx.arc(sx + side * r * 0.35, sy - r * 0.15, r * 0.24, 0, TAU); ctx.fill();
        ctx.fillStyle = "#111827";
        ctx.beginPath(); ctx.arc(sx + side * r * 0.35 + lx, sy - r * 0.15 + ly, r * 0.11, 0, TAU); ctx.fill();
      }
      ctx.font = `${Math.round(r * (boss ? 0.8 : 0.9))}px "Apple Color Emoji","Segoe UI Emoji","Noto Color Emoji",sans-serif`;
      ctx.textAlign = "center"; ctx.textBaseline = "middle";
      ctx.fillText(BADGE[c.id] || "", sx + r * 0.75, sy - r * 0.8);
      // Health.
      const bw = r * 2, f = Math.max(0, c.hp / c.max);
      ctx.fillStyle = "#0b1220"; ctx.fillRect(sx - bw / 2, sy - r - 7, bw, 4);
      ctx.fillStyle = f > 0.5 ? "#5fd068" : f > 0.25 ? "#facc15" : "#f87171";
      ctx.fillRect(sx - bw / 2, sy - r - 7, bw * f, 4);
    }
  }

  function drawFx(dt) {
    for (const sh of st.shots) {
      ctx.fillStyle = sh.color;
      ctx.shadowColor = sh.color; ctx.shadowBlur = 8;
      if (sh.kind === "egress") { ctx.fillRect(X(sh.x) - ts * 0.08, Y(sh.y) - ts * 0.06, ts * 0.16, ts * 0.12); }
      else { ctx.beginPath(); ctx.arc(X(sh.x), Y(sh.y), ts * 0.08, 0, TAU); ctx.fill(); }
      ctx.shadowBlur = 0;
    }
    for (let i = st.fx.length - 1; i >= 0; i--) {
      const e = st.fx[i];
      e.t += dt;
      if (e.t >= e.life) { st.fx.splice(i, 1); continue; }
      const f = 1 - e.t / e.life;
      switch (e.kind) {
        case "bit":
          e.x += e.vx * dt; e.y += e.vy * dt; e.vx *= 0.92; e.vy *= 0.92;
          ctx.globalAlpha = f; ctx.fillStyle = e.color;
          ctx.fillRect(X(e.x) - 2, Y(e.y) - 2, 4, 4);
          break;
        case "ring":
          ctx.globalAlpha = f; ctx.strokeStyle = e.color; ctx.lineWidth = 2;
          ctx.beginPath(); ctx.arc(X(e.x), Y(e.y), ts * (0.2 + (1 - f) * 0.6), 0, TAU); ctx.stroke();
          break;
        case "pulse":
          ctx.globalAlpha = f * 0.6; ctx.strokeStyle = e.color; ctx.lineWidth = 3;
          ctx.beginPath(); ctx.arc(X(e.x), Y(e.y), ts * e.r * (1 - f * 0.7), 0, TAU); ctx.stroke();
          break;
        case "beam":
          ctx.globalAlpha = f; ctx.strokeStyle = e.color; ctx.lineWidth = ts * 0.12 * f + 1;
          ctx.shadowColor = e.color; ctx.shadowBlur = 12;
          ctx.beginPath(); ctx.moveTo(X(e.x), Y(e.y)); ctx.lineTo(X(e.x2), Y(e.y2)); ctx.stroke();
          ctx.shadowBlur = 0;
          break;
      }
      ctx.globalAlpha = 1;
    }
    ctx.textAlign = "center"; ctx.textBaseline = "middle";
    for (let i = st.floats.length - 1; i >= 0; i--) {
      const fl = st.floats[i];
      fl.t += dt;
      if (fl.t >= fl.life) { st.floats.splice(i, 1); continue; }
      const y = Y(fl.y) - fl.t * ts * 0.7;
      ctx.globalAlpha = Math.min(1, 2 * (1 - fl.t / fl.life));
      ctx.font = `800 ${Math.round(Math.max(11, ts * 0.3))}px Inter, system-ui, sans-serif`;
      const w = ctx.measureText(fl.text).width + 12;
      const x = Math.min(cw - w / 2 - 4, Math.max(w / 2 + 4, X(fl.x)));
      rr(x - w / 2, y - ts * 0.24, w, ts * 0.48, ts * 0.14);
      ctx.fillStyle = "rgba(8,14,26,0.85)"; ctx.fill();
      ctx.strokeStyle = fl.color; ctx.lineWidth = 1.5; ctx.stroke();
      ctx.fillStyle = fl.color;
      ctx.fillText(fl.text, x, y + 1);
      ctx.globalAlpha = 1;
    }
  }

  let last = performance.now(), acc2 = 0, raf = 0;
  function frame(now) {
    const real = Math.min(0.1, (now - last) / 1000);
    last = now;
    if (!st.paused && st.phase !== "over") {
      acc2 += real * (st.fast ? rules.fastSpeed : 1);
      while (acc2 >= 1 / 60) { step(1 / 60); acc2 -= 1 / 60; if (st.phase === "over") break; }
    }
    for (const c of st.creeps) c.flash = Math.max(0, (c.flash || 0) - real);
    st.hurt = Math.max(0, st.hurt - real * 1.5);
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    drawBoard(now);
    drawCluster(now);
    drawTowers();
    drawCreeps(now);
    drawFx(real);
    raf = requestAnimationFrame(frame);
  }

  // Taps pick a pad or a tower.
  canvas.addEventListener("pointerdown", (e) => {
    const r = canvas.getBoundingClientRect();
    const x = Math.floor((e.clientX - r.left - ox) / ts), y = Math.floor((e.clientY - r.top - oy) / ts);
    const pad = M.pads.findIndex(([px, py]) => px === x && py === y);
    if (pad < 0) { st.sel = null; hooks.onPad && hooks.onPad(-1); return; }
    const ti = towerAt(pad);
    st.sel = ti >= 0 ? { tower: ti, pad } : { pad };
    if (ti >= 0) hooks.onTower && hooks.onTower(ti);
    else hooks.onPad && hooks.onPad(pad);
  });

  reset();
  raf = requestAnimationFrame(frame);
  return {
    layout, build, upgrade, sell, startWave, reset, floater,
    // padScreen is pad i's centre in CSS pixels on the canvas (for scripted taps).
    padScreen: (i) => [X(M.pads[i][0] + 0.5), Y(M.pads[i][1] + 0.5)],
    deselect: () => { st.sel = null; },
    setFast: (on) => { st.fast = on; hud(); },
    setPaused: (on) => { st.paused = on; },
    tower: (i) => {
      const t = st.towers[i];
      if (!t) return null;
      const k = towerKind[t.id];
      return { ...k, level: t.level, upCost: t.level < k.maxLevel ? upCost(t) : 0, sell: sellValue(t), range: rangeOf(t) };
    },
    money: () => st.money,
    phase: () => st.phase,
    wave: () => st.wave,
    stop: () => cancelAnimationFrame(raf),
  };
}
