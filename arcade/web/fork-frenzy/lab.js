// The Fork Frenzy cloning lab. Each family is a row: FORKY on its pedestal, the checkpoint
// cartridge, and the clones in their tubes. Colored dots under each creature are the
// secrets it knows: shared secrets have the same color everywhere, so it is easy to see
// that clones share the past (up to the checkpoint) but not the future.
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

function startLab(canvas, opts = {}) {
  const W = 480, H = 270, g = canvas.getContext("2d");
  g.imageSmoothingEnabled = false;
  let state = null, lastEventAt = null;
  const flash = new Map();  // creature id -> { kind, t0, ms }
  const bubbles = new Map(); // creature id -> { text, until }
  const r = (x, y, w, h, c) => { g.fillStyle = c; g.fillRect(Math.round(x), Math.round(y), Math.round(w), Math.round(h)); };
  const text = (s, x, y, c = NES.white, size = 6, align = "left") => {
    g.font = `${size}px "Press Start 2P"`; g.textAlign = align; g.textBaseline = "top";
    g.fillStyle = NES.black; g.fillText(s, Math.round(x) + 1, Math.round(y) + 1);
    g.fillStyle = c; g.fillText(s, Math.round(x), Math.round(y));
  };

  function sync(s) {
    const events = s.events || [];
    if (lastEventAt === null) lastEventAt = events.length ? events[events.length - 1].at : 0;
    for (const e of events) {
      if (e.at <= lastEventAt) continue;
      lastEventAt = e.at;
      if (e.type === "say" && e.creature) bubbles.set(e.creature, { text: e.text, until: performance.now() + 6000 });
      if (e.type === "fork") { flash.set(e.creature, { kind: "fork", t0: performance.now(), ms: e.ms }); Sfx.hatch(); }
      if (e.type === "checkpoint") { flash.set(e.familyId, { kind: "checkpoint", t0: performance.now(), ms: e.ms }); Sfx.revive(); }
      if (e.type === "teach") Sfx.quack();
      opts.onEvent?.(e);
    }
    state = s;
    opts.onState?.(s);
  }

  // A family's secrets get colors in the order the family learned them (so shared ones match).
  function secretColors(f) {
    const order = [];
    for (const c of [f.original, ...(f.clones || [])]) for (const sec of c.secrets || []) if (!order.includes(sec.toLowerCase())) order.push(sec.toLowerCase());
    return (sec) => SECRET_COLORS[order.indexOf(sec.toLowerCase()) % SECRET_COLORS.length];
  }

  function creature(c, f, x, y, now, colorOf, scale, idx = 0) {
    const awake = ["RUNNING", "RESUMING", "SUSPENDING"].includes(c.actor?.state);
    const fl = flash.get(c.id), age = fl ? (now - fl.t0) / 1000 : 99;
    if (age < 0.8) for (let i = 0; i < 8; i++) r(x + Math.random() * 30 - 2, y + Math.random() * 26 - 4, 2, 2, NES.yellow);
    const bob = c.busy ? Math.round(Math.sin(now / 90)) : 0;
    if (awake) { g.save(); g.globalAlpha = 0.3; r(x - 3, y - 3, 14 * scale + 6, 12 * scale + 6, NES.yellow); g.restore(); }
    drawSprite(g, forkySprite(f.color), x, y + bob, false, false, scale);
    if (c.persona && PERSONA_HATS[c.persona]) {
      g.save(); g.translate(x, y + bob); g.scale(scale / 1.5, scale / 1.5); PERSONA_HATS[c.persona](r, 3, 1); g.restore();
    }
    if (!awake && c.actor && !c.busy) { const z = (now / 1300) % 1; text("z", x + 14 * scale + z * 4, y - z * 8, NES.white, 5); }
    // secrets as dots
    (c.secrets || []).slice(0, 6).forEach((sec, i) => r(x + i * 6, y + 12 * scale + 4, 4, 4, colorOf(sec)));
    text(c.name, x + 7 * scale, y + 12 * scale + 10, f.playerId === opts.me ? NES.yellow : NES.white, 5, "center");
    if (age < 3 && fl?.kind === "fork") text(`FORK ${fmtMs(fl.ms)}`, x + 7 * scale, y - 12, NES.green, 5, "center");
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
    g.font = '5px "Press Start 2P"'; g.textAlign = "left"; g.textBaseline = "top"; g.fillStyle = NES.black;
    ls.forEach((l, i) => g.fillText(l, Math.round(x + 4), Math.round(y + 4 + i * 8)));
  }

  function family(f, y, rowH, now) {
    const colorOf = secretColors(f), scale = rowH > 120 ? 3 : 2, mine = f.playerId === opts.me;
    r(4, y, W - 8, rowH - 4, mine ? "#2c1068" : "#1c0838");
    text(f.nick.toUpperCase(), 10, y + 4, mine ? NES.yellow : NES.white, 6);
    const base = y + rowH - 12 * scale - 22;
    // pedestal + FORKY
    r(16, base + 12 * scale + 2, 14 * scale + 8, 4, "#7c7c7c");
    creature(f.original, f, 20, base, now, colorOf, scale);
    // checkpoint cartridge
    const cx = 20 + 14 * scale + 40, have = !!f.checkpointId, cf = flash.get(f.id), cage = cf ? (now - cf.t0) / 1000 : 99;
    r(cx - 1, base - 1, 22, 28, have ? NES.black : "#3c3c3c");
    r(cx, base, 20, 26, have ? "#7c7c7c" : "#2c2c2c"); r(cx + 3, base + 4, 14, 10, have ? DUCK_COLORS[f.color]?.H || "#00a800" : "#3c3c3c"); r(cx + 2, base + 20, 16, 3, "#1c1c1c");
    if (cage < 1) for (let i = 0; i < 6; i++) r(cx + Math.random() * 24 - 2, base + Math.random() * 30 - 2, 2, 2, NES.yellow);
    text("CHECKPOINT", cx + 10, base + 30, have ? NES.yellow : "#5c5c5c", 5, "center");
    if (f.checkpointMs) text(fmtMs(f.checkpointMs), cx + 10, base + 38, NES.yellow, 5, "center");
    // arrows
    const ay = base + 12;
    for (let x = 20 + 14 * scale + 6; x < cx - 4; x += 4) r(x, ay, 2, 2, have ? NES.yellow : "#3c3c3c");
    // clones in tubes
    const clones = f.clones || [], tube0 = cx + 44, gap = Math.min(90, (W - tube0 - 10) / 3);
    for (let i = 0; i < 3; i++) {
      const tx = tube0 + i * gap, c = clones[i];
      g.save(); g.globalAlpha = 0.18; r(tx - 4, base - 10, 14 * scale + 8, 12 * scale + 16, "#80d010"); g.restore();
      r(tx - 5, base - 12, 14 * scale + 10, 2, "#bcbcbc"); r(tx - 5, base + 12 * scale + 4, 14 * scale + 10, 2, "#bcbcbc");
      if (have) for (let x = cx + 22; x < tx - 6; x += 4) if (i === 0 || x > tube0 + (i - 1) * gap + 14 * scale) r(x, ay, 2, 2, c ? NES.green : "#3c3c3c");
      if (c) creature(c, f, tx, base, now, colorOf, scale, i + 1);
      else text("EMPTY", tx + 7 * scale, base + 8, "#3c3c3c", 5, "center");
    }
  }

  function frame(now) {
    r(0, 0, W, H, "#0c0418");
    for (let x = 0; x < W; x += 24) r(x, 0, 1, H, "#140828");
    const fams = (state?.families || []).slice();
    fams.sort((a, b) => (b.playerId === opts.me) - (a.playerId === opts.me));
    // Fewer families get bigger rows (up to opts.rows of them).
    const rows = Math.min(opts.rows || 3, Math.max(1, fams.length)), shown = fams.slice(0, rows), rowH = Math.floor((H - 22) / rows);
    shown.forEach((f, i) => family(f, 4 + i * rowH, rowH, now));
    if (!shown.length) {
      text("THE CLONING LAB IS EMPTY", W / 2, 100, NES.white, 8, "center");
      text("SCAN TO CLONE YOUR OWN AI AGENT", W / 2, 118, NES.yellow, 6, "center");
    }
    const s = state || {}, t = s.timings || {};
    r(0, H - 16, W, 16, NES.black);
    text(`FORKS ${s.forks || 0}   CHECKPOINT P50 ${t.checkpoint ? fmtMs(t.checkpoint.p50) : "--"}   FORK P50 ${t.fork ? fmtMs(t.fork.p50) : "--"}`, 8, H - 11, NES.green, 5);
    requestAnimationFrame(frame);
  }

  document.fonts.load('8px "Press Start 2P"').finally(() => {
    connectState(sync, "/fork-frenzy/api/events");
    requestAnimationFrame(frame);
  });
}
