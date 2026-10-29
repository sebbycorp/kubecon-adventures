// Original 8-bit sprites in the NES style, drawn from character maps.
// k outline, B body / D tail (player color), H head, W wing, G wing shade, w eye/neck ring,
// Y beak, O feet,
// O hat, S skin, G jacket, R mouth, N gun. "." is transparent.
"use strict";

const NES = {
  sky: "#3cbcfc", skyDeep: "#0078f8", black: "#000000", white: "#fcfcfc",
  grass: "#80d010", grassMid: "#00a800", grassDark: "#005800", green: "#80d010",
  dirt: "#ac7c00", dirtDark: "#503000", orange: "#fc9838", red: "#d82800",
  yellow: "#f8b800", trunk: "#7c3c00", skin: "#fcbcb0", gray: "#bcbcbc",
};

const DUCK_COLORS = {
  green:  { H: "#00a800", B: "#503000", D: "#000000" },  // classic mallard
  blue:   { H: "#0078f8", B: "#0000bc", D: "#000058" },
  red:    { H: "#f83800", B: "#881400", D: "#400000" },
  purple: { H: "#9878f8", B: "#4428bc", D: "#200858" },
  gold:   { H: "#f8b800", B: "#ac7c00", D: "#503000" },
  pink:   { H: "#f878f8", B: "#a80084", D: "#58003c" },
};

const DUCK_FRAMES = {
  up: [
    "....kkk.......................",
    "...kWWWk......................",
    "...kWWWWk.....................",
    "....kWWWWk..........kkkk......",
    "....kGWWWWk........kHHHHk.....",
    ".....kGWWWWk......kHHHHHHk....",
    ".....kGGWWWWk.....kHHHwkHk....",
    "......kGGWWWWk....kHHHHHHkkkk.",
    "......kkGGWWWWk...kHHHHHHYYYYk",
    ".......kkGGWWWWk...kHHHHkkkkk.",
    "kk......kBkGGWWWk.kwwwwk......",
    "kDkk...kBBBkkkkkkkBBBBk.......",
    ".kDDkkkBBBBBBBBBBBBBBBBk......",
    "..kDDBBBBBBBBBBBBBBBBBBk......",
    "...kDBBBBBBBBBBBBBBBBBBk......",
    "....kBBBBBBBBBBBBBBBBBk.......",
    ".....kBBBBBBBBBBBBBBBk........",
    "......kkBBBBBBBBBBkkk.........",
    "........kkkOOkkkkk............",
    "..........kOOOk...............",
    "...........kk.................",
    "..............................",
  ],
  mid: [
    "..............................",
    "..............................",
    "..............................",
    "....................kkkk......",
    "...................kHHHHk.....",
    "..................kHHHHHHk....",
    "..................kHHHwkHk....",
    "..................kHHHHHHkkkk.",
    "..................kHHHHHHYYYYk",
    "...................kHHHHkkkkk.",
    "kk.........kkkkkkkkwwwwk......",
    "kDkk....kkkWWWWWWWWkBBBk......",
    ".kDDkkkkWWWWWWWWWWWWkBBBk.....",
    "..kDDBBkGGWWWWWWWWWkBBBBk.....",
    "...kDBBBkGGGGGWWWkkBBBBBk.....",
    "....kBBBBkkkkkkkkBBBBBBk......",
    ".....kBBBBBBBBBBBBBBBBk.......",
    "......kkBBBBBBBBBBBkkk........",
    "........kkkOOkkkkkk...........",
    "..........kOOOk...............",
    "...........kk.................",
    "..............................",
  ],
  down: [
    "..............................",
    "..............................",
    "..............................",
    "....................kkkk......",
    "...................kHHHHk.....",
    "..................kHHHHHHk....",
    "..................kHHHwkHk....",
    "..................kHHHHHHkkkk.",
    "..................kHHHHHHYYYYk",
    "...................kHHHHkkkkk.",
    "kk.........kkkkkkkkwwwwk......",
    "kDkk....kkkBBBBBBBkBBBBk......",
    ".kDDkkkkBBBBBBBBBBBBBBBBk.....",
    "..kDDBBBBBkkkkkkkBBBBBBBk.....",
    "...kDBBBkWWWWWWWWkBBBBBk......",
    "....kBBkWWWWWWWWkBBBBBk.......",
    ".....kkWWWWWWWGkBBBBBk........",
    "......kWWWWWWGGkkkkkk.........",
    ".....kWWWWWGGk..kOOk..........",
    ".....kWWWGGk.....kk...........",
    "......kkkkk...................",
    "..............................",
  ],
  shot: [
    "..kk....................kk....",
    ".kWWk..................kWWk...",
    ".kWWWk.......kkkk.....kWWWk...",
    "..kWWWk.....kHHHHk...kWWWk....",
    "..kGWWWk...kHwkkwHk.kWWWGk....",
    "...kGWWWk..kHHHHHHk.kWWWGk....",
    "...kGGWWWk.kHHYYHHkkWWWGk.....",
    "....kGGWWWkkHYYYYHkWWWGGk.....",
    ".....kGGWWWkkwwwwkWWWGGk......",
    "......kkGGWkBBBBBkWGGkk.......",
    "........kkkBBBBBBBkkk.........",
    "..........kBBBBBBBk...........",
    "..........kDBBBBBDk...........",
    "...........kDBBBDk............",
    "............kOkOk.............",
    "............kOkOk.............",
    "............kk.kk.............",
    "..............................",
    "..............................",
    "..............................",
    "..............................",
    "..............................",
  ],
};

const HUNTER = [
  ".....kkkkk......",
  "....kOOOOOk.....",
  "...kOOOOOOOk....",
  "..kkkkkkkkkkk...",
  "....kSSSSSk.....",
  "....kSkSkSk.....",
  "....kSSSSSk.....",
  "....kSRRRSk.....",
  ".....kkkkk......",
  "...kGGGGGGGk....",
  "..kGGGGGGGGGk...",
  ".kSkGGGGGGGkSk..",
  ".kSkGGGGGGGkSk..",
  "..kkGGGGGGGkk...",
  "....kGGGGGk.....",
];

const HUNTER_COLORS = { O: NES.orange, S: NES.skin, G: "#00783c", R: NES.red };

const EGG = [
  "...kkkk...",
  "..kWWWWk..",
  ".kWWWWWWk.",
  ".kWBWWWWk.",
  "kWWWWWBWWk",
  "kWWWWWWWWk",
  "kWBWWWWWWk",
  ".kWWWWWWk.",
  "..kkkkkk..",
];

const FIXED_COLORS = { k: "#000000", W: "#fcfcfc", w: "#fcfcfc", G: "#bcbcbc", E: "#fcfcfc", Y: "#f8b800", O: "#f87858" };

const spriteCache = new Map();

// sprite renders a character map into an offscreen canvas (cached).
function sprite(map, palette, key) {
  if (spriteCache.has(key)) return spriteCache.get(key);
  const c = document.createElement("canvas");
  c.width = map[0].length;
  c.height = map.length;
  const g = c.getContext("2d");
  map.forEach((row, y) => {
    [...row].forEach((ch, x) => {
      if (ch === ".") return;
      g.fillStyle = palette[ch] || FIXED_COLORS[ch] || "#ff00ff";
      g.fillRect(x, y, 1, 1);
    });
  });
  spriteCache.set(key, c);
  return c;
}

function duckSprite(frame, color) {
  const pal = DUCK_COLORS[color] || DUCK_COLORS.green;
  return sprite(DUCK_FRAMES[frame], pal, "duck:" + frame + ":" + color);
}

function hunterSprite() { return sprite(HUNTER, HUNTER_COLORS, "hunter"); }
function eggSprite(color) { return sprite(EGG, DUCK_COLORS[color] || DUCK_COLORS.green, "egg:" + color); }

// drawSprite draws img at (x, y) with optional horizontal/vertical flip, integer-aligned.
function drawSprite(g, img, x, y, flipX = false, flipY = false, scale = 1) {
  x = Math.round(x); y = Math.round(y);
  g.save();
  g.translate(x + (flipX ? img.width * scale : 0), y + (flipY ? img.height * scale : 0));
  g.scale(flipX ? -scale : scale, flipY ? -scale : scale);
  g.drawImage(img, 0, 0);
  g.restore();
}

const FLAP = ["up", "mid", "down", "mid"];

// Tiny chiptune-style sound effects (WebAudio, synthesized; no samples).
const Sfx = {
  ctx: null,
  enable() { if (!this.ctx) this.ctx = new (window.AudioContext || window.webkitAudioContext)(); return this.ctx.resume(); },
  tone(freq, dur, type = "square", when = 0, vol = 0.08) {
    if (!this.ctx) return;
    const t = this.ctx.currentTime + when, o = this.ctx.createOscillator(), v = this.ctx.createGain();
    o.type = type; o.frequency.setValueAtTime(freq, t);
    v.gain.setValueAtTime(vol, t); v.gain.exponentialRampToValueAtTime(0.0001, t + dur);
    o.connect(v).connect(this.ctx.destination); o.start(t); o.stop(t + dur);
  },
  noise(dur, vol = 0.2) {
    if (!this.ctx) return;
    const n = this.ctx.sampleRate * dur, b = this.ctx.createBuffer(1, n, this.ctx.sampleRate), d = b.getChannelData(0);
    for (let i = 0; i < n; i++) d[i] = (Math.random() * 2 - 1) * (1 - i / n);
    const s = this.ctx.createBufferSource(), v = this.ctx.createGain();
    v.gain.value = vol; s.buffer = b; s.connect(v).connect(this.ctx.destination); s.start();
  },
  shot() { this.noise(0.25); },
  quack() { this.tone(520, 0.08); this.tone(440, 0.1, "square", 0.09); },
  fall() { for (let i = 0; i < 8; i++) this.tone(900 - i * 90, 0.07, "triangle", i * 0.07, 0.06); },
  revive() { [523, 659, 784, 1047].forEach((f, i) => this.tone(f, 0.1, "square", i * 0.08, 0.06)); },
  hatch() { [392, 523].forEach((f, i) => this.tone(f, 0.08, "square", i * 0.08, 0.06)); },
  bark() { [0, 0.18].forEach((w) => { this.tone(330, 0.06, "square", w, 0.08); this.tone(220, 0.08, "square", w + 0.05, 0.08); }); },
  laugh() { for (let i = 0; i < 6; i++) this.tone(i % 2 ? 392 : 523, 0.09, "square", i * 0.12, 0.06); },
  gotcha() { [523, 659, 784, 659, 1047].forEach((f, i) => this.tone(f, 0.09, "square", i * 0.09, 0.06)); },
  walk() { this.tone(110, 0.03, "triangle", 0, 0.05); },
};

function fmtMs(ms) {
  if (ms == null) return "--";
  return ms >= 1000 ? (ms / 1000).toFixed(1) + "S" : ms + "MS";
}

// connectState subscribes to the game's SSE stream and reconnects on errors.
function connectState(onState) {
  let es;
  const open = () => {
    es = new EventSource("/duck-hunt/api/events");
    es.onmessage = (m) => onState(JSON.parse(m.data));
    es.onerror = () => { es.close(); setTimeout(open, 2000); };
  };
  open();
}
