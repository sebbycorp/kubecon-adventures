// Cluster Defense: the 2D space renderer shared by the phone page and the big screen.
//
// startSpace(canvas, opts) draws the arena from server snapshots: a parallax starfield,
// the cluster station (one light per worker of the pool, lit while an agent holds it),
// Snapshot Storage, the subagents, ships and shots. opts.mode is "phone" (the camera
// follows opts.me) or "screen" (the whole arena). opts.pos(kind, id) gives interpolated
// positions (net.js); opts.input() the local controls {mx, my, fire} for the player's
// own ship, which is drawn a little ahead of the stream so steering feels immediate.
// Returns {apply(snap), draw(), view()}.

export const ROLES = {
  planner: { color: "#ffb347", icon: "🗺️" },
  coder: { color: "#5ad1ff", icon: "💻" },
  tester: { color: "#7dff8a", icon: "🧪" },
  researcher: { color: "#c79bff", icon: "🔭" },
  reviewer: { color: "#ff8fb1", icon: "🔍" },
  deployer: { color: "#ff6b5e", icon: "🚀" },
  summarizer: { color: "#ffe16b", icon: "📝" },
  scraper: { color: "#6bffd8", icon: "🕸️" },
};
export const SHIP = { green: "#3dff8f", blue: "#4da3ff", red: "#ff5a6a", purple: "#b77dff", gold: "#ffd23d", pink: "#ff7ad9" };
export const shipColor = (c) => SHIP[c] || "#9fd6ff";
export const roleOf = (r) => ROLES[r] || { color: "#cfd8ff", icon: "🤖" };
export const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);

const TAU = Math.PI * 2;

export function startSpace(canvas, opts) {
  const ctx = canvas.getContext("2d");
  const phone = opts.mode === "phone";
  const K = phone ? 1 : 1.4; // sprites read bigger on the whole-arena big screen
  const pos = opts.pos || (() => null), input = opts.input || (() => ({ mx: 0, my: 0, fire: false }));
  let snap = null, snapAt = 0, me = opts.me || "";
  let W = 160, H = 100, CL = { x: 80, y: 50 }, ST = { x: 146, y: 88 };
  let vw = 1, vh = 1, dpr = 1, scale = 1;
  const cam = { x: 80, y: 50 };
  let shake = 0;

  // ---- starfield: three layers of stars over a repeating tile, and soft nebulas ----
  const rnd = mulberry(7);
  const layers = [0.15, 0.4, 0.75].map((depth, i) => ({
    depth,
    stars: Array.from({ length: [140, 90, 50][i] }, () => ({ x: rnd() * 400, y: rnd() * 400, r: (0.4 + rnd() * 0.9) * (i + 1) * 0.55, tw: rnd() * TAU, c: rnd() < 0.15 ? "#ffd9a8" : rnd() < 0.3 ? "#a8d4ff" : "#ffffff" })),
  }));
  const nebulas = [
    { x: 30, y: 20, r: 55, c: "rgba(140,80,255,0.16)" }, { x: 130, y: 75, r: 60, c: "rgba(40,190,255,0.12)" },
    { x: 120, y: 15, r: 40, c: "rgba(255,90,180,0.10)" }, { x: 25, y: 85, r: 45, c: "rgba(60,255,200,0.08)" },
  ];

  // ---- effects ----
  const parts = []; // {x, y, vx, vy, t, life, c, r}
  const rings = []; // {x, y, t, life, c, r0, r1}
  const floats = []; // {x, y, t, life, text, c}
  const sayAt = new Map(); // agent id -> {say, t} when its line arrived
  function burst(x, y, c, n = 18, sp = 14, life = 0.7) {
    for (let i = 0; i < n && parts.length < 600; i++) {
      const a = Math.random() * TAU, s = sp * (0.3 + Math.random());
      parts.push({ x, y, vx: Math.cos(a) * s, vy: Math.sin(a) * s, t: 0, life: life * (0.6 + Math.random() * 0.6), c, r: 0.25 + Math.random() * 0.35 });
    }
  }
  const ring = (x, y, c, r0, r1, life) => { if (rings.length < 80) rings.push({ x, y, t: 0, life, c, r0, r1 }); };
  const floatText = (x, y, text, c, life = 1.2) => { if (floats.length < 40) floats.push({ x, y, t: 0, life, text, c }); };

  function apply(s) {
    snap = s; snapAt = performance.now();
    if (s.me) me = s.me;
    W = s.arena[0]; H = s.arena[1];
    CL = { x: s.cluster[0], y: s.cluster[1] }; ST = { x: s.storage[0], y: s.storage[1] };
    for (const a of s.agents) {
      if (a.say && (!sayAt.has(a.id) || sayAt.get(a.id).say !== a.say)) sayAt.set(a.id, { say: a.say, t: performance.now() });
    }
    for (const id of sayAt.keys()) if (!s.agents.some((a) => a.id === id)) sayAt.delete(id);
    for (const e of s.events || []) {
      const a = s.agents.find((x) => x.id === e.agent);
      switch (e.kind) {
        case "spawned": if (a) ring(a.x, a.y, roleOf(a.role).color, 0.5, 6, 0.8); break;
        case "hit": if (a) { burst(a.x, a.y, "#ffffff", 6, 10, 0.3); if (e.player === me) shake = Math.max(shake, 0.25); } break;
        case "suspended":
          if (a) {
            burst(a.x, a.y, "#9ff4ff", 30, 18, 0.9);
            ring(a.x, a.y, "#9ff4ff", 1, 9, 0.6);
            floatText(a.x, a.y - 3, e.player === me ? `❄ +1 · ${e.ms} ms` : `❄ ${e.ms} ms`, "#bff8ff", 1.4);
            if (e.player === me) shake = Math.max(shake, 0.6);
          }
          break;
        case "slipped": if (a) floatText(a.x, a.y - 3, "slipped!", "#ffb38a"); break;
        case "over": shake = phone ? 1.2 : 0.6; break;
      }
    }
  }

  // ---- camera ----
  function resize() {
    dpr = Math.min(window.devicePixelRatio || 1, phone ? 2 : 1.5);
    vw = canvas.clientWidth || window.innerWidth;
    vh = canvas.clientHeight || window.innerHeight;
    canvas.width = Math.round(vw * dpr);
    canvas.height = Math.round(vh * dpr);
  }
  window.addEventListener("resize", resize);
  resize();
  const sx = (x) => (x - cam.x) * scale + vw / 2;
  const sy = (y) => (y - cam.y) * scale + vh / 2;

  // ---- the local ship, a little ahead of the stream ----
  const meView = { x: 0, y: 0, h: 0, ok: false };
  function predictMe(s, dt) {
    const p = pos("ships", s.id) || s;
    const inp = input(), m = Math.hypot(inp.mx || 0, inp.my || 0);
    const lead = Math.min((performance.now() - snapAt) / 1000 + 0.08, 0.2);
    const x = p.x + (s.vx || 0) * lead * 0.6, y = p.y + (s.vy || 0) * lead * 0.6;
    if (!meView.ok || Math.hypot(meView.x - x, meView.y - y) > 10) Object.assign(meView, { x, y, h: s.h, ok: true });
    const k = 1 - Math.exp(-dt * 14);
    meView.x += (x - meView.x) * k;
    meView.y += (y - meView.y) * k;
    const want = m > 0.12 ? Math.atan2(inp.my, inp.mx) : s.h;
    let d = want - meView.h;
    d = Math.atan2(Math.sin(d), Math.cos(d));
    meView.h += d * Math.min(1, dt * 18);
    return meView;
  }

  // ---- drawing helpers ----
  function glow(x, y, r, c, a = 1) {
    const g = ctx.createRadialGradient(x, y, 0, x, y, r);
    g.addColorStop(0, c);
    g.addColorStop(1, "rgba(0,0,0,0)");
    ctx.globalAlpha = a;
    ctx.fillStyle = g;
    ctx.beginPath(); ctx.arc(x, y, r, 0, TAU); ctx.fill();
    ctx.globalAlpha = 1;
  }

  function drawBackground(t) {
    const g = ctx.createLinearGradient(0, 0, 0, vh);
    g.addColorStop(0, "#0b0f2e"); g.addColorStop(1, "#05040f");
    ctx.fillStyle = g; ctx.fillRect(0, 0, vw, vh);
    for (const n of nebulas) glow(sx(n.x * 0.6 + cam.x * 0.4), sy(n.y * 0.6 + cam.y * 0.4), n.r * scale * 0.9, n.c);
    for (const L of layers) {
      const ox = -cam.x * scale * L.depth, oy = -cam.y * scale * L.depth, tile = 400;
      for (const st of L.stars) {
        let x = (st.x + ox) % tile, y = (st.y + oy) % tile;
        if (x < 0) x += tile; if (y < 0) y += tile;
        const a = 0.55 + 0.45 * Math.sin(t * 1.7 + st.tw);
        ctx.fillStyle = st.c;
        ctx.globalAlpha = a;
        for (let tx = x; tx < vw; tx += tile) for (let ty = y; ty < vh; ty += tile) ctx.fillRect(tx, ty, st.r, st.r);
      }
    }
    ctx.globalAlpha = 1;
    // The arena edge: a faint rounded fence.
    ctx.strokeStyle = "rgba(120,160,255,0.18)";
    ctx.lineWidth = 2;
    ctx.setLineDash([8, 10]);
    roundRect(sx(0), sy(0), W * scale, H * scale, 18 * (scale / 6));
    ctx.stroke();
    ctx.setLineDash([]);
  }
  function roundRect(x, y, w, h, r) {
    ctx.beginPath();
    ctx.moveTo(x + r, y); ctx.arcTo(x + w, y, x + w, y + h, r); ctx.arcTo(x + w, y + h, x, y + h, r);
    ctx.arcTo(x, y + h, x, y, r); ctx.arcTo(x, y, x + w, y, r); ctx.closePath();
  }

  // The cluster: a blue wheel with one light per worker round it, and a load ring.
  function drawCluster(t, fill) {
    const x = sx(CL.x), y = sy(CL.y), u = scale;
    const slots = Math.max(1, fill.slots), busy = Math.min(slots, fill.running), f = busy / slots;
    const hot = f < 0.5 ? "#3dff8f" : f < 0.8 ? "#ffcc33" : "#ff4d5e";
    glow(x, y, 9 * u, f < 0.8 ? "rgba(60,120,255,0.35)" : "rgba(255,60,80,0.35)");
    // load ring
    ctx.lineWidth = 0.8 * u;
    ctx.strokeStyle = "rgba(255,255,255,0.08)";
    ctx.beginPath(); ctx.arc(x, y, 6.6 * u, 0, TAU); ctx.stroke();
    ctx.strokeStyle = hot;
    ctx.beginPath(); ctx.arc(x, y, 6.6 * u, -Math.PI / 2, -Math.PI / 2 + TAU * f); ctx.stroke();
    // worker lights
    for (let i = 0; i < slots; i++) {
      const a = (i / slots) * TAU - Math.PI / 2 + t * 0.15, r = 5.2 * u;
      const on = i < busy;
      ctx.fillStyle = on ? hot : "rgba(160,190,255,0.25)";
      const s = 0.9 * u;
      ctx.save(); ctx.translate(x + Math.cos(a) * r, y + Math.sin(a) * r); ctx.rotate(a);
      roundRect(-s / 2, -s / 2, s, s, s * 0.3); ctx.fill();
      ctx.restore();
    }
    // the wheel
    ctx.save(); ctx.translate(x, y); ctx.rotate(t * 0.4);
    ctx.fillStyle = "#326ce5";
    ctx.beginPath();
    for (let i = 0; i < 7; i++) { const a = (i / 7) * TAU - Math.PI / 2; ctx.lineTo(Math.cos(a) * 3.3 * u, Math.sin(a) * 3.3 * u); }
    ctx.closePath(); ctx.fill();
    ctx.strokeStyle = "#fff"; ctx.lineWidth = 0.32 * u;
    ctx.beginPath(); ctx.arc(0, 0, 1.6 * u, 0, TAU); ctx.stroke();
    for (let i = 0; i < 7; i++) { const a = (i / 7) * TAU - Math.PI / 2; ctx.beginPath(); ctx.moveTo(Math.cos(a) * 0.7 * u, Math.sin(a) * 0.7 * u); ctx.lineTo(Math.cos(a) * 2.5 * u, Math.sin(a) * 2.5 * u); ctx.stroke(); }
    ctx.restore();
    label(x, y + 8.4 * u, `CLUSTER ${busy}/${slots} WORKERS`, hot, Math.max(10, 1.25 * u));
  }

  // Snapshot Storage: a little silo with a stack of capsules.
  function drawStorage(t, stored) {
    const x = sx(ST.x), y = sy(ST.y), u = scale;
    glow(x, y, 7 * u, "rgba(80,230,255,0.25)");
    ctx.fillStyle = "#1b2b4a";
    roundRect(x - 3.4 * u, y - 3.6 * u, 6.8 * u, 7.2 * u, 1.4 * u); ctx.fill();
    ctx.strokeStyle = "#6ff0ff"; ctx.lineWidth = 0.25 * u; ctx.stroke();
    const n = Math.min(stored, 12);
    for (let i = 0; i < n; i++) {
      const cx = x - 2.2 * u + (i % 3) * 2.2 * u, cy = y + 2.4 * u - Math.floor(i / 3) * 1.4 * u;
      capsuleShape(cx, cy, 0.9 * u, 0.5 * u, "#9ff4ff", 0.9);
    }
    label(x, y + 5.6 * u, `SNAPSHOT STORAGE · ${stored}`, "#9ff4ff", Math.max(9, 1.05 * u));
  }
  function capsuleShape(x, y, w, h, c, a = 1) {
    ctx.globalAlpha = a;
    ctx.fillStyle = c;
    roundRect(x - w, y - h, 2 * w, 2 * h, h); ctx.fill();
    ctx.fillStyle = "rgba(255,255,255,0.6)";
    roundRect(x - w * 0.7, y - h * 0.6, w * 0.8, h * 0.4, h * 0.2); ctx.fill();
    ctx.globalAlpha = 1;
  }

  function label(x, y, text, c, px, bg = false) {
    ctx.font = `700 ${px}px "JetBrains Mono", ui-monospace, monospace`;
    ctx.textAlign = "center"; ctx.textBaseline = "middle";
    if (bg) {
      const w = ctx.measureText(text).width + px;
      ctx.fillStyle = "rgba(6,10,30,0.85)";
      roundRect(x - w / 2, y - px * 0.8, w, px * 1.6, px * 0.5); ctx.fill();
    }
    ctx.lineWidth = 3; ctx.strokeStyle = "rgba(0,0,0,0.6)"; ctx.strokeText(text, x, y);
    ctx.fillStyle = c; ctx.fillText(text, x, y);
  }

  function bubble(x, y, text, px, c) {
    ctx.font = `600 ${px}px "JetBrains Mono", ui-monospace, monospace`;
    const w = Math.min(ctx.measureText(text).width + px * 1.2, phone ? vw * 0.7 : 320);
    ctx.fillStyle = "rgba(10,16,40,0.92)";
    ctx.strokeStyle = c; ctx.lineWidth = 1.5;
    roundRect(x - w / 2, y - px * 1.9, w, px * 1.8, px * 0.6); ctx.fill(); ctx.stroke();
    ctx.beginPath(); ctx.moveTo(x - px * 0.4, y - px * 0.1); ctx.lineTo(x, y + px * 0.5); ctx.lineTo(x + px * 0.4, y - px * 0.1); ctx.fill();
    ctx.fillStyle = "#e9f1ff"; ctx.textAlign = "center"; ctx.textBaseline = "middle";
    let tx = text;
    while (ctx.measureText(tx).width > w - px && tx.length > 4) tx = tx.slice(0, -2);
    ctx.fillText(tx === text ? text : tx + "…", x, y - px);
  }

  // A subagent: a round little bot in its role's colour with big eyes and its icon.
  function drawAgent(a, p, t) {
    const R = roleOf(a.role), u = scale * K, x = sx(p.x), y = sy(p.y), r = 2.0 * u;
    if (a.state === "suspended") {
      glow(x, y, 3 * u, "rgba(120,240,255,0.5)");
      ctx.save(); ctx.translate(x, y); ctx.rotate(Math.sin(t * 2 + p.x) * 0.3);
      capsuleShape(0, 0, 1.7 * u, 0.9 * u, "#8fefff");
      ctx.restore();
      ctx.font = `${Math.max(8, 1.1 * u)}px sans-serif`; ctx.textAlign = "center"; ctx.textBaseline = "middle";
      ctx.fillText("❄", x, y);
      if (Math.random() < 0.3) parts.push({ x: p.x, y: p.y, vx: (Math.random() - 0.5) * 2, vy: (Math.random() - 0.5) * 2, t: 0, life: 0.5, c: "#9ff4ff", r: 0.2 });
      return;
    }
    const sus = a.state === "suspending";
    const bob = Math.sin(t * 3 + p.x * 0.7) * 0.25 * u;
    glow(x, y + bob, r * 1.9, sus ? "rgba(140,240,255,0.55)" : hexA(R.color, 0.35));
    // antenna
    ctx.strokeStyle = sus ? "#bff8ff" : R.color; ctx.lineWidth = 0.22 * u;
    ctx.beginPath(); ctx.moveTo(x, y + bob - r * 0.9); ctx.lineTo(x + 0.3 * u, y + bob - r * 1.45); ctx.stroke();
    ctx.fillStyle = a.done ? "#ffffff" : (Math.sin(t * 8) > 0 ? R.color : "#ffffff");
    ctx.beginPath(); ctx.arc(x + 0.3 * u, y + bob - r * 1.5, 0.28 * u, 0, TAU); ctx.fill();
    // body
    const g = ctx.createRadialGradient(x - r * 0.35, y + bob - r * 0.4, r * 0.1, x, y + bob, r);
    g.addColorStop(0, sus ? "#e8fdff" : lighten(R.color));
    g.addColorStop(1, sus ? "#5fb8cc" : R.color);
    ctx.fillStyle = g;
    ctx.beginPath(); ctx.arc(x, y + bob, r, 0, TAU); ctx.fill();
    if (a.hits > 0 && !sus) { ctx.strokeStyle = "#fff"; ctx.lineWidth = 0.3 * u; ctx.beginPath(); ctx.arc(x, y + bob, r + 0.4 * u, 0, TAU); ctx.stroke(); }
    // eyes look at the cluster
    const dx = CL.x - p.x, dy = CL.y - p.y, dl = Math.hypot(dx, dy) || 1, lx = (dx / dl) * 0.22 * u, ly = (dy / dl) * 0.22 * u;
    for (const s of [-1, 1]) {
      const ex = x + s * 0.7 * u, ey = y + bob - 0.25 * u;
      ctx.fillStyle = "#fff"; ctx.beginPath(); ctx.ellipse(ex, ey, 0.52 * u, sus ? 0.12 * u : 0.62 * u, 0, 0, TAU); ctx.fill();
      if (!sus) { ctx.fillStyle = "#14183a"; ctx.beginPath(); ctx.arc(ex + lx, ey + ly, 0.27 * u, 0, TAU); ctx.fill(); }
    }
    // smile
    ctx.strokeStyle = "rgba(20,24,58,0.8)"; ctx.lineWidth = 0.18 * u;
    ctx.beginPath(); ctx.arc(x, y + bob + 0.45 * u, 0.5 * u, 0.15 * Math.PI, 0.85 * Math.PI); ctx.stroke();
    // role badge
    ctx.font = `${Math.max(9, 1.3 * u)}px sans-serif`; ctx.textAlign = "center"; ctx.textBaseline = "middle";
    ctx.fillText(R.icon, x + r * 0.95, y + bob - r * 0.8);
    if (sus) {
      ctx.strokeStyle = `rgba(160,245,255,${0.5 + 0.5 * Math.sin(t * 12)})`; ctx.lineWidth = 0.35 * u;
      ctx.setLineDash([0.6 * u, 0.5 * u]);
      ctx.beginPath(); ctx.arc(x, y + bob, r + 0.9 * u, t * 3, t * 3 + TAU); ctx.stroke();
      ctx.setLineDash([]);
      label(x, y + bob + r + 1.6 * u, "SUSPENDING…", "#bff8ff", Math.max(9, 0.95 * u));
    } else {
      label(x, y + bob + r + 1.4 * u, a.name, hexA(R.color, 0.95), Math.max(8, 0.85 * u));
    }
    const s = sayAt.get(a.id);
    const showFor = phone ? 5000 : 4000;
    if (s && performance.now() - s.t < showFor) bubble(x, y + bob - r * 1.8, `“${s.say}”`, Math.max(9, phone ? 1.05 * u : 0.9 * u), R.color);
  }

  // A ship: a rounded rocket in the player's colour, nose along its heading.
  function drawShip(s, p, h, mine, t) {
    const c = shipColor(s.color), u = scale * K * (mine ? 1.15 : 1), x = sx(p.x), y = sy(p.y);
    const moving = Math.hypot(s.vx, s.vy) > 2 || (mine && Math.hypot(input().mx || 0, input().my || 0) > 0.12);
    ctx.save(); ctx.translate(x, y); ctx.rotate(h);
    if (!s.online) ctx.globalAlpha = 0.35;
    if (moving) {
      const fl = 1.4 + Math.random() * 0.8;
      const g = ctx.createLinearGradient(-1.2 * u, 0, -(1.2 + fl) * u, 0);
      g.addColorStop(0, "#fff7c0"); g.addColorStop(0.4, "#ffae3d"); g.addColorStop(1, "rgba(255,80,40,0)");
      ctx.fillStyle = g;
      ctx.beginPath(); ctx.moveTo(-1.0 * u, -0.55 * u); ctx.lineTo(-(1.2 + fl) * u, 0); ctx.lineTo(-1.0 * u, 0.55 * u); ctx.fill();
    }
    // fins
    ctx.fillStyle = shade(c);
    ctx.beginPath(); ctx.moveTo(-0.6 * u, -0.6 * u); ctx.lineTo(-1.4 * u, -1.4 * u); ctx.lineTo(-1.2 * u, -0.3 * u); ctx.fill();
    ctx.beginPath(); ctx.moveTo(-0.6 * u, 0.6 * u); ctx.lineTo(-1.4 * u, 1.4 * u); ctx.lineTo(-1.2 * u, 0.3 * u); ctx.fill();
    // hull
    ctx.fillStyle = c;
    ctx.beginPath();
    ctx.moveTo(1.9 * u, 0);
    ctx.quadraticCurveTo(0.9 * u, -1.0 * u, -1.1 * u, -0.75 * u);
    ctx.quadraticCurveTo(-1.5 * u, 0, -1.1 * u, 0.75 * u);
    ctx.quadraticCurveTo(0.9 * u, 1.0 * u, 1.9 * u, 0);
    ctx.fill();
    if (mine) { ctx.strokeStyle = "#ffffff"; ctx.lineWidth = 0.2 * u; ctx.stroke(); }
    // cockpit
    ctx.fillStyle = "#cdefff";
    ctx.beginPath(); ctx.ellipse(0.55 * u, 0, 0.55 * u, 0.4 * u, 0, 0, TAU); ctx.fill();
    ctx.fillStyle = "rgba(255,255,255,0.8)";
    ctx.beginPath(); ctx.ellipse(0.7 * u, -0.12 * u, 0.2 * u, 0.12 * u, 0, 0, TAU); ctx.fill();
    ctx.restore();
    label(x, y - 2.3 * u, s.nick, mine ? "#ffffff" : c, Math.max(9, (mine ? 1.0 : 0.85) * scale));
  }

  function drawBullets(s, age) {
    for (const b of s.bullets) {
      const x = b.x + b.vx * age, y = b.y + b.vy * age;
      const owner = s.ships.find((o) => o.id === b.owner);
      const c = owner ? shipColor(owner.color) : "#fff";
      const n = Math.hypot(b.vx, b.vy) || 1, tx = (b.vx / n) * 2.2, ty = (b.vy / n) * 2.2;
      ctx.strokeStyle = c; ctx.lineWidth = Math.max(2, 0.45 * scale); ctx.lineCap = "round";
      ctx.globalAlpha = 0.9;
      ctx.beginPath(); ctx.moveTo(sx(x - tx), sy(y - ty)); ctx.lineTo(sx(x), sy(y)); ctx.stroke();
      ctx.globalAlpha = 1;
      ctx.fillStyle = "#fff"; ctx.beginPath(); ctx.arc(sx(x), sy(y), Math.max(1.5, 0.28 * scale), 0, TAU); ctx.fill();
    }
    ctx.lineCap = "butt";
  }

  // Off-screen pointers (phone): the nearest running agents and the cluster.
  function drawPointers(s, from) {
    const pad = 26;
    const items = s.agents.filter((a) => a.state === "running").map((a) => ({ a, p: pos("agents", a.id) || a }))
      .map((o) => ({ ...o, d: Math.hypot(o.p.x - from.x, o.p.y - from.y) }))
      .filter((o) => { const x = sx(o.p.x), y = sy(o.p.y); return x < 0 || y < 0 || x > vw || y > vh; })
      .sort((a, b) => a.d - b.d).slice(0, 4);
    const pts = items.map((o) => ({ x: o.p.x, y: o.p.y, c: roleOf(o.a.role).color, icon: roleOf(o.a.role).icon }));
    const cx = sx(CL.x), cy = sy(CL.y);
    if (cx < 0 || cy < 0 || cx > vw || cy > vh) pts.push({ x: CL.x, y: CL.y, c: "#326ce5", icon: "☸" });
    for (const q of pts) {
      const dx = sx(q.x) - vw / 2, dy = sy(q.y) - vh / 2, k = Math.min((vw / 2 - pad) / Math.abs(dx || 1e-6), (vh / 2 - pad) / Math.abs(dy || 1e-6));
      const x = vw / 2 + dx * k, y = vh / 2 + dy * k, a = Math.atan2(dy, dx);
      ctx.save(); ctx.translate(x, y); ctx.rotate(a);
      ctx.fillStyle = q.c; ctx.beginPath(); ctx.moveTo(12, 0); ctx.lineTo(-6, -8); ctx.lineTo(-6, 8); ctx.fill();
      ctx.restore();
      ctx.font = "14px sans-serif"; ctx.textAlign = "center"; ctx.textBaseline = "middle";
      ctx.fillText(q.icon, x - Math.cos(a) * 18, y - Math.sin(a) * 18);
    }
  }

  let last = performance.now(), clock = 0;
  function draw() {
    const now = performance.now(), dt = Math.min((now - last) / 1000, 0.1);
    last = now; clock += dt;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    const s = snap;
    if (phone) scale = Math.min(vw, vh) / 40;
    else scale = Math.min(vw / (W + 4), vh / (H + 4));
    let mine = null;
    if (s) {
      const ms = s.ships.find((x) => x.id === me);
      if (phone && ms) {
        mine = predictMe(ms, dt);
        const k = 1 - Math.exp(-dt * 6);
        cam.x += (mine.x - cam.x) * k; cam.y += (mine.y - cam.y) * k;
      } else if (!phone) { cam.x = W / 2; cam.y = H / 2; }
    }
    shake = Math.max(0, shake - dt * 2.5);
    const shx = (Math.random() - 0.5) * shake * 0.8, shy = (Math.random() - 0.5) * shake * 0.8;
    cam.x += shx; cam.y += shy;
    drawBackground(clock);
    if (s) {
      drawStorage(clock, s.fill.stored);
      drawCluster(clock, s.fill);
      for (const r of rings) {
        r.t += dt;
        const f = r.t / r.life;
        ctx.strokeStyle = r.c; ctx.globalAlpha = Math.max(0, 1 - f); ctx.lineWidth = 0.3 * scale;
        ctx.beginPath(); ctx.arc(sx(r.x), sy(r.y), (r.r0 + (r.r1 - r.r0) * f) * scale, 0, TAU); ctx.stroke();
      }
      ctx.globalAlpha = 1;
      for (let i = rings.length - 1; i >= 0; i--) if (rings[i].t > rings[i].life) rings.splice(i, 1);
      for (const a of s.agents) drawAgent(a, pos("agents", a.id) || a, clock);
      drawBullets(s, Math.min((now - snapAt) / 1000, 0.12));
      for (const sh of s.ships) {
        if (!sh.online && sh.id !== me) continue;
        if (sh.id === me && mine) drawShip(sh, mine, mine.h, true, clock);
        else drawShip(sh, pos("ships", sh.id) || sh, sh.h, false, clock);
      }
      for (let i = parts.length - 1; i >= 0; i--) {
        const q = parts[i];
        q.t += dt;
        if (q.t > q.life) { parts.splice(i, 1); continue; }
        q.x += q.vx * dt; q.y += q.vy * dt; q.vx *= 0.96; q.vy *= 0.96;
        ctx.globalAlpha = 1 - q.t / q.life; ctx.fillStyle = q.c;
        ctx.beginPath(); ctx.arc(sx(q.x), sy(q.y), q.r * scale, 0, TAU); ctx.fill();
      }
      ctx.globalAlpha = 1;
      for (let i = floats.length - 1; i >= 0; i--) {
        const f = floats[i];
        f.t += dt;
        if (f.t > f.life) { floats.splice(i, 1); continue; }
        ctx.globalAlpha = Math.min(1, 2 * (1 - f.t / f.life));
        label(sx(f.x), sy(f.y - f.t * 3), f.text, f.c, Math.max(11, 1.2 * scale));
        ctx.globalAlpha = 1;
      }
      if (phone && mine) drawPointers(s, mine);
    }
    cam.x -= shx; cam.y -= shy;
  }

  return {
    apply,
    draw,
    resize,
    view: () => ({ x: cam.x, y: cam.y, scale }),
  };
}

function hexA(hex, a) {
  const n = parseInt(hex.slice(1), 16);
  return `rgba(${n >> 16},${(n >> 8) & 255},${n & 255},${a})`;
}
function lighten(hex) {
  const n = parseInt(hex.slice(1), 16), f = (v) => Math.min(255, v + 90);
  return `rgb(${f(n >> 16)},${f((n >> 8) & 255)},${f(n & 255)})`;
}
function shade(hex) {
  const n = parseInt(hex.slice(1), 16), f = (v) => Math.round(v * 0.6);
  return `rgb(${f(n >> 16)},${f((n >> 8) & 255)},${f(n & 255)})`;
}
function mulberry(a) {
  return () => {
    a |= 0; a = (a + 0x6d2b79f5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}
