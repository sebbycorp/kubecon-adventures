// Pod Packer's phone game: Tetris where the well is the cluster (each row a worker node)
// and every block is an agent pod. The game runs here with the rules the server serves
// (/pod-packer/api/rules); each line clear is reported at once (POST /api/clear) and the
// server snapshots that many real, awake agent pods on Agent Substrate (real suspends),
// answering with the real time. Game over reports the score, which the server checks.
// Play never waits on Substrate.
"use strict";
(async () => {
  const $ = (id) => document.getElementById(id);
  const show = (id, on = true) => $(id).classList.toggle("hidden", !on);
  const post = async (url, body) => {
    const r = await fetch(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body || {}) });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(j.error || r.statusText);
    return j;
  };

  // Players join the arcade once; a phone without a player gets a name on the home page.
  const me = await fetch("/api/me").catch(() => null);
  if (me && me.status === 401) { location.replace("/?next=/pod-packer"); return; }

  let R = await (await fetch("/pod-packer/api/rules")).json().catch(() => null);
  R = R || { cols: 10, rows: 20, gravity: [0.8, 0.72, 0.63, 0.55, 0.47, 0.38, 0.3, 0.22, 0.13, 0.1, 0.083, 0.07, 0.058, 0.05, 0.042],
    softDrop: 0.03, lockDelay: 0.5, linesPerLevel: 10, points: [0, 100, 300, 500, 800], backToBack: 1.5, combo: 50, softPoint: 1, hardPoint: 2 };
  const COLS = R.cols, ROWS = R.rows, HIDDEN = 2;

  // ---- sound and vibration ----
  let soundOn = true, ac = null;
  try { soundOn = localStorage.getItem("packer-sound") !== "off"; } catch {}
  const tone = (type, f0, f1, dur, vol, delay = 0) => {
    if (!soundOn) return;
    try { ac = ac || new (window.AudioContext || window.webkitAudioContext)(); } catch { return; }
    const t = ac.currentTime + delay, o = ac.createOscillator(), g = ac.createGain();
    o.connect(g); g.connect(ac.destination);
    o.type = type;
    o.frequency.setValueAtTime(f0, t);
    o.frequency.exponentialRampToValueAtTime(f1, t + dur);
    g.gain.setValueAtTime(vol, t);
    g.gain.exponentialRampToValueAtTime(0.001, t + dur);
    o.start(t); o.stop(t + dur + 0.02);
  };
  const sfx = (k) => ({
    move: () => tone("square", 300, 300, 0.03, 0.03),
    rot: () => tone("triangle", 520, 660, 0.06, 0.06),
    lock: () => tone("sine", 180, 120, 0.08, 0.08),
    drop: () => tone("square", 420, 90, 0.12, 0.08),
    clear: () => { tone("triangle", 660, 990, 0.18, 0.1); tone("triangle", 880, 1320, 0.18, 0.08, 0.08); },
    tetris: () => [523, 659, 784, 1046].forEach((f, i) => tone("square", f, f * 1.01, 0.14, 0.07, i * 0.07)),
    snap: () => tone("sine", 990, 1480, 0.2, 0.06),
    over: () => [392, 330, 262, 196].forEach((f, i) => tone("sawtooth", f, f * 0.98, 0.22, 0.07, i * 0.16)),
    level: () => [660, 880, 1100].forEach((f, i) => tone("triangle", f, f, 0.1, 0.07, i * 0.08)),
  })[k]?.();
  const buzz = (ms) => { try { if (soundOn && navigator.vibrate) navigator.vibrate(ms); } catch {} };
  const soundLabel = () => { for (const id of ["soundBtn", "soundBtn2"]) $(id).textContent = soundOn ? "SOUND: ON" : "SOUND: OFF"; };
  const toggleSound = () => { soundOn = !soundOn; try { localStorage.setItem("packer-sound", soundOn ? "on" : "off"); } catch {} soundLabel(); };
  soundLabel();

  // ---- pieces (SRS) ----
  const COLORS = { I: "#22d3ee", O: "#facc15", T: "#a78bfa", S: "#4ade80", Z: "#f87171", J: "#60a5fa", L: "#fb923c" };
  const SHAPES = {
    I: [[0, 1], [1, 1], [2, 1], [3, 1]], O: [[1, 0], [2, 0], [1, 1], [2, 1]], T: [[1, 0], [0, 1], [1, 1], [2, 1]],
    S: [[1, 0], [2, 0], [0, 1], [1, 1]], Z: [[0, 0], [1, 0], [1, 1], [2, 1]], J: [[0, 0], [0, 1], [1, 1], [2, 1]], L: [[2, 0], [0, 1], [1, 1], [2, 1]],
  };
  const SIZE = { I: 4, O: 4, T: 3, S: 3, Z: 3, J: 3, L: 3 };
  // cells of piece type t at rotation r (0-3, clockwise).
  function cells(t, r) {
    const n = SIZE[t];
    return SHAPES[t].map(([x, y]) => {
      for (let i = 0; i < ((r % 4) + 4) % 4; i++) [x, y] = [n - 1 - y, x];
      return [x, y];
    });
  }
  // SRS wall kicks (y down), from rotation a to b.
  const KICK = {
    "0>1": [[0, 0], [-1, 0], [-1, -1], [0, 2], [-1, 2]], "1>0": [[0, 0], [1, 0], [1, 1], [0, -2], [1, -2]],
    "1>2": [[0, 0], [1, 0], [1, 1], [0, -2], [1, -2]], "2>1": [[0, 0], [-1, 0], [-1, -1], [0, 2], [-1, 2]],
    "2>3": [[0, 0], [1, 0], [1, -1], [0, 2], [1, 2]], "3>2": [[0, 0], [-1, 0], [-1, 1], [0, -2], [-1, -2]],
    "3>0": [[0, 0], [-1, 0], [-1, 1], [0, -2], [-1, -2]], "0>3": [[0, 0], [1, 0], [1, -1], [0, 2], [1, 2]],
  };
  const KICK_I = {
    "0>1": [[0, 0], [-2, 0], [1, 0], [-2, 1], [1, -2]], "1>0": [[0, 0], [2, 0], [-1, 0], [2, -1], [-1, 2]],
    "1>2": [[0, 0], [-1, 0], [2, 0], [-1, -2], [2, 1]], "2>1": [[0, 0], [1, 0], [-2, 0], [1, 2], [-2, -1]],
    "2>3": [[0, 0], [2, 0], [-1, 0], [2, -1], [-1, 2]], "3>2": [[0, 0], [-2, 0], [1, 0], [-2, 1], [1, -2]],
    "3>0": [[0, 0], [1, 0], [-2, 0], [1, 2], [-2, -1]], "0>3": [[0, 0], [-1, 0], [2, 0], [-1, -2], [2, 1]],
  };

  // ---- the game ----
  let grid, cur, holdT, holdUsed, bag, queue, score, lines, level, combo, b2b, tetrises, pieces;
  let fallT, lockT, lockMoves, softing, over, clearing, runID = "", playing = false, startedAt = 0;
  const rows = () => Array.from({ length: ROWS + HIDDEN }, () => Array(COLS).fill(null));
  function nextFromBag() {
    if (!bag.length) { bag = Object.keys(SHAPES); for (let i = bag.length - 1; i > 0; i--) { const j = Math.floor(Math.random() * (i + 1)); [bag[i], bag[j]] = [bag[j], bag[i]]; } }
    return bag.pop();
  }
  function reset() {
    grid = rows(); bag = []; queue = [nextFromBag(), nextFromBag(), nextFromBag()];
    holdT = null; holdUsed = false; score = 0; lines = 0; level = 1; combo = -1; b2b = false; tetrises = 0; pieces = 0;
    fallT = 0; lockT = 0; lockMoves = 0; softing = false; over = false; clearing = null;
    spawn();
  }
  function spawn(t) {
    t = t || queue.shift();
    if (!queue.length || queue.length < 3) queue.push(nextFromBag());
    cur = { t, r: 0, x: t === "O" ? 3 : 3, y: t === "I" ? 0 : 0 };
    lockT = 0; lockMoves = 0; fallT = 0;
    if (!fits(cur.t, cur.r, cur.x, cur.y)) gameOver();
  }
  function fits(t, r, x, y) {
    for (const [cx, cy] of cells(t, r)) {
      const gx = x + cx, gy = y + cy;
      if (gx < 0 || gx >= COLS || gy >= ROWS + HIDDEN) return false;
      if (gy >= 0 && grid[gy][gx]) return false;
    }
    return true;
  }
  const grounded = () => !fits(cur.t, cur.r, cur.x, cur.y + 1);
  function move(dx) {
    if (!playing || clearing || over) return false;
    if (!fits(cur.t, cur.r, cur.x + dx, cur.y)) return false;
    cur.x += dx; sfx("move");
    if (grounded() && lockMoves < 15) { lockT = 0; lockMoves++; }
    return true;
  }
  function rotate(dir = 1) {
    if (!playing || clearing || over || cur.t === "O") return false;
    const a = cur.r, b = (cur.r + dir + 4) % 4, kicks = (cur.t === "I" ? KICK_I : KICK)[`${a}>${b}`];
    for (const [kx, ky] of kicks) {
      if (fits(cur.t, b, cur.x + kx, cur.y - ky)) {
        cur.r = b; cur.x += kx; cur.y -= ky; sfx("rot");
        if (grounded() && lockMoves < 15) { lockT = 0; lockMoves++; }
        return true;
      }
    }
    return false;
  }
  function hardDrop() {
    if (!playing || clearing || over) return;
    let d = 0;
    while (fits(cur.t, cur.r, cur.x, cur.y + 1)) { cur.y++; d++; }
    score += d * R.hardPoint;
    dropFx = { x: cur.x, y: cur.y, t: cur.t, r: cur.r, life: 0.25 };
    sfx("drop"); buzz(15);
    lock();
  }
  function hold() {
    if (!playing || clearing || over || holdUsed) return;
    const t = cur.t;
    if (holdT) { const h = holdT; holdT = t; spawn(h); } else { holdT = t; spawn(); }
    holdUsed = true; sfx("rot");
  }
  function lock() {
    for (const [cx, cy] of cells(cur.t, cur.r)) {
      const gx = cur.x + cx, gy = cur.y + cy;
      if (gy >= 0) grid[gy][gx] = cur.t;
    }
    pieces++;
    // Locking entirely above the visible well: the cluster is full.
    if (cells(cur.t, cur.r).every(([, cy]) => cur.y + cy < HIDDEN)) { gameOver(); return; }
    const full = [];
    for (let y = 0; y < ROWS + HIDDEN; y++) if (grid[y].every(Boolean)) full.push(y);
    if (full.length) {
      scoreClear(full.length);
      clearing = { rows: full, t: 0 };
      // node number: the lowest cleared row, counting nodes from the bottom (node-01).
      const node = ROWS + HIDDEN - Math.max(...full);
      reportClear(full.length, node, full);
      for (const y of full) for (let x = 0; x < COLS; x++) burst(x, y, COLORS[grid[y][x]] || "#fff", 3);
      if (full.length === 4) { shake = 1; buzz([30, 30, 60]); sfx("tetris"); } else { buzz(25); sfx("clear"); }
    } else {
      combo = -1; sfx("lock");
      spawn();
    }
    holdUsed = false;
  }
  function scoreClear(n) {
    combo++;
    let pts = R.points[n] * level;
    if (n === 4) { if (b2b) pts *= R.backToBack; b2b = true; tetrises++; } else b2b = false;
    if (combo > 0) pts += R.combo * combo * level;
    score += Math.round(pts);
    const before = level;
    lines += n;
    level = 1 + Math.floor(lines / R.linesPerLevel);
    if (level > before) { sfx("level"); toastText(`LEVEL ${level}`, "faster drops"); }
    if (combo > 0) addFloat({ text: `COMBO ×${combo + 1}`, x: COLS / 2, y: 8, c: "#ffd23d", life: 1.2, size: 0.9 });
    if (n === 4) addFloat({ text: b2b && tetrises > 1 ? "BACK-TO-BACK TETRIS!" : "TETRIS!", x: COLS / 2, y: 6, c: "#f472b6", life: 1.6, size: 1.2 });
  }
  function finishClear() {
    const keep = grid.filter((row, y) => !clearing.rows.includes(y));
    while (keep.length < ROWS + HIDDEN) keep.unshift(Array(COLS).fill(null));
    grid = keep;
    clearing = null;
    spawn();
  }
  // reportClear tells the server, which snapshots that many real agent pods; the answer
  // floats over the cleared rows with the real time.
  async function reportClear(n, node, ys) {
    const y = Math.min(...ys), label = `node-${String(node).padStart(2, "0")}`;
    const f = addFloat({ text: `${label} freed · snapshotting…`, x: COLS / 2, y: y - HIDDEN + 0.5, c: "#c4f5ce", life: 3.4, size: 0.62, small: true });
    if (!runID) { f.text = `${label} freed`; return; }
    try {
      const res = await post("/pod-packer/api/clear", { run: runID, lines: n, node });
      podsAwake = res.awake;
      if (res.snapped > 0) {
        f.text = `${label} freed · ${res.snapped} agent${res.snapped > 1 ? "s" : ""} snapshotted in ${res.ms} ms`;
        f.c = "#5fd068"; f.life = Math.max(f.life, 3); f.t = 0;
        snaps += res.snapped; snapMs.push(res.ms);
        sfx("snap");
      } else f.text = `${label} freed · ${res.note || "no pod awake yet"}`;
      podsLine();
    } catch (e) {
      f.text = `${label} freed · Substrate busy`;
    }
  }

  function gameOver() {
    if (over) return;
    over = true; playing = false;
    sfx("over"); buzz([60, 40, 120]);
    finish();
  }

  // ---- runs ----
  let snaps = 0, snapMs = [], podsAwake = 0, podsPool = 0;
  function podsLine() {
    $("pods").classList.toggle("on", podsAwake > 0);
    $("podsTxt").innerHTML = podsAwake > 0
      ? `<b>${podsAwake} agent pod${podsAwake > 1 ? "s" : ""}</b> awake on Agent Substrate · <b>${snaps}</b> snapshotted by your clears`
      : `agent pods waking on Agent Substrate… · <b>${snaps}</b> snapshotted`;
  }
  async function play() {
    show("title", false); show("end", false); show("menu", false);
    reset(); snaps = 0; snapMs = []; runID = "";
    startedAt = performance.now();
    playing = true;
    try {
      const s = await post("/pod-packer/api/start", {});
      runID = s.run; podsAwake = s.awake; podsPool = s.pool;
    } catch (e) {
      $("podsTxt").textContent = "playing offline: " + e.message;
    }
    podsLine();
  }
  async function finish() {
    show("end");
    $("endScore").textContent = score.toLocaleString();
    $("endDetail").textContent = `${lines} lines · level ${level}${tetrises ? ` · ${tetrises} tetris${tetrises > 1 ? "es" : ""}` : ""}`;
    const med = snapMs.length ? snapMs.slice().sort((a, b) => a - b)[snapMs.length >> 1] : 0;
    $("endSnap").innerHTML = snaps
      ? `Your clears snapshotted <b>${snaps} real agent${snaps > 1 ? "s" : ""}</b> on Agent Substrate${med ? ` · median <b>${med} ms</b>` : ""}.<br>When a real cluster fills up, Substrate snapshots idle agents to make room.`
      : `Substrate would have snapshotted idle agents here to make room. Clear rows to snapshot real ones.`;
    $("endRank").textContent = ""; $("endBeat").textContent = ""; $("endShare").innerHTML = "";
    let res = null;
    if (runID) {
      try { res = await post("/pod-packer/api/finish", { run: runID, score, lines, tetrises, pieces, level }); } catch {}
    }
    runID = "";
    if (res) {
      $("endRank").textContent = res.counted && res.rank ? (res.rank === 1 ? "#1 ON THE BIG SCREEN!" : `#${res.rank} ON THE BIG SCREEN`) : res.counted ? "" : "That run couldn't be verified.";
      $("endBeat").textContent = res.beat ? `Beat ${res.beat.nick} by ${res.beat.by.toLocaleString()} to reach #${res.beat.rank}` : res.rank === 1 ? "Nobody is ahead of you. Defend it." : "";
      boardRows($("endBoard"), res.top);
    }
    if (score > 0) Arcade.share($("endShare"), { game: "Pod Packer", score, path: "/pod-packer",
      facts: { lines, level, tetrises, snaps, snapshotMs: med } });
  }
  function boardRows(el, rows) {
    rows = rows || [];
    el.classList.add("ab-board");
    el.innerHTML = rows.length ? rows.map((r, i) => `<div class="ab-row${i === 0 ? " ab-top" : ""}"><span class="ab-rank">${["🥇", "🥈", "🥉"][i] || i + 1}</span><i class="ab-dot" style="background:${{ green: "#5fd068", blue: "#60a5fa", red: "#f87171", purple: "#a78bfa", gold: "#fbbf24", pink: "#f472b6" }[r.color] || "#8a8f94"}"></i><span class="ab-who"><b>${String(r.nick).replace(/[&<>"]/g, "")}</b><small>${String(r.detail || "").replace(/[&<>"]/g, "")}</small></span><span class="ab-score">${Number(r.score).toLocaleString()}</span></div>`).join("") : `<div class="ab-empty">No scores yet. Be the first.</div>`;
  }

  // ---- drawing ----
  const cv = $("cv"), cx = cv.getContext("2d");
  let cell = 20, ox = 0, oy = 0, side = 0, dpr = 1, W = 0, H = 0;
  function fit() {
    const box = $("well").getBoundingClientRect();
    dpr = Math.min(2, devicePixelRatio || 1);
    W = box.width; H = box.height;
    cv.width = Math.round(W * dpr); cv.height = Math.round(H * dpr);
    // The well plus a side panel 4.6 cells wide.
    cell = Math.floor(Math.min(H / ROWS, W / (COLS + 4.6 + 0.6)));
    side = cell * 4.6;
    ox = Math.max(8, Math.floor((W - (COLS * cell + side + cell * 0.6)) / 2));
    if (ox + COLS * cell + cell * 0.6 + side > W - 4) cell = Math.floor((W - 12) / (COLS + 4.6 + 0.6)), side = cell * 4.6;
    oy = Math.floor(Math.min(8, (H - ROWS * cell) / 2));
  }
  addEventListener("resize", fit);
  new ResizeObserver(fit).observe($("well"));
  fit();

  function rr(x, y, w, h, r) { cx.beginPath(); cx.roundRect ? cx.roundRect(x, y, w, h, r) : cx.rect(x, y, w, h); }
  // pod draws one agent block: a rounded cube in the piece colour with the kagent face.
  function pod(px, py, size, color, alpha = 1, face = true) {
    cx.globalAlpha = alpha;
    const g = cx.createLinearGradient(px, py, px, py + size);
    g.addColorStop(0, shadeHex(color, 0.25)); g.addColorStop(1, shadeHex(color, -0.25));
    cx.fillStyle = g; rr(px + 1, py + 1, size - 2, size - 2, size * 0.22); cx.fill();
    cx.strokeStyle = shadeHex(color, -0.45); cx.lineWidth = Math.max(1, size * 0.06); cx.stroke();
    if (face && size >= 12) {
      cx.fillStyle = "rgba(10,12,18,.82)";
      rr(px + size * 0.2, py + size * 0.32, size * 0.6, size * 0.36, size * 0.1); cx.fill();
      cx.fillStyle = shadeHex(color, 0.55);
      cx.fillRect(px + size * 0.3, py + size * 0.42, size * 0.12, size * 0.14);
      cx.fillRect(px + size * 0.58, py + size * 0.42, size * 0.12, size * 0.14);
    }
    cx.globalAlpha = 1;
  }
  function shadeHex(hex, k) {
    const n = parseInt(hex.slice(1), 16);
    const f = (c) => Math.max(0, Math.min(255, Math.round(k >= 0 ? c + (255 - c) * k : c * (1 + k))));
    return `rgb(${f(n >> 16)},${f((n >> 8) & 255)},${f(n & 255)})`;
  }
  function mini(t, x, y, size) {
    if (!t) return;
    const cs = cells(t, 0), n = SIZE[t];
    const minX = Math.min(...cs.map((c) => c[0])), maxX = Math.max(...cs.map((c) => c[0])), minY = Math.min(...cs.map((c) => c[1])), maxY = Math.max(...cs.map((c) => c[1]));
    const w = (maxX - minX + 1) * size, h = (maxY - minY + 1) * size;
    for (const [a, b] of cs) pod(x - w / 2 + (a - minX) * size, y - h / 2 + (b - minY) * size, size, COLORS[t], holdUsed && t === holdT ? 0.4 : 1);
    void n;
  }

  let parts = [], floats = [], shake = 0, dropFx = null, toast = null, clock = 0;
  const addFloat = (f) => { f.t = 0; floats.push(f); return f; };
  function burst(gx, gy, color, n) {
    for (let i = 0; i < n; i++) parts.push({ x: gx + 0.5, y: gy - HIDDEN + 0.5, vx: (Math.random() - 0.5) * 9, vy: -Math.random() * 7 - 2, life: 0.7 + Math.random() * 0.4, t: 0, c: color });
  }
  function toastText(a, b) { toast = { a, b, life: 1.4, t: 0 }; }

  function draw() {
    cx.setTransform(dpr, 0, 0, dpr, 0, 0);
    cx.clearRect(0, 0, W, H);
    const sx = shake ? (Math.random() - 0.5) * 10 * shake : 0, sy = shake ? (Math.random() - 0.5) * 10 * shake : 0;
    cx.save(); cx.translate(sx, sy);
    const ww = COLS * cell, wh = ROWS * cell;
    // The cluster: a dark well with a faint grid and node labels.
    cx.fillStyle = "#0e1016"; rr(ox - 4, oy - 4, ww + 8, wh + 8, 10); cx.fill();
    cx.strokeStyle = "#2a2f3b"; cx.lineWidth = 2; cx.stroke();
    cx.strokeStyle = "rgba(255,255,255,.035)"; cx.lineWidth = 1;
    for (let x = 1; x < COLS; x++) { cx.beginPath(); cx.moveTo(ox + x * cell, oy); cx.lineTo(ox + x * cell, oy + wh); cx.stroke(); }
    for (let y = 1; y < ROWS; y++) { cx.beginPath(); cx.moveTo(ox, oy + y * cell); cx.lineTo(ox + ww, oy + y * cell); cx.stroke(); }
    cx.font = `600 ${Math.max(7, cell * 0.3)}px Inter, sans-serif`; cx.textAlign = "right"; cx.textBaseline = "middle";
    cx.fillStyle = "rgba(139,147,161,.38)";
    if (ox > cell * 1.6) for (let y = 0; y < ROWS; y++) cx.fillText(`node-${String(ROWS - y).padStart(2, "0")}`, ox - 7, oy + y * cell + cell / 2);
    // Locked pods.
    for (let y = HIDDEN; y < ROWS + HIDDEN; y++) for (let x = 0; x < COLS; x++) {
      const t = grid[y][x];
      if (!t) continue;
      const flash = clearing && clearing.rows.includes(y);
      pod(ox + x * cell, oy + (y - HIDDEN) * cell, cell, flash ? "#ffffff" : COLORS[t], flash ? 1 - clearing.t / 0.28 : 1);
    }
    // The falling pod group and its ghost.
    if (cur && playing && !clearing) {
      let gy = cur.y;
      while (fits(cur.t, cur.r, cur.x, gy + 1)) gy++;
      for (const [a, b] of cells(cur.t, cur.r)) {
        const y = gy + b - HIDDEN;
        if (y >= 0) { cx.strokeStyle = COLORS[cur.t]; cx.globalAlpha = 0.45; cx.lineWidth = 2; rr(ox + (cur.x + a) * cell + 3, oy + y * cell + 3, cell - 6, cell - 6, cell * 0.2); cx.stroke(); cx.globalAlpha = 1; }
      }
      for (const [a, b] of cells(cur.t, cur.r)) {
        const y = cur.y + b - HIDDEN;
        if (y >= -0.5) pod(ox + (cur.x + a) * cell, oy + y * cell, cell, COLORS[cur.t]);
      }
    }
    if (dropFx) {
      cx.globalAlpha = dropFx.life * 1.2; cx.fillStyle = COLORS[dropFx.t];
      for (const [a, b] of cells(dropFx.t, dropFx.r)) { const yb = (dropFx.y + b - HIDDEN) * cell, len = Math.min(yb, cell * 4); cx.fillRect(ox + (dropFx.x + a) * cell + cell * 0.35, oy + yb - len, cell * 0.3, len); }
      cx.globalAlpha = 1;
    }
    // Side panel: next and hold.
    const px = ox + ww + cell * 0.6, pw = side;
    const panel = (y, h, label) => {
      cx.fillStyle = "#12141a"; rr(px, y, pw, h, 10); cx.fill(); cx.strokeStyle = "#262a33"; cx.lineWidth = 1; cx.stroke();
      cx.fillStyle = "#8b93a1"; cx.font = `700 ${Math.max(9, cell * 0.36)}px "Chakra Petch", sans-serif`; cx.textAlign = "center"; cx.textBaseline = "top";
      cx.fillText(label, px + pw / 2, y + 6);
    };
    const nh = cell * 7.2;
    panel(oy, nh, "NEXT");
    for (let i = 0; i < 3 && queue; i++) mini(queue[i], px + pw / 2, oy + cell * (1.9 + i * 1.95), i === 0 ? cell * 0.8 : cell * 0.6);
    panel(oy + nh + cell * 0.4, cell * 3, "HOLD");
    mini(holdT, px + pw / 2, oy + nh + cell * 2.3, cell * 0.7);
    // Cluster fill gauge: how full the nodes are.
    if (grid) {
      const used = grid.slice(HIDDEN).filter((r) => r.some(Boolean)).length / ROWS;
      const gy0 = oy + nh + cell * 3.8, gh = wh - (nh + cell * 3.8);
      if (gh > cell * 2) {
        panel(gy0, gh, "CLUSTER");
        const bh = gh - cell * 1.4;
        cx.fillStyle = "#1c2029"; rr(px + pw / 2 - cell * 0.5, gy0 + cell, cell, bh, 6); cx.fill();
        const c = used > 0.8 ? "#f87171" : used > 0.55 ? "#facc15" : "#5fd068";
        cx.fillStyle = c; rr(px + pw / 2 - cell * 0.5, gy0 + cell + bh * (1 - used), cell, bh * used, 6); cx.fill();
      }
    }
    // Particles and floating text.
    for (const p of parts) { cx.globalAlpha = Math.max(0, 1 - p.t / p.life); cx.fillStyle = p.c; cx.fillRect(ox + p.x * cell - 3, oy + p.y * cell - 3, 6, 6); }
    cx.globalAlpha = 1;
    cx.textAlign = "center"; cx.textBaseline = "middle";
    for (const f of floats) {
      const a = Math.min(1, (f.life - f.t) * 2);
      cx.globalAlpha = Math.max(0, a);
      let size = Math.max(9, cell * f.size);
      cx.font = `700 ${size}px ${f.small ? "Inter" : '"Chakra Petch"'}, sans-serif`;
      // Shrink to fit inside the well.
      const tw = cx.measureText(f.text).width + (f.small ? 16 : 8);
      if (tw > ww - 6) { size = Math.max(8, (size * (ww - 6)) / tw); cx.font = `700 ${size}px ${f.small ? "Inter" : '"Chakra Petch"'}, sans-serif`; }
      const y = oy + (f.y - f.t * 0.6) * cell;
      if (f.small) {
        const w = cx.measureText(f.text).width + 16;
        cx.fillStyle = "rgba(11,12,16,.86)"; rr(ox + ww / 2 - w / 2, y - size * 0.85, w, size * 1.7, 8); cx.fill();
        cx.strokeStyle = f.c; cx.lineWidth = 1; cx.stroke();
      } else { cx.lineWidth = 4; cx.strokeStyle = "rgba(0,0,0,.7)"; cx.strokeText(f.text, ox + ww / 2, y); }
      cx.fillStyle = f.c; cx.fillText(f.text, ox + ww / 2, y);
    }
    cx.globalAlpha = 1;
    if (toast) {
      cx.globalAlpha = Math.min(1, (toast.life - toast.t) * 2);
      cx.fillStyle = "#fff"; cx.font = `700 ${cell * 1.2}px "Chakra Petch", sans-serif`; cx.fillText(toast.a, ox + ww / 2, oy + wh * 0.32);
      cx.fillStyle = "#f472b6"; cx.font = `600 ${cell * 0.5}px Inter, sans-serif`; cx.fillText(toast.b, ox + ww / 2, oy + wh * 0.32 + cell);
      cx.globalAlpha = 1;
    }
    cx.restore();
    $("sScore").textContent = (score || 0).toLocaleString();
    $("sLevel").textContent = level || 1;
    $("sLines").textContent = lines || 0;
  }

  // ---- the loop ----
  let last = performance.now();
  function frame(now) {
    const dt = Math.min(0.05, Math.max(0, (now - last) / 1000));
    last = now; clock += dt;
    if (playing && !over && cur) {
      if (clearing) { clearing.t += dt; if (clearing.t >= 0.28) finishClear(); }
      else {
        const g = softing ? Math.min(R.softDrop, R.gravity[Math.min(level, R.gravity.length) - 1]) : R.gravity[Math.min(level, R.gravity.length) - 1];
        if (grounded()) {
          lockT += dt;
          if (lockT >= R.lockDelay) lock();
        } else {
          fallT += dt;
          while (fallT >= g && !grounded()) { fallT -= g; cur.y++; if (softing) score += R.softPoint; }
          if (grounded()) fallT = 0;
        }
      }
    }
    shake = Math.max(0, shake - dt * 2.5);
    if (dropFx) { dropFx.life -= dt; if (dropFx.life <= 0) dropFx = null; }
    for (const p of parts) { p.t += dt; p.vy += 22 * dt; p.x += p.vx * dt; p.y += p.vy * dt; }
    parts = parts.filter((p) => p.t < p.life);
    for (const f of floats) f.t += dt;
    floats = floats.filter((f) => f.t < f.life);
    if (toast) { toast.t += dt; if (toast.t > toast.life) toast = null; }
    // Held buttons repeat (DAS).
    for (const k of ["left", "right"]) {
      const h = held[k];
      if (h) { h.t += dt; if (h.t > 0.17) { h.r += dt; while (h.r > 0.05) { h.r -= 0.05; move(k === "left" ? -1 : 1); } } }
    }
    draw();
    requestAnimationFrame(frame);
  }

  // ---- controls: buttons, gestures, keys ----
  const held = {};
  function button(id, down, up) {
    const el = $(id);
    let pid = null;
    el.addEventListener("pointerdown", (e) => { e.preventDefault(); pid = e.pointerId; try { el.setPointerCapture(pid); } catch {} el.classList.add("on"); down(); });
    const end = (e) => { if (e.pointerId !== pid) return; pid = null; el.classList.remove("on"); up && up(); };
    el.addEventListener("pointerup", end); el.addEventListener("pointercancel", end); el.addEventListener("lostpointercapture", end);
  }
  button("bLeft", () => { move(-1); held.left = { t: 0, r: 0 }; }, () => { held.left = null; });
  button("bRight", () => { move(1); held.right = { t: 0, r: 0 }; }, () => { held.right = null; });
  button("bRot", () => rotate(1));
  button("bSoft", () => { softing = true; }, () => { softing = false; });
  button("drop", hardDrop);
  button("bHold", hold);
  const releaseAll = () => { held.left = held.right = null; softing = false; };
  addEventListener("blur", releaseAll);
  document.addEventListener("visibilitychange", () => { if (document.hidden) releaseAll(); });
  // Gestures on the well: tap rotates, a horizontal drag moves a column per cell, a fast
  // swipe down hard drops.
  let touch = null;
  $("well").addEventListener("pointerdown", (e) => { touch = { x: e.clientX, y: e.clientY, t: performance.now(), moved: 0, lastX: e.clientX }; });
  $("well").addEventListener("pointermove", (e) => {
    if (!touch) return;
    const dx = e.clientX - touch.lastX;
    if (Math.abs(dx) >= cell * 0.9) { move(dx > 0 ? 1 : -1); touch.lastX = e.clientX; touch.moved++; }
  });
  $("well").addEventListener("pointerup", (e) => {
    if (!touch) return;
    const dy = e.clientY - touch.y, dx = e.clientX - touch.x, dt = performance.now() - touch.t;
    if (dy > 60 && dy > Math.abs(dx) && dt < 400) hardDrop();
    else if (!touch.moved && Math.abs(dx) < 10 && Math.abs(dy) < 10 && dt < 300) rotate(1);
    touch = null;
  });
  addEventListener("keydown", (e) => {
    const k = e.key;
    if (k === "ArrowLeft") move(-1); else if (k === "ArrowRight") move(1); else if (k === "ArrowUp" || k === "x") rotate(1); else if (k === "z") rotate(-1);
    else if (k === "ArrowDown") softing = true; else if (k === " ") hardDrop(); else if (k === "c" || k === "Shift") hold(); else return;
    e.preventDefault();
  });
  addEventListener("keyup", (e) => { if (e.key === "ArrowDown") softing = false; });

  // ---- buttons and title ----
  $("heroPods").innerHTML = ["I", "T", "S", "Z", "L", "J", "O", "I"].map((t) => `<i style="background:${COLORS[t]}"></i>`).join("");
  $("play").onclick = play;
  $("again").onclick = play;
  $("soundBtn").onclick = toggleSound;
  $("soundBtn2").onclick = toggleSound;
  $("boardBtn").onclick = () => { show("title", false); show("menu"); };
  $("menuBtn").onclick = () => show("menu");
  $("resume").onclick = () => { show("menu", false); if (!playing && !over) show("title"); };
  for (const id of ["endCta", "menuCta"]) Arcade.ctas($(id), "sm");
  Arcade.board($("menuBoard"), "/pod-packer", { n: 10, detail: true, empty: "No scores yet. Be the first." });
  grid = rows(); queue = []; level = 1;
  requestAnimationFrame(frame);

  // A test hook for headless bots and screenshots.
  window.podDebug = {
    state: () => ({ playing, over, score, lines, level, pieces, snaps, runID, cur: cur && { t: cur.t, r: cur.r, x: cur.x, y: cur.y }, grid: grid && grid.map((r) => r.map((c) => (c ? 1 : 0))), clearing: !!clearing }),
    // place puts the current piece at rotation r and column x (if it fits) and hard drops it.
    place: (r, x) => {
      if (!playing || clearing || over || !cur) return false;
      for (let i = 0; i < 4 && cur.r !== ((r % 4) + 4) % 4; i++) if (!rotate(1)) break;
      const dir = x > cur.x ? 1 : -1;
      while (cur.x !== x && move(dir));
      hardDrop();
      return true;
    },
    cells, fits: (t, r, x, y) => fits(t, r, x, y),
  };
})();
