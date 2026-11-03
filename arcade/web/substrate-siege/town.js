// Substrate Siege: the static city, built once from the map that GET
// /substrate-siege/api/city serves. A stylised night city: towers, mid-rises and low
// shops in natural facade colours with warm and cool windows, rooftop clutter (AC units,
// water tanks, antennas), a few neon signs, sidewalks with curbs and street lights, and
// asphalt with yellow centre dashes, white edge lines and zebra crossings. Everything is
// merged or instanced (about 15 draw calls), and nothing assumes a map size, road width
// or block position.
import * as THREE from "./vendor/three.module.min.js";

const S = 2.6; // world units per facade texture repeat
const PANE_W = S / 5, PANE_H = S / 6; // the window grid in facadeTextures
const FACADES = [0x8d939c, 0x8a5d4c, 0xa48f6e, 0x58708a, 0x454d5a, 0xb4b6ba, 0x6f7a68]; // concrete, brick, sand, blue glass, dark glass, white, sage
const ROOFS = [0x3a3e45, 0x4c4f55, 0x5a5c60, 0x3d5040, 0x5e3e36, 0x46505e]; // tar, gravel, concrete, green roof, red tile, slate
const NEON = [0x3df5ff, 0xff3df0, 0xffb43d]; // cyan, magenta, amber
const WARM = 0xffc46b, COOL = 0xcdeeff;

// rand is a small deterministic generator, so every phone draws the same city.
export function rand(seed) {
  let s = seed >>> 0 || 1;
  return () => ((s = (s * 16807) % 2147483647) / 2147483647);
}

const texture = (c, srgb = true) => {
  const t = new THREE.CanvasTexture(c);
  t.wrapS = t.wrapT = THREE.RepeatWrapping;
  if (srgb) t.colorSpace = THREE.SRGBColorSpace;
  t.anisotropy = 4;
  return t;
};

// facadeTextures are one S x S patch of wall: a 5 x 6 grid of windows. map is the wall
// (white, tinted per building by vertex colour) with dark panes; glow lights about a
// third of the panes, warm or cool. The bright "awake" windows are instanced on the
// same grid.
function facadeTextures() {
  const mk = () => { const c = document.createElement("canvas"); c.width = c.height = 128; return [c, c.getContext("2d")]; };
  const [mc, m] = mk(), [gc, g] = mk(), r = rand(21);
  m.fillStyle = "#ffffff"; m.fillRect(0, 0, 128, 128);
  m.fillStyle = "#d9dbe0"; for (let j = 0; j < 6; j++) m.fillRect(0, j * (128 / 6) + 19, 128, 2); // floor lines
  g.fillStyle = "#000"; g.fillRect(0, 0, 128, 128);
  const cw = 128 / 5, ch = 128 / 6;
  for (let j = 0; j < 6; j++) for (let i = 0; i < 5; i++) {
    const x = i * cw + 5, y = j * ch + 4, w = cw - 10, h = ch - 9, k = r();
    m.fillStyle = "#1c2430"; m.fillRect(x, y, w, h);
    if (k < 0.34) {
      g.fillStyle = k < 0.2 ? "#a06a28" : "#5d8aa0";
      g.fillRect(x, y, w, h);
      g.fillStyle = "rgba(0,0,0,.35)"; g.fillRect(x, y + h * 0.55, w, h * 0.45); // a lit room's lower half is darker
    }
  }
  return { map: texture(mc), glow: texture(gc) };
}

// rectsOf finds the solid rectangles of tile ch (city blocks are rectangles).
export function rectsOf(tiles, ch) {
  const H = tiles.length, W = tiles[0].length, seen = new Set(), out = [];
  for (let y = 0; y < H; y++) for (let x = 0; x < W; x++) {
    if (tiles[y][x] !== ch || seen.has(y * W + x)) continue;
    let w = 0, h = 0;
    while (x + w < W && tiles[y][x + w] === ch) w++;
    while (y + h < H && tiles[y + h].slice(x, x + w) === ch.repeat(w)) h++;
    for (let j = 0; j < h; j++) for (let i = 0; i < w; i++) seen.add((y + j) * W + x + i);
    out.push({ x, y, w, h });
  }
  return out;
}

// buildingGeometry merges every building: sides (group 0, facade texture, UVs in world
// units so windows keep one size) and roofs (group 1). Vertex colours give each building
// its facade and roof colour.
function buildingGeometry(boxes) {
  const sides = { p: [], n: [], uv: [], c: [] }, roofs = { p: [], n: [], uv: [], c: [] };
  const quad = (q, a, b, c, d, n, uv, col) => {
    for (const i of [0, 1, 2, 0, 2, 3]) { q.p.push(...[a, b, c, d][i]); q.n.push(...n); q.uv.push(...uv[i]); q.c.push(col.r, col.g, col.b); }
  };
  for (const { x0, z0, x1, z1, h, ou, ov, color, roof } of boxes) {
    const col = new THREE.Color(color), top = new THREE.Color(roof);
    const uvs = (u0, u1) => [[u0 / S + ou, ov], [u1 / S + ou, ov], [u1 / S + ou, ov + h / S], [u0 / S + ou, ov + h / S]];
    quad(sides, [x0, 0, z1], [x1, 0, z1], [x1, h, z1], [x0, h, z1], [0, 0, 1], uvs(x0, x1), col);
    quad(sides, [x1, 0, z0], [x0, 0, z0], [x0, h, z0], [x1, h, z0], [0, 0, -1], uvs(x1, x0), col);
    quad(sides, [x1, 0, z1], [x1, 0, z0], [x1, h, z0], [x1, h, z1], [1, 0, 0], uvs(z1, z0), col);
    quad(sides, [x0, 0, z0], [x0, 0, z1], [x0, h, z1], [x0, h, z0], [-1, 0, 0], uvs(z0, z1), col);
    quad(roofs, [x0, h, z1], [x1, h, z1], [x1, h, z0], [x0, h, z0], [0, 1, 0], [[0, 0], [1, 0], [1, 1], [0, 1]], top);
  }
  const g = new THREE.BufferGeometry(), all = (k) => [...sides[k], ...roofs[k]];
  g.setAttribute("position", new THREE.Float32BufferAttribute(all("p"), 3));
  g.setAttribute("normal", new THREE.Float32BufferAttribute(all("n"), 3));
  g.setAttribute("uv", new THREE.Float32BufferAttribute(all("uv"), 2));
  g.setAttribute("color", new THREE.Float32BufferAttribute(all("c"), 3));
  g.addGroup(0, sides.p.length / 3, 0);
  g.addGroup(sides.p.length / 3, roofs.p.length / 3, 1);
  return g;
}

// instanced builds a static InstancedMesh from [matrix, colour?] pairs.
function instanced(geo, mat, items, shadow = false) {
  const m = new THREE.InstancedMesh(geo, mat, Math.max(items.length, 1));
  items.forEach(([mx, col], i) => { m.setMatrixAt(i, mx); if (col !== undefined) m.setColorAt(i, new THREE.Color(col)); });
  m.count = items.length;
  m.castShadow = shadow;
  m.receiveShadow = shadow;
  return m;
}

const UP = new THREE.Vector3(0, 1, 0);
const mtx = (x, y, z, ry = 0, sx = 1, sy = 1, sz = 1) => new THREE.Matrix4().compose(new THREE.Vector3(x, y, z),
  new THREE.Quaternion().setFromAxisAngle(UP, ry), new THREE.Vector3(sx, sy, sz));

// glowSpot is a soft round spot for halos and lamp pools.
function glowSpot() {
  const c = document.createElement("canvas");
  c.width = c.height = 64;
  const g = c.getContext("2d"), grd = g.createRadialGradient(32, 32, 0, 32, 32, 32);
  grd.addColorStop(0, "rgba(255,255,255,.6)"); grd.addColorStop(1, "rgba(255,255,255,0)");
  g.fillStyle = grd; g.fillRect(0, 0, 64, 64);
  return new THREE.CanvasTexture(c);
}

// fadeUniforms is shared by every material that fade() patches: the camera, the point
// to keep visible (the local player) and the radius of the see-through tube between
// them (0 turns fading off).
export const fadeUniforms = () => ({ uFadeCam: { value: new THREE.Vector3() }, uFadeAt: { value: new THREE.Vector3() }, uFadeR: { value: 0 } });

// fade patches mat so any part of it inside the tube from the camera to the player is
// drawn as a sparse screen door (3 pixels in 4 dropped): a building in front of you
// turns see-through, and the opaque pass needs no sorting.
export function fade(mat, u) {
  mat.onBeforeCompile = (sh) => {
    Object.assign(sh.uniforms, u);
    sh.vertexShader = sh.vertexShader.replace("#include <common>", "#include <common>\nvarying vec3 vFadeW;")
      .replace("#include <project_vertex>", `#include <project_vertex>
vec4 fadeW = vec4(transformed, 1.0);
#ifdef USE_INSTANCING
fadeW = instanceMatrix * fadeW;
#endif
vFadeW = (modelMatrix * fadeW).xyz;`);
    sh.fragmentShader = sh.fragmentShader.replace("#include <common>", `#include <common>
uniform vec3 uFadeCam; uniform vec3 uFadeAt; uniform float uFadeR; varying vec3 vFadeW;
bool fadedOut() {
  if (uFadeR <= 0.0) return false;
  vec3 d = uFadeAt - uFadeCam; float len = length(d); vec3 n = d / len;
  float t = dot(vFadeW - uFadeCam, n);
  if (t <= 0.0 || t >= len - 0.5 || length(vFadeW - uFadeCam - n * t) > uFadeR) return false;
  vec2 q = mod(floor(gl_FragCoord.xy), 2.0);
  return q.x + q.y > 0.0;
}`).replace("void main() {", "void main() {\n  if (fadedOut()) discard;");
  };
  mat.customProgramCacheKey = () => "fade";
  return mat;
}

// buildTown adds the static city to scene and returns {setAwake(n), districts}.
// setAwake lights n bright windows, one per busy worker (an awake gVisor sandbox).
// opts.fade: fadeUniforms() for see-through buildings; opts.maxHeight caps the towers
// (the phone's follow-cam uses 2.5 so players on near lanes stay visible).
export function buildTown(scene, city, { fade: fu = fadeUniforms(), maxHeight = Infinity } = {}) {
  const W = city.w, H = city.h, tiles = city.tiles, r = rand(11), F = (m) => fade(m, fu);
  const spot = glowSpot(), additive = { transparent: true, blending: THREE.AdditiveBlending, depthWrite: false };

  // Asphalt everywhere; every block is a raised sidewalk whose edge is the curb.
  const ground = new THREE.Mesh(new THREE.PlaneGeometry(W + 60, H + 60).rotateX(-Math.PI / 2),
    new THREE.MeshStandardMaterial({ color: 0x1a1c21, roughness: 0.85, metalness: 0.1 }));
  ground.position.set(W / 2, 0, H / 2);
  ground.receiveShadow = true;
  scene.add(ground);
  const blocks = rectsOf(tiles, "#"); // Dispatch and the Vault have their own floors
  scene.add(instanced(new THREE.BoxGeometry(1, 0.07, 1), new THREE.MeshStandardMaterial({ color: 0x3a3d44, roughness: 0.9 }),
    blocks.map((b) => [mtx(b.x + b.w / 2, 0.035, b.y + b.h / 2, 0, b.w, 1, b.h)]), true));

  // Buildings: each block holds one, two or four buildings, set back from the curb:
  // towers (2.2-3.5), mid-rises (1.2-2.2) and low shops (0.6-0.9) with an awning.
  const IN = 0.25, boxes = [];
  for (const b of blocks) {
    const parts = [], k = r();
    if (b.w >= 4 && b.h >= 4 && k < 0.4) {
      const sx = 2 + (r() < 0.5 ? 0 : 1), sy = 2 + (r() < 0.5 ? 0 : 1);
      parts.push([0, 0, sx, sy], [sx, 0, b.w, sy], [0, sy, sx, b.h], [sx, sy, b.w, b.h]);
    } else if (k < 0.8) {
      const s = 2 + Math.floor(r() * 2);
      parts.push(...(r() < 0.5 ? [[0, 0, s, b.h], [s, 0, b.w, b.h]] : [[0, 0, b.w, s], [0, s, b.w, b.h]]));
    } else parts.push([0, 0, b.w, b.h]);
    for (const [a, c, d, e] of parts) {
      const kind = r(), shop = kind < 0.28, tower = kind > 0.72;
      // A tower steps back from the street a little more; shops come right up to it.
      const inset = shop ? IN : IN + (tower ? 0.15 : 0.05);
      boxes.push({
        x0: b.x + a + (a === 0 ? inset : 0.04), z0: b.y + c + (c === 0 ? inset : 0.04),
        x1: b.x + d - (d === b.w ? inset : 0.04), z1: b.y + e - (e === b.h ? inset : 0.04),
        h: Math.min(maxHeight, shop ? 0.6 + r() * 0.3 : tower ? 2.2 + r() * 1.3 : 1.2 + r() * 1.0), shop,
        color: FACADES[Math.floor(r() * FACADES.length)], roof: ROOFS[Math.floor(r() * ROOFS.length)],
        // Pane offsets are whole panes, so the windows stay on one world grid.
        ou: Math.floor(r() * 5) / 5, ov: Math.floor(r() * 6) / 6,
        neon: !shop && r() < 0.18 ? NEON[Math.floor(r() * 3)] : null,
      });
    }
  }
  const tex = facadeTextures();
  const towers = new THREE.Mesh(buildingGeometry(boxes), [
    F(new THREE.MeshStandardMaterial({ vertexColors: true, map: tex.map, emissive: 0xffffff, emissiveMap: tex.glow, emissiveIntensity: 1.1, roughness: 0.75, metalness: 0.1 })),
    F(new THREE.MeshStandardMaterial({ vertexColors: true, roughness: 0.9 })),
  ]);
  towers.castShadow = towers.receiveShadow = true;
  scene.add(towers);

  // Rooftops: AC units, water tanks and antennas; shops get an awning and a lit window.
  const ac = [], tanks = [], masts = [], awnings = [], shopGlow = [];
  for (const b of boxes) {
    const w = b.x1 - b.x0, d = b.z1 - b.z0, at = (fx, fz) => [b.x0 + 0.3 + fx * (w - 0.6), b.z0 + 0.3 + fz * (d - 0.6)];
    if (b.shop) {
      const col = [0xb8423a, 0x2f6f8f, 0x3f7a4a, 0xc0902a][Math.floor(r() * 4)];
      awnings.push([mtx((b.x0 + b.x1) / 2, 0.42, b.z1 + 0.12, 0, w - 0.2, 0.05, 0.28), col]);
      shopGlow.push([mtx((b.x0 + b.x1) / 2, 0.22, b.z1 + 0.011, 0, w - 0.4, 0.26, 1), r() < 0.5 ? WARM : COOL]);
    }
    for (let i = 0, n = Math.floor(r() * 3) + (b.shop ? 0 : 1); i < n; i++) {
      const [x, z] = at(r(), r()), k = r();
      if (k < 0.5) ac.push([mtx(x, b.h + 0.1, z, r() * 3)]);
      else if (k < 0.75 && !b.shop) tanks.push([mtx(x, b.h + 0.2, z)]);
      else masts.push([mtx(x, b.h + 0.3, z)]);
    }
  }
  const grey = F(new THREE.MeshStandardMaterial({ color: 0x767b84, roughness: 0.7 }));
  scene.add(instanced(new THREE.BoxGeometry(0.34, 0.2, 0.28), grey, ac));
  scene.add(instanced(new THREE.CylinderGeometry(0.2, 0.2, 0.4, 10), F(new THREE.MeshStandardMaterial({ color: 0x6a4a34, roughness: 0.8 })), tanks));
  scene.add(instanced(new THREE.CylinderGeometry(0.015, 0.025, 0.6, 4), grey, masts));
  scene.add(instanced(new THREE.BoxGeometry(1, 1, 1), F(new THREE.MeshStandardMaterial({ roughness: 0.6 })), awnings));
  scene.add(instanced(new THREE.PlaneGeometry(1, 1), F(new THREE.MeshBasicMaterial({ toneMapped: false })), shopGlow));

  // Neon: a sign bar high on the south face, or a blade on a corner, with a soft halo.
  const signs = [], halos = [];
  for (const b of boxes) {
    if (b.neon === null) continue;
    if (r() < 0.6 && b.x1 - b.x0 > 1.4) {
      const w = Math.min(b.x1 - b.x0 - 0.4, 1 + r() * 1.2), x = (b.x0 + b.x1) / 2, y = b.h - 0.3 - r() * 0.3;
      signs.push([mtx(x, y, b.z1 + 0.03, 0, w, 0.16, 0.05), b.neon]);
      halos.push([mtx(x, y, b.z1 + 0.05, 0, w + 0.9, 0.8, 1), b.neon]);
    } else {
      const hh = Math.min(b.h - 0.5, 1.4), y = b.h - 0.25 - hh / 2;
      signs.push([mtx(b.x1 - 0.05, y, b.z1 + 0.18, 0, 0.07, hh, 0.36), b.neon]);
      halos.push([mtx(b.x1 + 0.02, y, b.z1 + 0.18, Math.PI / 2, 0.9, hh + 0.6, 1), b.neon]);
    }
  }
  scene.add(instanced(new THREE.BoxGeometry(1, 1, 1), F(new THREE.MeshBasicMaterial({ toneMapped: false })), signs));
  scene.add(instanced(new THREE.PlaneGeometry(1, 1), new THREE.MeshBasicMaterial({ map: spot, ...additive }), halos));

  // Street lights on every block corner, with a warm pool of light on the road.
  const poles = [], heads = [], pools = [];
  for (const b of blocks) {
    for (const [x, z, ry] of [[b.x + 0.12, b.y + 0.12, -Math.PI * 0.75], [b.x + b.w - 0.12, b.y + 0.12, -Math.PI * 0.25],
      [b.x + 0.12, b.y + b.h - 0.12, Math.PI * 0.75], [b.x + b.w - 0.12, b.y + b.h - 0.12, Math.PI * 0.25]]) {
      const dx = Math.sin(ry) * 0.35, dz = Math.cos(ry) * 0.35;
      poles.push([mtx(x, 0.6, z)]);
      heads.push([mtx(x + dx, 1.18, z + dz, ry)]);
      pools.push([mtx(x + dx * 2, 0.02, z + dz * 2, 0, 2.2, 1, 2.2)]);
    }
  }
  scene.add(instanced(new THREE.CylinderGeometry(0.03, 0.04, 1.2, 5), new THREE.MeshStandardMaterial({ color: 0x2d3138 }), poles));
  scene.add(instanced(new THREE.BoxGeometry(0.12, 0.05, 0.3), new THREE.MeshBasicMaterial({ color: 0xffe2a8, toneMapped: false }), heads));
  scene.add(instanced(new THREE.PlaneGeometry(1, 1).rotateX(-Math.PI / 2), new THREE.MeshBasicMaterial({ map: spot, color: 0x8a6a3a, ...additive }), pools));

  // Awake windows: bright panes on the window grid of the taller buildings.
  const slots = [];
  for (const b of boxes) {
    if (b.shop) continue;
    const along = (lo, hi) => { const out = []; for (let v = (Math.ceil(lo / PANE_W - 0.5) + 0.5) * PANE_W; v < hi - 0.2; v += PANE_W) if (v > lo + 0.2) out.push(v); return out; };
    // Whole-pane texture offsets keep pane centres on the world grid (n + 0.5) * PANE.
    for (let y = PANE_H * 1.5; y < b.h - 0.25; y += PANE_H) {
      for (const x of along(b.x0, b.x1)) slots.push(mtx(x, y, b.z1 + 0.012));
      for (const z of along(b.z0, b.z1)) slots.push(mtx(b.x1 + 0.012, y, z, Math.PI / 2), mtx(b.x0 - 0.012, y, z, -Math.PI / 2));
    }
  }
  const sr = rand(5);
  for (let i = slots.length - 1; i > 0; i--) { const j = Math.floor(sr() * (i + 1)); [slots[i], slots[j]] = [slots[j], slots[i]]; }
  const awake = instanced(new THREE.PlaneGeometry(PANE_W * 0.6, PANE_H * 0.55), F(new THREE.MeshBasicMaterial({ toneMapped: false })),
    slots.slice(0, 400).map((m, i) => [m, i % 3 === 0 ? COOL : WARM]));
  awake.count = 0;
  scene.add(awake);
  const setAwake = (n) => { awake.count = Math.min(n, slots.length, 400); };

  // Road markings: yellow centre dashes and white edge lines along every road run
  // (whatever its width; a road is at most MAX_ROAD tiles across and longer than it is
  // wide, and junctions get none), and white zebra crossings where a road meets one.
  const MAX_ROAD = 4, road = (x, y) => x >= 0 && y >= 0 && x < W && y < H && tiles[y][x] === ".";
  const run = (x, y, dx, dy) => {
    let a = 0, b = 0;
    while (road(x - (a + 1) * dx, y - (a + 1) * dy)) a++;
    while (road(x + (b + 1) * dx, y + (b + 1) * dy)) b++;
    return [-a, a + b + 1];
  };
  const junction = (x, y) => road(x, y) && run(x, y, 1, 0)[1] > MAX_ROAD && run(x, y, 0, 1)[1] > MAX_ROAD;
  const yellow = [], white = [], zebra = [], done = new Set();
  for (let y = 0; y < H; y++) for (let x = 0; x < W; x++) {
    if (!road(x, y)) continue;
    for (const [vertical, cx, cy] of [[false, 0, 1], [true, 1, 0]]) {
      const [off, across] = run(x, y, cx, cy); // the road's width here
      if (across > MAX_ROAD || run(x, y, cy, cx)[1] <= across + 1) continue; // open area or junction
      const lo = (vertical ? x : y) + off, key = vertical ? `v${y},${lo}` : `h${x},${lo}`;
      const at = (c, len, ry) => (vertical ? mtx(c, 0.012, y + 0.5, Math.PI / 2 + ry, len, 1, 1) : mtx(x + 0.5, 0.012, c, ry, len, 1, 1));
      if (!done.has(key)) {
        done.add(key);
        yellow.push([at(lo + across / 2, 0.5, 0)]);
        // Edge lines only where a curb runs alongside (not at the map border).
        for (const [c, side] of [[lo + 0.18, -1], [lo + across - 0.18, 1]]) {
          const beyond = side < 0 ? lo - 1 : lo + across;
          if (beyond >= 0 && beyond < (vertical ? W : H)) white.push([at(c, 1, 0)]);
        }
      }
      if (junction(x + cy, y + cx) || junction(x - cy, y - cx)) {
        for (const o of [0.25, 0.75]) zebra.push([vertical ? mtx(x + o, 0.013, y + 0.5, Math.PI / 2, 0.7, 1, 1) : mtx(x + 0.5, 0.013, y + o, 0, 0.7, 1, 1)]);
      }
    }
  }
  const flat = (w, d) => new THREE.PlaneGeometry(w, d).rotateX(-Math.PI / 2);
  scene.add(instanced(flat(1, 0.07), new THREE.MeshBasicMaterial({ color: 0xe6b84a }), yellow));
  scene.add(instanced(flat(1, 0.045), new THREE.MeshBasicMaterial({ color: 0xb8bcc4 }), white));
  scene.add(instanced(flat(1, 0.12), new THREE.MeshBasicMaterial({ color: 0xd8dde6, transparent: true, opacity: 0.4 }), zebra));

  // District borders: a faint dashed line where one node's blocks end and the next begin.
  const districts = Object.entries(city.districts || {}).sort((a, b) => a[1].x - b[1].x);
  const pts = districts.slice(1).flatMap(([, d]) => [d.x + 0.5, 0.02, 0, d.x + 0.5, 0.02, H]);
  if (pts.length) {
    const g = new THREE.BufferGeometry();
    g.setAttribute("position", new THREE.Float32BufferAttribute(pts, 3));
    scene.add(new THREE.LineSegments(g, new THREE.LineDashedMaterial({ color: 0x5ef0ff, dashSize: 0.3, gapSize: 0.3, transparent: true, opacity: 0.3 })).computeLineDistances());
  }

  return { setAwake, districts };
}
