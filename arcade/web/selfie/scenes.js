// Substrate Selfie's ten scenes, drawn on a 1080x1350 canvas (4:5, Instagram's portrait
// size) from the visitor's photo, the Agent Substrate octopus, the kagent logo and a
// caption. All art is drawn here with shapes and gradients: no photos but the visitor's.
// KubeCon + CloudNativeCon North America 2026: November 9-12, Salt Palace, Salt Lake City.

export const W = 1080, H = 1350;
const TAU = Math.PI * 2;

// ---- helpers --------------------------------------------------------------

function rr(c, x, y, w, h, r) {
  c.beginPath();
  c.moveTo(x + r, y);
  c.arcTo(x + w, y, x + w, y + h, r);
  c.arcTo(x + w, y + h, x, y + h, r);
  c.arcTo(x, y + h, x, y, r);
  c.arcTo(x, y, x + w, y, r);
  c.closePath();
}

// photo draws img covering the box (x, y, w, h), cropped around its centre and a little
// above it, where a selfie's face usually is.
function photo(c, img, x, y, w, h, { r = 0, filter = "" } = {}) {
  const s = Math.max(w / img.width, h / img.height);
  const sw = w / s, sh = h / s;
  const sx = (img.width - sw) / 2, sy = Math.max(0, Math.min(img.height - sh, (img.height - sh) * 0.38));
  c.save();
  if (r) { rr(c, x, y, w, h, r); c.clip(); }
  if (filter) c.filter = filter;
  c.drawImage(img, sx, sy, sw, sh, x, y, w, h);
  c.restore();
}

function lines(c, text, maxW) {
  const words = String(text).split(/\s+/), out = [];
  let line = "";
  for (const w of words) {
    const t = line ? line + " " + w : w;
    if (c.measureText(t).width > maxW && line) { out.push(line); line = w; } else line = t;
  }
  if (line) out.push(line);
  return out;
}

// text draws wrapped text centred (or left) at x, top y, shrinking to fit maxLines.
function text(c, s, x, y, maxW, { size = 48, font = "Inter", weight = 800, color = "#fff", align = "center", lh = 1.15, maxLines = 3, stroke = "", sw = 0, shadow = "" } = {}) {
  let ls;
  for (;;) {
    c.font = `${weight} ${size}px "${font}", Inter, sans-serif`; // quoted: "Press Start 2P" is invalid bare
    ls = lines(c, s, maxW);
    if (ls.length <= maxLines || size <= 18) break;
    size -= 2;
  }
  c.save();
  c.textAlign = align;
  c.textBaseline = "top";
  if (shadow) { c.shadowColor = shadow; c.shadowBlur = size * 0.35; c.shadowOffsetY = size * 0.06; }
  ls.slice(0, maxLines).forEach((l, i) => {
    if (stroke) { c.lineWidth = sw; c.strokeStyle = stroke; c.lineJoin = "round"; c.strokeText(l, x, y + i * size * lh); }
    c.fillStyle = color;
    c.fillText(l, x, y + i * size * lh);
  });
  c.restore();
  return y + ls.length * size * lh;
}

function octo(c, o, x, y, w, rot = 0) {
  const h = w * (o.height / o.width);
  c.save();
  c.translate(x, y);
  c.rotate(rot);
  c.shadowColor = "rgba(0,0,0,.35)"; c.shadowBlur = 24; c.shadowOffsetY = 10;
  c.drawImage(o, -w / 2, -h / 2, w, h);
  c.restore();
}

function logo(c, k, x, y, s, { plate = "" } = {}) {
  if (plate) {
    c.save(); c.fillStyle = plate; rr(c, x - s * 0.15, y - s * 0.12, s * 1.3, s * 1.24, s * 0.22); c.fill(); c.restore();
  }
  c.drawImage(k, x, y, s, s);
}

// footer is the small credit every picture carries.
function footer(c, color = "rgba(255,255,255,.85)", bg = "rgba(0,0,0,.35)") {
  c.save();
  c.font = "600 26px Inter";
  const t = "kubecon.agentpwned.ai · kagent + Agent Substrate";
  const w = c.measureText(t).width + 40;
  c.fillStyle = bg; rr(c, (W - w) / 2, H - 62, w, 44, 22); c.fill();
  c.fillStyle = color; c.textAlign = "center"; c.textBaseline = "middle"; c.fillText(t, W / 2, H - 40);
  c.restore();
}

function rand(seed) { // a small seeded PRNG, so a scene looks the same every time
  let s = seed >>> 0;
  return () => ((s = (s * 1664525 + 1013904223) >>> 0) / 4294967296);
}

function mountains(c, y, colors, seed) {
  const r = rand(seed);
  colors.forEach((col, i) => {
    c.fillStyle = col;
    c.beginPath();
    c.moveTo(0, H);
    let x = 0;
    const base = y + i * 70;
    c.lineTo(0, base);
    while (x < W) {
      const nx = x + 90 + r() * 140;
      c.lineTo((x + nx) / 2, base - 120 - r() * 220 + i * 40);
      c.lineTo(nx, base - r() * 40);
      x = nx;
    }
    c.lineTo(W, H);
    c.fill();
  });
}

// ---- the scenes -----------------------------------------------------------

function keynote(c, a) {
  const g = c.createLinearGradient(0, 0, 0, H);
  g.addColorStop(0, "#07051a"); g.addColorStop(1, "#1a0b33");
  c.fillStyle = g; c.fillRect(0, 0, W, H);
  // spotlights
  for (const [x, col] of [[180, "rgba(122,92,255,.28)"], [540, "rgba(80,200,255,.22)"], [900, "rgba(255,90,200,.25)"]]) {
    c.fillStyle = col; c.beginPath(); c.moveTo(x - 30, 0); c.lineTo(x + 30, 0); c.lineTo(x + 260, 980); c.lineTo(x - 260, 980); c.fill();
  }
  // the Jumbotron
  c.fillStyle = "#0b0b14"; rr(c, 90, 150, 900, 610, 26); c.fill();
  c.strokeStyle = "#5a4bff"; c.lineWidth = 10; rr(c, 90, 150, 900, 610, 26); c.stroke();
  photo(c, a.photo, 110, 170, 860, 570, { r: 16 });
  // lower third
  c.fillStyle = "rgba(10,8,30,.82)"; c.fillRect(110, 640, 860, 100);
  text(c, a.nick ? a.nick.toUpperCase() : "LIVE FROM THE AUDIENCE", 140, 652, 600, { size: 38, align: "left", maxLines: 1, color: "#fff" });
  text(c, "KubeCon + CloudNativeCon North America 2026 · Keynote", 140, 698, 800, { size: 24, weight: 600, align: "left", maxLines: 1, color: "#b9b2ff" });
  octo(c, a.octo, 905, 245, 250, 0.35); // photobombing from the corner of the screen
  // stage, podium and audience
  c.fillStyle = "#120d2a"; c.fillRect(0, 820, W, 120);
  c.fillStyle = "#2a2160"; c.fillRect(0, 820, W, 8);
  c.fillStyle = "#1d1640"; rr(c, 450, 760, 180, 160, 12); c.fill();
  logo(c, a.kagent, 500, 790, 80);
  const r = rand(7);
  for (let row = 0; row < 4; row++) for (let i = 0; i < 14; i++) {
    c.fillStyle = `rgba(${20 + row * 6},${14 + row * 4},${50 + row * 10},1)`;
    const x = i * 80 + (row % 2) * 40 + r() * 10, y = 950 + row * 52;
    c.beginPath(); c.arc(x, y, 26, 0, TAU); c.fill(); c.fillRect(x - 34, y + 18, 68, 60);
  }
  text(c, a.caption, W / 2, 1105, 960, { size: 50, color: "#fff", shadow: "#000", maxLines: 3 });
  text(c, "SALT LAKE CITY · NOV 9–12, 2026", W / 2, 60, 900, { size: 34, weight: 800, color: "#c9c2ff", maxLines: 1 });
  footer(c);
}

function wanted(c, a) {
  const g = c.createRadialGradient(W / 2, H / 2, 200, W / 2, H / 2, 900);
  g.addColorStop(0, "#f3dfb2"); g.addColorStop(1, "#c79b58");
  c.fillStyle = g; c.fillRect(0, 0, W, H);
  const r = rand(11);
  for (let i = 0; i < 900; i++) { c.fillStyle = `rgba(90,55,20,${r() * 0.08})`; c.fillRect(r() * W, r() * H, 3 + r() * 6, 2 + r() * 4); }
  c.strokeStyle = "#5b3a14"; c.lineWidth = 6; c.strokeRect(40, 40, W - 80, H - 80);
  c.lineWidth = 2; c.strokeRect(58, 58, W - 116, H - 116);
  text(c, "WANTED", W / 2, 90, 980, { size: 190, font: "Rye", weight: 400, color: "#3d2408", maxLines: 1 });
  text(c, "DEAD OR ALIVE · OR SNAPSHOTTED", W / 2, 300, 900, { size: 34, font: "Rye", weight: 400, color: "#5b3a14", maxLines: 1 });
  photo(c, a.photo, 230, 370, 620, 560, { filter: "sepia(.85) contrast(1.1) brightness(.95)" });
  c.strokeStyle = "#3d2408"; c.lineWidth = 10; c.strokeRect(230, 370, 620, 560);
  text(c, "FOR RUNNING 10,000 AGENTS ON 3 NODES", W / 2, 960, 900, { size: 44, font: "Rye", weight: 400, color: "#3d2408", maxLines: 2 });
  text(c, a.caption, W / 2, 1075, 860, { size: 38, font: "Georgia", weight: 700, color: "#3d2408", maxLines: 3 });
  text(c, "REWARD: $10,000 IN CLOUD CREDITS", W / 2, 1210, 900, { size: 36, font: "Rye", weight: 400, color: "#7a1e0e", maxLines: 1 });
  octo(c, a.octo, 950, 760, 190, 0.18);
  logo(c, a.kagent, 95, 420, 100);
}

function card(c, a) {
  c.fillStyle = "#101524"; c.fillRect(0, 0, W, H);
  const holo = c.createLinearGradient(0, 0, W, H);
  ["#ff7ad9", "#7ae7ff", "#b6ff7a", "#ffd36b", "#c48bff"].forEach((col, i, arr) => holo.addColorStop(i / (arr.length - 1), col));
  c.fillStyle = holo; rr(c, 70, 50, 940, 1250, 48); c.fill();
  c.fillStyle = "#fdf7e7"; rr(c, 100, 80, 880, 1190, 34); c.fill();
  c.fillStyle = "#26304d"; rr(c, 130, 110, 820, 90, 20); c.fill();
  text(c, (a.nick || "Agent Wrangler").toUpperCase(), 160, 128, 560, { size: 48, align: "left", maxLines: 1, color: "#fff" });
  text(c, "HP 140", 920, 134, 300, { size: 36, align: "right", maxLines: 1, color: "#ffd36b" });
  photo(c, a.photo, 130, 220, 820, 560, { r: 18 });
  c.strokeStyle = "#c9a43a"; c.lineWidth = 12; rr(c, 130, 220, 820, 560, 18); c.stroke();
  octo(c, a.octo, 860, 730, 210, 0.15);
  c.fillStyle = "#e9dfc4"; rr(c, 130, 800, 820, 44, 12); c.fill();
  text(c, "LEGENDARY AGENT WRANGLER · kagent edition", W / 2, 808, 800, { size: 24, weight: 700, color: "#6b5a2a", maxLines: 1 });
  const stats = [["Snapshot speed", "140 ms"], ["Pod charisma", "99/99"], ["Cold starts", "0"]];
  stats.forEach(([k, v], i) => {
    const y = 870 + i * 62;
    text(c, k, 160, y, 500, { size: 34, weight: 700, align: "left", maxLines: 1, color: "#26304d" });
    text(c, v, 920, y, 300, { size: 34, weight: 800, align: "right", maxLines: 1, color: "#b0287a" });
  });
  text(c, a.caption, W / 2, 1065, 800, { size: 36, weight: 600, font: "Georgia", color: "#26304d", maxLines: 3 });
  logo(c, a.kagent, 140, 1190, 60);
  text(c, "★ ★ ★ ★ ★  #001/2026", 920, 1205, 600, { size: 28, weight: 800, align: "right", maxLines: 1, color: "#c9a43a" });
}

function magazine(c, a) {
  photo(c, a.photo, 0, 0, W, H);
  const g = c.createLinearGradient(0, H * 0.45, 0, H);
  g.addColorStop(0, "rgba(0,0,0,0)"); g.addColorStop(1, "rgba(0,0,0,.85)");
  c.fillStyle = g; c.fillRect(0, 0, W, H);
  c.fillStyle = "rgba(0,0,0,.25)"; c.fillRect(0, 0, W, 260);
  text(c, "CLOUD", 60, 30, 1000, { size: 190, font: "Bebas Neue", weight: 400, align: "left", color: "#ff3b5c", maxLines: 1, stroke: "#fff", sw: 6 });
  text(c, "NATIVE", 470, 30, 600, { size: 190, font: "Bebas Neue", weight: 400, align: "left", color: "#fff", maxLines: 1 });
  text(c, "NOVEMBER 2026 · THE SALT LAKE CITY ISSUE · $0.00 (IT'S OPEN SOURCE)", 60, 222, 980, { size: 22, weight: 700, align: "left", color: "#fff", maxLines: 1 });
  const cover = ["10,000 AGENTS, 3 NODES: THE SECRET", "WHY IDLE PODS ARE OVER", "WE SNAPSHOTTED A DEVELOPER"];
  cover.forEach((l, i) => text(c, l, 60, 330 + i * 120, 380, { size: 36, font: "Bebas Neue", weight: 400, align: "left", color: i === 1 ? "#ffd23d" : "#fff", maxLines: 2, shadow: "#000" }));
  text(c, "PERSON OF THE YEAR", 60, 930, 900, { size: 60, font: "Bebas Neue", weight: 400, align: "left", color: "#ffd23d", maxLines: 1, shadow: "#000" });
  text(c, a.caption, 60, 1000, 760, { size: 46, weight: 800, align: "left", color: "#fff", maxLines: 3, shadow: "#000" });
  octo(c, a.octo, 920, 1080, 260, -0.1);
  logo(c, a.kagent, 930, 300, 100, { plate: "rgba(255,255,255,.9)" });
  // barcode
  c.fillStyle = "#fff"; c.fillRect(60, 1200, 200, 90);
  for (let i = 0; i < 40; i++) { c.fillStyle = "#000"; c.fillRect(70 + i * 4.5, 1210, (i * 7) % 3 + 1, 60); }
}

function billboard(c, a) {
  const sky = c.createLinearGradient(0, 0, 0, H);
  sky.addColorStop(0, "#ff9a62"); sky.addColorStop(0.45, "#ffc78a"); sky.addColorStop(0.6, "#8fb6e8");
  c.fillStyle = sky; c.fillRect(0, 0, W, H);
  c.fillStyle = "rgba(255,240,200,.9)"; c.beginPath(); c.arc(820, 300, 90, 0, TAU); c.fill();
  mountains(c, 900, ["#6b7aa8", "#4b5884", "#33405f"], 21);
  // snow caps
  c.fillStyle = "rgba(255,255,255,.85)";
  for (const [x, y] of [[200, 700], [520, 640], [860, 690]]) { c.beginPath(); c.moveTo(x - 70, y + 60); c.lineTo(x, y); c.lineTo(x + 70, y + 60); c.lineTo(x + 30, y + 45); c.lineTo(x, y + 70); c.lineTo(x - 30, y + 45); c.fill(); }
  // highway
  c.fillStyle = "#2b2d33"; c.fillRect(0, 1180, W, 170);
  c.fillStyle = "#ffd23d"; for (let x = 0; x < W; x += 120) c.fillRect(x, 1258, 60, 10);
  // the billboard
  c.fillStyle = "#3b3f48"; c.fillRect(310, 830, 26, 360); c.fillRect(744, 830, 26, 360);
  c.fillStyle = "#fff"; rr(c, 80, 170, 920, 680, 14); c.fill();
  photo(c, a.photo, 100, 190, 520, 640);
  c.fillStyle = "#14532d"; c.fillRect(620, 190, 360, 640);
  text(c, "NOW IN", 800, 230, 320, { size: 40, weight: 800, color: "#bbf7d0", maxLines: 1 });
  text(c, "SALT LAKE CITY", 800, 280, 330, { size: 58, font: "Bebas Neue", weight: 400, color: "#fff", maxLines: 2 });
  text(c, a.caption, 800, 420, 320, { size: 34, weight: 700, color: "#fff", maxLines: 7 });
  logo(c, a.kagent, 750, 735, 90);
  octo(c, a.octo, 860, 150, 240, 0.12); // sitting on top of the billboard
  text(c, "I-15 · EXIT 307 · KUBECON →", W / 2, 1200, 900, { size: 30, weight: 800, color: "#fff", maxLines: 1 });
  footer(c);
}

function lake(c, a) {
  const sky = c.createLinearGradient(0, 0, 0, 700);
  sky.addColorStop(0, "#5fb4ff"); sky.addColorStop(1, "#cfe9ff");
  c.fillStyle = sky; c.fillRect(0, 0, W, H);
  mountains(c, 640, ["#b8a6c9", "#9a88ae"], 31);
  const water = c.createLinearGradient(0, 640, 0, 960);
  water.addColorStop(0, "#f2a6c1"); water.addColorStop(1, "#7fd3d1"); // the lake's famous pinks and teals
  c.fillStyle = water; c.fillRect(0, 640, W, 330);
  c.fillStyle = "#f5f1e8"; c.fillRect(0, 960, W, 390); // salt flats
  c.fillStyle = "rgba(200,190,170,.5)";
  for (let i = 0; i < 12; i++) { c.beginPath(); c.moveTo(0, 990 + i * 30); c.lineTo(W, 980 + i * 34); c.lineWidth = 2; c.strokeStyle = "rgba(190,175,150,.5)"; c.stroke(); }
  // the selfie: a phone held up, the visitor and the octopus in frame
  c.save(); c.translate(W / 2, 560); c.rotate(-0.06);
  c.fillStyle = "#111"; rr(c, -330, -420, 660, 860, 60); c.fill();
  photo(c, a.photo, -305, -395, 610, 810, { r: 42 });
  c.restore();
  octo(c, a.octo, 820, 660, 330, -0.25);
  logo(c, a.kagent, 110, 110, 110, { plate: "rgba(255,255,255,.85)" });
  text(c, "GREAT SALT LAKE, UTAH", W / 2, 1015, 980, { size: 54, font: "Bebas Neue", weight: 400, color: "#2a6f8f", maxLines: 1 });
  text(c, a.caption, W / 2, 1085, 960, { size: 44, weight: 800, color: "#1f2937", maxLines: 3 });
  footer(c, "#1f2937", "rgba(255,255,255,.75)");
}

function employee(c, a) {
  // wood panelling
  for (let x = 0; x < W; x += 120) { c.fillStyle = x % 240 ? "#6b4226" : "#734a2b"; c.fillRect(x, 0, 120, H); }
  const r = rand(41);
  c.strokeStyle = "rgba(40,20,8,.35)";
  for (let i = 0; i < 120; i++) { const x = r() * W; c.beginPath(); c.moveTo(x, 0); c.bezierCurveTo(x + 20, 400, x - 20, 900, x + 10, H); c.stroke(); }
  c.fillStyle = "#3a2312"; c.fillRect(0, 1180, W, 170);
  // the plaque
  const gold = c.createLinearGradient(0, 120, 0, 1120);
  gold.addColorStop(0, "#f7d774"); gold.addColorStop(0.5, "#b8862b"); gold.addColorStop(1, "#f2cf6a");
  c.fillStyle = "#1c120a"; rr(c, 140, 110, 800, 1020, 30); c.fill();
  c.fillStyle = gold; rr(c, 165, 135, 750, 970, 20); c.fill();
  c.fillStyle = "#2b1a0c"; rr(c, 195, 165, 690, 910, 14); c.fill();
  logo(c, a.kagent, 490, 190, 100);
  text(c, "EMPLOYEE OF THE MONTH", W / 2, 305, 650, { size: 50, font: "Playfair Display", weight: 700, color: "#f7d774", maxLines: 1 });
  c.save(); c.beginPath(); c.ellipse(W / 2, 640, 230, 270, 0, 0, TAU); c.clip();
  photo(c, a.photo, W / 2 - 230, 370, 460, 540); c.restore();
  c.strokeStyle = gold; c.lineWidth = 14; c.beginPath(); c.ellipse(W / 2, 640, 230, 270, 0, 0, TAU); c.stroke();
  c.fillStyle = gold; rr(c, 300, 935, 480, 70, 10); c.fill();
  text(c, a.nick || "Our Favourite Human", W / 2, 950, 440, { size: 36, font: "Playfair Display", weight: 700, color: "#2b1a0c", maxLines: 1 });
  text(c, "NOVEMBER 2026 · kagent", W / 2, 1025, 600, { size: 26, weight: 700, color: "#f7d774", maxLines: 1 });
  text(c, a.caption, W / 2, 1195, 940, { size: 40, weight: 700, color: "#fff4d6", maxLines: 2, shadow: "#000" });
  octo(c, a.octo, 940, 1040, 230, 0.2);
}

function badge(c, a) {
  c.fillStyle = "#e6ebf2"; c.fillRect(0, 0, W, H);
  // lanyard
  c.fillStyle = "#326ce5";
  c.beginPath(); c.moveTo(380, 0); c.lineTo(470, 0); c.lineTo(520, 230); c.lineTo(500, 230); c.fill();
  c.beginPath(); c.moveTo(700, 0); c.lineTo(610, 0); c.lineTo(560, 230); c.lineTo(580, 230); c.fill();
  c.save(); c.fillStyle = "#fff"; c.font = "800 18px Inter";
  for (let i = 0; i < 6; i++) { c.save(); c.translate(400 + i * 14, 30 + i * 36); c.rotate(1.35); c.fillText("kagent", 0, 0); c.restore(); }
  c.restore();
  c.fillStyle = "#9aa4b2"; rr(c, 500, 220, 80, 60, 10); c.fill();
  // the badge
  c.save(); c.translate(W / 2, 760); c.rotate(0.03);
  c.shadowColor = "rgba(0,0,0,.25)"; c.shadowBlur = 40; c.shadowOffsetY = 18;
  c.fillStyle = "#fff"; rr(c, -400, -480, 800, 1000, 36); c.fill();
  c.shadowColor = "transparent";
  c.fillStyle = "#326ce5"; rr(c, -400, -480, 800, 150, 36); c.fill(); c.fillRect(-400, -380, 800, 50);
  text(c, "kind: Pod", -360, -455, 500, { size: 44, font: "JetBrains Mono", weight: 800, align: "left", color: "#fff", maxLines: 1 });
  text(c, "namespace: kubecon-2026", -360, -400, 600, { size: 28, font: "JetBrains Mono", weight: 600, align: "left", color: "#dbe7ff", maxLines: 1 });
  logo(c, a.kagent, 250, -460, 100, { plate: "rgba(255,255,255,.92)" });
  photo(c, a.photo, -360, -290, 320, 400, { r: 18 });
  const name = (a.nick || "human").toLowerCase().replace(/[^a-z0-9]+/g, "-").slice(0, 14) || "human";
  const fields = [["name", name + "-7f9c4"], ["status", "● Running"], ["restarts", "0"], ["image", "you:latest"], ["node", "salt-palace-1"]];
  fields.forEach(([k, v], i) => {
    text(c, k + ":", -10, -280 + i * 78, 160, { size: 26, font: "JetBrains Mono", weight: 600, align: "left", color: "#6b7280", maxLines: 1 });
    text(c, v, 140, -280 + i * 78, 250, { size: 30, font: "JetBrains Mono", weight: 800, align: "left", color: k === "status" ? "#16a34a" : "#111827", maxLines: 1 });
  });
  text(c, a.caption, 0, 160, 720, { size: 38, weight: 800, color: "#111827", maxLines: 3 });
  for (let i = 0; i < 46; i++) { c.fillStyle = "#111"; c.fillRect(-340 + i * 15, 390, (i * 5) % 4 + 2, 80); }
  c.restore();
  octo(c, a.octo, 880, 1250, 250, -0.15);
}

function arcade(c, a) {
  c.fillStyle = "#05010f"; c.fillRect(0, 0, W, H);
  // pixelated photo and octopus
  const px = (img, x, y, w, h, n) => {
    const t = document.createElement("canvas"); t.width = n; t.height = Math.round(n * h / w);
    const tc = t.getContext("2d");
    if (img === a.photo) photo(tc, img, 0, 0, t.width, t.height); else tc.drawImage(img, 0, 0, t.width, t.height);
    c.save(); c.imageSmoothingEnabled = false; c.drawImage(t, x, y, w, h); c.restore();
  };
  text(c, "HIGH SCORES", W / 2, 70, 980, { size: 66, font: "Press Start 2P", weight: 400, color: "#ffd23d", maxLines: 1 });
  c.strokeStyle = "#ff3bd4"; c.lineWidth = 8; c.strokeRect(300, 190, 480, 480);
  px(a.photo, 310, 200, 460, 460, 64);
  const nick = (a.nick || "YOU").toUpperCase().slice(0, 10);
  const rows = [["1ST", nick, "999999"], ["2ND", "OCTOPUS", "140000"], ["3RD", "IDLE POD", "000000"]];
  rows.forEach(([r, n, s], i) => {
    const y = 720 + i * 72, col = i === 0 ? "#5fd068" : "#e5e7eb";
    text(c, r, 120, y, 200, { size: 34, font: "Press Start 2P", weight: 400, align: "left", color: col, maxLines: 1 });
    text(c, n, 330, y, 420, { size: 34, font: "Press Start 2P", weight: 400, align: "left", color: col, maxLines: 1 });
    text(c, s, 960, y, 300, { size: 34, font: "Press Start 2P", weight: 400, align: "right", color: col, maxLines: 1 });
  });
  text(c, a.caption, W / 2, 970, 940, { size: 30, font: "Press Start 2P", weight: 400, color: "#7ae7ff", lh: 1.5, maxLines: 4 });
  px(a.octo, 60, 1130, 230, 176, 48);
  px(a.kagent, 860, 1160, 140, 140, 24);
  text(c, "INSERT COIN · 1 AGENT = 1 CREDIT", W / 2, 1290, 700, { size: 18, font: "Press Start 2P", weight: 400, color: "#ff3bd4", maxLines: 1 });
  c.fillStyle = "rgba(0,0,0,.28)"; for (let y = 0; y < H; y += 6) c.fillRect(0, y, W, 2); // scanlines
}

function snapshot(c, a) {
  const g = c.createLinearGradient(0, 0, 0, H);
  g.addColorStop(0, "#0b1d2a"); g.addColorStop(1, "#06121b");
  c.fillStyle = g; c.fillRect(0, 0, W, H);
  // Snapshot Storage cabinet
  c.fillStyle = "#24384a"; rr(c, 90, 860, 900, 430, 20); c.fill();
  for (let i = 0; i < 2; i++) { c.fillStyle = "#2f4a62"; rr(c, 130, 900 + i * 190, 820, 160, 14); c.fill(); c.fillStyle = "#8fb3cf"; rr(c, 480, 960 + i * 190, 120, 22, 11); c.fill(); }
  c.fillStyle = "#e6f6ff"; rr(c, 150, 912, 260, 44, 8); c.fill();
  text(c, "SNAPSHOT STORAGE", 280, 920, 240, { size: 22, weight: 800, color: "#24384a", maxLines: 1 });
  logo(c, a.kagent, 840, 905, 80);
  // the polaroid, frozen
  c.save(); c.translate(W / 2, 470); c.rotate(-0.05);
  c.shadowColor = "rgba(111,211,255,.35)"; c.shadowBlur = 60;
  c.fillStyle = "#fbfbf7"; c.fillRect(-340, -380, 680, 800);
  c.shadowColor = "transparent";
  photo(c, a.photo, -305, -345, 610, 600, { filter: "saturate(.7) hue-rotate(-10deg) brightness(1.05)" });
  const ice = c.createLinearGradient(-305, -345, 305, 255);
  ice.addColorStop(0, "rgba(160,230,255,.45)"); ice.addColorStop(0.5, "rgba(255,255,255,.12)"); ice.addColorStop(1, "rgba(120,200,255,.4)");
  c.fillStyle = ice; c.fillRect(-305, -345, 610, 600);
  const r = rand(53);
  c.strokeStyle = "rgba(255,255,255,.75)"; c.lineWidth = 3;
  for (let i = 0; i < 9; i++) { // frost crystals
    const x = -280 + r() * 560, y = -320 + r() * 550, s = 18 + r() * 30;
    for (let k = 0; k < 3; k++) { const an = k * Math.PI / 3; c.beginPath(); c.moveTo(x - Math.cos(an) * s, y - Math.sin(an) * s); c.lineTo(x + Math.cos(an) * s, y + Math.sin(an) * s); c.stroke(); }
  }
  const id = "snap-" + (Math.abs([...(a.nick || "x")].reduce((h, ch) => (h * 31 + ch.charCodeAt(0)) | 0, 7)) % 0xffff).toString(16).padStart(4, "0");
  text(c, `${id} · ${a.snapshotMs ? a.snapshotMs + " ms" : "frozen"}`, 0, 275, 600, { size: 34, font: "JetBrains Mono", weight: 800, color: "#24384a", maxLines: 1 });
  text(c, a.caption, 0, 330, 600, { size: 30, font: "Caveat", weight: 700, color: "#1f2937", maxLines: 2 });
  c.restore();
  octo(c, a.octo, 885, 215, 220, 0.22); // photobombing the polaroid's corner
  text(c, "ASLEEP · 0 CPU · WAKES IN MILLISECONDS", W / 2, 50, 980, { size: 34, weight: 800, color: "#9fe6ff", maxLines: 1 });
  footer(c);
}

export const DRAW = { keynote, wanted, card, magazine, billboard, lake, employee, badge, arcade, snapshot };

// FONTS are the Google Fonts the scenes use; the page loads them before drawing.
export const FONTS = ["800 40px Inter", "600 40px Inter", "400 40px Rye", "400 40px 'Bebas Neue'", "700 40px 'Playfair Display'",
  "400 40px 'Press Start 2P'", "800 40px 'JetBrains Mono'", "700 40px Caveat"];

// render draws scene id into a new canvas. a: {photo, octo, kagent, caption, nick, snapshotMs}.
export function render(id, a) {
  const cv = document.createElement("canvas");
  cv.width = W; cv.height = H;
  const c = cv.getContext("2d");
  (DRAW[id] || keynote)(c, a);
  return cv;
}
