// A live NES-style diagram of how Agent Substrate runs one duck: kagent session ->
// worker pool (gVisor sandboxes per node) <-> snapshot store, and LLM calls through
// agentgateway. Driven by the duck's real actor state; transitions are animated
// because RESUMING/SUSPENDING often last less than one poll.
"use strict";

class SubstrateView {
  constructor(canvas) {
    this.c = canvas;
    this.g = canvas.getContext("2d");
    this.g.imageSmoothingEnabled = false;
    this.W = canvas.width;   // 320
    this.H = canvas.height;  // 220
    this.prev = null;        // last actor state seen
    this.anim = null;        // { kind: "restore" | "snapshot", t0 }
    this.busySince = 0;
  }

  // ---- drawing helpers ------------------------------------------------------
  rect(x, y, w, h, c) { this.g.fillStyle = c; this.g.fillRect(Math.round(x), Math.round(y), Math.round(w), Math.round(h)); }
  frame(x, y, w, h, c) { this.rect(x, y, w, 2, c); this.rect(x, y + h - 2, w, 2, c); this.rect(x, y, 2, h, c); this.rect(x + w - 2, y, 2, h, c); }
  text(s, x, y, c = NES.white, size = 6, align = "left") {
    const g = this.g;
    g.font = `${size}px "Press Start 2P"`; g.textAlign = align; g.textBaseline = "top";
    g.fillStyle = NES.black; g.fillText(s, Math.round(x) + 1, Math.round(y) + 1);
    g.fillStyle = c; g.fillText(s, Math.round(x), Math.round(y));
  }
  arrow(x1, y1, x2, y2, c, dashed, now) {
    const g = this.g, n = Math.max(1, Math.round(Math.hypot(x2 - x1, y2 - y1) / 4)), off = dashed ? Math.floor(now / 80) % 2 : 0;
    for (let i = 0; i < n; i++) if (!dashed || (i + off) % 2) this.rect(x1 + ((x2 - x1) * i) / n, y1 + ((y2 - y1) * i) / n, 2, 2, c);
  }
  cartridge(x, y, color, mine) { // an NES cartridge = one snapshot
    this.rect(x - 1, y - 1, 14, 18, NES.black);
    this.rect(x, y, 12, 16, mine ? "#7c7c7c" : "#3c3c3c");
    this.rect(x + 2, y + 3, 8, 7, mine ? (DUCK_COLORS[color]?.B || NES.grass) : "#5c5c5c");
    this.rect(x + 1, y + 13, 10, 2, "#1c1c1c");
  }
  key(x, y) { this.rect(x, y + 2, 5, 5, NES.yellow); this.rect(x + 5, y + 4, 7, 2, NES.yellow); this.rect(x + 9, y + 6, 2, 2, NES.yellow); this.rect(x + 1, y + 3, 3, 3, NES.black); }
  lock(x, y) { this.frame(x + 2, y - 4, 6, 6, NES.yellow); this.rect(x, y, 10, 8, NES.yellow); this.rect(x + 4, y + 3, 2, 3, NES.black); }
  cloud(x, y) { for (const [cx, cy, r] of [[10, 8, 7], [20, 6, 8], [30, 9, 6]]) for (let dy = -r; dy <= r; dy++) { const w = Math.sqrt(r * r - dy * dy); this.rect(x + cx - w, y + cy + dy, w * 2, 1, NES.white); } }

  // ---- main draw ------------------------------------------------------------
  // duck: the duck from the game state; s: the full game state (for pools/agents).
  draw(duck, s, now) {
    const { W, H } = this, a = duck.actor, st = a?.state || "", awake = ["RUNNING", "RESUMING", "SUSPENDING"].includes(st);
    const parked = duck.state === "shot" || duck.state === "reviving";
    // Animate state changes, including ones the 500 ms poll only saw the end of.
    if (this.prev !== null && st !== this.prev) {
      const wasAwake = ["RUNNING", "RESUMING", "SUSPENDING"].includes(this.prev);
      if (awake && !wasAwake) this.anim = { kind: "restore", t0: now };
      if (st === "SUSPENDED" && wasAwake) this.anim = { kind: "snapshot", t0: now };
    }
    this.prev = st;
    if (duck.busy && !this.busySince) this.busySince = now;
    if (!duck.busy) this.busySince = 0;
    const anim = this.anim && now - this.anim.t0 < 900 ? this.anim : (this.anim = null);

    this.rect(0, 0, W, H, NES.black);
    const tag = a ? "#" + a.name.slice(-4).toUpperCase() : "";

    // kagent session
    this.frame(4, 26, 66, 44, NES.sky);
    this.text("KAGENT", 10, 31, NES.sky);
    this.text("SESSION", 10, 42, NES.white);
    this.text(tag, 10, 53, NES.yellow);
    this.text("AGENT SUBSTRATE: YOUR DUCK " + tag, W / 2, 4, NES.orange, 6, "center");

    // worker pool, one column per node
    const pool = (s.substrate?.pools || []).find((p) => p.name === "ducks") || { workers: 12, nodes: ["kagent-worker", "kagent-worker2"] };
    const nodes = pool.nodes?.length ? pool.nodes : ["node"], px = 84, py = 20, pw = 150, ph = 92;
    this.frame(px, py, pw, ph, NES.grass);
    this.text(`WORKER POOL "DUCKS"`, px + 6, py + 5, NES.grass);
    const perNode = Math.max(1, Math.ceil((pool.workers || 12) / nodes.length)), colW = (pw - 12) / nodes.length;
    const awakeOthers = Math.max(0, (s.awake?.now || 0) - (awake ? 1 : 0));
    let mySlot = null, othersDrawn = 0;
    nodes.forEach((node, ni) => {
      const nx = px + 6 + ni * colW;
      this.text(node.replace("kagent-", "").toUpperCase(), nx + 2, py + 17, a?.node === node ? NES.yellow : NES.gray, 5);
      for (let i = 0; i < perNode; i++) {
        const sx = nx + 2 + (i % 3) * 22, sy = py + 28 + Math.floor(i / 3) * 16;
        this.rect(sx, sy, 20, 13, "#1c1c1c"); this.frame(sx, sy, 20, 13, "#3c3c3c");
        const mine = awake && !anim?.kind?.startsWith("restore") && (a?.node === node || (!a?.node && ni === 0)) && i === (parseInt(a?.name?.slice(-2) || "0", 16) % perNode);
        if (mine) mySlot = { x: sx, y: sy };
        else if (othersDrawn < awakeOthers && (i + ni) % 2 === 0) { this.rect(sx + 3, sy + 3, 14, 7, "#5c5c5c"); othersDrawn++; }
      }
    });
    this.text("GVISOR SANDBOXES", px + 6, py + ph - 10, NES.gray, 5);

    // snapshot store
    const sx0 = 84, sy0 = 150, sw = 150, sh = 52;
    this.frame(sx0, sy0, sw, sh, NES.yellow);
    this.text("SNAPSHOT STORE", sx0 + 6, sy0 + 5, NES.yellow);
    const asleep = s.substrate?.suspended || 0, shown = Math.min(8, asleep), myCart = { x: sx0 + 8, y: sy0 + 18 };
    for (let i = 0, slot = 1; i < shown; i++) {
      if (i === 0 && !awake && !anim) continue; // slot 0 is yours while you sleep
      this.cartridge(sx0 + 8 + slot++ * 16, sy0 + 18, "", false);
      if (slot > 7) break;
    }
    if (asleep > 8) this.text(`+${asleep - 8}`, sx0 + sw - 26, sy0 + 40, NES.gray, 5);
    this.text(`${asleep} ASLEEP`, sx0 + 6, sy0 + sh - 10, NES.gray, 5);

    // agentgateway + OpenAI
    this.frame(246, 26, 70, 44, "#d800cc");
    this.text("AGENT", 252, 31, "#f878f8"); this.text("GATEWAY", 252, 41, "#f878f8"); this.key(254, 54);
    this.cloud(258, 96); this.text("OPENAI", 281, 117, NES.white, 5, "center");

    // arrows
    this.arrow(70, 48, px, 48, NES.sky, duck.busy, now);
    this.text("A2A", 72, 38, NES.sky, 5);
    const midX = px + pw / 2;
    this.arrow(midX - 18, sy0, midX - 18, py + ph, NES.grass, anim?.kind === "restore", now);
    this.arrow(midX + 18, py + ph, midX + 18, sy0, NES.yellow, anim?.kind === "snapshot", now);
    this.text("RESTORE", midX - 22, py + ph + 6, NES.grass, 5, "right");
    this.text(fmtMs(s.timings?.restore?.p50 || null), midX - 22, py + ph + 15, NES.grass, 5, "right");
    this.text("SNAPSHOT", midX + 22, py + ph + 6, NES.yellow, 5);
    this.text(fmtMs(s.timings?.snapshot?.p50 || null), midX + 22, py + ph + 15, NES.yellow, 5);
    const llm = awake && duck.busy;
    this.arrow(px + pw, 48, 246, 48, "#f878f8", llm, now);
    this.arrow(281, 70, 281, 92, "#f878f8", llm, now);

    // the duck itself
    const color = duck.color;
    if (anim) {
      const p = Math.min(1, (now - anim.t0) / 900), from = anim.kind === "restore" ? myCart : (mySlot || { x: midX, y: py + 40 }), to = anim.kind === "restore" ? { x: midX - 10, y: py + 44 } : myCart;
      const x = from.x + (to.x - from.x) * p, y = from.y + (to.y - from.y) * p;
      this.cartridge(x, y, color, true);
    } else if (awake && mySlot) {
      if (Math.floor(now / 120) % 2) this.frame(mySlot.x - 2, mySlot.y - 2, 24, 17, NES.yellow);
      drawSprite(this.g, duckSprite(FLAP[Math.floor(now / 120) % 4], color), mySlot.x + 2, mySlot.y);
    } else if (a) {
      this.cartridge(myCart.x, myCart.y, color, true);
      if (parked) this.lock(myCart.x + 1, myCart.y - 10);
      else { const z = (now / 1200) % 1; this.text("z", myCart.x + 13 + z * 6, myCart.y - 4 - z * 8, NES.white, 6); }
    }
    if (llm) { // a request packet bouncing to OpenAI and back
      const t = ((now - this.busySince) / 1400) % 1, path = t < 0.5 ? t * 2 : 2 - t * 2;
      const x = px + pw + (281 - px - pw) * Math.min(1, path * 1.6), y = 48 + Math.max(0, path * 1.6 - 1) / 0.6 * 44;
      this.rect(x - 2, y - 2, 5, 5, NES.white);
    }

    // caption: what is happening right now
    const cap = !a ? ["HATCHING: KAGENT IS CREATING", "THE SESSION AND ITS ACTOR"]
      : anim?.kind === "restore" ? ["WAKING UP: SUBSTRATE RESTORES", "THE SNAPSHOT INTO A SANDBOX"]
      : anim?.kind === "snapshot" ? ["FALLING ASLEEP: THE SANDBOX IS", "SAVED AS A SNAPSHOT, WORKER FREED"]
      : llm ? ["THINKING: THE SANDBOX CALLS OPENAI", "VIA AGENTGATEWAY (IT HOLDS THE KEY)"]
      : awake ? ["AWAKE IN A GVISOR SANDBOX", "ON " + (a.node || "A WORKER").toUpperCase()]
      : parked ? ["SHOT: KAGENT PARKED THE SESSION.", "ONLY A REVIVE CAN WAKE IT"]
      : ["ASLEEP AS A SNAPSHOT: USES NO", "SANDBOX UNTIL SOMEONE PROMPTS IT"];
    this.text(cap[0], 4, H - 18, awake || anim ? NES.yellow : NES.white, 5);
    this.text(cap[1], 4, H - 9, awake || anim ? NES.yellow : NES.white, 5);
  }
}
