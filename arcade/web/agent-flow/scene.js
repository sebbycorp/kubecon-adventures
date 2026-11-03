// Agent Flow big screen: an isometric map of the real components (agentgateway,
// kagent-controller, the Agent Substrate control plane, gVisor sandboxes, rustfs) with a
// cube for every live request, moved by the stages the backend streams.
"use strict";

const NS = "http://www.w3.org/2000/svg";
const S = 36, OX = 640, OY = 150;
const P = (x, y, z = 0) => [OX + (x - y) * S * 0.866, OY + (x + y) * S * 0.5 - z * S];
const pts = (a) => a.map((p) => p.join(",")).join(" ");
function el(t, a, parent) {
  const e = document.createElementNS(NS, t);
  for (const k in a) e.setAttribute(k, a[k]);
  parent.appendChild(e);
  return e;
}

// The belt the cubes ride, from the phones (off screen left) to the LLM (off screen top right).
const NODES = [
  { key: "phones", at: [-14, 4] },
  { key: "gw", at: [2, 4] },
  { key: "ctrl", at: [7, 4] },
  { key: "ate", at: [7, 9] },
  { key: "box", at: [13, 9] },
  { key: "egress", at: [13, 4] },
  { key: "llm", at: [13, -14] },
];
const DIST = [0];
for (let i = 1; i < NODES.length; i++) {
  const [a, b] = [NODES[i - 1].at, NODES[i].at];
  DIST.push(DIST[i - 1] + Math.hypot(b[0] - a[0], b[1] - a[1]));
}
const NODE = Object.fromEntries(NODES.map((n, i) => [n.key, i]));
// Where each backend stage sends a request's cube.
const STAGE_NODE = {
  gateway: "gw", controller: "ctrl", schedule: "ate", restore: "ate", sandbox: "box",
  llm: "llm", tool: "box", hook: "egress", reply: "phones", snapshot: "phones", asleep: "phones", error: "phones",
};
const DONE = new Set(["reply", "snapshot", "asleep", "error"]);

// GAMES names the kagent Agents of every arcade game (and the stray test agents), with
// the colour their actors get on the map. Agents not listed still show, in grey.
const GAMES = {
  duck: ["Duck Hunt", "#facc15"], "duck-direct": ["Duck Hunt test", "#a3893a"], "duck-noschema": ["Duck Hunt test", "#a3893a"],
  "hello-duck": ["kagent demo", "#9ca3af"], flowbot: ["Agent Flow", "#5fd068"], heister: ["Substrate Siege", "#38bdf8"],
  sidekick: ["Substrate Siege ally", "#7dd3fc"], subagent: ["Cluster Defense", "#fb923c"], rogue: ["Gateway Defense", "#22d3ee"], loadbot: ["load test", "#6b7280"],
};
const gameOf = (agent) => GAMES[agent] || [agent, "#9ca3af"];

function posAt(s) {
  let i = 1;
  while (i < DIST.length - 1 && s > DIST[i]) i++;
  const [a, b] = [NODES[i - 1].at, NODES[i].at];
  const f = Math.min(1, Math.max(0, (s - DIST[i - 1]) / (DIST[i] - DIST[i - 1] || 1)));
  return [a[0] + (b[0] - a[0]) * f, a[1] + (b[1] - a[1]) * f];
}

function createScene(svg, opts = {}) {
  const L = {};
  for (const k of ["grid", "island", "shadow", "floor", "obj", "orbs", "cubes", "lab"]) L[k] = el("g", {}, svg);
  for (let i = -14; i <= 24; i++) {
    el("line", { x1: P(i, -14)[0], y1: P(i, -14)[1], x2: P(i, 24)[0], y2: P(i, 24)[1], stroke: "var(--grid)" }, L.grid);
    el("line", { x1: P(-14, i)[0], y1: P(-14, i)[1], x2: P(24, i)[0], y2: P(24, i)[1], stroke: "var(--grid)" }, L.grid);
  }
  function box(x, y, w, d, h, g = L.obj, o = {}) {
    const c = el("g", {}, g), st = o.st || "#9a9fa4", z = o.z || 0;
    el("polygon", { points: pts([P(x + w, y, z), P(x + w, y + d, z), P(x + w, y + d, z + h), P(x + w, y, z + h)]), fill: o.f2 || "#101112", stroke: st }, c);
    el("polygon", { points: pts([P(x, y + d, z), P(x + w, y + d, z), P(x + w, y + d, z + h), P(x, y + d, z + h)]), fill: o.f3 || "#0c0d0e", stroke: st }, c);
    el("polygon", { points: pts([P(x, y, z + h), P(x + w, y, z + h), P(x + w, y + d, z + h), P(x, y + d, z + h)]), fill: o.f1 || "url(#topface)", stroke: o.st ? st : "#b9bec2" }, c);
    return c;
  }
  // belts draws the conveyor as one path with rounded corners, then its slats and the
  // moving dashes that always flow from the phones toward the LLM.
  function belts() {
    const d = "M" + NODES.map((n) => P(...n.at).join(",")).join(" L");
    const bw = 2 * 0.866 * S * 0.55;
    el("path", { d, fill: "none", stroke: "#5d6166", "stroke-width": bw + 2, "stroke-linejoin": "round" }, L.floor);
    el("path", { d, fill: "none", stroke: "#141516", "stroke-width": bw, "stroke-linejoin": "round" }, L.floor);
    for (let i = 1; i < NODES.length; i++) {
      const [x1, y1] = NODES[i - 1].at, [x2, y2] = NODES[i].at, hz = y1 === y2, w = 0.53;
      const lo = (hz ? Math.min(x1, x2) : Math.min(y1, y2)) + 0.6, hi = (hz ? Math.max(x1, x2) : Math.max(y1, y2)) - 0.6;
      for (let t = lo; t < hi; t += 0.33) {
        const [p, r] = hz ? [P(t, y1 - w), P(t, y1 + w)] : [P(x1 - w, t), P(x1 + w, t)];
        el("line", { x1: p[0], y1: p[1], x2: r[0], y2: r[1], stroke: "#232527" }, L.floor);
      }
    }
    el("path", { d, class: "flowline" }, L.floor);
  }
  // orbit is a station's ring; its arrows circle it (faster while the station is busy).
  function orbit(x, y, r) {
    const [cx, cy] = P(x, y), g = el("g", {}, L.floor), rx = r * S * 1.2247, ry = r * S * 0.7071;
    const e = el("ellipse", { cx, cy, rx, ry, fill: "none", stroke: "var(--green)", opacity: 0.18 }, g);
    const arrows = [0.3, 1.9, 3.5, 5.1].map((a) => ({ a, p: el("path", { d: "M-5,-4 L0,0 L-5,4", fill: "none", stroke: "var(--green)", opacity: 0.45 }, g) }));
    const o = { e, arrows, cx, cy, rx, ry, phase: Math.random() * 6 };
    o.spin = (dt, on) => {
      o.phase += dt * (on ? 2.4 : 0.3);
      for (const ar of arrows) {
        const a = ar.a + o.phase, x2 = cx + Math.cos(a) * rx, y2 = cy + Math.sin(a) * ry;
        const dir = Math.atan2(Math.cos(a) * ry, -Math.sin(a) * rx) * 57.3; // tangent, clockwise on screen
        ar.p.setAttribute("transform", `translate(${x2} ${y2}) rotate(${dir + 180})`);
        ar.p.setAttribute("opacity", on ? 0.9 : 0.4);
      }
    };
    o.spin(0, false);
    return o;
  }
  function cyl(x, y, h) {
    const [cx, cy] = P(x, y), rx = 0.34 * S * 1.22, ry = 0.34 * S * 0.7;
    el("path", { d: `M${cx - rx},${cy} v${-h * S} a${rx},${ry} 0 0 0 ${2 * rx},0 v${h * S} a${rx},${ry} 0 0 1 ${-2 * rx},0`, fill: "#101112", stroke: "#9a9fa4" }, L.obj);
    return el("ellipse", { cx, cy: cy - h * S, rx, ry, fill: "var(--greenDim)", stroke: "var(--green)" }, L.obj);
  }
  const LAB = {};
  function label(key, x, y, z, name, role, icon, short) {
    const [a, b] = P(x, y, z), g = el("g", { class: "station" }, L.lab);
    if (opts.onPick) g.addEventListener("click", () => opts.onPick(key));
    el("title", {}, g).textContent = `${name}: ${role}`;
    if (opts.compact) {
      const n = short || name, w = n.length * 15 + 26 + (icon ? 34 : 0);
      const r = el("rect", { x: a - w / 2, y: b - 22, width: w, height: 40, rx: 10, fill: "#121314", stroke: "#34373a" }, g);
      const d = document.createElementNS(NS, "circle"); // no status dot; the border shows activity
      el("text", { x: a - w / 2 + 14, y: b + 7, fill: "#f0f0f0", "font-size": 25, "font-weight": 600 }, g).textContent = n;
      if (icon) el("image", { href: icon, x: a + w / 2 - 36, y: b - 15, width: 28, height: 26 }, g);
      LAB[key] = { r, d };
      return;
    }
    let w = Math.max(name.length * 7.6, role.length * 5.9) + 22;
    if (icon) w += 26;
    const r = el("rect", { x: a - w / 2, y: b - 21, width: w, height: 38, rx: 8, fill: "#121314", stroke: "#34373a" }, g);
    const d = document.createElementNS(NS, "circle"); // no status dot; the border shows activity
    el("text", { x: a - w / 2 + 11, y: b - 4, fill: "#e6e7e8", "font-size": 12.5, "font-weight": 600 }, g).textContent = name;
    el("text", { x: a - w / 2 + 11, y: b + 10, fill: "#7d8287", "font-size": 10.5 }, g).textContent = role;
    if (icon) el("image", { href: icon, x: a + w / 2 - 29, y: b - 14, width: 23, height: 21 }, g);
    g.setAttribute("opacity", 0.82);
    LAB[key] = { r, d };
  }

  const style = el("style", {}, svg);
  style.textContent = `.flowline { fill: none; stroke: var(--green); stroke-width: 2; stroke-dasharray: 3 16; opacity: .35;
      animation: agf-flow var(--flowdur, 1.8s) linear infinite; }
    @keyframes agf-flow { to { stroke-dashoffset: -38; } }
    .station { cursor: pointer; } .station:hover rect { stroke: var(--green); }
    .hit { fill: transparent; cursor: pointer; } .pickable { cursor: pointer; }`;
  L.fx = el("g", {}, svg);
  // The map sits on a raised slab, like a diorama; machines cast soft shadows on it.
  {
    const [x0, y0, x1, y1, t] = [-0.6, 0.5, 19.6, 13.6, 0.45];
    el("polygon", { points: pts([P(x0, y1, 0), P(x1, y1, 0), P(x1, y1, -t), P(x0, y1, -t)]), fill: "url(#slabfront)", stroke: "#2c3033" }, L.island);
    el("polygon", { points: pts([P(x1, y0, 0), P(x1, y1, 0), P(x1, y1, -t), P(x1, y0, -t)]), fill: "url(#slabside)", stroke: "#2c3033" }, L.island);
    el("polygon", { points: pts([P(x0, y0), P(x1, y0), P(x1, y1), P(x0, y1)]), fill: "#101112", "fill-opacity": 0.82, stroke: "#3a3f43" }, L.island);
    const [a, b] = [P(x0, y1), P(x1, y1)], [c] = [P(x1, y0)];
    el("polyline", { points: pts([a, b, c]), fill: "none", stroke: "var(--green)", opacity: 0.35, "stroke-width": 1.5, filter: "url(#glow)" }, L.island);
  }
  const shadow = (x, y, w, d, k = 0.35) => el("polygon", { points: pts([P(x + k, y + k), P(x + w + k, y + k), P(x + w + k, y + d + k), P(x + k, y + d + k)]), fill: "#000", opacity: 0.55, filter: "url(#soft)" }, L.shadow);
  for (const [x, y, w, d] of [[1.3, 2.9, 0.9, 0.5], [1.3, 4.6, 0.9, 0.5], [8.0, 2.0, 1.8, 1.5], [4.2, 10.2, 0.8, 0.8], [11.6, 8.3, 2.8, 1.4], [14.5, 8.75, 0.5, 0.5], [15.9, 10.3, 1.2, 1.0], [11.75, 3.85, 0.35, 0.35], [6, 8, 2, 2], [9.1, 5.3, 0.6, 0.6]]) shadow(x, y, w, d);
  belts();
  const ORB = {};
  for (const n of NODES.slice(1, -1)) ORB[n.key] = orbit(...n.at, n.key === "box" ? 2.6 : 1.9);
  const disc = (x, y, r, h, g = L.obj, o = {}) => {
    const [cx, cy] = P(x, y, o.z || 0), rx = r * S * 1.2247, ry = r * S * 0.7071;
    el("path", { d: `M${cx - rx},${cy} v${-h * S} a${rx},${ry} 0 0 0 ${2 * rx},0 v${h * S} a${rx},${ry} 0 0 1 ${-2 * rx},0`, fill: o.f || "#101112", stroke: o.st || "#9a9fa4" }, g);
    return el("ellipse", { cx, cy: cy - h * S, rx, ry, fill: o.top || "#161718", stroke: o.st || "#9a9fa4" }, g);
  };
  const dyn = el("g", {}, L.obj); // machine parts redrawn every frame

  // Every component is its own kind of machine:
  // agentgateway, a security speed gate: drawn in machines(), since its flaps and lights move.

  // kagent-controller, a card reader: a dock on the belt and a block with a slot that takes a session card.
  box(6.3, 3.3, 1.4, 1.4, 0.12);
  box(8.0, 2.0, 1.8, 1.5, 0.9, L.obj, { f1: "#18191a" });
  const slot = el("polygon", { points: pts([P(8.35, 2.68, 0.9), P(9.45, 2.68, 0.9), P(9.45, 2.82, 0.9), P(8.35, 2.82, 0.9)]), fill: "#060707", stroke: "#5d6166" }, L.obj);
  // The session card is drawn in front of the block but clipped at the slot, so the part
  // that has gone into the reader disappears.
  const cardG = el("g", { "clip-path": "url(#cardclip)" }, L.obj);
  const pg = cyl(9.4, 5.6, 0.75);
  // ate-api-server + controller, a turntable that places each actor.
  disc(7, 9, 1.15, 0.22);
  el("ellipse", { cx: P(7, 9, 0.22)[0], cy: P(7, 9, 0.22)[1], rx: 0.8 * S * 1.2247, ry: 0.8 * S * 0.7071, fill: "none", stroke: "#3a3d40" }, L.obj);
  // ate-idp, a badge kiosk that prints an identity for every new actor.
  box(4.2, 10.2, 0.8, 0.8, 0.6, L.obj, { f1: "#18191a" });
  el("polygon", { points: pts([P(4.35, 10.55, 0.6), P(4.85, 10.55, 0.6), P(4.85, 10.65, 0.6), P(4.35, 10.65, 0.6)]), fill: "#060707", stroke: "#5d6166" }, L.obj);
  const idp = el("circle", { cx: P(4.9, 11, 0.45)[0], cy: P(4.9, 11, 0.45)[1], r: 3.5, fill: "var(--greenDim)" }, L.obj);
  // atelet + gVisor, a glass cage on a platform, with a crane that reaches in.
  box(11.6, 8.4, 2.8, 1.2, 0.15);
  // A tower crane beside the cage: footing, lattice mast, cab, peak with tie cables, and a
  // jib over the cage balanced by a counterweight (its trolley and claw move in machines()).
  box(14.5, 8.75, 0.5, 0.5, 0.18);
  box(14.6, 8.85, 0.3, 0.3, 3.1, L.obj, { z: 0.18 });
  for (let z = 0.18; z < 3.1; z += 0.48) { // lattice bracing on the two visible faces
    const q = [[P(14.6, 9.15, z), P(14.9, 9.15, z + 0.48)], [P(14.9, 9.15, z), P(14.6, 9.15, z + 0.48)], [P(14.9, 8.85, z), P(14.9, 9.15, z + 0.48)], [P(14.9, 9.15, z), P(14.9, 8.85, z + 0.48)]];
    for (const [a, b] of q) el("line", { x1: a[0], y1: a[1], x2: b[0], y2: b[1], stroke: "#5d6166" }, L.obj);
  }
  box(14.55, 8.8, 0.4, 0.4, 0.32, L.obj, { z: 3.0, f1: "#1b1c1d" }); // cab
  box(11.75, 8.92, 4.4, 0.16, 0.16, L.obj, { z: 3.32 }); // jib and counter-jib
  box(15.55, 8.8, 0.5, 0.4, 0.42, L.obj, { z: 3.48, f1: "#1b1c1d" }); // counterweight
  box(14.67, 8.92, 0.16, 0.16, 0.7, L.obj, { z: 3.48 }); // peak
  {
    const peak = P(14.75, 9, 4.18);
    for (const end of [P(11.8, 9, 3.48), P(15.95, 9, 3.48)]) el("line", { x1: peak[0], y1: peak[1], x2: end[0], y2: end[1], stroke: "#9a9fa4" }, L.obj);
  }
  const cage = el("g", { fill: "none", "stroke-dasharray": "5 5" }, L.obj);
  {
    const [x0, y0, x1, y1, z0, z1] = [11.7, 8.3, 14.3, 9.7, 0.15, 1.6];
    const C = [[x0, y0], [x1, y0], [x1, y1], [x0, y1]];
    el("polygon", { points: pts([P(x0, y1, z0), P(x1, y1, z0), P(x1, y1, z1), P(x0, y1, z1)]), fill: "#5fd06808", stroke: "none" }, cage);
    el("polygon", { points: pts([P(x1, y0, z0), P(x1, y1, z0), P(x1, y1, z1), P(x1, y0, z1)]), fill: "#5fd06806", stroke: "none" }, cage);
    for (let i = 0; i < 4; i++) {
      const [ax, ay] = C[i], [bx2, by2] = C[(i + 1) % 4];
      for (const z of [z1]) { const [p1, p2] = [P(ax, ay, z), P(bx2, by2, z)]; el("line", { x1: p1[0], y1: p1[1], x2: p2[0], y2: p2[1] }, cage); }
      const [q1, q2] = [P(ax, ay, z0), P(ax, ay, z1)];
      el("line", { x1: q1[0], y1: q1[1], x2: q2[0], y2: q2[1] }, cage);
    }
    cage.setAttribute("stroke", "#6b7f70");
  }
  // Snapshot Storage, a rack of drawers with status LEDs.
  const rf = [];
  for (let i = 0; i < 2; i++) {
    box(15.9, 10.3, 1.2, 1.0, 0.45, L.obj, { z: i * 0.47, f1: "#18191a" });
    const [h1, h2] = [P(16.25, 11.3, i * 0.47 + 0.22), P(16.75, 11.3, i * 0.47 + 0.22)];
    el("line", { x1: h1[0], y1: h1[1], x2: h2[0], y2: h2[1], stroke: "#5d6166", "stroke-width": 2 }, L.obj);
    const led = P(15.98, 11.3, i * 0.47 + 0.22);
    rf.push(el("circle", { cx: led[0] + 2, cy: led[1], r: 3.5, fill: "var(--greenDim)" }, L.obj));
  }
  // atenet-egress, a boom barrier across the belt (its arm and light in machines()).
  box(11.75, 3.85, 0.35, 0.35, 1.0);
  box(14.2, 3.95, 0.14, 0.14, 0.82); // rest post the arm drops onto
  // The LLM is outside the cluster: the belt leaves the platform through a slim exit portal
  // at its edge (its glow and chevrons are drawn in machines()).
  const TW = { x: 13, y: 0.55, w: 0.8, h: 1.5 }; // portal centre on the belt, half-width, height
  const awakeG = el("g", {}, L.obj);
  // awake draws one small cube on the sandbox platform per actor running in Substrate right
  // now, in its game's colour: a number (all green) or a list of colours.
  let awakeKey = "";
  function awake(n) {
    const cols = (Array.isArray(n) ? n : Array(Math.max(0, n)).fill("#5fd068")).slice(0, 15);
    const k = cols.join();
    if (k === awakeKey) return;
    awakeKey = k;
    awakeG.innerHTML = "";
    cols.forEach((col, i) => {
      const c = box(11.75 + (i % 5) * 0.52, 8.55 + Math.floor(i / 5) * 0.5, 0.3, 0.3, 0.3, awakeG, { st: col, f1: "#1a2420", f2: "#121816", f3: "#0f1412", z: 0.15 });
      c.animate([{ opacity: 0 }, { opacity: 1 }], { duration: 400 });
    });
  }

  // wall shows every sleeping agent in Substrate as one tile on the floor beside Snapshot
  // Storage: the density story (many agents, few sandboxes, sleepers use no CPU).
  const wallG = el("g", {}, L.shadow);
  let wallN = -1;
  function wall(n) {
    n = Math.min(n, 189);
    if (n === wallN) return;
    const grew = n > wallN && wallN >= 0;
    wallN = n;
    wallG.innerHTML = "";
    for (let i = 0; i < n; i++) {
      const x = 17.55 + (i % 9) * 0.21, y = 8.9 + Math.floor(i / 9) * 0.21, d = 0.15;
      const t = el("polygon", { points: pts([P(x, y), P(x + d, y), P(x + d, y + d), P(x, y + d)]), fill: "var(--green)", opacity: 0.32 }, wallG);
      if (grew && i === n - 1) t.animate([{ opacity: 1 }, { opacity: 0.32 }], { duration: 1600 });
    }
  }

  // machines animates the moving parts from what is happening right now.
  const M = { ring: 0, llm: 0, alarmUntil: 0, hookZ: 2.6, trolley: 14.15, cardPhase: 0, badge: 0.1, arm: 0, drawer: 0, turn: 0, scan: 0, gate: 0, waves: 0 };
  const ease = (cur, want, dt, k = 5) => cur + (want - cur) * (1 - Math.exp(-dt * k));
  function machines(glow, dt) {
    dyn.innerHTML = "";
    const now = performance.now(), G = "var(--green)", grey = "#9a9fa4";
    // agentgateway is a security speed gate: two cabinets either side of the lane with glass
    // flaps that slide back as a request arrives, status screens, an LED strip, and a
    // scanner arch overhead. The near cabinet and its flap are drawn in front of the cubes.
    const alarm = now < M.alarmUntil; // agentgateway just blocked a request: shut, and flash red
    M.gate = ease(M.gate, glow.gate && !alarm ? 1 : 0, dt, glow.gate ? 7 : 3);
    {
      const open = M.gate, ok = open > 0.5;
      const state = alarm ? (Math.sin(now / 90) > 0 ? "#ef4444" : "#7f1d1d") : ok ? G : "#f59e0b";
      L.front.innerHTML = "";
      const cab = (y, g) => {
        box(1.3, y, 0.9, 0.5, 1.0, g, { f1: "#1b1c1e" });
        const scr = [P(1.55, y + 0.12, 1.0), P(1.95, y + 0.12, 1.0), P(1.95, y + 0.38, 1.0), P(1.55, y + 0.38, 1.0)];
        el("polygon", { points: pts(scr), fill: alarm ? "#3a1010" : ok ? "#12301a" : "#2a1c0b", stroke: state, filter: "url(#glow)" }, g);
      };
      const flap = (y0, y1, g) => {
        if (Math.abs(y1 - y0) < 0.02) return;
        el("polygon", { points: pts([P(1.75, y0, 0.3), P(1.75, y1, 0.3), P(1.75, y1, 0.9), P(1.75, y0, 0.9)]), fill: alarm ? "#ef4444" : ok ? "#5fd068" : "#f59e0b", "fill-opacity": alarm ? 0.3 : 0.16, stroke: state, "stroke-opacity": 0.8 }, g);
      };
      cab(2.9, dyn);
      const [l1, l2] = [P(1.4, 3.4, 0.72), P(2.1, 3.4, 0.72)]; // LED strip facing the lane
      el("line", { x1: l1[0], y1: l1[1], x2: l2[0], y2: l2[1], stroke: state, "stroke-width": 3, "stroke-linecap": "round", filter: "url(#glow)" }, dyn);
      flap(3.4, 3.4 + 0.59 * (1 - open), dyn);
      box(1.68, 3.07, 0.16, 0.16, 1.3, dyn, { z: 1.0 }); // far post of the scanner arch
      box(1.68, 3.07, 0.16, 1.86, 0.2, dyn, { z: 2.3, st: ok ? G : undefined, f1: ok ? "#1a3320" : undefined }); // arch beam
      const [s1, s2] = [P(1.76, 3.25, 2.29), P(1.76, 4.75, 2.29)];
      el("line", { x1: s1[0], y1: s1[1], x2: s2[0], y2: s2[1], stroke: G, "stroke-width": 2, opacity: (0.35 + 0.65 * open).toFixed(2) }, dyn);
      if (open > 0.05) { // the scan sweeping down the lane
        const z = 2.2 - ((now / 600) % 1) * 2.05, [a, b] = [P(1.76, 3.42, z), P(1.76, 4.58, z)];
        el("line", { x1: a[0], y1: a[1], x2: b[0], y2: b[1], stroke: G, "stroke-width": 3, opacity: open, filter: "url(#glow)" }, dyn);
      }
      flap(4.6 - 0.59 * (1 - open), 4.6, L.front);
      cab(4.6, L.front);
      box(1.68, 4.77, 0.16, 0.16, 1.3, L.front, { z: 1.0 }); // near post
    }
    // kagent: the session card swipes in and out of the reader's slot, quickly while a
    // request is at the controller.
    M.cardPhase += dt * (glow.ctrl ? 5.5 : 1.4);
    {
      const depth = (0.5 - 0.5 * Math.cos(M.cardPhase)) * (glow.ctrl ? 0.8 : 0.55); // 0 = out, 0.8 = deep in
      const z = 0.9 - depth + 0.05, st = glow.ctrl ? G : grey;
      cardG.innerHTML = "";
      box(8.55, 2.71, 0.7, 0.08, 0.95, cardG, { z, st, f1: "#1b1c1d", f3: "#141516" });
      const dot = P(8.9, 2.79, z + 0.62);
      el("circle", { cx: dot[0], cy: dot[1], r: 5, fill: G, opacity: glow.ctrl ? 1 : 0.75 }, cardG);
      slot.setAttribute("stroke", depth > 0.35 ? G : "#5d6166");
      slot.setAttribute("fill", depth > 0.35 && glow.ctrl ? "#12301a" : "#060707");
    }
    // Scheduler: an arrow rides round the turntable, fast while it places an actor.
    M.turn += dt * (glow.ate ? 3.2 : 0.5);
    {
      const [cx, cy] = P(7, 9, 0.22), rx = 0.8 * S * 1.2247, ry = 0.8 * S * 0.7071, a0 = M.turn, a1 = a0 + 1.6;
      const p0 = [cx + Math.cos(a0) * rx, cy + Math.sin(a0) * ry], p1 = [cx + Math.cos(a1) * rx, cy + Math.sin(a1) * ry];
      el("path", { d: `M${p0} A${rx},${ry} 0 0 1 ${p1}`, fill: "none", stroke: G, "stroke-width": 3, opacity: glow.ate ? 1 : 0.5 }, dyn);
      const dir = Math.atan2(Math.cos(a1) * ry, -Math.sin(a1) * rx) * 57.3;
      el("path", { d: "M-7,-5 L0,0 L-7,5", fill: "none", stroke: G, "stroke-width": 3, opacity: glow.ate ? 1 : 0.5, transform: `translate(${p1[0]} ${p1[1]}) rotate(${dir})` }, dyn);
    }
    // ate-idp: a badge pops out of the kiosk for a new actor.
    M.badge = ease(M.badge, glow.idp ? 0.6 : 0.08, dt, 6);
    box(4.38, 10.57, 0.45, 0.06, 0.5, dyn, { z: 0.6 + M.badge - 0.45, st: glow.idp ? G : grey, f1: "#1b1c1d" });
    // Sandbox crane: the trolley rolls out over the agent, then the claw drops onto it;
    // when the agent is done the claw lifts and the trolley parks by the mast.
    M.trolley = ease(M.trolley, glow.box ? 13.0 : 14.15, dt, 3);
    const over = Math.abs(M.trolley - 13.0) < 0.15;
    M.hookZ = ease(M.hookZ, glow.box && over ? 0.75 : 2.6, dt, 4);
    {
      const tx = M.trolley, st = glow.box ? G : grey;
      box(tx - 0.2, 8.86, 0.4, 0.28, 0.12, dyn, { z: 3.2, st }); // trolley under the jib
      for (const dy of [-0.06, 0.06]) {
        const [a, b] = P(tx, 9 + dy, 3.2), [c, d] = P(tx, 9 + dy, M.hookZ + 0.42);
        el("line", { x1: a, y1: b, x2: c, y2: d, stroke: grey }, dyn);
      }
      box(tx - 0.18, 8.82, 0.36, 0.36, 0.22, dyn, { z: M.hookZ + 0.2, st, f1: glow.box ? "#1a3320" : undefined }); // claw head
      const open = glow.box && over ? 0.12 : 0.24; // the claws close on the agent
      for (const dx of [-1, 1]) {
        const [a, b] = P(tx + dx * 0.16, 9, M.hookZ + 0.2), [c, d] = P(tx + dx * open, 9, M.hookZ - 0.05);
        el("line", { x1: a, y1: b, x2: c, y2: d, stroke: st, "stroke-width": 2, "stroke-linecap": "round" }, dyn);
      }
    }
    cage.setAttribute("stroke", glow.box ? "#5fd068" : "#6b7f70");
    // Egress: a boom barrier. The arm swings up as a request nears egress (and every few
    // seconds when idle), then drops back onto its rest post.
    M.arm = ease(M.arm, glow.barrier ? 1.35 : 0, dt, glow.barrier ? 4.5 : 2.6);
    {
      const pv = [11.92, 4.02, 0.92], L2 = 2.35, ca = Math.cos(M.arm), sa = Math.sin(M.arm);
      const [a, b] = P(...pv), [c, d] = P(pv[0] + ca * L2, pv[1], pv[2] + sa * L2);
      const [w1, w2] = P(pv[0] - ca * 0.45, pv[1], pv[2] - sa * 0.45); // counterweight end
      el("line", { x1: a, y1: b, x2: w1, y2: w2, stroke: "#9a9fa4", "stroke-width": 5, "stroke-linecap": "round" }, dyn);
      box(pv[0] - ca * 0.45 - 0.1, pv[1] - 0.1, 0.2, 0.2, 0.2, dyn, { z: pv[2] - sa * 0.45 - 0.1 });
      el("line", { x1: a, y1: b, x2: c, y2: d, stroke: "#e8e8e8", "stroke-width": 5, "stroke-linecap": "round" }, dyn);
      el("line", { x1: a, y1: b, x2: c, y2: d, stroke: "#0d0e0f", "stroke-width": 5, "stroke-dasharray": "6 8", "stroke-linecap": "butt" }, dyn);
      el("circle", { cx: a, cy: b, r: 4, fill: "#d4d4d8" }, dyn); // pivot
      const lt = P(11.92, 4.02, 1.2);
      el("circle", { cx: lt[0], cy: lt[1], r: 6, fill: M.arm > 0.5 ? G : "#f59e0b", filter: "url(#glow)" }, dyn);
    }
    // Snapshot Storage: the top drawer slides out while a snapshot is saved or restored.
    M.drawer = ease(M.drawer, glow.rf ? 0.55 : 0, dt, 5);
    if (M.drawer > 0.02) box(15.95, 10.3 + M.drawer, 1.1, 1.0, 0.4, dyn, { z: 0.96, st: G, f1: "#1a3320" });
    // LLM exit portal: a slim frame across the belt whose opening glows, brighter with
    // chevrons streaming in while a call is out.
    M.llm = ease(M.llm, glow.llm ? 1 : 0, dt, 5);
    M.ring += dt * (glow.llm ? 1.6 : 0.4);
    {
      const on = M.llm, { x, y, w, h } = TW, fr = 0.1;
      const c = on > 0.5 ? G : "#9a9fa4";
      const q = (x0, x1, z0, z1) => [P(x0, y, z0), P(x1, y, z0), P(x1, y, z1), P(x0, y, z1)];
      el("polygon", { points: pts(q(x - w, x + w, 0, h)), fill: "url(#portalglow)", opacity: (0.35 + 0.65 * on).toFixed(2) }, dyn);
      for (let i = 0; i < 3; i++) { // chevrons moving through the opening
        const k = (M.ring + i / 3) % 1, z = 0.25 + 0.25 * i, op = on * Math.sin(Math.PI * k);
        if (op < 0.05) continue;
        const [a, b] = P(x - 0.25, y + 0.6 - k * 0.9, z), [m, n] = P(x, y + 0.45 - k * 0.9, z), [d, e] = P(x + 0.25, y + 0.6 - k * 0.9, z);
        el("polyline", { points: `${a},${b} ${m},${n} ${d},${e}`, fill: "none", stroke: G, "stroke-width": 2.5, opacity: op.toFixed(2) }, dyn);
      }
      const frame = [P(x - w - fr, y, 0), P(x - w - fr, y, h + fr), P(x + w + fr, y, h + fr), P(x + w + fr, y, 0), P(x + w, y, 0), P(x + w, y, h), P(x - w, y, h), P(x - w, y, 0)];
      el("polygon", { points: pts(frame), fill: "#141517", stroke: c, "stroke-linejoin": "round", filter: on > 0.5 ? "url(#glow)" : null }, dyn);
    }
  }

  label("gw", 1.75, 4, 3.45, "agentgateway", "front door · prompt guard · limits", window.AGW_ICON);
  label("ctrl", 7.2, 2.6, 3.6, "kagent-controller", "sessions · A2A · agent CRDs", window.KAGENT_ICON, "kagent");
  label("pg", 10.2, 6.6, 1.4, "postgres", "chat history");
  label("ate", 7, 9, 2.0, "ate-api-server + controller", "schedules agent actors", null, "Substrate");
  label("idp", 4.6, 11.8, -0.9, "ate-idp", "gives each agent an identity", window.KAGENT_ICON);
  label("box", 12.2, 11.9, -0.7, "atelet · gVisor sandbox", "your agent runs here, isolated", null, "gVisor sandbox");
  label("rf", 19.0, 9.2, 1.0, "Snapshot Storage", "sleeping agents (rustfs)");
  label("egress", 16.6, 5.2, 0.3, "atenet-egress", "agentgateway · per-agent allowlist", window.AGW_ICON, "egress");
  label("llm", 13, 0.55, 2.4, "OpenAI", "LLM calls via agentgateway", window.OPENAI_ICON, "LLM");
  {
    const [c, d] = P(-6, 4);
    el("text", { x: c - 10, y: d + 34, fill: "#6d7277", "font-size": 12, "font-family": "ui-monospace,monospace" }, L.lab).textContent = "← phones";
  }

  // Click targets over each machine (the labels are clickable too).
  const SPOTS = { llm: [13, 0.55], gw: [2, 4], ctrl: [8.6, 3], pg: [9.4, 5.6], ate: [7, 9], idp: [4.6, 10.6], box: [13, 9], egress: [12.6, 4], rf: [16.5, 10.8] };
  if (opts.onPick) {
    const hit = el("g", {}, svg);
    for (const [key, at] of Object.entries(SPOTS)) {
      const [cx, cy] = P(...at, 0.5);
      el("ellipse", { class: "hit", cx, cy, rx: 1.3 * S, ry: 1.0 * S }, hit).addEventListener("click", () => opts.onPick(key));
    }
    L.hit = hit;
  }
  const spot = (key) => P(...(SPOTS[key] || [7, 6]));
  const pulse = (e, ms = 700) => e.animate([{ fill: "#9cff9f" }, { fill: "var(--greenDim)" }], { duration: ms });
  const flash = (key) => {
    const l = LAB[key];
    if (!l) return;
    l.r.animate([{ stroke: "#9cff9f", strokeWidth: 3 }, { stroke: "#34373a", strokeWidth: 1 }], { duration: 1200 });
  };
  // A snapshot orb travelling between the sandbox and rustfs.
  function orb(from, to, color = "var(--green)", r = 7, delay = 0, lift = 60) {
    const [a, b] = P(...from, 0.6), [c, d] = P(...to, 0.9);
    const o = el("circle", { r, cx: a, cy: b, fill: color, opacity: 0, filter: "url(#glow)" }, L.orbs);
    o.animate([{ cx: a, cy: b, opacity: 0.9 }, { cx: (a + c) / 2, cy: Math.min(b, d) - lift, opacity: 0.9 }, { cx: c, cy: d, opacity: 0.9 }],
      { duration: 900, delay, easing: "ease-in-out" }).onfinish = () => o.remove();
  }
  // actor shows one lifecycle change of another game's actor: a new actor flies from the
  // Substrate scheduler to the sandboxes, a wake comes out of Snapshot Storage, a sleep goes
  // into it, and a deleted actor fades out over the storage floor.
  const ATE = [7, 9], BOX = [13, 9], RF = [16.6, 11.1];
  function actor(kind, color, delay = 0) {
    switch (kind) {
      case "new": orb(ATE, BOX, color, 5, delay, 28); break;
      case "wake": orb(RF, BOX, color, 5, delay); break;
      case "sleep": orb(BOX, RF, color, 5, delay); break;
      case "gone": {
        const [cx, cy] = P(RF[0] + 1.4, RF[1] - 0.6, 0.4);
        const e = el("circle", { cx, cy, r: 4, fill: "none", stroke: color, "stroke-width": 2, opacity: 0 }, L.orbs);
        e.animate([{ r: 4, opacity: 0.9 }, { r: 16, opacity: 0 }], { duration: 900, delay, easing: "ease-out" }).onfinish = () => e.remove();
      }
    }
  }
  // bubble floats a short message up from a spot on the map, then fades it out. Bubbles at
  // the same spot stack upward instead of covering each other.
  const stacks = new Map();
  function bubble(at, text, kind = "") {
    const k = opts.compact ? 1.9 : 1, key = at.join(",");
    const slot = [0, 1, 2, 3, 4, 5].find((i) => !(stacks.get(key) || new Set()).has(i)) ?? 0;
    if (!stacks.has(key)) stacks.set(key, new Set());
    stacks.get(key).add(slot);
    const fs = 11 * k, w = text.length * fs * 0.62 + 16 * k, h = 22 * k;
    const [a, b0] = P(...at), b = b0 - slot * (h + 6 * k), g = el("g", {}, L.fx);
    const color = kind === "bad" ? "#ff6b6b" : kind === "code" ? "#cfe9d2" : "#e8e8e8";
    el("rect", { x: a - w / 2, y: b - h, width: w, height: h, rx: 7 * k, fill: kind === "bad" ? "#2a1212" : "#0f1a12", stroke: kind === "bad" ? "#ff6b6b" : "var(--green)" }, g);
    el("text", { x: a, y: b - h / 2 + fs * 0.36, fill: color, "font-size": fs, "text-anchor": "middle", "font-family": kind === "code" ? "ui-monospace,monospace" : "Inter,system-ui,sans-serif" }, g).textContent = text;
    g.animate([{ transform: "translateY(8px)", opacity: 0 }, { transform: "translateY(0)", opacity: 1, offset: 0.12 }, { transform: "translateY(-10px)", opacity: 1, offset: 0.75 }, { transform: "translateY(-26px)", opacity: 0 }],
      { duration: 3200, easing: "ease-out" }).onfinish = () => { g.remove(); stacks.get(key).delete(slot); };
  }
  // ripple sends a ring out from a station when a request arrives.
  function ripple(key) {
    const n = NODES.find((x) => x.key === key);
    if (!n) return;
    const [cx, cy] = P(...n.at);
    const e = el("ellipse", { cx, cy, rx: S * 1.2247, ry: S * 0.7071, fill: "none", stroke: "var(--green)", "stroke-width": 2, style: "transform-box: fill-box; transform-origin: center" }, L.floor);
    e.animate([{ transform: "scale(.6)", opacity: 0.9 }, { transform: "scale(2.6)", opacity: 0 }], { duration: 1100, easing: "ease-out" }).onfinish = () => e.remove();
  }
  // trail leaves a fading green spark behind a moving cube.
  function trail(x, y) {
    const [a, b] = P(x, y, 0.08);
    const t = el("circle", { cx: a, cy: b, r: opts.compact ? 6 : 3.5, fill: "var(--green)", opacity: 0.55 }, L.floor);
    t.animate([{ opacity: 0.55 }, { opacity: 0 }], { duration: 700, easing: "ease-out" }).onfinish = () => t.remove();
  }
  // tour highlights one station with a short explainer, for when nobody is using the screen.
  const TOUR = [
    ["gw", [2, 4, 3.0], "agentgateway: every request comes in here"],
    ["ctrl", [7.2, 3.2, 3.2], "kagent: sessions, A2A, the agent's CRD"],
    ["ate", [7, 9, 2.6], "Substrate schedules each agent as an actor"],
    ["box", [13, 9, 2.6], "every agent gets its own gVisor sandbox"],
    ["egress", [13, 4, 2.2], "only allowed hosts can get out"],
    ["rf", [18.6, 10.6, 1.2], "Snapshot Storage: idle agents sleep here"],
  ];
  function tour(i) {
    const [key, at, text] = TOUR[i % TOUR.length];
    ripple(key);
    flash(key);
    bubble(at, text);
    if (key === "rf") rf.forEach((c) => pulse(c, 1500));
    return P(at[0], at[1]);
  }
  const defs = el("defs", {}, svg);
  const grad = (id, stops, x2 = 0, y2 = 1) => {
    const g = el("linearGradient", { id, x1: 0, y1: 0, x2, y2 }, defs);
    stops.forEach(([o, c]) => el("stop", { offset: o, "stop-color": c }, g));
  };
  const cc = el("clipPath", { id: "cardclip" }, defs);
  el("polygon", { points: pts([P(8.3, 2.75, 0.9), P(9.5, 2.75, 0.9), P(9.5, 2.75, 9), P(8.3, 2.75, 9)]) }, cc);
  grad("portalglow", [[0, "#5fd06855"], [1, "#5fd06808"]]);
  grad("topface", [[0, "#24262a"], [1, "#16171a"]], 1, 1);
  grad("slabfront", [[0, "#15171a"], [1, "#0a0b0c"]]);
  grad("slabside", [[0, "#111316"], [1, "#08090a"]]);
  const sf = el("filter", { id: "soft", x: "-50%", y: "-50%", width: "200%", height: "200%" }, defs);
  el("feGaussianBlur", { stdDeviation: 6 }, sf);
  const f = el("filter", { id: "glow", x: "-50%", y: "-50%", width: "200%", height: "200%" }, defs);
  el("feGaussianBlur", { stdDeviation: 3, result: "b" }, f);
  const m = el("feMerge", {}, f);
  el("feMergeNode", { in: "b" }, m); el("feMergeNode", { in: "SourceGraphic" }, m);

  // Each visitor's agent has one of 12 shapes (picked from their player ID); its colour
  // says how it woke up: green for a brand-new agent, blue for one restored from a snapshot.
  const PALETTES = {
    new: { st: "#5fd068", f1: "#1a3320", f2: "#122016", f3: "#0f1a12", text: "#cfe9d2", tag: "new" },
    restored: { st: "#60a5fa", f1: "#16243a", f2: "#101b2c", f3: "#0c1422", text: "#cfe0fb", tag: "restored" },
    other: { st: "#4d5a50", f1: "#161917", f2: "#121413", f3: "#0e100f", text: "#9aa3a0", tag: "" },
    bad: { st: "#ff6b6b", f1: "#3a1a1a", f2: "#2a1212", f3: "#1f0e0e", text: "#ffd0d0", tag: "" },
  };
  function agentShape(g, x, y, s, shape, pal) {
    const h = s / 2, fill = { st: pal.st, f1: pal.f1, f2: pal.f2, f3: pal.f3 };
    const B = (dx, dy, w, d, ht, z = 0) => box(x + dx, y + dy, w, d, ht, g, { ...fill, z });
    const poly = (q, f) => el("polygon", { points: pts(q), fill: f, stroke: pal.st, "stroke-linejoin": "round" }, g);
    const drum = (r, ht, z = 0) => {
      const [px, py] = P(x, y, z), rx = r * S * 1.2247, ry = r * S * 0.7071;
      el("path", { d: `M${px - rx},${py} v${-ht * S} a${rx},${ry} 0 0 0 ${2 * rx},0 v${ht * S} a${rx},${ry} 0 0 1 ${-2 * rx},0`, fill: pal.f2, stroke: pal.st }, g);
      return el("ellipse", { cx: px, cy: py - ht * S, rx, ry, fill: pal.f1, stroke: pal.st }, g);
    };
    switch (shape) {
      case 1: B(-h * 0.7, -h * 0.7, s * 0.7, s * 0.7, s * 1.5); return s * 1.5; // tower
      case 2: B(-h * 1.3, -h * 1.3, s * 1.3, s * 1.3, s * 0.42); return s * 0.42; // slab
      case 3: drum(h, s * 0.8); return s * 0.8; // drum
      case 4: { // pyramid
        const t = [x, y, s * 1.15];
        poly([P(x + h, y - h), P(x + h, y + h), P(...t)], pal.f2);
        poly([P(x + h, y + h), P(x - h, y + h), P(...t)], pal.f3);
        return s * 1.15;
      }
      case 5: { // hexagonal prism
        const ht = s * 0.7, pt = (k, z) => P(x + Math.cos(k) * h * 1.08, y + Math.sin(k) * h * 1.08, z);
        const A = [0, 1, 2, 3, 4, 5].map((i) => (i * Math.PI) / 3 + Math.PI / 6);
        A.forEach((a, i) => {
          const b = A[(i + 1) % 6], m = (a + b) / 2 + (i === 5 ? Math.PI : 0);
          if (Math.cos(m) + Math.sin(m) > 0) poly([pt(a, 0), pt(b, 0), pt(b, ht), pt(a, ht)], Math.cos(m) > Math.sin(m) ? pal.f2 : pal.f3);
        });
        poly(A.map((a) => pt(a, ht)), pal.f1);
        return ht;
      }
      case 6: B(-h, -h, s, s, s * 0.55); B(-h * 0.6, -h * 0.6, s * 0.6, s * 0.6, s * 0.5, s * 0.55); return s * 1.05; // stacked
      case 7: { // diamond
        const zm = s * 0.55, top = P(x, y, s * 1.15), bot = P(x, y, 0.02);
        const E = [P(x + h, y, zm), P(x, y + h, zm), P(x - h, y, zm), P(x, y - h, zm)];
        poly([E[3], E[0], bot], pal.f3); poly([E[0], E[1], bot], pal.f2); poly([E[1], E[2], bot], pal.f3);
        poly([E[3], E[0], top], pal.f1); poly([E[1], E[2], top], pal.f1); poly([E[0], E[1], top], pal.f2);
        return s * 1.15;
      }
      case 8: { // robot head with an antenna
        B(-h * 0.9, -h * 0.9, s * 0.9, s * 0.9, s * 0.85);
        const [a, b] = P(x, y, s * 0.85), [c, d] = P(x, y, s * 1.25);
        el("line", { x1: a, y1: b, x2: c, y2: d, stroke: pal.st }, g);
        el("circle", { cx: c, cy: d, r: s * 5, fill: pal.st }, g);
        const [e1, e2] = [P(x - h * 0.3, y + h * 0.9, s * 0.55), P(x + h * 0.3, y + h * 0.9, s * 0.55)];
        for (const [ex, ey] of [e1, e2]) el("circle", { cx: ex, cy: ey, r: s * 3.2, fill: pal.st }, g);
        return s * 1.25;
      }
      case 9: { // capsule
        drum(h * 0.85, s * 0.75);
        const [px, py] = P(x, y, s * 0.75), rx = h * 0.85 * S * 1.2247;
        el("path", { d: `M${px - rx},${py} a${rx},${rx * 0.9} 0 0 1 ${2 * rx},0 a${rx},${rx * 0.58} 0 0 1 ${-2 * rx},0`, fill: pal.f1, stroke: pal.st }, g);
        return s * 1.3;
      }
      case 10: B(-h, -h, s * 0.5, s, s * 0.95); B(-h + s * 0.5, -h + s * 0.5, s * 0.5, s * 0.5, s * 0.5); return s * 0.95; // L-block
      case 11: { // sphere
        const [cx, cy] = P(x, y, h * 1.05), r = h * S * 1.15;
        el("circle", { cx, cy, r, fill: pal.f1, stroke: pal.st }, g);
        el("ellipse", { cx, cy, rx: r, ry: r * 0.32, fill: "none", stroke: pal.st, opacity: 0.6 }, g);
        el("circle", { cx: cx - r * 0.35, cy: cy - r * 0.4, r: r * 0.18, fill: pal.st, opacity: 0.35 }, g);
        return s * 1.1;
      }
      default: B(-h, -h, s, s, s); return s; // cube
    }
  }
  // drawCube draws a request as its agent's shape. With opts.me set (the phone), other
  // people's agents are dimmed.
  function drawCube(g, x, y, nick, bad, other, flag, shape = 0, kind = "new") {
    g.innerHTML = "";
    const pal = PALETTES[bad ? "bad" : other ? "other" : kind === "restored" ? "restored" : "new"];
    const sz = opts.compact && !other ? 0.9 : 0.64;
    const top = agentShape(g, x, y, sz, shape, pal);
    if (flag && !other) {
      // A small flag on the agent while it runs a tool, like Tessl's issue markers.
      const [fx, fy] = P(x + sz / 2 - 0.05, y - sz / 2 + 0.05, top), h = 18 * (opts.compact ? 1.8 : 1);
      el("line", { x1: fx, y1: fy, x2: fx, y2: fy - h, stroke: "#f59e0b", "stroke-width": 1.5 }, g);
      el("path", { d: `M${fx},${fy - h} l${h * 0.6},${h * 0.22} l${-h * 0.6},${h * 0.22}z`, fill: "#f59e0b" }, g);
    }
    if (other && opts.compact) return;
    const [a, b] = P(x, y, top + 0.45), k = opts.compact ? 2.2 : 1;
    el("text", { x: a, y: b, fill: pal.text, "font-size": 11 * k, "font-weight": opts.compact ? 600 : 500, "text-anchor": "middle", "font-family": "ui-monospace,monospace" }, g).textContent = opts.compact ? "you" : nick;
    if (pal.tag) el("text", { x: a, y: b - 12 * k, fill: pal.st, "font-size": 9 * k, "text-anchor": "middle", "font-family": "Inter,system-ui,sans-serif", "letter-spacing": "0.06em" }, g).textContent = pal.tag.toUpperCase();
  }

  // drawMini draws another game's agent turn: a small cube in its game's colour, with the
  // game's name over it (red when the turn failed).
  function drawMini(g, x, y, color, name, bad) {
    g.innerHTML = "";
    const st = bad ? "#ff6b6b" : color;
    const top = agentShape(g, x, y, 0.42, 0, { st, f1: "#151b18", f2: "#101512", f3: "#0c100e", text: st });
    const [a, b] = P(x, y, top + 0.32);
    el("text", { x: a, y: b, fill: st, "font-size": 9, "text-anchor": "middle", "font-family": "Inter,system-ui,sans-serif", opacity: 0.85 }, g).textContent = name;
  }

  svg.appendChild(L.cubes); // cubes on top: above the labels and click targets, so a cube can be picked
  L.front = el("g", { "pointer-events": "none" }, svg); // scenery in front of the cubes (the gate's near pillar)
  svg.appendChild(L.fx); // bubbles stay on top of everything
  const alarm = () => { M.alarmUntil = performance.now() + 3000; };
  return { L, ORB, LAB, pg, idp, rf, pulse, flash, orb, actor, drawCube, drawMini, bubble, ripple, awake, trail, tour, machines, spot, wall, alarm };
}

function fmtMs(ms) {
  if (ms == null) return "–";
  if (ms < 1000) return ms + "ms";
  if (ms < 60000) return (ms / 1000).toFixed(1) + "s";
  return Math.floor(ms / 60000) + "m " + Math.round((ms % 60000) / 1000) + "s";
}

// lifecycle lists the stages an agent went through for a run, with real timings, for the
// strip on the story card and the phone: [label, detail, reached].
function lifecycle(r) {
  const at = (stage) => r.steps.some((x) => x.stage === stage);
  const out = [];
  if (r.new) out.push(["New actor", fmtMs(r.createMs), true]);
  else if (r.restoreMs || at("restore")) {
    out.push(["Asleep", fmtMs(r.sleptMs) + " · 0 CPU", true]);
    out.push(["Restoring", r.restoreMs ? fmtMs(r.restoreMs) : "…", true]);
  }
  out.push(["Running", r.answer ? fmtMs(r.ms) : "…", at("sandbox") || at("llm")]);
  out.push(["Saving", r.snapshotMs ? fmtMs(r.snapshotMs) : "", at("snapshot")]);
  out.push(["Asleep", "0 CPU", at("asleep") && !!r.snapshotMs]);
  return out;
}

// shapeOf picks one of the 12 agent shapes from a player ID, so a visitor keeps theirs.
function shapeOf(id) {
  let h = 0;
  for (const ch of String(id)) h = (h * 31 + ch.charCodeAt(0)) >>> 0;
  return h % 12;
}

// startFlowScreen wires the scene to the backend's live state.
// With me set, the scene highlights that player's cubes; live: false leaves the event
// stream to the caller, which feeds states in through the returned apply.
// With camera, the view zooms in and follows the newest live request (or the idle tour).
// onPick(key) is called when a station is clicked; clicking a cube follows that request.
function startFlowScreen(svg, { onState, onStep, onHook, onPick, onMove, onActor, onOp, me, compact, camera, live = true }) {
  let picked = null, pickedUntil = 0, pinned = null;
  const sc = createScene(svg, { compact, me, onPick: onPick && ((key) => { picked = key; pickedUntil = Date.now() + 9000; sc.ripple(key); sc.flash(key); onPick(key); }) });
  const VW = 1360, VH = 762, cam = { x: 700, y: 410 }; // the whole map, with room to breathe
  let focusId = null, focusUntil = 0, tourAt = null;
  const cubes = new Map(); // run ID -> {g, s, target, nick, stage, bad}
  const seenSteps = new Map(); // run ID -> steps already announced
  let seenHook = Date.now() - 2000, seenMove = Date.now() - 2000, seenActor = Date.now() - 2000;
  const saidAt = {}; // station -> when it last got an actor bubble
  // Other games' turns (kagent Ops): a mini cube per session rides the belt from the
  // gateway out to the LLM and back. lastOp is the newest Op already shown.
  const minis = new Map(); // session -> {g, s, target, color, game, bad, final}
  const MAXMINI = 28;
  let lastOp = null, opsSeen = false;
  const opAgentAt = {}; // agent -> when its last Op arrived (its poll moves are then skipped)
  const turnTimes = []; // when each other-game turn started, for the turns-a-minute stat
  let firstState = true;

  function apply(st) {
    for (const r of st.runs || []) {
      const node = STAGE_NODE[r.stage] || "gw";
      let c = cubes.get(r.id);
      const known = seenSteps.get(r.id) || 0;
      if (!c) {
        // Old finished runs from before this screen opened are not replayed.
        if (firstState && r.done) { seenSteps.set(r.id, r.steps.length); continue; }
        if (r.done && known >= r.steps.length) continue;
        c = { g: document.createElementNS(NS, "g"), s: 0, nick: r.nick, stage: "", bad: false, other: !!me && r.playerId !== me };
        if (onPick) { c.g.classList.add("pickable"); c.g.addEventListener("click", () => { pinned = r.id; focusId = r.id; focusUntil = 0; picked = null; }); }
        if (c.other) sc.L.cubes.prepend(c.g); else sc.L.cubes.appendChild(c.g);
        cubes.set(r.id, c);
      }
      c.target = DIST[NODE[node]];
      c.final = DONE.has(r.stage);
      c.flag = r.stage === "tool" || r.stage === "hook";
      c.kind = r.steps.some((x) => x.stage === "restore") ? "restored" : "new";
      c.shape = shapeOf(r.playerId);
      c.started = r.started;
      c.bad = r.stage === "error";
      if (c.stage !== r.stage && !c.other) {
        if (r.stage === "restore") sc.orb([16.6, 11.1], [13, 9]);
        if (r.stage === "snapshot") sc.orb([13, 9], [16.6, 11.1]);
        c.stage = r.stage;
      }
      for (const step of r.steps.slice(known)) {
        if (!c.other && !firstState) effect(step);
        onStep && onStep(r, step);
      }
      seenSteps.set(r.id, r.steps.length);
    }
    for (const h of st.hooks || []) {
      if (h.at > seenHook) {
        seenHook = h.at;
        sc.flash("gw"); sc.flash("egress");
        onHook && onHook(h);
      }
    }
    firstState = false;
    // Focus the camera and story card on the newest live request; hold a finished one briefly.
    const now = Date.now(), fc = cubes.get(focusId);
    if (fc && fc.final && !focusUntil) focusUntil = now + 3500;
    if (pinned && cubes.has(pinned) && !(fc && fc.final && now > focusUntil && focusUntil)) focusId = pinned;
    else if (!fc || (focusUntil && now > focusUntil)) {
      pinned = null;
      const live = [...cubes.entries()].filter(([, c]) => !c.final && !c.other).sort((a, b) => b[1].started - a[1].started);
      if (live.length) { focusId = live[0][0]; focusUntil = 0; } else if (!fc) focusId = null;
    }
    // Every other game's kagent calls, newest last; only the ones after lastOp are new.
    const ops = st.ops || [];
    let from = 0;
    if (lastOp) {
      const i = ops.findLastIndex((o) => o.at === lastOp.at && o.session === lastOp.session && o.kind === lastOp.kind && o.phase === lastOp.phase);
      from = i >= 0 ? i + 1 : ops.findIndex((o) => o.at > lastOp.at) >>> 0;
    }
    const fresh = opsSeen ? ops.slice(from) : [];
    // On the first state, count the last minute's turns the server already has, so the
    // turns-a-minute stat is right from the start rather than climbing from zero.
    if (!opsSeen) for (const o of ops) if (o.kind === "turn" && o.phase === "start" && Date.now() - o.at < 60000) turnTimes.push(o.at);
    if (ops.length) { lastOp = ops[ops.length - 1]; opsSeen = true; }
    if (!opsSeen && st.ops) opsSeen = true;
    for (const o of fresh) {
      if (me) break;
      const [game, color] = gameOf(o.agent);
      opAgentAt[o.agent] = Date.now();
      switch (o.kind) {
        case "turn": {
          let m = minis.get(o.session);
          if (o.phase === "start") {
            turnTimes.push(Date.now());
            if (!m && minis.size < MAXMINI) {
              m = { g: document.createElementNS(NS, "g"), s: DIST[NODE.gw], color, game };
              sc.L.cubes.prepend(m.g);
              minis.set(o.session, m);
            }
            if (m) Object.assign(m, { target: DIST[NODE.llm], final: false, bad: false });
          } else if (m) {
            Object.assign(m, { target: DIST[NODE.gw], final: true, bad: o.err });
          }
          break;
        }
        case "create": if (!o.err) { sc.actor("new", color); sc.ripple("ate"); } break;
        case "suspend": if (!o.err) sc.actor("sleep", color); break;
        case "resume": if (!o.err) sc.actor("wake", color); break;
        case "checkpoint": if (!o.err) sc.orb([13, 9], [16.6, 11.1], "#ffd23d", 5); break;
        case "delete": if (!o.err) sc.actor("gone", color); break;
      }
      onOp && onOp(o, game, color);
    }
    while (turnTimes.length && Date.now() - turnTimes[0] > 60000) turnTimes.shift();

    // Every other game's actors: orbs in their game's colour (a few per batch), a bubble per
    // station at most every 1.2 s, and the awake cubes coloured by game.
    for (const m of st.actors || []) {
      if (m.at <= seenActor) continue;
      seenActor = Math.max(seenActor, m.at);
      if (firstState || me) continue;
      const [game, color] = gameOf(m.agent);
      if (Date.now() - (opAgentAt[m.agent] || 0) < 15000 && m.kind !== "gone") continue; // its Ops already drew this
      for (let i = 0; i < Math.min(m.n, 4); i++) sc.actor(m.kind, color, i * 140);
      const where = { new: [7, 9, 2.6], wake: [18.6, 10.6, 1.2], sleep: [18.6, 10.6, 1.2], gone: [18.6, 10.6, 1.2] }[m.kind];
      const verb = { new: "new actor", wake: "woke", sleep: "asleep", gone: "deleted" }[m.kind];
      const now = Date.now(), key = String(where);
      if (where && !compact && now - (saidAt[key] || 0) > 1200) {
        saidAt[key] = now;
        sc.bubble(where, `${game} · ${m.n > 1 ? m.n + " " : ""}${verb}`);
      }
      if (m.kind === "new" || m.kind === "wake") sc.ripple("box");
      onActor && onActor(m, game, color);
    }
    const cols = [];
    for (const a of st.substrate?.agents || []) for (let i = 0; i < a.awake; i++) cols.push(gameOf(a.name)[1]);
    sc.awake(cols.length || !st.substrate?.agents ? cols : st.substrate?.running || 0);
    sc.wall(st.substrate?.suspended || 0);
    // Sleeps and wakes from the phone's buttons (and auto-sleep): the snapshot orb moves
    // between the sandbox and Snapshot Storage with the real timing.
    for (const m of st.moves || []) {
      if (m.at <= seenMove) continue;
      seenMove = m.at;
      if (me && m.playerId !== me) continue;
      if (!m.error && m.kind === "wake") { sc.orb([16.6, 11.1], [13, 9]); sc.ripple("box"); sc.bubble([18.6, 10.6, 1.2], "restored in " + fmtMs(m.ms)); }
      else if (!m.error) { sc.orb([13, 9], [16.6, 11.1]); sc.bubble([18.6, 10.6, 1.2], "snapshot in " + fmtMs(m.ms)); }
      onMove && onMove(m);
    }
    onState && onState(st);
  }

  // effect turns one real step into a floating bubble at the station where it happened.
  const clip = (t, n) => (t.length > n ? t.slice(0, n - 1) + "…" : t);
  let lastCmd = "";
  function effect(step) {
    const d = step.detail || "";
    if ((step.stage === "tool" || step.stage === "hook") && step.text !== "tool output") lastCmd = d;
    switch (step.stage) {
      case "tool":
        if (step.text !== "tool output") sc.bubble([13, 9, 2.6], "$ " + clip(d.split("\n")[0], 34), "code");
        else if (/bad address|could not resolve|name or service not known/i.test(d)) { sc.bubble([13, 4, 2.2], "✗ blocked by egress", "bad"); sc.flash("egress"); }
        else if (/icanhazdadjoke/i.test(lastCmd)) sc.bubble([13, 4, 2.2], "✓ allowed by EgressPolicy");
        break;
      case "hook": {
        const host = (d.match(/https?:\/\/([a-z0-9.-]+)/i) || [])[1] || "";
        sc.bubble([13, 4, 2.2], d.includes("/agent-flow/hook") ? "POST /agent-flow/hook" : "→ " + host, "code");
        break;
      }
      case "llm": sc.bubble([13, 1, 1.4], "→ LLM"); break;
      case "schedule": sc.bubble([7, 9, 2.6], "new agent actor"); break;
      case "restore": if (step.text === "restored") sc.bubble([18.6, 10.6, 1.2], "restored in " + d); break;
      case "asleep": if (d) sc.bubble([18.6, 10.6, 1.2], "snapshot in " + d); break;
      case "reply": sc.bubble([2, 4, 3.0], "✓ answered in " + d); break;
      case "error":
        if (step.text.includes("agentgateway")) { sc.alarm(); sc.bubble([2, 4, 2.6], step.text.includes("rate") ? "429 · rate limited" : "403 · blocked by prompt guard", "bad"); }
        else sc.bubble([2, 4, 3.0], "error", "bad");
        break;
    }
  }

  let last = performance.now();
  function frame(now) {
    const dt = Math.min(0.1, (now - last) / 1000);
    last = now;
    const glow = {};
    for (const [id, c] of cubes) {
      const speed = 11; // grid units per second
      const d = c.target - c.s;
      c.s += Math.sign(d) * Math.min(Math.abs(d), speed * dt);
      const [x, y] = posAt(c.s);
      sc.drawCube(c.g, x, y, c.nick, c.bad, c.other, c.flag, c.shape, c.kind);
      if (id === focusId) c.pos = P(x, y);
      if (Math.abs(d) > 0.05 && !c.other && now - (c.trailAt || 0) > 45) { c.trailAt = now; sc.trail(x, y); }
      if (c.target === DIST[NODE.llm] && !c.other) glow.llm = true;
      if (Math.abs(d) < 0.01 && !c.other) {
        const n = NODES[DIST.indexOf(c.target)];
        if (n) glow[n.key] = true;
        if (n && c.at !== n.key) { c.at = n.key; if (n.key !== "phones" && n.key !== "llm") sc.ripple(n.key); }
        if (c.final && c.target === 0) { c.g.remove(); cubes.delete(id); }
      }
      if (c.other) continue;
      if (c.stage === "controller" && Math.abs(d) < 0.01) glow.pg = true;
      if (c.stage === "schedule" && Math.abs(d) < 0.01) glow.idp = true;
      if ((c.stage === "restore" || c.stage === "snapshot")) glow.rf = true;
    }
    for (const [id, m] of minis) {
      const d = m.target - m.s;
      m.s += Math.sign(d) * Math.min(Math.abs(d), 13 * dt);
      const [x, y] = posAt(m.s);
      sc.drawMini(m.g, x, y, m.color, m.game, m.bad);
      if (Math.abs(d) < 0.01) {
        if (m.target === DIST[NODE.llm]) glow.llm = true;
        if (m.final) { m.g.remove(); minis.delete(id); }
      } else if (now - (m.trailAt || 0) > 90) { m.trailAt = now; sc.trail(x, y); }
      const n = NODES.find((q) => Math.abs(DIST[NODE[q.key]] - m.s) < 0.6);
      if (n) glow[n.key] = true;
    }
    // The belts speed up with the number of requests in flight.
    const busy = [...cubes.values()].filter((c) => !c.final).length + Math.ceil(minis.size / 3);
    const dur = Math.max(0.45, 1.8 - busy * 0.3).toFixed(2) + "s";
    if (svg.style.getPropertyValue("--flowdur") !== dur) svg.style.setProperty("--flowdur", dur);
    for (const k in sc.ORB) {
      const on = !!glow[k];
      sc.ORB[k].spin(dt, on);
      sc.ORB[k].e.setAttribute("opacity", on ? 0.85 : (0.16 + 0.08 * Math.sin(now / 900 + sc.ORB[k].phase)).toFixed(3));
      const lb = sc.LAB[k];
      if (lb) { lb.r.setAttribute("stroke", on ? "var(--green)" : "#34373a"); lb.d.setAttribute("fill", on ? "var(--green)" : "#3a3d40"); }
    }
    for (const k of ["pg", "idp", "rf"]) {
      const lb = sc.LAB[k], on = !!glow[k];
      lb.r.setAttribute("stroke", on ? "var(--green)" : "#34373a"); lb.d.setAttribute("fill", on ? "var(--green)" : "#3a3d40");
    }
    // The barrier opens for requests near egress, and on a slow cycle when nothing is moving.
    const eg = DIST[NODE.egress];
    glow.barrier = [...cubes.values()].some((c) => !c.other && Math.abs(c.s - eg) < 3.2)
      || (!busy && now % 7000 < 2300);
    // The security gate opens for requests approaching it (either way), and now and then when idle.
    const gw = DIST[NODE.gw];
    glow.gate = [...cubes.values()].some((c) => !c.other && Math.abs(c.s - gw) < 2.4) || (!busy && (now + 3500) % 9000 < 1800);
    sc.machines(glow, dt);
    if (camera) {
      const f = cubes.get(focusId);
      if (picked && Date.now() > pickedUntil) picked = null;
      const [tx, ty] = picked ? sc.spot(picked) : f && f.pos ? f.pos : tourAt || [720, 400];
      // A gentle drift toward the action, never more than ~70px from the centre.
      const k = 1 - Math.exp(-dt * 0.7);
      cam.x += (Math.min(770, Math.max(630, 700 + (tx - 700) * 0.25)) - cam.x) * k;
      cam.y += (Math.min(470, Math.max(350, 410 + (ty - 410) * 0.25)) - cam.y) * k;
      svg.setAttribute("viewBox", `${(cam.x - VW / 2).toFixed(1)} ${(cam.y - VH / 2).toFixed(1)} ${VW} ${VH}`);
    }
    sc.pg.setAttribute("fill", glow.pg ? "var(--green)" : "var(--greenDim)");
    sc.idp.setAttribute("fill", glow.idp ? "var(--green)" : "var(--greenDim)");
    for (const r of sc.rf) r.setAttribute("fill", glow.rf ? "var(--green)" : "var(--greenDim)");
    requestAnimationFrame(frame);
  }
  requestAnimationFrame(frame);

  const active = () => [...cubes.values()].some((c) => !c.final) || minis.size > 0;
  const turnsPerMin = () => turnTimes.length;
  const tour = (i) => { tourAt = sc.tour(i); };
  const focus = () => (picked ? null : focusId);
  const pickedKey = () => picked;
  if (!live) return { apply, tour, active, focus, picked: pickedKey, turnsPerMin };
  let es;
  (function connect() {
    es = new EventSource("/agent-flow/api/events");
    es.onmessage = (m) => apply(JSON.parse(m.data));
    es.onerror = () => { es.close(); setTimeout(connect, 2000); };
  })();
  return { apply, tour, active, focus, picked: pickedKey, turnsPerMin };
}
