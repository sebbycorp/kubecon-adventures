// Substrate Siege: HUD helpers shared by the phone page and the big screen (plain
// HTML and 2D canvas, no WebGL): part icons, the run's goal line, the clock and the
// minimap.

// Colours live in city.js (partColor, colorOf); callers pass them in, so this file has
// no imports and each page loads one copy of each module.
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);

// RIG_KINDS are the four parts of a rig, in strip order.
export const RIG_KINDS = [["board", "BOARD"], ["cpu", "CPU"], ["ram", "RAM"], ["drive", "DRIVE"]];

// partIcon is a small SVG of a part kind ("ram", "cpu", "drive", "board") in colour c.
export function partIcon(kind, c, size = 26) {
  const shapes = {
    ram: `<rect x="2" y="8" width="28" height="10" rx="1.5" fill="${c}"/><g fill="#001a08">${[6, 11, 16, 21, 26].map((x) => `<rect x="${x - 1.5}" y="10" width="3" height="5"/>`).join("")}</g><g fill="${c}">${[5, 9, 13, 17, 21, 25].map((x) => `<rect x="${x}" y="19" width="2" height="3"/>`).join("")}</g>`,
    cpu: `<rect x="8" y="5" width="16" height="16" rx="2" fill="${c}"/><rect x="12" y="9" width="8" height="8" fill="#001a08"/><g stroke="${c}" stroke-width="1.5">${[10, 14, 18, 22].map((v) => `<path d="M${v} 2v3M${v} 21v3M5 ${v - 3}h3M24 ${v - 3}h3"/>`).join("")}</g>`,
    drive: `<rect x="4" y="5" width="24" height="16" rx="2.5" fill="${c}"/><circle cx="13" cy="13" r="5" fill="#001a08"/><circle cx="13" cy="13" r="1.5" fill="${c}"/><rect x="21" y="16" width="4" height="2" fill="#001a08"/>`,
    board: `<rect x="3" y="3" width="26" height="20" rx="2" fill="${c}"/><g fill="#001a08"><rect x="7" y="7" width="7" height="7"/><rect x="17" y="6" width="9" height="2"/><rect x="17" y="10" width="9" height="2"/><rect x="7" y="17" width="19" height="2"/></g>`,
  };
  return `<svg viewBox="0 0 32 26" width="${size}" height="${Math.round((size * 26) / 32)}" aria-hidden="true">${shapes[kind] || shapes.ram}</svg>`;
}

// fmtClock is m:ss for a number of seconds.
export function fmtClock(s) {
  s = Math.max(0, Math.ceil(s));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

// runLine is the phone's one-line goal for the moment: book a frozen agent next to you,
// or freeze the ones on the loose.
export function runLine(snap, me, a) {
  const near = a && snap.targets.find((t) => t.state === "Frozen" && !t.busy && Math.hypot(t.x - a.x, t.y - a.y) < 1.6);
  if (near) return `Tap <b>BOOK</b> to snapshot <b>${esc(String(near.name).toUpperCase())}</b>: +2`;
  const run = snap.missions.find((m) => m.squad === (a && a.squad) && !m.outcome);
  const mine = snap.targets.filter((t) => run && t.mission === run.id);
  const loose = mine.filter((t) => t.state === "Working").length;
  if (!loose && mine.some((t) => t.state === "Frozen")) return "Walk up to a frozen agent and tap <b>BOOK</b>: +2";
  if (!loose) return "More agents arriving…";
  return `Follow the arrow · <b>2 hits</b> freezes an agent · <b>${loose}</b> on the loose`;
}

// minimap draws the city, the Vault and Dispatch once, then the moving dots each call.
export function minimap(canvas, city) {
  const { w: W, h: H, tiles } = city;
  const base = document.createElement("canvas");
  const k = Math.max(1, Math.floor(Math.min(canvas.width / W, canvas.height / H)));
  base.width = W * k; base.height = H * k;
  const b = base.getContext("2d");
  b.fillStyle = "#000"; b.fillRect(0, 0, base.width, base.height);
  for (let y = 0; y < H; y++) for (let x = 0; x < W; x++) {
    const c = tiles[y][x];
    b.fillStyle = c === "#" || c === "V" ? "#1c2632" : c === "D" ? "#1e9e4a" : "#05070a";
    b.fillRect(x * k, y * k, k, k);
  }
  const g = canvas.getContext("2d");
  const ox = (canvas.width - base.width) / 2, oy = (canvas.height - base.height) / 2;
  return function draw(snap, me, view) {
    g.fillStyle = "#000"; g.fillRect(0, 0, canvas.width, canvas.height);
    g.drawImage(base, ox, oy);
    if (!snap) return;
    const dot = (x, y, c, r) => { g.fillStyle = c; g.fillRect(ox + x * k - r / 2, oy + y * k - r / 2, r, r); };
    for (const d of snap.drops) dot(d.x, d.y, "#ffd23d", 2);
    for (const t of snap.targets) {
      if (t.state === "Booked" || t.state === "Deleted") continue;
      dot(t.x, t.y, t.state === "Working" ? "#ff3b3b" : "#66ffff", 4);
    }
    for (const a of snap.avatars) if (a.online && a.id !== me) dot(a.x, a.y, "#7fbf8f", 3);
    const m = snap.avatars.find((a) => a.id === me);
    if (m) {
      const p = (view && view.me) || m;
      g.strokeStyle = "#fff"; g.lineWidth = 1;
      g.strokeRect(ox + p.x * k - 3, oy + p.y * k - 3, 6, 6);
      dot(p.x, p.y, "#fff", 3);
    }
  };
}
