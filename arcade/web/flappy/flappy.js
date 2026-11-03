// Flappy Substrate's phone game. The swim runs here, on a fixed 120 Hz step with the
// course the server serves (/flappy/api/rules), so the server can check every score
// against the same physics. PLAY asks the server to wake this player's own octopus agent
// (a real Substrate resume); a crash reports the gaps and bubbles and the server
// snapshots the agent (a real suspend). Play never waits on Substrate.
"use strict";
(async () => {
  const $ = (id) => document.getElementById(id);
  const show = (id, on = true) => $(id).classList.toggle("hidden", !on);
  const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
  const post = async (url, body) => {
    const r = await fetch(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body || {}) });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(j.error || r.statusText);
    return j;
  };

  // Players join the arcade once; a phone without a player gets a name on the home page.
  const me = await fetch("/api/me").catch(() => null);
  if (me && me.status === 401) { location.replace("/?next=/flappy"); return; }

  let R = await (await fetch("/flappy/api/rules")).json().catch(() => null);
  R = R || { height: 640, surface: 34, floor: 590, gravity: 1500, flap: 430, maxFall: 620, speed0: 165, accel: 2.4, maxSpeed: 300,
    firstGap: 420, spacing: 250, pillarW: 72, gap0: 205, gapMin: 148, gapShrink: 1.2, radius: 21, countdown: 3, bubbleEvery: 2 };

  // ---- sound and vibration ----
  let soundOn = true, ac = null;
  try { soundOn = localStorage.getItem("flappy-sound") !== "off"; } catch {}
  const sfx = (kind) => {
    if (!soundOn) return;
    try { ac = ac || new (window.AudioContext || window.webkitAudioContext)(); } catch { return; }
    const t = ac.currentTime, o = ac.createOscillator(), g = ac.createGain();
    o.connect(g); g.connect(ac.destination);
    const set = {
      flap: ["sine", 520, 820, 0.09, 0.12], point: ["triangle", 880, 1320, 0.12, 0.10], bubble: ["sine", 1200, 1800, 0.10, 0.10],
      crash: ["sawtooth", 220, 60, 0.35, 0.16], snap: ["triangle", 660, 990, 0.25, 0.10], wake: ["sine", 300, 900, 0.3, 0.10], tick: ["square", 440, 440, 0.06, 0.05],
    }[kind];
    if (!set) return;
    const [type, f0, f1, dur, vol] = set;
    o.type = type;
    o.frequency.setValueAtTime(f0, t);
    o.frequency.exponentialRampToValueAtTime(f1, t + dur);
    g.gain.setValueAtTime(vol, t);
    g.gain.exponentialRampToValueAtTime(0.001, t + dur);
    o.start(t); o.stop(t + dur + 0.02);
  };
  const buzz = (ms) => { try { if (soundOn && navigator.vibrate) navigator.vibrate(ms); } catch {} };
  const soundLabel = () => { for (const id of ["soundBtn", "soundBtn2"]) $(id).textContent = soundOn ? "SOUND: ON" : "SOUND: OFF"; };
  const toggleSound = () => { soundOn = !soundOn; try { localStorage.setItem("flappy-sound", soundOn ? "on" : "off"); } catch {} soundLabel(); };
  soundLabel();

  // ---- the sea ----
  const cv = $("sea"), cx = cv.getContext("2d");
  const octo = new Image();
  octo.src = document.querySelector('link[rel="icon"]').href.replace("octo-128", "octo-256");
  let W = 360, H = R.height, k = 1, dpr = 1;
  function fit() {
    dpr = Math.min(2, devicePixelRatio || 1);
    cv.width = Math.round(innerWidth * dpr);
    cv.height = Math.round(innerHeight * dpr);
    k = cv.height / H; // world units to pixels: the sea is always 640 tall
    W = cv.width / k;
  }
  addEventListener("resize", fit);
  fit();
  const octoX = () => Math.min(W * 0.3, 160);

  // Parallax scenery, generated once.
  const rnd = (a, b) => a + Math.random() * (b - a);
  const weeds = Array.from({ length: 26 }, (_, i) => ({ x: i * 70 + rnd(0, 50), h: rnd(60, 150), far: Math.random() < 0.5, ph: rnd(0, 6) }));
  const fish = Array.from({ length: 7 }, () => ({ x: rnd(0, 1200), y: rnd(120, 520), s: rnd(10, 22), v: rnd(10, 30), ph: rnd(0, 6) }));
  const motes = Array.from({ length: 40 }, () => ({ x: rnd(0, 1600), y: rnd(0, 640), r: rnd(1, 3.5), v: rnd(10, 30) }));

  // ---- the run ----
  const STATE = { title: 0, waking: 1, count: 2, swim: 3, sink: 4, end: 5 };
  let state = STATE.title, runID = "", agentOn = false;
  let y, vy, dist, speed, t, gaps, bubbles, nextPillar, pillars, bubs, squash, shake, crashAt, inks, flash, countT, pops;
  function reset() {
    y = H * 0.45; vy = 0; dist = 0; speed = R.speed0; t = 0; gaps = 0; bubbles = 0; nextPillar = 0;
    pillars = []; bubs = []; squash = 0; shake = 0; crashAt = 0; inks = []; flash = 0; pops = [];
    $("score").textContent = "0";
  }
  reset();

  // Pillar i sits at course distance firstGap + i*spacing. Some are themed barriers.
  const THEMES = ["coral", "kelp", "coral", "oom", "kelp", "rate", "coral", "kelp"];
  function addPillar(i) {
    const gh = Math.max(R.gapMin, R.gap0 - i * R.gapShrink);
    const lo = R.surface + gh / 2 + 36, hi = R.floor - gh / 2 - 36;
    const prev = pillars.length ? pillars[pillars.length - 1].cy : (lo + hi) / 2;
    const cy = Math.max(lo, Math.min(hi, prev + rnd(-1, 1) * Math.min(170, 70 + i * 4)));
    const p = { i, x: R.firstGap + i * R.spacing, cy, gh, theme: i < 3 ? "coral" : THEMES[(i * 7 + 3) % THEMES.length], passed: false };
    pillars.push(p);
    // A bubble floats between this pair and the next, about one gap in bubbleEvery.
    if (i % R.bubbleEvery === 1) bubs.push({ x: p.x + R.spacing / 2, y: Math.max(R.surface + 40, Math.min(R.floor - 40, cy + rnd(-60, 60))), got: false, ph: rnd(0, 6) });
  }

  function flap() {
    if (state === STATE.swim) {
      vy = -R.flap;
      squash = 1;
      sfx("flap");
      buzz(8);
      for (let i = 0; i < 4; i++) pops.push({ x: octoX() - 18, y: y + 14, vx: rnd(-60, -20), vy: rnd(-30, 30), r: rnd(2, 4), life: 0.6 });
    }
  }
  addEventListener("pointerdown", (e) => {
    if (e.target.closest(".card, button, a")) return;
    e.preventDefault();
    flap();
  }, { passive: false });
  addEventListener("keydown", (e) => { if (e.code === "Space" || e.code === "ArrowUp") { e.preventDefault(); flap(); } });

  // ---- server calls ----
  function agentChip(on, text) {
    agentOn = on;
    $("agent").classList.toggle("on", on);
    $("agentTxt").textContent = text;
  }
  function toast(html, ms = 1600) {
    $("toast").innerHTML = html;
    show("toast");
    clearTimeout(toast.t);
    if (ms) toast.t = setTimeout(() => show("toast", false), ms);
  }

  let wokeMs = 0; // this run's real wake-up time, for the share button
  async function play() {
    for (const id of ["title", "end", "menu"]) show(id, false);
    reset();
    show("score"); show("agent"); show("menuBtn");
    state = STATE.waking;
    agentChip(false, "waking your agent…");
    toast(`waking your agent<small>a real Agent Substrate resume</small>`, 0);
    let s = null;
    try { s = await post("/flappy/api/start"); } catch (e) { toast(esc(e.message), 2200); state = STATE.title; show("title"); return; }
    runID = s.run;
    wokeMs = s.agent === "awake" ? s.wokeMs : 0;
    if (s.rules) R = s.rules;
    if (s.agent === "awake") {
      agentChip(true, `agent awake · woke in ${s.wokeMs} ms`);
      toast(`⚡ woke in ${s.wokeMs} ms<small>your octopus is running in its own gVisor sandbox</small>`, 1400);
      sfx("wake");
    } else {
      agentChip(false, `swimming without an agent · ${s.reason || "no free sandbox"}`);
      toast(`🫧 ${esc(s.reason || "no free sandbox")}<small>you can still swim; your score still counts</small>`, 1800);
    }
    state = STATE.count;
    countT = R.countdown;
  }

  async function crash(why) {
    if (state !== STATE.swim) return;
    state = STATE.sink;
    crashAt = performance.now();
    shake = 1; flash = 1;
    sfx("crash"); buzz([40, 30, 60]);
    for (let i = 0; i < 26; i++) inks.push({ x: octoX(), y, vx: rnd(-140, 140), vy: rnd(-140, 100), r: rnd(6, 18), life: rnd(0.6, 1.3) });
    $("endWhy").textContent = why;
    $("endScore").textContent = String(gaps + 5 * bubbles);
    $("endDetail").textContent = `${gaps} gap${gaps === 1 ? "" : "s"} · ${bubbles} bubble${bubbles === 1 ? "" : "s"}`;
    $("endSnap").innerHTML = agentOn ? `📦 <span>snapshotting your agent…</span>` : `🫧 <span>no agent this run</span>`;
    $("endRank").textContent = ""; $("endBeat").textContent = ""; $("endBoard").innerHTML = "";
    let res = null;
    try { res = await post("/flappy/api/crash", { run: runID, gaps, bubbles }); } catch (e) { res = { error: e.message }; }
    // Let the octopus finish sinking into the chest first.
    const wait = Math.max(0, 1300 - (performance.now() - crashAt));
    setTimeout(() => showEnd(res), wait);
  }

  function showEnd(res) {
    state = STATE.end;
    show("menuBtn", false);
    if (res && !res.error) {
      if (res.agent && res.asleep) {
        $("endSnap").innerHTML = `📦 <span>snapshotted in <b>${res.snapMs} ms</b> · your agent sleeps in Snapshot Storage</span>`;
        agentChip(false, `agent asleep · snapshotted in ${res.snapMs} ms`);
        sfx("snap");
      } else if (res.agent) {
        $("endSnap").innerHTML = `📦 <span>the snapshot is still settling</span>`;
      }
      if (!res.counted) $("endDetail").textContent = "this score didn't pass the server's check";
      $("endRank").textContent = res.rank ? (res.rank === 1 ? "🏆 #1 ON THE BIG SCREEN" : `#${res.rank} today`) + (res.newBest ? " · new best!" : "") : "";
      $("endBeat").innerHTML = res.beat ? `Beat <b>${esc(res.beat.nick)}</b> by ${res.beat.by} to reach #${res.beat.rank}` : res.rank === 1 ? "Nobody's ahead of you. Defend it." : "";
      $("endBoard").innerHTML = (res.top || []).map((r, i) => `<div class="ab-row${i === 0 ? " ab-top" : ""}"><span class="ab-rank">${i + 1}</span><span class="ab-dot" style="background:${dot(r.color)}"></span><span class="ab-who"><b>${esc(r.nick)}</b><small>${esc(r.detail || "")}</small></span><span class="ab-score">${r.score}</span></div>`).join("") || `<div class="ab-empty">No scores yet.</div>`;
    } else {
      $("endSnap").innerHTML = `⚠️ <span>${esc((res && res.error) || "couldn't reach the server")}</span>`;
    }
    Arcade.share($("endShare"), { game: "Flappy Substrate", score: gaps + 5 * bubbles, path: "/flappy",
      facts: { gaps, bubbles, snapshotMs: res && res.agent && res.asleep ? res.snapMs : 0, wakeMs: wokeMs } });
    show("end");
  }
  const dot = (c) => ({ green: "#5fd068", blue: "#60a5fa", red: "#f87171", purple: "#a78bfa", gold: "#fbbf24", pink: "#f472b6" })[c] || "#8a8f94";

  // ---- simulation (fixed step) ----
  const STEP = 1 / 120;
  function step(dt) {
    t += dt;
    for (const f of fish) f.x -= f.v * dt;
    for (const m of motes) { m.y -= m.v * dt; if (m.y < 0) { m.y = H; m.x = rnd(0, W + 400) + dist * 0.3; } }
    if (state === STATE.count) {
      const before = Math.ceil(countT);
      countT -= dt;
      if (Math.ceil(countT) !== before && countT > 0) sfx("tick");
      y = H * 0.45 + Math.sin(t * 3) * 8;
      if (countT <= 0) { state = STATE.swim; vy = -R.flap * 0.6; }
      return;
    }
    if (state === STATE.title || state === STATE.waking || state === STATE.end) { y = H * 0.45 + Math.sin(t * 2) * 10; return; }
    if (state === STATE.sink) {
      vy = Math.min(vy + R.gravity * 0.35 * dt, 260);
      y = Math.min(R.floor - 40, y + vy * dt);
      return;
    }
    // Swimming.
    speed = Math.min(R.maxSpeed, speed + R.accel * dt);
    dist += speed * dt;
    vy = Math.min(R.maxFall, vy + R.gravity * dt);
    y += vy * dt;
    while (R.firstGap + nextPillar * R.spacing < dist + W + 200) addPillar(nextPillar++);
    while (pillars.length && pillars[0].x < dist - 300) pillars.shift();
    while (bubs.length && bubs[0].x < dist - 300) bubs.shift();
    const ox = octoX(), r = R.radius;
    if (y - r < R.surface) return crash("You broke the surface");
    if (y + r > R.floor) return crash("You hit the sea floor");
    for (const p of pillars) {
      const left = p.x - dist + ox - R.pillarW / 2, right = left + R.pillarW;
      const top = p.cy - p.gh / 2, bot = p.cy + p.gh / 2;
      // The octopus's circle against the two pillar rectangles.
      if (hits(ox, y, r, left, -1e4, right, top) || hits(ox, y, r, left, bot, right, 1e4)) {
        return crash(p.theme === "oom" ? "OOM! You hit the memory wall" : p.theme === "rate" ? "429! Rate limited" : "You hit the coral");
      }
      if (!p.passed && dist > p.x + R.pillarW / 2) {
        p.passed = true;
        gaps++;
        $("score").textContent = String(gaps + 5 * bubbles);
        sfx("point");
      }
    }
    for (const b of bubs) {
      if (b.got) continue;
      const bx = b.x - dist + ox;
      if (Math.hypot(bx - ox, b.y - y) < r + 16) {
        b.got = true;
        bubbles++;
        $("score").textContent = String(gaps + 5 * bubbles);
        sfx("bubble"); buzz(12);
        pops.push({ x: bx, y: b.y, text: "+5", life: 0.8, vy: -50 });
      }
    }
  }

  // hits reports whether a circle at (cx, cy) of radius r touches the rectangle x0..x1, y0..y1.
  function hits(cx0, cy0, r, x0, y0, x1, y1) {
    const nx = Math.max(x0, Math.min(cx0, x1)), ny = Math.max(y0, Math.min(cy0, y1));
    return Math.hypot(cx0 - nx, cy0 - ny) < r;
  }

  // ---- drawing ----
  function draw() {
    const now = performance.now() / 1000;
    cx.setTransform(1, 0, 0, 1, 0, 0);
    // Water, darker with depth.
    const g = cx.createLinearGradient(0, 0, 0, cv.height);
    g.addColorStop(0, "#0b6d93"); g.addColorStop(0.45, "#064b6d"); g.addColorStop(1, "#021d30");
    cx.fillStyle = g;
    cx.fillRect(0, 0, cv.width, cv.height);
    const sx = shake > 0 ? rnd(-1, 1) * 8 * shake : 0, sy = shake > 0 ? rnd(-1, 1) * 8 * shake : 0;
    cx.setTransform(k, 0, 0, k, sx * dpr, sy * dpr);
    const ox = octoX();

    // Light rays from the surface.
    cx.save();
    cx.globalCompositeOperation = "lighter";
    for (let i = 0; i < 6; i++) {
      const x = ((i * 190 - dist * 0.08 + now * 6) % (W + 300)) - 100;
      const a = 0.05 + 0.03 * Math.sin(now * 0.7 + i);
      const lg = cx.createLinearGradient(0, 0, 0, H * 0.9);
      lg.addColorStop(0, `rgba(180,240,255,${a})`); lg.addColorStop(1, "rgba(180,240,255,0)");
      cx.fillStyle = lg;
      cx.beginPath(); cx.moveTo(x, 0); cx.lineTo(x + 70, 0); cx.lineTo(x + 170, H); cx.lineTo(x + 40, H); cx.closePath(); cx.fill();
    }
    cx.restore();

    // Far fish silhouettes.
    cx.fillStyle = "rgba(2,30,48,.45)";
    for (const f of fish) {
      const x = ((f.x - dist * 0.25) % (W + 300) + W + 300) % (W + 300) - 100, yy = f.y + Math.sin(now + f.ph) * 6;
      cx.beginPath(); cx.ellipse(x, yy, f.s, f.s * 0.45, 0, 0, Math.PI * 2); cx.fill();
      cx.beginPath(); cx.moveTo(x + f.s * 0.8, yy); cx.lineTo(x + f.s * 1.5, yy - f.s * 0.45); cx.lineTo(x + f.s * 1.5, yy + f.s * 0.45); cx.fill();
    }
    // Seaweed, two parallax layers.
    for (const layer of [true, false]) {
      cx.strokeStyle = layer ? "rgba(10,70,60,.55)" : "rgba(30,140,90,.7)";
      cx.lineWidth = layer ? 6 : 9;
      cx.lineCap = "round";
      for (const w of weeds) {
        if (w.far !== layer) continue;
        const span = 26 * 70;
        const x = (((w.x - dist * (layer ? 0.35 : 0.6)) % span) + span) % span - 40;
        if (x > W + 40) continue;
        cx.beginPath(); cx.moveTo(x, R.floor + 30);
        for (let s = 1; s <= 6; s++) cx.lineTo(x + Math.sin(now * 1.4 + w.ph + s * 0.8) * s * 2.2, R.floor + 30 - (w.h * s) / 6);
        cx.stroke();
      }
    }
    // Drifting motes.
    cx.fillStyle = "rgba(220,250,255,.35)";
    for (const m of motes) {
      const x = ((m.x - dist * 0.3) % (W + 200) + W + 200) % (W + 200);
      cx.beginPath(); cx.arc(x, m.y, m.r, 0, Math.PI * 2); cx.fill();
    }

    // Pillars.
    for (const p of pillars) {
      const left = p.x - dist + ox - R.pillarW / 2, top = p.cy - p.gh / 2, bot = p.cy + p.gh / 2;
      if (left > W + 10 || left + R.pillarW < -10) continue;
      drawPillar(p, left, R.surface - 40, top, true, now);
      drawPillar(p, left, bot, R.floor + 40, false, now);
    }
    // Bubbles to collect.
    for (const b of bubs) {
      if (b.got) continue;
      const x = b.x - dist + ox, yy = b.y + Math.sin(now * 2.4 + b.ph) * 6;
      if (x < -30 || x > W + 30) continue;
      const bg = cx.createRadialGradient(x - 5, yy - 5, 2, x, yy, 15);
      bg.addColorStop(0, "rgba(255,255,255,.95)"); bg.addColorStop(0.35, "rgba(160,240,255,.5)"); bg.addColorStop(1, "rgba(90,200,255,.15)");
      cx.fillStyle = bg;
      cx.beginPath(); cx.arc(x, yy, 14, 0, Math.PI * 2); cx.fill();
      cx.strokeStyle = "rgba(220,250,255,.9)"; cx.lineWidth = 1.5; cx.stroke();
    }

    // The sea floor, and Snapshot Storage when the octopus sinks.
    cx.fillStyle = "#c9a76a";
    cx.beginPath(); cx.moveTo(0, R.floor + 6);
    for (let x = 0; x <= W + 20; x += 20) cx.lineTo(x, R.floor + 6 + Math.sin((x + dist) * 0.03) * 4);
    cx.lineTo(W + 20, H); cx.lineTo(0, H); cx.closePath(); cx.fill();
    cx.fillStyle = "#a88850";
    cx.fillRect(0, R.floor + 26, W + 20, H);
    if (state === STATE.sink || state === STATE.end) drawChest(ox, R.floor - 6, now);
    // The surface.
    cx.fillStyle = "rgba(200,245,255,.22)";
    cx.beginPath(); cx.moveTo(0, 0);
    for (let x = 0; x <= W + 20; x += 16) cx.lineTo(x, R.surface + Math.sin(x * 0.05 + now * 2) * 4);
    cx.lineTo(W + 20, 0); cx.closePath(); cx.fill();

    // Ink and flap bubbles.
    for (const p of inks) {
      cx.fillStyle = `rgba(20,10,40,${Math.max(0, p.life) * 0.7})`;
      cx.beginPath(); cx.arc(p.x, p.y, p.r, 0, Math.PI * 2); cx.fill();
    }
    for (const p of pops) {
      if (p.text) {
        cx.fillStyle = `rgba(255,236,120,${Math.max(0, p.life)})`;
        cx.font = "700 22px Fredoka, sans-serif"; cx.textAlign = "center";
        cx.fillText(p.text, p.x, p.y);
      } else {
        cx.strokeStyle = `rgba(220,250,255,${Math.max(0, p.life)})`; cx.lineWidth = 1.2;
        cx.beginPath(); cx.arc(p.x, p.y, p.r, 0, Math.PI * 2); cx.stroke();
      }
    }

    // The octopus: tilted by its speed, squashed on a flap, bobbing when idle.
    if (octo.complete && octo.naturalWidth) {
      const w = 82, h = w; // the image is square with the octopus in the middle
      const tilt = state === STATE.swim || state === STATE.sink ? Math.max(-0.45, Math.min(0.9, vy / 700)) : Math.sin(now * 2) * 0.08;
      const sq = 1 + squash * 0.18;
      cx.save();
      cx.translate(ox, y);
      cx.rotate(tilt);
      cx.scale(1 / sq + 0.06 * squash, sq);
      if (state === STATE.sink || state === STATE.end) {
        // It shrinks into the chest as it settles: snapshotted.
        const f = Math.min(1, (performance.now() - crashAt) / 1600);
        cx.scale(1 - 0.55 * f, 1 - 0.55 * f);
        cx.globalAlpha = 1 - 0.6 * f;
      }
      cx.drawImage(octo, -w / 2, -h / 2, w, h);
      cx.restore();
    }
    // Countdown.
    if (state === STATE.count) {
      cx.fillStyle = "#fff"; cx.textAlign = "center";
      cx.font = "700 110px Fredoka, sans-serif";
      cx.fillText(String(Math.max(1, Math.ceil(countT))), W / 2, H * 0.36);
      cx.font = "600 20px Inter, sans-serif"; cx.fillStyle = "#bfefff";
      cx.fillText("tap to swim up", W / 2, H * 0.36 + 40);
    }
    // Crash flash.
    if (flash > 0) { cx.fillStyle = `rgba(255,255,255,${flash * 0.6})`; cx.fillRect(-20, -20, W + 40, H + 40); }
  }

  function drawPillar(p, x, y0, y1, top, now) {
    const w = R.pillarW, h = y1 - y0;
    if (h <= 0) return;
    const col = { coral: ["#ff7a8a", "#d94b6a", "#ffb3bd"], kelp: ["#3fbf72", "#1f8a4c", "#8ff0b0"], oom: ["#ff5b5b", "#a11f2f", "#ffd0d0"], rate: ["#ffb02e", "#b86b00", "#ffe1a1"] }[p.theme];
    cx.fillStyle = col[1];
    roundRect(x, y0, w, h, 14); cx.fill();
    cx.fillStyle = col[0];
    roundRect(x + 5, y0, w - 10, h, 12); cx.fill();
    if (p.theme === "coral") {
      cx.fillStyle = col[2];
      for (let yy = y0 + 18; yy < y1 - 10; yy += 34) { cx.beginPath(); cx.arc(x + 18 + ((yy * 7) % 30), yy, 5, 0, Math.PI * 2); cx.fill(); }
    } else if (p.theme === "kelp") {
      cx.strokeStyle = col[2]; cx.lineWidth = 3;
      for (let yy = y0 + 20; yy < y1 - 10; yy += 26) { cx.beginPath(); cx.moveTo(x + w / 2, yy); cx.quadraticCurveTo(x + w / 2 + Math.sin(now * 2 + yy) * 14, yy + 10, x + w / 2, yy + 20); cx.stroke(); }
    } else {
      // Hazard stripes and a label: the theme's barrier.
      cx.save(); roundRect(x + 5, y0, w - 10, h, 12); cx.clip();
      cx.fillStyle = "rgba(0,0,0,.28)";
      for (let yy = y0 - w; yy < y1 + w; yy += 28) { cx.beginPath(); cx.moveTo(x, yy); cx.lineTo(x + w, yy + w * 0.6); cx.lineTo(x + w, yy + w * 0.6 + 12); cx.lineTo(x, yy + 12); cx.fill(); }
      cx.restore();
      const ly = top ? y1 - 26 : y0 + 26;
      cx.fillStyle = "#1b0b10"; roundRect(x + 6, ly - 14, w - 12, 28, 8); cx.fill();
      cx.fillStyle = col[2]; cx.font = "700 17px Fredoka, sans-serif"; cx.textAlign = "center"; cx.textBaseline = "middle";
      cx.fillText(p.theme === "oom" ? "OOM" : "429", x + w / 2, ly + 1);
      cx.textBaseline = "alphabetic";
    }
    // A cap on the gap side.
    cx.fillStyle = col[1];
    roundRect(x - 6, top ? y1 - 18 : y0, w + 12, 18, 9); cx.fill();
  }
  function drawChest(x, y, now) {
    cx.save();
    cx.translate(x, y);
    cx.fillStyle = "#6b3f1d"; roundRect(-46, -38, 92, 44, 8); cx.fill();
    cx.fillStyle = "#8b5527"; roundRect(-46, -52, 92, 20, 10); cx.fill();
    cx.fillStyle = "#ffd23d"; cx.fillRect(-6, -40, 12, 14);
    cx.strokeStyle = "#ffd23d"; cx.lineWidth = 3; cx.strokeRect(-44, -36, 88, 40);
    cx.fillStyle = `rgba(255,226,110,${0.75 + 0.25 * Math.sin(now * 4)})`;
    cx.font = "700 15px Fredoka, sans-serif"; cx.textAlign = "center";
    cx.lineWidth = 4; cx.strokeStyle = "rgba(2,26,42,.8)";
    cx.strokeText("SNAPSHOT STORAGE", 0, -62); cx.fillText("SNAPSHOT STORAGE", 0, -62);
    cx.restore();
  }
  function roundRect(x, y, w, h, r) {
    r = Math.min(r, w / 2, Math.abs(h) / 2);
    cx.beginPath();
    cx.moveTo(x + r, y); cx.arcTo(x + w, y, x + w, y + h, r); cx.arcTo(x + w, y + h, x, y + h, r);
    cx.arcTo(x, y + h, x, y, r); cx.arcTo(x, y, x + w, y, r); cx.closePath();
  }

  let last = performance.now(), acc = 0;
  function frame(nowMs) {
    const dt = Math.min(0.1, Math.max(0, (nowMs - last) / 1000));
    last = nowMs;
    acc += dt;
    while (acc >= STEP) { step(STEP); acc -= STEP; }
    squash = Math.max(0, squash - dt * 5);
    shake = Math.max(0, shake - dt * 2.5);
    flash = Math.max(0, flash - dt * 3);
    for (const p of inks) { p.x += p.vx * dt; p.y += p.vy * dt; p.vx *= 0.96; p.vy *= 0.96; p.r += dt * 10; p.life -= dt; }
    inks = inks.filter((p) => p.life > 0);
    for (const p of pops) { p.x += (p.vx || 0) * dt; p.y += (p.vy || -40) * dt; p.life -= dt; }
    pops = pops.filter((p) => p.life > 0);
    draw();
    requestAnimationFrame(frame);
  }
  requestAnimationFrame(frame);

  // ---- buttons ----
  $("play").onclick = play;
  $("again").onclick = play;
  $("soundBtn").onclick = toggleSound;
  $("soundBtn2").onclick = toggleSound;
  $("boardBtn").onclick = () => { show("title", false); show("menu"); };
  $("menuBtn").onclick = () => show("menu");
  $("resume").onclick = () => { show("menu", false); if (state === STATE.title) show("title"); };
  for (const id of ["endCta", "menuCta"]) Arcade.ctas($(id), "sm");
  Arcade.board($("menuBoard"), "/flappy", { n: 10, detail: true, empty: "No scores yet. Be the first." });
  // A test hook for headless screenshots: window.flappyDebug.state() etc.
  window.flappyDebug = {
    state: () => {
      const next = pillars.find((p) => p.x + R.pillarW / 2 + R.radius > dist);
      return { state, gaps, bubbles, y, vy, dist, runID, agentOn, nextCy: next ? next.cy : H * 0.45, nextX: next ? next.x - dist : 999 };
    },
    flap, crash,
  };
})();
