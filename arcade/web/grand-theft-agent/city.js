// Grand Theft Agent: the Three.js city, shared by the phone page and the big screen.
//
// startCity(canvas, opts) builds the scene once from the city map (GET
// /grand-theft-agent/api/city) and returns {apply(snap), draw(), resize(), stats()}.
// apply takes each server snapshot; draw renders one frame. All state (frozen, booked,
// HP, positions) comes from entity fields; events only add floaters and tracers, so a
// dropped snapshot never leaves the picture wrong.
//
// The look: a stylised night city. town.js builds the static city (towers, mid-rises and
// shops in natural colours, warm and cool windows, rooftops, a few neon signs, sidewalks,
// street lights and lane markings). This file adds the landmarks (Dispatch in green, the
// gold Vault), players glowing in their colour, dark suited hunters with green shades and
// a slate outline, cyan ice for frozen targets and glowing chips for loot. On the phone,
// buildings between the camera and the player turn see-through. Every model is built
// from primitives, and characters and props are instanced, so the whole city is a few
// dozen draw calls.
import * as THREE from "./vendor/three.module.min.js";
import { buildTown, rand, fadeUniforms } from "./town.js";

export const PLAYER_COLORS = { green: "#2bff6a", blue: "#3dd6ff", red: "#ff4d5e", purple: "#b98cff", gold: "#ffd23d", pink: "#ff5ad1" };
export const colorOf = (name) => PLAYER_COLORS[name] || name || "#2bff6a";
const RAM_COLORS = ["#c8d6cc", "#c8d6cc", "#2bff6a", "#4aa8ff", "#c77dff", "#ffd23d"]; // DDR1..DDR5
const PART_COLORS = { cpu: "#5ef0ff", drive: "#ff9f43", board: "#19e6b0" };
export const partColor = (p) => (p.kind === "ram" ? RAM_COLORS[p.tier] || RAM_COLORS[1] : PART_COLORS[p.kind] || "#9dffb0");

const PITCH = (58 * Math.PI) / 180; // camera angle below the horizon
const FIRE_RANGE = 6, FIRE_CD = 0.25, SPEED = 5;
const CAP = { people: 96, hunters: 128, ice: 160, drops: 128, tracers: 96, bits: 400 };
const GREEN = 0x2bff6a;

// shapeOf picks one of 12 hunter shapes from an ID, like Agent Flow does for agents.
export function shapeOf(id) {
  let h = 0;
  for (const ch of String(id)) h = (h * 31 + ch.charCodeAt(0)) >>> 0;
  return h % 12;
}

// webglOK reports whether this browser can give us a WebGL context.
export function webglOK() {
  try {
    const c = document.createElement("canvas");
    return !!(window.WebGLRenderingContext && (c.getContext("webgl2") || c.getContext("webgl")));
  } catch { return false; }
}

export const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);

// staticTexture is the TV static inside ice blocks, with a bright frame for edges.
function staticTexture() {
  const c = document.createElement("canvas");
  c.width = c.height = 64;
  const g = c.getContext("2d"), r = rand(3);
  for (let y = 0; y < 64; y += 2) for (let x = 0; x < 64; x += 2) {
    const v = 90 + r() * 165;
    g.fillStyle = `rgb(${v * 0.6},${v},${v})`; g.fillRect(x, y, 2, 2);
  }
  g.strokeStyle = "#e8ffff"; g.lineWidth = 4; g.strokeRect(2, 2, 60, 60);
  const t = new THREE.CanvasTexture(c);
  t.wrapS = t.wrapT = THREE.RepeatWrapping;
  return t;
}

// glowTexture is a soft round spot for chips and flashes.
function glowTexture() {
  const c = document.createElement("canvas");
  c.width = c.height = 64;
  const g = c.getContext("2d"), grd = g.createRadialGradient(32, 32, 0, 32, 32, 32);
  grd.addColorStop(0, "rgba(255,255,255,1)"); grd.addColorStop(0.35, "rgba(255,255,255,.45)"); grd.addColorStop(1, "rgba(255,255,255,0)");
  g.fillStyle = grd; g.fillRect(0, 0, 64, 64);
  return new THREE.CanvasTexture(c);
}

// glowByColor makes a lit material also glow in its instance colour.
function glowByColor(mat, k) {
  mat.onBeforeCompile = (s) => {
    s.fragmentShader = s.fragmentShader.replace("#include <emissivemap_fragment>",
      `#include <emissivemap_fragment>\n#ifdef USE_COLOR\ntotalEmissiveRadiance += vColor.rgb * ${k.toFixed(2)};\n#endif`);
  };
  mat.customProgramCacheKey = () => "glow" + k;
  return mat;
}

// inst is an InstancedMesh that is redrawn every frame.
function inst(geo, mat, cap, { shadow = true, color = false } = {}) {
  const m = new THREE.InstancedMesh(geo, mat, cap);
  m.frustumCulled = false;
  m.castShadow = shadow;
  m.count = 0;
  if (color) m.setColorAt(0, new THREE.Color(1, 1, 1));
  m.instanceMatrix.setUsage(THREE.DynamicDrawUsage);
  return m;
}

const feet = (geo, y) => geo.translate(0, y, 0);

// startCity draws the city on canvas.
// opts.mode: "phone" (follow-cam on opts.me) or "screen" (the whole city).
// opts.city: the map from /grand-theft-agent/api/city.
// opts.pos(kind, id): interpolated {x, y} (net.js); falls back to snapshot positions.
// opts.input(): the local controls {mx, my, fire}, for prediction and the player's tracer.
// opts.logo: URL of the kagent logo for Dispatch. opts.onEvent(e): every snapshot event.
export function startCity(canvas, opts) {
  const { mode = "phone", city } = opts;
  const pos = opts.pos || (() => null), input = opts.input || (() => ({ mx: 0, my: 0, fire: false }));
  const onEvent = opts.onEvent || (() => {});
  let me = opts.me || "";
  const W = city.w, H = city.h, tiles = city.tiles;
  const phone = mode === "phone";

  // Phones (touch, coarse pointer) render without antialiasing, at a pixel ratio of at
  // most 1.5 and without shadow maps (blob shadows instead): measured, that is where
  // their frame time goes. Laptops and the big screen get the full picture.
  const lowPower = phone && ((window.matchMedia && matchMedia("(pointer: coarse)").matches) || navigator.maxTouchPoints > 0);
  const dpr = window.devicePixelRatio || 1;
  const QUALITY = { high: { ratio: Math.min(dpr, lowPower ? 1.5 : 2), shadows: !lowPower }, low: { ratio: Math.min(dpr, lowPower ? 1 : 1.25), shadows: false } };
  const renderer = new THREE.WebGLRenderer({ canvas, antialias: !lowPower, powerPreference: "high-performance" });
  renderer.setPixelRatio(QUALITY.high.ratio);
  renderer.shadowMap.enabled = QUALITY.high.shadows;
  renderer.shadowMap.type = THREE.PCFSoftShadowMap;
  const scene = new THREE.Scene();
  scene.background = new THREE.Color(0x04060b);
  const cam = new THREE.PerspectiveCamera(42, 1, 0.5, 400);

  // Night, but readable: a cool sky fill, a warm bounce from the street lights, and a
  // pale moon for shading (and shadows on laptops).
  scene.add(new THREE.HemisphereLight(0x9fb0d8, 0x5a4630, 1.6), new THREE.AmbientLight(0x404860, 0.8));
  const sun = new THREE.DirectionalLight(0xdfe6ff, 1.7);
  sun.castShadow = QUALITY.high.shadows;
  sun.shadow.mapSize.set(phone ? 1024 : 2048, phone ? 1024 : 2048);
  sun.shadow.bias = -0.0008;
  sun.shadow.radius = 3;
  scene.add(sun, sun.target);

  // ---- the static city ----
  const staticTex = staticTexture(), glowTex = glowTexture();
  const fade = fadeUniforms();
  const town = buildTown(scene, city, { fade, maxHeight: phone ? 2.5 : Infinity });
  const districts = town.districts;

  // Dispatch: a landing pad with the kagent logo and a radio tower (the LLM route).
  const D = city.dispatch, dc = new THREE.Vector3(D.x + D.w / 2, 0, D.y + D.h / 2);
  const padR = Math.min(D.w, D.h) / 2 - 0.25;
  const pad = new THREE.Mesh(new THREE.CylinderGeometry(padR, padR + 0.1, 0.08, 48), new THREE.MeshStandardMaterial({ color: 0x062a12, emissive: 0x0f5f2a, emissiveIntensity: 0.7 }));
  pad.position.set(dc.x, 0.04, dc.z);
  pad.receiveShadow = true;
  const ring = new THREE.Mesh(new THREE.TorusGeometry(padR, 0.05, 6, 64), new THREE.MeshBasicMaterial({ color: GREEN }));
  ring.rotation.x = Math.PI / 2;
  ring.position.set(dc.x, 0.1, dc.z);
  const logoCanvas = document.createElement("canvas");
  logoCanvas.width = logoCanvas.height = 256;
  const logoTex = new THREE.CanvasTexture(logoCanvas);
  logoTex.colorSpace = THREE.SRGBColorSpace;
  if (opts.logo) {
    const img = new Image();
    img.onload = () => {
      const g = logoCanvas.getContext("2d");
      g.globalAlpha = 0.85;
      g.drawImage(img, 48, 58, 160, 142);
      logoTex.needsUpdate = true;
    };
    img.src = opts.logo;
  }
  const logo = new THREE.Mesh(new THREE.PlaneGeometry(padR * 1.3, padR * 1.3).rotateX(-Math.PI / 2),
    new THREE.MeshBasicMaterial({ map: logoTex, transparent: true, depthWrite: false }));
  logo.position.set(dc.x, 0.09, dc.z);
  const mast = new THREE.Mesh(new THREE.CylinderGeometry(0.04, 0.08, 2.6, 6), new THREE.MeshStandardMaterial({ color: 0x0d2a16 }));
  mast.position.set(D.x + D.w - 0.3, 1.3, D.y + 0.3);
  mast.castShadow = true;
  const beaconMat = new THREE.MeshBasicMaterial({ color: GREEN });
  const beacon = new THREE.Mesh(new THREE.SphereGeometry(0.13, 12, 8), beaconMat);
  beacon.position.set(mast.position.x, 2.65, mast.position.z);
  scene.add(pad, ring, logo, mast, beacon);
  let towerFlash = 0;

  // The Impound Vault (Snapshot Storage): a gold rack whose south wall is drawers, one
  // lit per booked target.
  const V = city.vault, vh = 1.4;
  const vaultGeo = new THREE.BoxGeometry(V.w - 0.1, vh, V.h - 0.1).translate(V.x + V.w / 2, vh / 2, V.y + V.h / 2);
  const vault = new THREE.Mesh(vaultGeo, new THREE.MeshStandardMaterial({ color: 0x1a1400, emissive: 0x4b3700, emissiveIntensity: 0.6, metalness: 0.9, roughness: 0.25 }));
  vault.castShadow = vault.receiveShadow = true;
  scene.add(vault, new THREE.LineSegments(new THREE.EdgesGeometry(vaultGeo), new THREE.LineBasicMaterial({ color: 0xffd23d })));
  const drawerCols = Math.max(1, Math.floor((V.w - 0.3) / 0.62)), drawerRows = 3;
  const drawers = new THREE.InstancedMesh(new THREE.BoxGeometry(0.52, 0.32, 0.05), new THREE.MeshBasicMaterial(), drawerCols * drawerRows);
  {
    const m = new THREE.Matrix4(), x0 = V.x + V.w / 2 - ((drawerCols - 1) * 0.62) / 2;
    for (let i = 0; i < drawerCols * drawerRows; i++) {
      drawers.setMatrixAt(i, m.makeTranslation(x0 + (i % drawerCols) * 0.62, 0.3 + Math.floor(i / drawerCols) * 0.4, V.y + V.h - 0.03));
      drawers.setColorAt(i, new THREE.Color(0x3a2c00));
    }
  }
  scene.add(drawers);
  const booked = new Set(); // target IDs seen Booked, filed one per drawer
  let filed = 0;
  function fileDrawers() {
    const n = Math.min(booked.size, drawers.count);
    if (n === filed) return;
    for (let i = 0; i < drawers.count; i++) drawers.setColorAt(i, new THREE.Color(i < n ? 0xffd23d : 0x3a2c00));
    drawers.instanceColor.needsUpdate = true;
    filed = n;
  }

  // ---- characters ----
  // Players and allies: a glowing capsule body, round head, dark visor and a ground ring.
  const bodyGeo = feet(new THREE.CapsuleGeometry(0.27, 0.34, 6, 14), 0.5);
  const headGeo = feet(new THREE.SphereGeometry(0.25, 18, 12), 1.0);
  const visorGeo = new THREE.BoxGeometry(0.34, 0.09, 0.08).translate(0, 1.02, 0.2);
  const ringGeo = new THREE.RingGeometry(0.4, 0.5, 28).rotateX(-Math.PI / 2).translate(0, 0.03, 0);
  const lit = (o) => glowByColor(new THREE.MeshStandardMaterial({ roughness: 0.4, ...o }), 0.5);
  const P = {
    body: inst(bodyGeo, lit({}), CAP.people, { color: true }),
    head: inst(headGeo, lit({}), CAP.people, { color: true }),
    visor: inst(visorGeo, new THREE.MeshStandardMaterial({ color: 0x02140a, roughness: 0.2, metalness: 0.6 }), CAP.people),
    ring: inst(ringGeo, new THREE.MeshBasicMaterial({ transparent: true, opacity: 0.8, side: THREE.DoubleSide, depthWrite: false }), CAP.people, { shadow: false, color: true }),
  };
  // X-ray: a player hidden behind a building still shows as a flat silhouette.
  const xray = (src, color) => {
    const mat = new THREE.MeshBasicMaterial({ transparent: true, opacity: 0.4, depthFunc: THREE.GreaterDepth, depthWrite: false });
    if (color !== undefined) mat.color.set(color);
    const m = new THREE.InstancedMesh(src.geometry, mat, src.instanceMatrix.count);
    m.instanceMatrix = src.instanceMatrix;
    if (color === undefined) m.instanceColor = src.instanceColor;
    m.frustumCulled = false;
    m.renderOrder = 5;
    return m;
  };
  const PX = xray(P.body, 0xe8ecf4); // a white ghost of a player hidden behind a building

  // Hunters (targets): dark suits, green inverted-hull outlines and shades; 12 shapes
  // from 3 builds x 4 hats.
  const hullGeo = new THREE.CapsuleGeometry(0.27, 0.34, 6, 14).scale(1.13, 1.08, 1.13).translate(0, 0.5, 0);
  const T = {
    body: inst(bodyGeo, new THREE.MeshStandardMaterial({ roughness: 0.5, metalness: 0.4 }), CAP.hunters, { color: true }),
    head: inst(headGeo, new THREE.MeshStandardMaterial({ roughness: 0.5, metalness: 0.3 }), CAP.hunters, { color: true }),
    hull: inst(hullGeo, new THREE.MeshBasicMaterial({ color: GREEN, side: THREE.BackSide }), CAP.hunters, { shadow: false, color: true }),
    shades: inst(new THREE.BoxGeometry(0.4, 0.08, 0.1).translate(0, 1.04, 0.2), new THREE.MeshBasicMaterial({ color: GREEN }), CAP.hunters, { shadow: false, color: true }),
    crown: inst(new THREE.CylinderGeometry(0.16, 0.18, 0.3, 14).translate(0, 1.33, 0), new THREE.MeshStandardMaterial({ color: 0x050805, roughness: 0.6 }), CAP.hunters),
    brim: inst(new THREE.CylinderGeometry(0.33, 0.33, 0.03, 18).translate(0, 1.19, 0), new THREE.MeshStandardMaterial({ color: 0x050805, roughness: 0.6 }), CAP.hunters),
    stick: inst(new THREE.CylinderGeometry(0.018, 0.018, 0.34, 5).translate(0, 1.38, 0), new THREE.MeshBasicMaterial({ color: 0x1a5a2a }), CAP.hunters, { shadow: false }),
    tip: inst(new THREE.SphereGeometry(0.06, 8, 6).translate(0, 1.56, 0), new THREE.MeshBasicMaterial({ color: GREEN }), CAP.hunters, { shadow: false }),
    ring: inst(ringGeo, new THREE.MeshBasicMaterial({ transparent: true, opacity: 0.7, side: THREE.DoubleSide, depthWrite: false }), CAP.hunters, { shadow: false, color: true }),
  };
  const TX = xray(T.body, 0x6a7590); // a slate ghost for hunters
  const none = [0, 0, 0];
  const BUILDS = [[1, 1, 1], [1.22, 0.9, 1.22], [0.88, 1.14, 0.88]];

  // Ice: frozen targets and allies sit in a cyan block of static; a target whose freeze
  // is in flight (third hit landed, server not answered yet) flickers in a thinner one.
  const iceGeo = new THREE.BoxGeometry(0.9, 1.5, 0.9).translate(0, 0.75, 0);
  const iceMat = (o) => new THREE.MeshStandardMaterial({ color: 0x66ffff, emissive: 0x22aacc, emissiveIntensity: 0.7, map: staticTex, transparent: true, opacity: o, roughness: 0.1, depthWrite: false });
  const ice = inst(iceGeo, iceMat(0.42), CAP.ice, { shadow: false });
  const freezing = inst(iceGeo, iceMat(0.25), CAP.ice, { shadow: false });
  ice.renderOrder = freezing.renderOrder = 6;

  // Loot: small glowing chips over a soft ground glow, coloured by rarity.
  const chips = inst(new THREE.BoxGeometry(0.44, 0.07, 0.17), new THREE.MeshBasicMaterial(), CAP.drops, { color: true });
  const glows = inst(new THREE.PlaneGeometry(1, 1).rotateX(-Math.PI / 2),
    new THREE.MeshBasicMaterial({ map: glowTex, transparent: true, blending: THREE.AdditiveBlending, depthWrite: false }), CAP.drops + CAP.tracers, { shadow: false, color: true });

  // Tracers (shots, drag tethers) and de-rez bits: thin glowing boxes.
  const tracers = inst(new THREE.BoxGeometry(1, 1, 1), new THREE.MeshBasicMaterial({ transparent: true, blending: THREE.AdditiveBlending, depthWrite: false }), CAP.tracers, { shadow: false, color: true });
  const bits = inst(new THREE.BoxGeometry(0.09, 0.09, 0.09), new THREE.MeshBasicMaterial(), CAP.bits, { shadow: false, color: true });

  // Blob shadows: a soft dark spot under every character when shadow maps are off.
  const blobs = inst(new THREE.PlaneGeometry(1, 1).rotateX(-Math.PI / 2),
    new THREE.MeshBasicMaterial({ map: glowTex, color: 0x000000, transparent: true, opacity: 0.75, depthWrite: false }), CAP.people + CAP.hunters, { shadow: false });
  const blob = [], blobScale = [1, 1, 1]; // reused every frame

  for (const m of [...Object.values(P), PX, ...Object.values(T), TX, ice, freezing, chips, glows, tracers, bits, blobs]) scene.add(m);

  // ---- HTML labels over the canvas ----
  const overlay = opts.overlay || (() => {
    const d = document.createElement("div");
    canvas.parentNode.insertBefore(d, canvas.nextSibling);
    return d;
  })();
  overlay.classList.add("gta-labels");
  if (!document.getElementById("gta-label-css")) {
    const st = document.createElement("style");
    st.id = "gta-label-css";
    st.textContent = `
.gta-labels{position:absolute;inset:0;overflow:hidden;pointer-events:none;font-family:"JetBrains Mono",ui-monospace,monospace}
.gta-labels>div{position:absolute;left:0;top:0;white-space:nowrap;will-change:transform;text-shadow:0 0 4px #000,0 0 2px #000}
.gl-nick{font-size:12px;font-weight:700}
.gl-name{font-size:11px;color:#9dffb0;display:flex;flex-direction:column;align-items:center;gap:2px}
.gl-name i{font-style:normal;font-size:9px;letter-spacing:.08em;padding:0 4px;border-radius:3px;color:#001a08}
.gl-new{background:#2bff6a}.gl-restored{background:#4aa8ff}.gl-esc{background:#ff4d5e}
.gl-bar{width:40px;height:4px;border-radius:2px;background:#0a2a12;overflow:hidden;border:1px solid #1f8f3f}
.gl-bar b{display:block;height:100%;background:#ffd23d}
.gl-bub{max-width:200px;white-space:normal;background:rgba(0,20,6,.88);border:1px solid #2bff6a;border-radius:8px;padding:3px 7px;color:#d8ffe0;font-size:11px;box-shadow:0 0 10px #2bff6a55}
.gl-float{font-weight:700;font-size:12px;transition:none}
.gl-place{font-size:10px;letter-spacing:.18em;color:#5bff8a;opacity:.85}.gl-gold{color:#ffd23d}
.gl-chip{font-size:10px;font-weight:700}`;
    document.head.appendChild(st);
  }
  const labels = new Map(); // key -> {el, html, used}
  const v3 = new THREE.Vector3();
  let vw = 1, vh2 = 1;
  function screenOf(x, y, z) {
    v3.set(x, y, z).project(cam);
    if (v3.z > 1 || v3.x < -1.3 || v3.x > 1.3 || v3.y < -1.3 || v3.y > 1.3) return null;
    return [((v3.x + 1) / 2) * vw, ((1 - v3.y) / 2) * vh2];
  }
  function label(key, html, x, y, z, cls) {
    let l = labels.get(key);
    if (!l) {
      l = { el: document.createElement("div"), html: null, cls: null };
      overlay.appendChild(l.el);
      labels.set(key, l);
    }
    l.used = true;
    if (l.html !== html) { l.el.innerHTML = html; l.html = html; }
    if (l.cls !== cls) { l.el.className = cls; l.cls = cls; }
    const s = screenOf(x, y, z);
    if (!s) { l.el.style.display = "none"; return; }
    l.el.style.display = "";
    l.el.style.transform = `translate(${s[0].toFixed(1)}px,${s[1].toFixed(1)}px) translate(-50%,-100%)`;
  }
  const floaters = []; // {el, x, z, y0, t, life}
  // floater is a rising line of text over a spot; y0 is where it starts (HIT counters sit
  // low and short, results start higher and stack).
  function floater(text, x, z, color, life = 1.8, y0 = 3.1) {
    if (floaters.length > 30) floaters.shift().el.remove();
    // Stack on top of fresh floaters at the same spot instead of overprinting them.
    const lift = y0 < 3 ? 0 : floaters.filter((f) => f.y0 >= 3 && f.t < 0.8 && Math.abs(f.x - x) + Math.abs(f.z - z) < 1.5).length * 0.8;
    const el = document.createElement("div");
    el.className = "gl-float";
    el.style.color = color;
    el.textContent = text;
    overlay.appendChild(el);
    floaters.push({ el, x, z, y0: y0 + lift, t: 0, life });
  }

  // ---- state from snapshots ----
  let snap = null, snapAt = 0;
  const seen = new Map(); // target ID -> {state, hits, step, say, sayUntil}
  const allySay = new Map(); // ally ID -> {say, until}
  const shots = []; // live tracers {a:[x,y,z], b:[x,y,z], t, life, color}
  const parts = []; // de-rez bits {x,y,z,vx,vy,vz,t,life,color}
  const meView = { x: 0, y: 0, ok: false };

  function tracer(a, b, color, life = 0.16) {
    if (shots.length >= CAP.tracers - 8) shots.shift();
    shots.push({ a, b, t: 0, life, color: new THREE.Color(color) });
  }
  function burst(x, z, color, n = 36) {
    for (let i = 0; i < n && parts.length < CAP.bits; i++) {
      const a = Math.random() * Math.PI * 2, s = 0.6 + Math.random() * 1.6;
      parts.push({ x: x + (Math.random() - 0.5) * 0.5, y: 0.2 + Math.random() * 1.2, z: z + (Math.random() - 0.5) * 0.5,
        vx: Math.cos(a) * s, vy: 1 + Math.random() * 2, vz: Math.sin(a) * s, t: 0, life: 0.9 + Math.random() * 0.6, color: new THREE.Color(color) });
    }
  }
  const byId = (list, id) => (list || []).find((e) => e.id === id);
  const where = (kind, id) => {
    const e = snap && byId(snap[kind], id);
    if (!e) return null;
    return pos(kind, id) || { x: e.x, y: e.y };
  };

  function apply(s) {
    const prev = snap;
    snap = s; snapAt = performance.now();
    if (s.me) me = s.me;
    if (prev && s.now < prev.now - 0.5) { booked.clear(); seen.clear(); } // admin reset
    town.setAwake(s.substrate ? s.substrate.busyWorkers : 0);
    for (const t of s.targets) {
      const o = seen.get(t.id);
      if (o) {
        if (t.state === "Deleted" && o.state !== "Deleted") burst(t.x, t.y, 0xbfefff, 60);
        if (t.state === "Booked" && o.state !== "Booked") burst(t.x, t.y, 0xffd23d, 24);
        if (t.hits > o.hits && t.state === "Working") {
          const shooter = nearestShooter(s, t);
          if (shooter) tracer([shooter.x, 0.75, shooter.y], [t.x, 0.75, t.y], 0x9fffff);
          if (t.hits < 3) floater(`HIT ${t.hits}/3`, t.x, t.y, "#9fffff", 0.7, 2.3);
        }
        if (t.step > o.step) towerFlash = 1;
      }
      if (t.state === "Booked") booked.add(t.id);
      const say = t.say && (!o || o.say !== t.say) ? performance.now() + 4000 : o ? o.sayUntil : 0;
      seen.set(t.id, { state: t.state, hits: t.hits, step: t.step, say: t.say, sayUntil: say });
    }
    for (const al of s.allies) { // ally lines show for 4 s too
      const o = allySay.get(al.id);
      allySay.set(al.id, al.say && (!o || o.say !== al.say) ? { say: al.say, until: performance.now() + 4000 } : o || { say: al.say, until: 0 });
    }
    for (const id of seen.keys()) if (!s.targets.some((t) => t.id === id)) seen.delete(id);
    for (const id of allySay.keys()) if (!s.allies.some((a) => a.id === id)) allySay.delete(id);
    fileDrawers();
    for (const e of s.events || []) event(e, s);
  }

  // nearestShooter guesses who landed a hit: the closest avatar or ally in range.
  function nearestShooter(s, t) {
    let best = null, bd = FIRE_RANGE + 0.5;
    for (const a of [...s.avatars.filter((a) => a.online && !a.down && a.id !== me), ...s.allies]) {
      const d = Math.hypot(a.x - t.x, a.y - t.y);
      if (d < bd) { best = a; bd = d; }
    }
    return best;
  }

  function event(e, s) {
    onEvent(e);
    const t = byId(s.targets, e.target), a = byId(s.avatars, e.player);
    const at = t || a;
    switch (e.kind) {
      case "frozen": if (t) floater(`❄ suspended in ${e.ms} ms`, t.x, t.y, "#6ff", 2.6); break;
      case "sprung": if (t) floater(`↻ restored in ${e.ms} ms`, t.x, t.y, "#7ab8ff", 2.6); break;
      case "slipped": if (t) floater("it slipped away", t.x, t.y, "#ff9f43", 2.2); break;
      case "booked": if (t) floater("▣ booked into the Vault", t.x, t.y, "#ffd23d", 2.2); break;
      case "booked-line": if (t) floater(`“${e.text}”`, t.x, t.y, "#ffd23d", 3.2); break;
      case "deleted": if (t) floater("✖ deleted", t.x, t.y, "#ff4d5e", 2); break;
      case "escaped": if (t) floater("got away with it", t.x, t.y, "#ff4d5e", 2.5); break;
      case "drop": if (t) floater(`★ ${e.text}`, t.x, t.y, "#ffd23d", 1.8); break;
      case "pickup": if (a && a.id === me) floater(`+ ${e.text}`, a.x, a.y, "#d8ffe0", 1.2); break;
      case "banked": if (a && a.id === me) floater(`banked ${e.text}`, a.x, a.y, "#ffd23d", 1.8); break;
      case "down": if (a) floater("DOWN", a.x, a.y, "#ff4d5e", 1.6); break;
      case "ally": if (a) floater("ally online", a.x, a.y, "#2bff6a", 1.8); break;
      case "hit": if (t && a) tracer([t.x, 0.8, t.y], [a.x, 0.7, a.y], 0xff3b3b, 0.2); break;
      case "ally-hit": {
        const h = byId(s.targets, e.text), al = byId(s.allies, e.target);
        if (h && al) tracer([h.x, 0.8, h.y], [al.x, 0.55, al.y], 0xff3b3b, 0.2);
        break;
      }
      default: if (at && e.kind.endsWith("-failed")) floater("failed, try again", at.x, at.y, "#ff9f43", 1.6);
    }
  }

  // ---- camera ----
  const focus = new THREE.Vector3(dc.x, 0, dc.z);
  let dist = 30;
  function resize() {
    vw = canvas.clientWidth || window.innerWidth;
    vh2 = canvas.clientHeight || window.innerHeight;
    renderer.setSize(vw, vh2, false);
    cam.aspect = vw / vh2;
    cam.fov = phone && cam.aspect < 0.8 ? 46 : 40;
    cam.updateProjectionMatrix();
    const vf = (cam.fov * Math.PI) / 180, hf = 2 * Math.atan(Math.tan(vf / 2) * cam.aspect);
    if (phone) {
      // About 14 tiles across a portrait phone, 16 deep on a laptop; capped by the city size.
      dist = Math.max(Math.min(14, W) / (2 * Math.tan(hf / 2)), Math.min(16, H) / (2 * Math.tan(vf / 2)));
    } else {
      // The whole city: the smallest distance at which every corner is on screen.
      let lo = 5, hi = 300;
      for (let i = 0; i < 30; i++) {
        dist = (lo + hi) / 2;
        place(new THREE.Vector3(W / 2, 0, H / 2));
        const fits = [[0, 0], [W, 0], [0, H], [W, H]].every(([x, z]) => {
          const p = new THREE.Vector3(x, 0, z).project(cam);
          return Math.abs(p.x) < 0.97 && Math.abs(p.y) < 0.95;
        });
        if (fits) hi = dist; else lo = dist;
      }
      dist = hi;
      focus.set(W / 2, 0, H / 2);
    }
    const span = phone ? dist * 0.75 + 4 : Math.max(W, H) * 0.75;
    Object.assign(sun.shadow.camera, { left: -span, right: span, top: span, bottom: -span, near: 1, far: 120 });
    sun.shadow.camera.updateProjectionMatrix();
    place(focus);
  }
  function place(f) {
    cam.position.set(f.x, Math.sin(PITCH) * dist, f.z + Math.cos(PITCH) * dist);
    cam.lookAt(f.x, 0, f.z);
    cam.updateMatrixWorld();
    sun.position.set(f.x - 12, 30, f.z + 18);
    sun.target.position.set(f.x, 0, f.z);
  }
  window.addEventListener("resize", resize);
  resize();

  // ---- the local player: drawn ahead of the 100 ms interpolation ----
  function free(x, y) {
    for (const dx of [-0.3, 0.3]) for (const dy of [-0.3, 0.3]) {
      const tx = Math.floor(x + dx), ty = Math.floor(y + dy);
      if (tx < 0 || ty < 0 || tx >= W || ty >= H || tiles[ty][tx] === "#") return false;
    }
    return true;
  }
  function predictMe(a, dt) {
    let { x, y } = a;
    const inp = input(), n = Math.hypot(inp.mx || 0, inp.my || 0);
    if (n > 0 && !a.down) {
      const ahead = Math.min((performance.now() - snapAt) / 1000 + 0.05, 0.15) * SPEED * (a.dragging ? 0.75 : 1);
      const dx = (inp.mx / n) * ahead, dy = (inp.my / n) * ahead;
      if (free(x + dx, y + dy)) { x += dx; y += dy; } else if (free(x + dx, y)) x += dx; else if (free(x, y + dy)) y += dy;
    }
    if (!meView.ok || Math.hypot(meView.x - x, meView.y - y) > 3) Object.assign(meView, { x, y, ok: true });
    const k = 1 - Math.exp(-dt * 18);
    meView.x += (x - meView.x) * k;
    meView.y += (y - meView.y) * k;
    return meView;
  }

  // ---- drawing ----
  const M = new THREE.Matrix4(), Q = new THREE.Quaternion(), V3 = new THREE.Vector3(), S3 = new THREE.Vector3(), C = new THREE.Color();
  const UP = new THREE.Vector3(0, 1, 0), X = new THREE.Vector3(1, 0, 0);
  const lie = new THREE.Quaternion().setFromAxisAngle(X, -Math.PI / 2);
  let last = performance.now(), clock = 0, fireCD = 0, frames = 0, frameMs = 0;
  const yawOf = (fx, fy) => (fx || fy ? Math.atan2(fx, fy) : 0);
  function put(mesh, i, x, z, yaw, s = 1, down = false, y = 0) {
    Q.setFromAxisAngle(UP, yaw);
    if (down) Q.multiply(lie);
    V3.set(x, y + (down ? 0.28 * s : 0), z);
    S3.set(s, s, s);
    mesh.setMatrixAt(i, M.compose(V3, Q, S3));
  }
  const putS = (mesh, i, x, z, yaw, sc, y = 0) => {
    Q.setFromAxisAngle(UP, yaw);
    mesh.setMatrixAt(i, M.compose(V3.set(x, y, z), Q, S3.set(sc[0], sc[1], sc[2])));
  };

  function draw() {
    const now = performance.now(), dt = Math.min((now - last) / 1000, 0.1);
    last = now; clock += dt;
    staticTex.offset.set(Math.random(), Math.random());
    freezing.material.opacity = 0.12 + Math.random() * 0.3;
    towerFlash = Math.max(0, towerFlash - dt * 1.5);
    beaconMat.color.setRGB(0.17 + towerFlash * 0.8, 1, 0.42 + towerFlash * 0.5);
    beacon.scale.setScalar(1 + towerFlash * 1.5 + Math.sin(clock * 3) * 0.08);
    ring.material.color.setRGB(0.17, 0.75 + Math.sin(clock * 2) * 0.25, 0.42);
    for (const l of labels.values()) l.used = false;
    const s = snap;
    let np = 0, nh = 0, ni = 0, nf = 0, nd = 0, ng = 0, nt = 0;
    blob.length = 0;

    if (s) {
      // Players.
      const meA = byId(s.avatars, me);
      const mp = meA ? predictMe(meA, dt) : null;
      for (const a of s.avatars) {
        if (!a.online || np >= CAP.people) continue;
        const p = a.id === me ? mp : pos("avatars", a.id) || a;
        const col = C.set(a.down ? "#3a4a3e" : colorOf(a.color));
        const yaw = yawOf(a.fx, a.fy);
        for (const k of ["body", "head", "visor"]) put(P[k], np, p.x, p.y, yaw, a.id === me ? 1.12 : 1, a.down);
        put(P.ring, np, p.x, p.y, 0, a.id === me ? 1.12 : 1);
        P.body.setColorAt(np, col); P.head.setColorAt(np, col); P.ring.setColorAt(np, a.down ? C.set("#ff4d5e") : col);
        np++;
        blob.push(p.x, p.y, 1);
        const tag = a.id === me ? `<b style="color:#fff">${esc(a.nick)}</b>` : esc(a.nick);
        label("e:a" + a.id, tag, p.x, a.down ? 0.9 : 1.62, p.y, "gl-nick");
        labels.get("e:a" + a.id).el.style.color = colorOf(a.color);
        if (a.dragging) {
          const t = where("targets", a.dragging);
          if (t) tracer([p.x, 0.6, p.y], [t.x, 0.7, t.y], 0x6fffff, 0.0001);
        }
      }
      // Allies: smaller, in the owner's colour, frozen in ice when hunters drop them.
      for (const al of s.allies) {
        if (np >= CAP.people) break;
        const p = pos("allies", al.id) || al, owner = byId(s.avatars, al.owner);
        const col = C.set(colorOf(owner ? owner.color : "green"));
        const frozen = al.state === "Frozen";
        if (frozen) col.multiplyScalar(0.35).add(C.clone().set("#0a4a55"));
        for (const k of ["body", "head", "visor"]) put(P[k], np, p.x, p.y, 0, 0.72);
        put(P.ring, np, p.x, p.y, 0, 0.72);
        P.body.setColorAt(np, col); P.head.setColorAt(np, col); P.ring.setColorAt(np, col);
        np++;
        blob.push(p.x, p.y, 0.75);
        if (frozen && ni < CAP.ice) put(ice, ni++, p.x, p.y, 0, 0.75);
        const abub = al.say && (allySay.get(al.id) || {}).until > now ? `<span class="gl-bub">“${esc(al.say)}”</span>` : "";
        label("e:l" + al.id, `${abub}<span>ally · DDR${al.tier}</span>`, p.x, 1.25, p.y, "gl-name");
        labels.get("e:l" + al.id).el.style.color = colorOf(owner ? owner.color : "green");
      }
      // Hunters.
      for (const t of s.targets) {
        if (t.state === "Booked" || t.state === "Deleted" || nh >= CAP.hunters) continue;
        const p = pos("targets", t.id) || t;
        const sh = shapeOf(t.id), build = BUILDS[sh % 3], hat = Math.floor(sh / 3);
        const frozen = t.state === "Frozen" || t.state === "Dragged";
        const going = t.state === "Working" && (t.busy || t.hits >= 3); // freeze in flight
        const yaw = Math.sin(clock * 0.7 + sh) * 0.6;
        const sc = build.map((v) => v * 1.12);
        for (const k of ["body", "head", "hull", "shades", "ring"]) putS(T[k], nh, p.x, p.y, yaw, sc);
        // Hats: 0 bare, 1 top hat, 2 flat cap with an antenna, 3 antenna. Unused parts
        // are scaled to nothing so every hunter keeps one slot in each mesh.
        putS(T.crown, nh, p.x, p.y, yaw, hat === 1 ? sc : none);
        putS(T.brim, nh, p.x, p.y, yaw, hat === 1 || hat === 2 ? sc : none);
        putS(T.stick, nh, p.x, p.y, yaw, hat >= 2 ? sc : none, hat === 2 ? -0.06 : 0);
        putS(T.tip, nh, p.x, p.y, yaw, hat >= 2 ? sc : none, hat === 2 ? -0.06 : 0);
        const flick = going && Math.random() < 0.5;
        const suit = frozen ? "#123c44" : t.state === "Escaped" ? "#2a0d10" : flick ? "#1d5560" : "#0b120c";
        // Green is kept for the shades; the outline is a quiet slate so it reads on asphalt.
        const edge = frozen || flick ? "#9fffff" : t.state === "Escaped" ? "#ff4d5e" : "#2bff6a";
        const hull = frozen || flick ? "#9fffff" : t.state === "Escaped" ? "#ff4d5e" : "#6a7590";
        T.body.setColorAt(nh, C.set(suit)); T.head.setColorAt(nh, C.set(suit));
        T.hull.setColorAt(nh, C.set(hull)); T.shades.setColorAt(nh, C.set(edge)); T.ring.setColorAt(nh, C.set(hull));
        nh++;
        blob.push(p.x, p.y, 1.1);
        if (frozen && ni < CAP.ice) putS(ice, ni++, p.x, p.y, 0, sc);
        if (going && nf < CAP.ice) putS(freezing, nf++, p.x, p.y, 0, sc.map((v) => v * (0.9 + Math.random() * 0.15)));
        const o = seen.get(t.id) || {};
        const tagCls = t.state === "Escaped" ? `<i class="gl-esc">ESCAPED</i>` : t.restored ? `<i class="gl-restored">RESTORED</i>` : t.new ? `<i class="gl-new">NEW</i>` : "";
        const bar = t.steps ? `<span class="gl-bar"><b style="width:${Math.round((100 * t.step) / t.steps)}%"></b></span>` : "";
        // One stacked label (bubble, tag, name, progress) so the parts never overlap.
        const bub = t.say && o.sayUntil > now ? `<span class="gl-bub">“${esc(t.say)}”</span>` : "";
        label("e:t" + t.id, `${bub}${tagCls}<span>${esc(t.name)}</span>${bar}`, p.x, 1.95, p.y, "gl-name");
      }
      // Loot on the street.
      for (const d of s.drops) {
        if (nd >= CAP.drops) break;
        const col = partColor(d), gold = d.kind === "ram" && d.tier === 5;
        const bob = 0.32 + Math.sin(clock * 3 + d.x * 7) * 0.06;
        put(chips, nd, d.x, d.y, clock * 1.6 + d.y, gold ? 1.3 : 1, false, bob);
        chips.setColorAt(nd++, C.set(col));
        putS(glows, ng, d.x, d.y, 0, gold ? [1.8, 1, 1.8] : [1.1, 1, 1.1], 0.02);
        glows.setColorAt(ng++, C.set(col).multiplyScalar(gold ? 1 : 0.7));
        if (phone || gold) {
          label("e:d" + d.id, (gold ? "★ " : "") + esc(d.label), d.x, 0.75, d.y, "gl-chip");
          labels.get("e:d" + d.id).el.style.color = col;
        }
      }
      // The local player's own shots, drawn at once rather than after the round trip.
      fireCD -= dt;
      const inp = input();
      if (inp.fire && mp && meA && !meA.down && fireCD <= 0) {
        let best = null, bd = FIRE_RANGE;
        for (const t of s.targets) {
          if (t.state !== "Working" || t.busy) continue;
          const p = pos("targets", t.id) || t, d = Math.hypot(p.x - mp.x, p.y - mp.y);
          if (d < bd) { best = p; bd = d; }
        }
        if (best) { tracer([mp.x, 0.75, mp.y], [best.x, 0.75, best.y], 0x9fffff); fireCD = FIRE_CD; }
      }
      if (phone && mp) {
        const k = 1 - Math.exp(-dt * 6);
        focus.x += (mp.x - focus.x) * k;
        focus.z += (mp.y - focus.z) * k;
        place(focus);
        // See-through tube from the camera to the player's chest.
        fade.uFadeCam.value.copy(cam.position);
        fade.uFadeAt.value.set(mp.x, 0.7, mp.y);
        fade.uFadeR.value = 1.3;
      }
    }

    // Tracers and their glow at the far end.
    for (let i = shots.length - 1; i >= 0; i--) {
      const sh = shots[i];
      sh.t += dt;
      const f = sh.life < 0.01 ? 1 : 1 - sh.t / sh.life;
      if (f <= 0 && sh.life >= 0.01) { shots.splice(i, 1); continue; }
      const [ax, ay, az] = sh.a, [bx, by, bz] = sh.b, len = Math.hypot(bx - ax, by - ay, bz - az);
      if (nt < CAP.tracers && len > 0.01) {
        V3.set((ax + bx) / 2, (ay + by) / 2, (az + bz) / 2);
        Q.setFromUnitVectors(X, S3.set(bx - ax, by - ay, bz - az).normalize());
        const th = (sh.life < 0.01 ? 0.03 : 0.07) * Math.max(f, 0.2);
        tracers.setMatrixAt(nt, M.compose(V3, Q, S3.set(len, th, th)));
        tracers.setColorAt(nt++, C.copy(sh.color).multiplyScalar(Math.max(f, 0.3)));
        if (sh.life >= 0.01 && ng < glows.instanceMatrix.count) {
          putS(glows, ng, bx, bz, 0, [1.2 * f, 1, 1.2 * f], by * 0.3);
          glows.setColorAt(ng++, C.copy(sh.color).multiplyScalar(f));
        }
      }
      if (sh.life < 0.01) shots.splice(i, 1); // tethers last one frame
    }
    // De-rez bits.
    let nb = 0;
    for (let i = parts.length - 1; i >= 0; i--) {
      const b = parts[i];
      b.t += dt;
      if (b.t > b.life) { parts.splice(i, 1); continue; }
      b.vy -= 4 * dt;
      b.x += b.vx * dt; b.y = Math.max(0.05, b.y + b.vy * dt); b.z += b.vz * dt;
      bits.setMatrixAt(nb, M.compose(V3.set(b.x, b.y, b.z), Q.identity(), S3.setScalar(1 - b.t / b.life)));
      bits.setColorAt(nb++, b.color);
    }

    let nbl = 0;
    if (!renderer.shadowMap.enabled) {
      for (let i = 0; i < blob.length && nbl < blobs.instanceMatrix.count; i += 3) {
        blobScale[0] = blobScale[2] = blob[i + 2];
        putS(blobs, nbl++, blob[i], blob[i + 1], 0, blobScale, 0.02);
      }
    }
    const counts = [[blobs, nbl], [P.body, np], [P.head, np], [P.visor, np], [P.ring, np], [PX, np], [ice, ni], [freezing, nf], [chips, nd], [glows, ng], [tracers, nt], [bits, nb], [TX, nh]];
    for (const k in T) counts.push([T[k], nh]);
    for (const [m, n] of counts) {
      m.count = n;
      m.instanceMatrix.needsUpdate = true;
      if (m.instanceColor) m.instanceColor.needsUpdate = true;
    }

    // Floaters rise and fade.
    for (let i = floaters.length - 1; i >= 0; i--) {
      const f = floaters[i];
      f.t += dt;
      if (f.t > f.life) { f.el.remove(); floaters.splice(i, 1); continue; }
      const s2 = screenOf(f.x, f.y0 + f.t * 0.6, f.z);
      f.el.style.opacity = String(Math.min(1, 2 * (1 - f.t / f.life)));
      f.el.style.display = s2 ? "" : "none";
      if (s2) f.el.style.transform = `translate(${s2[0].toFixed(1)}px,${s2[1].toFixed(1)}px) translate(-50%,-100%)`;
    }
    // Place labels.
    label("dispatch", "DISPATCH · kagent", dc.x, 0.3, D.y + D.h - 0.2, "gl-place");
    label("vault", "SNAPSHOT VAULT", V.x + V.w / 2, vh + 0.5, V.y + V.h / 2, "gl-place gl-gold");
    if (!phone) for (const [name, d] of districts) label("n" + name, name, d.x + d.w / 2, 0.05, -0.3, "gl-place");
    for (const [k, l] of labels) {
      if (!l.used) {
        if (k.startsWith("e:")) { l.el.remove(); labels.delete(k); } else l.el.style.display = "none";
      }
    }

    renderer.render(scene, cam);
    frames++;
    frameMs += performance.now() - now;
  }

  // setLowQuality trades looks for speed (QUALITY.low: a lower pixel ratio, no shadows).
  // Antialiasing is fixed when the context is made, so it stays as it was.
  let low = false;
  function setLowQuality(on) {
    if (on === low) return;
    low = on;
    const q = on ? QUALITY.low : QUALITY.high;
    renderer.setPixelRatio(q.ratio);
    renderer.shadowMap.enabled = q.shadows;
    sun.castShadow = q.shadows;
    scene.traverse((o) => { for (const m of [].concat(o.material || [])) m.needsUpdate = true; }); // shadow defines changed
    resize();
  }

  return {
    apply,
    draw,
    resize,
    setLowQuality,
    lowQuality: () => low,
    // stats: draw calls and triangles of the last frame, frames drawn, mean CPU ms per draw().
    stats: () => ({ calls: renderer.info.render.calls, triangles: renderer.info.render.triangles, drawn: frames, cpuMs: frames ? frameMs / frames : 0, pixelRatio: renderer.getPixelRatio(), shadows: renderer.shadowMap.enabled && sun.castShadow }),
    // view is where the camera is centred (for the minimap).
    view: () => ({ x: focus.x, y: focus.z, me: meView.ok ? { x: meView.x, y: meView.y } : null }),
  };
}
