// Gateway Defense's simulation, with no DOM: the phone's td.js draws it, and
// balance.mjs runs it headless to tune the rules. The rules come from the server
// (games/gatewaydef/config.go); waveAt mirrors its WaveAt.
"use strict";

// waveAt is wave n (1-based): one of rules.waves, or an endless wave after them.
export function waveAt(rules, n) {
  if (n >= 1 && n <= rules.waves.length) return rules.waves[n - 1];
  const e = rules.endless, k = n - rules.waves.length;
  return { n, name: "Endless " + k, mix: e.mix, count: e.count + k * e.countStep, gap: Math.max(e.minGap, e.gap - k * e.gapStep),
    hpScale: e.hpScale * Math.pow(e.hpGrowth, k), boss: e.bossEvery > 0 && k % e.bossEvery === 0, endless: true };
}

// createSim runs one game. hooks (all optional): onHud(), onKill(kill), onLeak(kind,
// lost, x, y), onWave(n, cleared, wave), onEnd(result), sfx(kind), fx(kind, data) for
// the renderer's effects.
export function createSim(rules, hooks = {}) {
  const M = rules.map;
  const creepKind = Object.fromEntries([...rules.creeps, rules.boss].map((c) => [c.id, c]));
  const towerKind = Object.fromEntries(rules.towers.map((t) => [t.id, t]));
  const call = (name, ...a) => hooks[name] && hooks[name](...a);

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
      spawnQ: [], creeps: [], towers: [], shots: [], auto: 0, kills: 0, leaks: 0, won: false,
      combo: 0, comboT: -9, bestCombo: 0, firstLeakWave: 0,
    };
    call("onHud");
  }

  const towerAt = (pad) => st.towers.findIndex((t) => t.pad === pad);
  const level = (t, base, mult) => base * Math.pow(mult, t.level - 1);
  const rangeOf = (t) => level(t, towerKind[t.id].range, rules.upRange);
  const damageOf = (t) => level(t, towerKind[t.id].damage, rules.upDamage);
  const upCost = (t) => Math.round(towerKind[t.id].cost * rules.upCost * t.level);
  const sellValue = (t) => Math.round(t.spent * rules.sellBack);
  const clearMoney = (n) => rules.clearMoney + rules.clearStep * n;

  // ---- actions ----
  function build(pad, id) {
    const k = towerKind[id];
    if (!k || pad < 0 || pad >= M.pads.length || towerAt(pad) >= 0 || st.money < k.cost || st.phase === "over") return false;
    st.money -= k.cost;
    st.towers.push({ pad, id, level: 1, cd: 0, spent: k.cost, ang: -Math.PI / 2, kick: 0, spin: Math.random() * 6 });
    call("fx", "build", { pad, id });
    call("sfx", "build");
    call("onHud");
    return true;
  }
  function upgrade(i) {
    const t = st.towers[i];
    if (!t || t.level >= towerKind[t.id].maxLevel || st.phase === "over") return false;
    const c = upCost(t);
    if (st.money < c) return false;
    st.money -= c;
    t.spent += c;
    t.level++;
    call("fx", "upgrade", { pad: t.pad, id: t.id, level: t.level });
    call("sfx", "upgrade");
    call("onHud");
    return true;
  }
  function sell(i) {
    const t = st.towers[i];
    if (!t || st.phase === "over") return false;
    st.money += sellValue(t);
    st.towers.splice(i, 1);
    call("sfx", "sell");
    call("onHud");
    return true;
  }
  function startWave() {
    if (st.phase !== "build") return false;
    const w = waveAt(rules, st.wave + 1);
    st.wave++;
    st.phase = "wave";
    st.auto = 0;
    st.spawnQ = [];
    for (let i = 0; i < w.count; i++) st.spawnQ.push({ at: st.t + 0.6 + i * w.gap, id: w.mix[i % w.mix.length], scale: w.hpScale });
    if (w.boss) st.spawnQ.push({ at: st.t + 0.6 + w.count * w.gap + 1.5, id: rules.boss.id, scale: w.hpScale / rules.waves[rules.waves.length - 1].hpScale });
    call("onWave", st.wave, false, w);
    call("sfx", w.boss ? "bossWave" : "wave");
    call("onHud");
    return true;
  }

  // ---- simulation ----
  function spawn(q, s = 0, hpOverride = 0) {
    const k = creepKind[q.id], hp = hpOverride || k.hp * q.scale;
    const c = { uid: ++seq, id: q.id, hp, max: hp, s, slowUntil: 0, slowF: 0, stunUntil: 0, by: null, bob: Math.random() * 6.283,
      summoned: 0, scale: q.scale, flash: 0, deflect: 0 };
    st.creeps.push(c);
    if (q.id === rules.boss.id) call("fx", "bossIn", { uid: c.uid });
    return c;
  }

  function step(dt) {
    if (st.phase === "over") return;
    st.t += dt;
    while (st.spawnQ.length && st.spawnQ[0].at <= st.t) spawn(st.spawnQ.shift());
    // Creeps walk; the ones that reach the cluster cost lives.
    for (let i = st.creeps.length - 1; i >= 0; i--) {
      const c = st.creeps[i], k = creepKind[c.id];
      c.flash = Math.max(0, c.flash - dt);
      c.deflect = Math.max(0, c.deflect - dt);
      if (st.t < c.stunUntil) continue;
      const slow = st.t < c.slowUntil ? c.slowF : 0;
      c.s += k.speed * (1 - slow) * dt;
      if (c.s >= LANE) {
        st.creeps.splice(i, 1);
        const lost = k.lives || 1;
        st.lives = Math.max(0, st.lives - lost);
        st.leaks++;
        if (!st.firstLeakWave) st.firstLeakWave = st.wave;
        const [x, y] = posAt(LANE);
        call("onLeak", k, lost, x, y);
        call("sfx", "leak");
        call("onHud");
        if (st.lives <= 0) return end();
      }
    }
    // Towers aim at the creep furthest along the lane within range (a shielded creep
    // only draws fire from a tower that can break its shield, unless nothing else is near).
    for (const t of st.towers) {
      t.cd -= dt;
      t.kick = Math.max(0, t.kick - dt * 6);
      const k = towerKind[t.id], [px, py] = M.pads[t.pad], tx = px + 0.5, ty = py + 0.5, r = rangeOf(t);
      let best = null, bestS = -1, fallback = null, fallS = -1;
      for (const c of st.creeps) {
        const [cx, cy] = posAt(c.s);
        if (Math.hypot(cx - tx, cy - ty) > r) continue;
        if (canHurt(t, c)) { if (c.s > bestS) { best = c; bestS = c.s; } }
        else if (c.s > fallS) { fallback = c; fallS = c.s; }
      }
      best = best || ((k.slow || k.stun) ? fallback : null);
      if (!best) continue;
      const [cx, cy] = posAt(best.s);
      t.ang = Math.atan2(cy - ty, cx - tx);
      if (t.cd > 0) continue;
      t.cd = 1 / k.rate;
      t.kick = 1;
      const dmg = damageOf(t);
      if (t.id === "suspend" || t.id === "limit") {
        // The beam and the limiter's pulse land at once.
        call("fx", t.id === "suspend" ? "beam" : "pulse", { x: tx, y: ty, x2: cx, y2: cy, r, level: t.level });
        hit(best, t, dmg);
        if (t.id === "limit") {
          // The pulse slows everything in range, not just the target.
          for (const c of st.creeps) {
            const [ox, oy] = posAt(c.s);
            if (c !== best && Math.hypot(ox - tx, oy - ty) <= r) { c.slowUntil = st.t + 1; c.slowF = k.slow; }
          }
        }
      } else {
        st.shots.push({ x: tx, y: ty, target: best, tower: t, dmg, kind: t.id, level: t.level });
      }
      call("sfx", "shot-" + t.id);
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
      st.money += clearMoney(st.wave);
      if (st.wave === rules.waves.length) {
        st.won = true;
        st.score += rules.lifeBonus * st.lives;
      }
      call("onWave", st.wave, true, waveAt(rules, st.wave));
      st.phase = "build";
      st.auto = rules.autoNext;
      call("sfx", st.wave === rules.waves.length ? "victory" : "clear");
      call("onHud");
    }
    if (st.phase === "build" && st.wave > 0 && st.auto > 0) {
      st.auto -= dt;
      if (st.auto <= 0) startWave();
      else if (Math.ceil(st.auto) !== Math.ceil(st.auto + dt)) call("onHud");
    }
  }

  // canHurt is whether t's shots do damage to c (a shield needs its Weak tower, tuned up).
  function canHurt(t, c) {
    const ck = creepKind[c.id];
    return !ck.shield || (ck.weak === t.id && t.level >= rules.shieldLevel);
  }

  function hit(c, t, dmg) {
    if (!st.creeps.includes(c)) return;
    const k = towerKind[t.id], ck = creepKind[c.id];
    const [x, y] = posAt(c.s);
    if (k.slow) { c.slowUntil = st.t + 1; c.slowF = k.slow; }
    if (k.stun) c.stunUntil = st.t + (c.id === rules.boss.id ? k.stun / 3 : k.stun);
    if (!canHurt(t, c)) {
      c.deflect = 0.25;
      call("fx", "deflect", { x, y, tower: t.id });
      return;
    }
    const mult = ck.weak === t.id ? rules.weakMult : 1;
    const raw = dmg * mult, armor = t.id === "suspend" ? (ck.armor || 0) / 2 : ck.armor || 0;
    c.hp -= Math.max(raw * rules.armorFloor, raw - armor);
    c.by = t.id;
    c.flash = 0.12;
    call("fx", "hit", { x, y, tower: t.id, weak: mult > 1, level: t.level, uid: c.uid });
    // A summoner calls in help as it drops past each mark.
    if (ck.summon) {
      while (c.summoned < ck.summonAt.length && c.hp / c.max <= ck.summonAt[c.summoned]) {
        c.summoned++;
        for (let i = 0; i < ck.summonN; i++) spawn({ id: ck.summon, scale: c.scale * rules.waves[rules.waves.length - 1].hpScale * 0.6 }, Math.max(0, c.s - 0.4 - i * 0.35));
        call("fx", "summon", { x, y, n: ck.summonN });
        call("sfx", "summon");
      }
    }
    if (c.hp > 0) return;
    st.creeps.splice(st.creeps.indexOf(c), 1);
    st.money += ck.bounty;
    st.score += ck.points;
    st.kills++;
    st.combo = st.t - st.comboT < 1.2 ? st.combo + 1 : 1;
    st.comboT = st.t;
    st.bestCombo = Math.max(st.bestCombo, st.combo);
    if (ck.split) {
      for (let i = 0; i < ck.splitN; i++) spawn({ id: ck.split, scale: c.scale }, Math.max(0, c.s - 0.25 * i), creepKind[ck.split].hp * c.scale * ck.splitHP);
      call("fx", "split", { x, y, n: ck.splitN });
    }
    call("onKill", { tower: t.id, creep: c.id, x, y, boss: c.id === rules.boss.id, bounty: ck.bounty, points: ck.points, combo: st.combo, color: ck.color });
    call("sfx", c.id === rules.boss.id ? "boss" : "kill");
    call("onHud");
  }

  function end() {
    // The wave reached: ending between waves (from the menu) counts the next one, so the
    // last cleared wave's bonus is inside the server's MaxScore for it.
    const reached = st.phase === "build" && st.wave > 0 ? st.wave + 1 : Math.max(1, st.wave);
    st.phase = "over";
    st.shots = [];
    call("onHud");
    call("onEnd", { wave: reached, quit: !!st.quit, score: st.score, lives: st.lives, won: st.won, kills: st.kills, leaks: st.leaks, bestCombo: st.bestCombo });
  }

  // quit ends the run where it is (from the menu), as if the cluster fell.
  function quit() {
    if (st.phase === "over") return;
    st.quit = true;
    st.lives = 0;
    end();
  }

  reset();
  return {
    rules, pts, posAt, LANE, creepKind, towerKind,
    get st() { return st; },
    reset, step, build, upgrade, sell, startWave, quit,
    towerAt, rangeOf, damageOf, upCost, sellValue, canHurt, clearMoney,
  };
}
