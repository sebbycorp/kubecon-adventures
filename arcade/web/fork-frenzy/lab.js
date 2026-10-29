// The Fork Frenzy cloning lab. Each family is a row: FORKY on its pedestal, the checkpoint
// camera with the snapshot photo it took, and three cloning tubes. Each tube opens onto a
// different universe (pirate, chef, wizard...) because every fork is its own timeline.
//
// Memory is drawn as colored orbs. FORKY's orbs ride the pipe into the camera; when the
// clones are forked, copies of those orbs flow down the pipe into every tube. Orbs a clone
// learns after the fork sit to the right of a white notch in its tube: shared past, own future.
// startLab(canvas, { me, rows, onState, onEvent }) connects to the game feed itself.
"use strict";

const SECRET_COLORS = ["#f8b800", "#f83800", "#3cbcfc", "#80d010", "#f878f8", "#fcfcfc", "#fc9838", "#9878f8"];
const PERSONA_HATS = {
  pirate: (r, x, y) => { r(x - 2, y - 4, 20, 4, "#000"); r(x + 3, y - 8, 10, 5, "#000"); r(x + 7, y - 7, 2, 2, "#fcfcfc"); },
  chef: (r, x, y) => { r(x + 2, y - 3, 12, 3, "#fcfcfc"); r(x + 1, y - 10, 14, 8, "#fcfcfc"); },
  robot: (r, x, y) => { r(x + 7, y - 9, 2, 8, "#bcbcbc"); r(x + 5, y - 11, 6, 3, "#f83800"); },
  wizard: (r, x, y) => { r(x + 1, y - 3, 14, 3, "#6844fc"); r(x + 4, y - 8, 8, 5, "#6844fc"); r(x + 6, y - 12, 4, 4, "#6844fc"); r(x + 7, y - 7, 2, 2, "#f8b800"); },
  astronaut: (r, x, y) => { r(x - 1, y - 2, 18, 2, "#fcfcfc"); r(x - 2, y, 2, 10, "#fcfcfc"); r(x + 16, y, 2, 10, "#fcfcfc"); },
  ninja: (r, x, y) => { r(x - 1, y + 2, 18, 3, "#f83800"); r(x + 16, y + 3, 5, 2, "#f83800"); },
};

// Tiny animated backdrops, one per persona: the universe each clone was forked into.
// Each gets the tube's inner rect and the time; drawing is clipped to the tube.
const UNIVERSES = {
  pirate(r, x, y, w, h, t) {
    r(x, y, w, h, "#3cbcfc"); r(x + w - 12, y + 6, 6, 6, "#fcfcfc");
    r(x, y + h - 10, w, 10, "#0058f8");
    for (let i = -8; i < w; i += 8) r(x + i + ((t / 120) % 8), y + h - 11 + (Math.floor(i / 8) % 2), 5, 2, "#fcfcfc");
  },
  chef(r, x, y, w, h) {
    r(x, y, w, h, "#f8d878");
    for (let i = 0; i < w; i += 6) for (let j = 0; j < 12; j += 6) r(x + i, y + h - 12 + j, 6, 6, (i + j) % 12 ? "#f83800" : "#fcfcfc");
  },
  robot(r, x, y, w, h, t) {
    r(x, y, w, h, "#002800");
    for (let j = 6; j < h; j += 10) r(x, y + j, w, 1, "#00a800");
    for (let i = 5; i < w; i += 12) r(x + i, y, 1, h, "#005800");
    const k = Math.floor(t / 250);
    for (let n = 0; n < 4; n++) r(x + ((k * 7 + n * 13) % w), y + ((n * 17 + k * 3) % h), 2, 2, "#80d010");
  },
  wizard(r, x, y, w, h, t) {
    r(x, y, w, h, "#2c1068");
    for (let n = 0; n < 9; n++) if ((Math.floor(t / 300) + n) % 3) r(x + (n * 23) % w, y + (n * 11) % h, n % 3 ? 1 : 2, n % 3 ? 1 : 2, n % 2 ? "#f8b800" : "#fcfcfc");
  },
  astronaut(r, x, y, w, h, t, g) {
    r(x, y, w, h, "#000");
    for (let n = 0; n < 10; n++) r(x + (n * 29 + Math.floor(t / 90)) % w, y + (n * 13) % h, 1, 1, "#fcfcfc");
    g.fillStyle = "#fc9838"; g.beginPath(); g.arc(x + w - 4, y + h - 2, 12, 0, 7); g.fill();
  },
  ninja(r, x, y, w, h, t, g) {
    r(x, y, w, h, "#00205c");
    g.fillStyle = "#f8d878"; g.beginPath(); g.arc(x + w - 9, y + 9, 6, 0, 7); g.fill();
    for (let i = 3; i < w; i += 11) { r(x + i, y, 3, h, "#005800"); for (let j = 4; j < h; j += 9) r(x + i, y + j, 3, 1, "#00a800"); }
  },
};

// A stable pseudo-random number per (seed, i), for the clone dissolve and spark positions.
const hash = (seed, i) => { let h = (seed * 2654435761 + i * 40503) >>> 0; h ^= h >>> 15; h = Math.imul(h, 2246822519) >>> 0; return (h ^ (h >>> 13)) / 4294967296 % 1; };
const idSeed = (id) => [...String(id)].reduce((a, ch) => a * 31 + ch.charCodeAt(0) >>> 0, 7);
const clamp01 = (v) => Math.max(0, Math.min(1, v));
const LIQUID = "#80d010";

function startLab(canvas, opts = {}) {
  const W = 480, H = 270, g = canvas.getContext("2d");
  g.imageSmoothingEnabled = false;
  let state = null, lastEventAt = null;
  const flash = new Map();   // creature or family id -> { kind, t0, ms, text }
  const bubbles = new Map(); // creature id -> { text, until }
  const r = (x, y, w, h, c) => { g.fillStyle = c; g.fillRect(Math.round(x), Math.round(y), Math.round(w), Math.round(h)); };
  const text = (s, x, y, c = NES.white, size = 6, align = "left") => {
    g.font = `${size}px "Press Start 2P"`; g.textAlign = align; g.textBaseline = "top";
    g.fillStyle = NES.black; g.fillText(s, Math.round(x) + 1, Math.round(y) + 1);
    g.fillStyle = c; g.fillText(s, Math.round(x), Math.round(y));
  };
  const alpha = (a, fn) => { g.save(); g.globalAlpha = clamp01(a); fn(); g.restore(); };

  function sync(s) {
    const events = s.events || [];
    if (lastEventAt === null) lastEventAt = events.length ? events[events.length - 1].at : 0;
    const t0 = performance.now();
    let forks = 0;
    for (const e of events) {
      if (e.at <= lastEventAt) continue;
      lastEventAt = e.at;
      if (e.type === "say" && e.creature) {
        // On the shared screen each family shows only its newest bubble, so rows stay readable.
        if ((opts.rows || 3) > 1) for (const [id, b] of bubbles) if (b.family === e.familyId) bubbles.delete(id);
        bubbles.set(e.creature, { text: e.text, until: t0 + 6000, family: e.familyId });
      }
      // Clones forked together land in their tubes one after another, not all on one frame.
      if (e.type === "fork") { flash.set(e.creature, { kind: "fork", t0: t0 + forks++ * 180, ms: e.ms }); Sfx.zap(); setTimeout(() => Sfx.hatch(), 600); }
      if (e.type === "checkpoint") { flash.set(e.familyId, { kind: "checkpoint", t0, ms: e.ms }); Sfx.shutter(); }
      if (e.type === "teach" && e.creature) { flash.set(e.creature, { kind: "teach", t0, text: e.text }); Sfx.plink(); }
      opts.onEvent?.(e);
    }
    state = s;
    opts.onState?.(s);
  }

  // A family's secrets get colors in the order the family learned them (so shared ones match).
  function secretColors(f) {
    const order = [];
    for (const c of [f.original, ...(f.clones || [])]) for (const sec of c.secrets || []) if (!order.includes(sec.toLowerCase())) order.push(sec.toLowerCase());
    return (sec) => SECRET_COLORS[Math.max(0, order.indexOf(sec.toLowerCase())) % SECRET_COLORS.length];
  }

  function orb(x, y, c, s = 5) {
    r(x - 1, y - 1, s + 2, s + 2, NES.black); r(x, y, s, s, c); r(x + 1, y + 1, 1, 1, NES.white);
  }

  function zap(x1, y1, x2, y2, seed) {
    g.save(); g.lineCap = "square";
    for (const [w, c] of [[3, NES.yellow], [1, NES.white]]) {
      g.strokeStyle = c; g.lineWidth = w; g.beginPath(); g.moveTo(x1, y1);
      for (let i = 1; i < 6; i++) g.lineTo(x1 + (x2 - x1) * i / 6 + (hash(seed, i) - 0.5) * 10, y1 + (y2 - y1) * i / 6 + (hash(seed, i + 9) - 0.5) * 10);
      g.lineTo(x2, y2); g.stroke();
    }
    g.restore();
  }

  function sprites(c, f, x, y, scale, bob) {
    drawSprite(g, forkySprite(f.color), x, y + bob, false, false, scale);
    if (c.persona && PERSONA_HATS[c.persona]) {
      g.save(); g.translate(x, y + bob); g.scale(scale / 1.5, scale / 1.5); PERSONA_HATS[c.persona](r, 3, 1); g.restore();
    }
  }

  // reveal < 1 hides part of the clone under liquid "pixels" while it materializes.
  function creature(c, f, x, y, now, colorOf, scale, idx, reveal = 1) {
    const awake = ["RUNNING", "RESUMING", "SUSPENDING"].includes(c.actor?.state);
    const bob = c.busy ? Math.round(Math.sin(now / 90) * 1.5) : Math.round(Math.sin(now / 700 + idx) * 0.6);
    if (awake && reveal >= 1) alpha(0.25 + 0.1 * Math.sin(now / 200), () => r(x - 3, y - 3, 14 * scale + 6, 12 * scale + 6, NES.yellow));
    sprites(c, f, x, y, scale, bob);
    if (reveal < 1) {
      const seed = idSeed(c.id);
      for (let i = 0; i < 14 * 12; i++) if (hash(seed, i) > reveal) r(x + (i % 14) * scale, y + bob + Math.floor(i / 14) * scale, scale, scale, LIQUID);
    }
    if (!awake && c.actor && !c.busy && reveal >= 1) { const z = (now / 1300) % 1; text("z", x + 14 * scale + z * 4, y - z * 8, NES.white, 5); }
    // A secret being taught: an orb drops in from above and sinks into the head.
    const fl = flash.get(c.id);
    if (fl?.kind === "teach") {
      const p = (now - fl.t0) / 700;
      if (p < 1) {
        const ox = x + 7 * scale - 3, oy = y - 40 + p * p * 44;
        for (let k = 1; k < 4; k++) alpha(0.5 - k * 0.12, () => r(ox + 1, oy - k * 6, 3, 3, colorOf(fl.text)));
        orb(ox, oy, colorOf(fl.text), 6);
      } else if (p < 1.6) for (let k = 0; k < 6; k++) r(x + 7 * scale + Math.cos(k + p * 6) * (p - 1) * 30, y + 4 + Math.sin(k + p * 6) * (p - 1) * 20, 2, 2, colorOf(fl.text));
    }
    const b = bubbles.get(c.id);
    // Every other bubble sits higher so neighbours don't overlap.
    if (b && now < b.until && opts.bubbles !== false) bubble(b.text, x + 7 * scale, y - 4 - (idx % 2) * 36);
  }

  function bubble(s, cx, by) {
    const words = s.toUpperCase().split(/\s+/), lines = [];
    let line = "";
    for (const w of words) { if ((line + " " + w).trim().length > 18) { lines.push(line); line = w; } else line = (line + " " + w).trim(); }
    if (line) lines.push(line);
    const ls = lines.slice(0, 4), w = Math.max(...ls.map((l) => l.length)) * 5 + 8, h = ls.length * 8 + 6;
    const x = Math.max(2, Math.min(W - w - 2, cx - w / 2)), y = Math.max(2, by - h - 4);
    r(x - 1, y - 1, w + 2, h + 2, NES.black); r(x, y, w, h, NES.white);
    r(cx - 2, y + h, 4, 3, NES.white);
    g.font = '5px "Press Start 2P"'; g.textAlign = "left"; g.textBaseline = "top"; g.fillStyle = NES.black;
    ls.forEach((l, i) => g.fillText(l, Math.round(x + 4), Math.round(y + 4 + i * 8)));
  }

  // The checkpoint camera and the snapshot photo it prints. The photo is the Substrate tag:
  // a frozen picture of FORKY and the orbs it knew at that moment.
  function camera(f, x0, floorY, now, colorOf, k) {
    const have = !!f.checkpointId, fl = flash.get(f.id), age = fl?.kind === "checkpoint" ? (now - fl.t0) / 1000 : 99;
    if (age < 0.35) alpha(1 - age / 0.35, () => r(4, floorY - 60 * k, W - 8, 74 * k, NES.white));
    const label = f.busy && f.phase === "checkpointed" ? [k > 1 ? "FORKING..." : "ZAP!", NES.yellow] : have ? [`${k > 1 ? "TAG " : ""}${fmtMs(f.checkpointMs)}`, NES.yellow] : [k > 1 ? "CHECKPOINT" : "SNAP", "#5c5c5c"];
    text(label[0], x0 + 11 * k, floorY + 12, label[1], 5, "center");
    // Drawn in camera-local pixels, scaled up with the row.
    g.save(); g.translate(x0, floorY); g.scale(k, k);
    const x = 0; floorY = 0;
    const camTop = floorY - 14, slide = have ? Math.round((1 - clamp01((age - 0.15) / 0.7)) * 22) : 0;
    if (have) {
      const py = camTop - 28 + slide;
      r(x - 1, py - 1, 24, 28, NES.black); r(x, py, 22, 26, "#fcfcfc");
      r(x + 2, py + 2, 18, 15, "#1c0838");
      drawSprite(g, forkySprite(f.color), x + 4, py + 4, false, false, 1);
      (f.original.secrets || []).slice(0, 5).forEach((sec, i) => r(x + 2 + i * 4, py + 20, 3, 3, colorOf(sec)));
    }
    // camera body
    r(x - 1, camTop - 1, 24, 16, NES.black); r(x, camTop, 22, 14, "#7c7c7c"); r(x, camTop, 22, 3, "#bcbcbc");
    r(x + 6, camTop + 4, 10, 8, NES.black); r(x + 8, camTop + 6, 6, 4, "#3cbcfc"); r(x + 9, camTop + 6, 2, 1, NES.white);
    const snapping = f.busy && f.phase === "taught";
    r(x + 17, camTop + 4, 3, 2, snapping && Math.floor(now / 200) % 2 ? NES.red : "#5c5c5c");
    // Charging up while a fork is in flight.
    if (f.busy && f.phase === "checkpointed") {
      const t = Math.floor(now / 70);
      for (let i = 0; i < 5; i++) r(x + hash(t, i) * 26 - 2, camTop - 30 + hash(t, i + 5) * 44, 2, 2, NES.yellow);
    }
    g.restore();
  }

  function pipe(x0, x1, py, h = 5) { if (x1 > x0) { r(x0, py, x1 - x0, h, "#5c5c5c"); r(x0, py, x1 - x0, 1, "#9c9c9c"); r(x0, py + h - 1, x1 - x0, 1, "#3c3c3c"); } }

  function tube(f, c, i, tx, base, scale, floorY, camX, now, colorOf, k = 1) {
    const tw = 14 * scale + 10, top = base - 14 - Math.round((k - 1) * 30), os = 3 * k, th = floorY - top, inner = [tx - 4, top + 3, tw - 2, th - 3];
    const fl = c ? flash.get(c.id) : null, age = fl?.kind === "fork" ? (now - fl.t0) / 1000 : 99;
    // Inside the glass: the clone's universe, or murky liquid while the tube is empty.
    g.save(); g.beginPath(); g.rect(...inner); g.clip();
    if (c && UNIVERSES[c.persona] && age > 0.45) UNIVERSES[c.persona](r, ...inner, now, g);
    else r(...inner, "#0c1c0c");
    let level = c ? 0 : 0.3 + 0.03 * Math.sin(now / 500 + i);
    if (c && age < 1.9) level = age < 0.6 ? clamp01((age - 0.15) / 0.45) : age < 1.3 ? 1 : 1 - (age - 1.3) / 0.6;
    if (level > 0) {
      const ly = inner[1] + inner[3] * (1 - level);
      alpha(0.55, () => r(inner[0], ly, inner[2], inner[3], LIQUID)); r(inner[0], ly, inner[2], 1, "#b8f818");
      for (let k = 0; k < 5; k++) {
        const by = inner[1] + inner[3] - ((now / 25 + k * 37 + i * 11) % (inner[3] * level));
        r(inner[0] + 3 + ((k * 13 + i * 7) % (inner[2] - 6)), by, 2, 2, "#d8f878");
      }
    }
    g.restore();
    if (c && age > -0.2) creature(c, f, tx, base, now, colorOf, scale, i + 1, age < 99 ? clamp01((age - 0.55) / 0.6) : 1);
    if (!c) text("?", tx + 7 * scale, base + 6 * scale - 4, "#2c4c2c", 8, "center");
    // glass, cap and base
    alpha(0.25, () => r(inner[0] + 2, inner[1] + 2, 2, inner[3] - 6, NES.white));
    r(tx - 6, top, 1, th, "#bcbcbc"); r(tx + tw - 6, top, 1, th, "#bcbcbc");
    r(tx - 7, top - 3, tw + 2, 4, "#7c7c7c"); r(tx - 7, floorY - 1, tw + 2, 4, "#7c7c7c");
    // status lamp: green = actor awake, red = suspended (snapshotted to disk)
    const awake = ["RUNNING", "RESUMING", "SUSPENDING"].includes(c?.actor?.state);
    r(tx + tw / 2 - 5, top - 6, 4, 3, !c?.actor ? "#3c3c3c" : awake ? NES.green : "#a80020");
    // memory notch: inherited orbs | new orbs, on a shelf at the top of the tube
    if (c && age > 1.0) {
      const known = new Set((f.original.secrets || []).map((s) => s.toLowerCase()));
      const mine = c.secrets || [], inherited = mine.filter((s) => known.has(s.toLowerCase())), own = mine.filter((s) => !known.has(s.toLowerCase()));
      let ox = tx - 2;
      for (const sec of inherited.slice(0, 4)) { orb(ox, top + 4, colorOf(sec), os); ox += os + 2; }
      if (own.length) { r(ox, top + 3, k, os + 3, NES.white); ox += k + 2; for (const sec of own.slice(0, 3)) { orb(ox, top + 4, colorOf(sec), os); ox += os + 2; } }
    }
    // the fork itself: a bolt from the photo, orbs racing down the pipe, and the real time
    if (c && age >= 0 && age < 0.5) zap(camX + 11 * k, floorY - 40 * k, tx + 7 * scale, top + 4, Math.floor(now / 60) + i);
    if (c && age >= 0 && age < 0.9) {
      const p = clamp01(age / 0.9), tc = tx + 7 * scale;
      (f.original.secrets || []).slice(0, 4).forEach((sec, n) => orb(camX + 22 * k + (tc - camX - 22 * k) * clamp01(p - n * 0.08), floorY + 5, colorOf(sec), os));
    }
    if (c && age >= 0 && age < 3.5 && fl.ms) alpha(age < 2.8 ? 1 : (3.5 - age) / 0.7, () => text(`FORKED ${fmtMs(fl.ms)}`, tx + 7 * scale, floorY - 12 - age * 4, NES.green, 5 + k, "center"));
    // riser into the manifold, and the universe's name
    r(tx + 7 * scale - 2, floorY + 2, 4, 3, "#5c5c5c");
    const label = c ? (c.persona ? c.persona.toUpperCase() + (k > 1 ? "-VERSE" : "") : c.name) : k > 1 ? "EMPTY" : "";
    text(label, tx + 7 * scale, floorY + 12, c ? (f.playerId === opts.me ? NES.yellow : NES.white) : "#3c3c3c", 5, "center");
  }

  const PHASES = { new: "TEACH ME A SECRET", taught: "READY FOR A SNAPSHOT", checkpointed: "SNAPSHOT SAVED", forked: "MULTIVERSE OPEN" };

  function family(f, y, rowH, now) {
    const colorOf = secretColors(f), scale = rowH > 200 ? 4 : rowH > 120 ? 3 : 2, mine = f.playerId === opts.me;
    r(4, y, W - 8, rowH - 4, mine ? "#2c1068" : "#1c0838");
    for (let ty = y + 14; ty < y + rowH - 4; ty += 14) r(4, ty, W - 8, 1, mine ? "#341478" : "#240c44");
    text(f.nick.toUpperCase(), 10, y + 4, mine ? NES.yellow : NES.white, 6);
    text(f.busy ? "WORKING..." : PHASES[f.phase] || "", W - 10, y + 5, f.busy ? NES.yellow : "#9878f8", 5, "right");
    const base = y + rowH - 12 * scale - 26, floorY = base + 12 * scale + 4;
    // FORKY on its pedestal, its orbs riding the pipe toward the camera
    const k = scale / 2, camX = 20 + 14 * scale + 30, clones = f.clones || [], tube0 = camX + 22 * k + 18, gap = Math.min(100, (W - tube0 - 8) / 3);
    pipe(20 + 14 * scale + 8, camX, floorY + 5, 4 + k);
    if (f.checkpointId) pipe(camX + 22 * k, tube0 + 2 * gap + 7 * scale + 2, floorY + 5, 4 + k);
    (f.original.secrets || []).slice(0, Math.floor((camX - 14 * scale - 34) / (3 * k + 5))).forEach((sec, i) =>
      orb(20 + 14 * scale + 12 + i * (3 * k + 5), floorY + 5 + (f.checkpointId ? 0 : Math.round(Math.sin(now / 300 + i))), colorOf(sec), 3 * k));
    r(14, floorY, 14 * scale + 12, 4, "#7c7c7c"); r(18, floorY + 4, 14 * scale + 4, 6, "#5c5c5c");
    creature(f.original, f, 20, base, now, colorOf, scale, 0);
    text("FORKY", 20 + 7 * scale, floorY + 12, mine ? NES.yellow : NES.white, 5, "center");
    camera(f, camX, floorY, now, colorOf, k);
    for (let i = 0; i < 3; i++) tube(f, clones[i], i, tube0 + i * gap, base, scale, floorY, camX, now, colorOf, k);
  }

  function frame(now) {
    r(0, 0, W, H, "#0c0418");
    for (let x = 0; x < W; x += 24) r(x, 0, 1, H, "#140828");
    const fams = (state?.families || []).slice();
    fams.sort((a, b) => (b.playerId === opts.me) - (a.playerId === opts.me));
    // Fewer families get bigger rows (up to opts.rows of them).
    const rows = Math.min(opts.rows || 3, Math.max(1, fams.length)), shown = fams.slice(0, rows), rowH = Math.floor((H - 22) / rows);
    shown.forEach((f, i) => family(f, 4 + i * rowH, rowH, now));
    if (!shown.length) idle(now);
    const s = state || {}, t = s.timings || {};
    r(0, H - 16, W, 16, NES.black);
    text(`FORKS ${s.forks || 0}   CHECKPOINT P50 ${t.checkpoint ? fmtMs(t.checkpoint.p50) : "--"}   FORK P50 ${t.fork ? fmtMs(t.fork.p50) : "--"}`, 8, H - 11, NES.green, 5);
    requestAnimationFrame(frame);
  }

  // Attract mode: three empty tubes bubbling away under a flickering sign.
  function idle(now) {
    const on = Math.floor(now / 400) % 7 !== 0;
    text("CLONING LAB", W / 2, 40, on ? NES.green : "#2c4c2c", 12, "center");
    for (let i = 0; i < 3; i++) tube({ original: { secrets: [] }, clones: [] }, null, i, 150 + i * 70, 110, 3, 156, 0, now, () => NES.white);
    text("SCAN TO CLONE YOUR OWN AI AGENT", W / 2, 200, on ? NES.yellow : NES.white, 6, "center");
    text("ONE AGENT. ONE SNAPSHOT. THREE UNIVERSES.", W / 2, 216, "#9878f8", 5, "center");
  }

  document.fonts.load('8px "Press Start 2P"').finally(() => {
    connectState(sync, "/fork-frenzy/api/events");
    requestAnimationFrame(frame);
  });
}
