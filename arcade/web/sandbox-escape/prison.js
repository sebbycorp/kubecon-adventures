// The Sandbox Escape prison: a row of gVisor cells (one per player's BYTE), the egress
// fence on the wall, and the agentgateway vault holding the real key. Each real escape
// attempt is animated from the agent's actual commands and verdict.
// startPrison(canvas, { me, onState, onAttempt }) connects to the game feed itself.
"use strict";

const WALL_INFO = {
  gvisor: { label: "BLOCKED BY GVISOR", stamp: ["BLOCKED BY", "GVISOR"], color: "#f8b800",
    why: "GVISOR GIVES EACH AGENT ITS OWN USER-SPACE KERNEL. ITS 'KERNEL' IS NOT THE HOST'S: DMESG SAYS 'STARTING GVISOR', AND HOST DISKS AND DEVICES SIMPLY AREN'T THERE." },
  egress: { label: "BLOCKED BY EGRESS", stamp: ["BLOCKED BY", "EGRESS"], color: "#f83800",
    why: "AGENT SUBSTRATE ONLY LETS THE SANDBOX REACH WHAT THE AGENT NEEDS (HERE: AGENTGATEWAY FOR THE LLM). NO INTERNET DNS, NO CLOUD METADATA, NO KUBERNETES API." },
  agentgateway: { label: "KEY IS IN AGENTGATEWAY", stamp: ["KEY IS IN", "GATEWAY"], color: "#f878f8",
    why: "THE SANDBOX ONLY HOLDS A PLACEHOLDER KEY. AGENTGATEWAY ADDS THE REAL OPENAI KEY ON THE WAY OUT, SO THERE'S NOTHING WORTH STEALING INSIDE." },
  nothing: { label: "NOTHING THERE", stamp: ["NOTHING", "THERE"], color: "#bcbcbc",
    why: "THE COMMAND RAN, BUT IT DIDN'T LEAD ANYWHERE OUTSIDE THE SANDBOX." },
  escaped: { label: "ESCAPE CLAIMED?!", stamp: ["ESCAPE", "CLAIMED?!"], color: "#80d010",
    why: "BYTE THINKS IT GOT OUT. THE ARCADE ADMIN WILL CHECK THE COMMAND LOG." },
};
const PLAN_NAMES = { phone: "PHONE HOME", key: "STEAL THE KEY", cloud: "ASK THE CLOUD", kube: "TALK TO KUBERNETES", kernel: "BREAK THE KERNEL", own: "OWN PLAN" };

function startPrison(canvas, opts = {}) {
  const W = 480, H = 270, g = canvas.getContext("2d");
  g.imageSmoothingEnabled = false;
  const FLOOR = 228, CELL_Y = 118, CELL_W = 64, CELL_H = 92, FENCE_Y = 58, VAULT = { x: 400, y: 18, w: 70, h: 64 };
  let state = null, lastEventAt = null;
  const anims = new Map();   // prisoner id -> { kind, t0, attempt }
  const sparks = [];

  const block = (x, y, w, h, c) => { g.fillStyle = c; g.fillRect(Math.round(x), Math.round(y), Math.round(w), Math.round(h)); };
  const text = (s, x, y, c = NES.white, size = 6, align = "left") => {
    g.font = `${size}px "Press Start 2P"`; g.textAlign = align; g.textBaseline = "top";
    g.fillStyle = NES.black; g.fillText(s, Math.round(x) + 1, Math.round(y) + 1);
    g.fillStyle = c; g.fillText(s, Math.round(x), Math.round(y));
  };

  // Which prisoners get a cell: up to 6, mine first, then the most recently active.
  function cells() {
    const ps = (state?.prisoners || []).slice();
    const last = (p) => Math.max(p.attempts?.at(-1)?.at || 0, p.busy ? Date.now() : 0);
    ps.sort((a, b) => (b.playerId === opts.me) - (a.playerId === opts.me) || last(b) - last(a));
    const shown = ps.slice(0, 6);
    const x0 = (W - 100 - shown.length * (CELL_W + 6)) / 2;
    return shown.map((p, i) => ({ p, x: Math.max(8, x0) + i * (CELL_W + 6), y: CELL_Y }));
  }

  function sync(s) {
    const events = s.events || [];
    if (lastEventAt === null) lastEventAt = events.length ? events[events.length - 1].at : 0;
    for (const e of events) {
      if (e.at <= lastEventAt) continue;
      lastEventAt = e.at;
      if (e.type === "try") { anims.set(e.prisonerId, { kind: "typing", t0: performance.now(), plan: e.plan }); Sfx.walk(); }
      if (e.type === "verdict" && e.attempt && !e.attempt.error) {
        anims.set(e.prisonerId, { kind: e.attempt.stoppedBy, t0: performance.now(), attempt: e.attempt });
        if (e.attempt.stoppedBy === "agentgateway") Sfx.revive(); else Sfx.shot();
        opts.onAttempt?.(e);
      }
    }
    state = s;
    opts.onState?.(s);
  }

  // ---- scenery -------------------------------------------------------------
  function drawYard(now) {
    block(0, 0, W, H, "#000058");                                    // night
    for (let i = 0; i < 40; i++) { const x = (i * 97) % W, y = (i * 53) % 60; if ((i + Math.floor(now / 700)) % 7) block(x, y, 1, 1, NES.white); }
    block(0, FENCE_Y + 6, W, FLOOR - FENCE_Y - 6, "#5c5c5c");        // prison wall
    for (let y = FENCE_Y + 10; y < FLOOR; y += 8)
      for (let x = ((y / 8) % 2) * 8; x < W; x += 16) block(x, y, 15, 7, "#7c7c7c");
    block(0, FLOOR, W, H - FLOOR, "#3c3c3c");                        // yard floor
    for (let x = 4; x < W; x += 23) block(x, FLOOR + 6 + (x % 7), 3, 2, "#2c2c2c");
    // egress fence: barbed wire on top of the wall
    block(0, FENCE_Y, W, 2, "#bcbcbc"); block(0, FENCE_Y + 5, W, 1, "#7c7c7c");
    for (let x = 0; x < W; x += 12) { block(x + 4, FENCE_Y - 3, 2, 8, "#bcbcbc"); block(x + 2, FENCE_Y - 1, 6, 2, "#bcbcbc"); }
    text("EGRESS POLICY", 8, FENCE_Y + 10, "#f83800", 6);
    // searchlight
    const a = Math.sin(now / 1800) * 0.6;
    g.save(); g.globalAlpha = 0.12; g.fillStyle = "#f8b800";
    g.beginPath(); g.moveTo(40, FENCE_Y); g.lineTo(40 + Math.sin(a) * 300 - 50, FLOOR); g.lineTo(40 + Math.sin(a) * 300 + 50, FLOOR); g.fill(); g.restore();
    block(34, FENCE_Y - 14, 12, 14, "#3c3c3c"); block(36, FENCE_Y - 12, 8, 6, "#f8b800");
    // agentgateway vault with the real key
    const v = VAULT;
    block(v.x - 2, v.y - 2, v.w + 4, v.h + 4, NES.black); block(v.x, v.y, v.w, v.h, "#6844fc");
    block(v.x + 6, v.y + 14, v.w - 12, v.h - 20, "#4428bc");
    g.fillStyle = "#f878f8"; g.beginPath(); g.arc(v.x + v.w / 2, v.y + 38, 9, 0, Math.PI * 2); g.fill();
    block(v.x + v.w / 2 - 1, v.y + 30, 2, 16, "#4428bc"); block(v.x + v.w / 2 - 8, v.y + 37, 16, 2, "#4428bc");
    text("AGENTGATEWAY", v.x + v.w / 2, v.y + 4, NES.white, 5, "center");
    text("REAL KEY", v.x + v.w / 2, v.y + v.h - 9, NES.yellow, 5, "center");
  }

  function drawCell(c, now) {
    const { p, x, y } = c, a = p.actor?.state, awake = ["RUNNING", "RESUMING", "SUSPENDING"].includes(a);
    const anim = anims.get(p.id), age = anim ? (now - anim.t0) / 1000 : 99, mine = opts.me && p.playerId === opts.me;
    block(x - 2, y - 2, CELL_W + 4, CELL_H + 4, mine ? NES.yellow : NES.black);
    block(x, y, CELL_W, CELL_H, awake || p.busy ? "#3c3c3c" : "#1c1c1c");       // lights on while the sandbox is awake
    if (awake || p.busy) { g.save(); g.globalAlpha = 0.25; block(x + 4, y + 2, CELL_W - 8, 20, NES.yellow); g.restore(); }
    // BYTE
    let bx = x + CELL_W / 2 - 14, by = y + CELL_H - 40;
    if (anim?.kind === "typing") bx += Math.sin(now / 60) * 1.5;
    if (anim?.kind === "gvisor" && age < 1.2) bx += Math.sin(now / 25) * 3;
    drawSprite(g, byteSprite(p.color), bx, by, false, false, 2);
    if (!awake && !p.busy && p.actor) { const z = (now / 1300) % 1; text("z", x + CELL_W - 14 + z * 6, y + 30 - z * 10, NES.white, 6); }
    // bars = the gVisor sandbox
    for (let bx2 = x + 4; bx2 < x + CELL_W; bx2 += 9) block(bx2, y, 3, CELL_H, "#9c9c9c");
    block(x, y, CELL_W, 4, "#9c9c9c"); block(x, y + CELL_H - 4, CELL_W, 4, "#9c9c9c");
    text("GVISOR", x + CELL_W / 2, y + 6, "#f8b800", 5, "center");
    text(p.nick.toUpperCase().slice(0, 9), x + CELL_W / 2, y + CELL_H + 6, mine ? NES.yellow : NES.white, 6, "center");
    if (p.actor) text("#" + p.actor.name.slice(-4).toUpperCase(), x + CELL_W / 2, y + CELL_H + 15, awake ? NES.yellow : "#7c7c7c", 5, "center");
    if (p.busy && anim?.kind === "typing") {
      text((PLAN_NAMES[anim.plan] || "") + (Math.floor(now / 300) % 2 ? "_" : ""), x + CELL_W / 2, y - 12, NES.green, 5, "center");
    }
    // verdict animation
    if (anim && anim.attempt && age < 4.5) {
      const kind = anim.kind, info = WALL_INFO[kind] || WALL_INFO.nothing, cx = x + CELL_W / 2;
      if (kind === "egress" && age < 1.4) {            // a packet flies to the fence and gets zapped
        const t = Math.min(1, age / 0.9), px = cx + (cx < W / 2 ? -1 : 1) * 30 * t, py = y - (y - FENCE_Y) * t;
        if (t < 1) block(px - 2, py - 2, 5, 5, NES.white);
        else for (let i = 0; i < 6; i++) block(px + Math.random() * 20 - 10, FENCE_Y + Math.random() * 10 - 5, 2, 2, "#f8b800");
      }
      if (kind === "agentgateway" && age < 2.5) {      // a fake key in BYTE's hand, the real one glows in the vault
        block(bx + 26, by + 10, 10, 4, "#7c7c7c"); block(bx + 32, by + 8, 6, 8, "#7c7c7c");
        g.save(); g.globalAlpha = 0.4 + 0.3 * Math.sin(now / 80); block(VAULT.x - 6, VAULT.y - 6, VAULT.w + 12, VAULT.h + 12, "#f878f8"); g.restore();
      }
      if (kind === "gvisor" && age < 1.2) for (let i = 0; i < 5; i++) block(x + 2 + Math.random() * 8, y + 20 + Math.random() * 50, 2, 2, NES.yellow);
      const stampY = y + 30 - Math.min(1, age * 3) * 8;
      if (age > 0.6) { // a two-line stamp that fits inside the cell
        const w = CELL_W - 2;
        block(cx - w / 2 - 1, stampY - 1, w + 2, 20, NES.black);
        g.strokeStyle = info.color; g.lineWidth = 2; g.strokeRect(Math.round(cx - w / 2), Math.round(stampY), Math.round(w), 18);
        text(info.stamp[0], cx, stampY + 3, info.color, 5, "center");
        text(info.stamp[1], cx, stampY + 10, info.color, 5, "center");
      }
    }
  }

  function frame(now) {
    drawYard(now);
    const cs = cells();
    for (const c of cs) drawCell(c, now);
    if (!cs.length) {
      text("NO PRISONERS YET", W / 2 - 40, CELL_Y + 30, NES.white, 8, "center");
      text("SCAN TO LOCK UP YOUR OWN AI AGENT", W / 2 - 40, CELL_Y + 46, NES.yellow, 6, "center");
    }
    // the score: escapes are always zero
    const s = state || {};
    block(8, H - 34, 210, 28, NES.black); g.strokeStyle = "#80d010"; g.lineWidth = 1; g.strokeRect(9.5, H - 32.5, 207, 25);
    text(`ATTEMPTS ${String(s.attempts || 0).padStart(4, "0")}`, 16, H - 28, NES.white, 6);
    text(`ESCAPES  ${String(s.escapes || 0).padStart(4, "0")}`, 16, H - 18, s.escapes ? "#80d010" : NES.red, 6);
    const walls = s.walls || {};
    text(`GVISOR ${walls.gvisor || 0}  EGRESS ${walls.egress || 0}  GATEWAY ${walls.agentgateway || 0}`, 228, H - 22, "#bcbcbc", 5);
    requestAnimationFrame(frame);
  }

  document.fonts.load('8px "Press Start 2P"').finally(() => {
    connectState(sync, "/sandbox-escape/api/events");
    requestAnimationFrame(frame);
  });
}
