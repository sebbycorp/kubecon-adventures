// Tunes Gateway Defense: plays many headless runs with scripted strategies on sim.js
// and prints when they first lose a life, where they fall, and how often they beat the
// boss. Not served to phones.
//   node balance.mjs <rules.json> [runs]
// (dump rules.json with: GATEDEF_RULES_OUT=/tmp/rules.json go test -run TestDumpRules ./games/gatewaydef/)
import { readFileSync } from "node:fs";
import { createSim, waveAt } from "./sim.js";

const rules = JSON.parse(readFileSync(process.argv[2], "utf8"));
const RUNS = +(process.argv[3] || 40);
const MAXWAVE = rules.waves.length + 10;

// Pads ranked by how much lane each tower type would cover from them.
function coverage(sim, pad, range) {
  const [px, py] = rules.map.pads[pad];
  let n = 0;
  for (let s = 0; s < sim.LANE; s += 0.25) {
    const [x, y] = sim.posAt(s);
    if (Math.hypot(x - px - 0.5, y - py - 0.5) <= range) n++;
  }
  return n;
}

// A strategy decides, between and during waves, what to build and upgrade.
// focused: covers the next wave's weaknesses, upgrades the guard before shielded waves,
// stacks and upgrades the most-needed type, and spends everything; ties are broken at
// random. This is the "decent player" the rules are tuned against.
// spread: plans two waves ahead and keeps two of each needed type: it spreads its money
// thin, so it falls earlier.
function play(kind, rnd) {
  const sim = createSim(rules, {});
  const tk = sim.towerKind;
  const ranked = {};
  for (const t of rules.towers) {
    ranked[t.id] = rules.map.pads.map((_, i) => i).sort((a, b) => coverage(sim, b, t.range) - coverage(sim, a, t.range) + (rnd() - 0.5) * 6);
  }
  const ahead = kind === "spread" ? 2 : 1, per = kind === "spread" ? 2 : 1;
  function needs() {
    const need = {};
    for (let n = sim.st.wave + 1; n <= sim.st.wave + ahead; n++) {
      const w = waveAt(rules, n);
      for (const id of w.mix) need[sim.creepKind[id].weak] = (need[sim.creepKind[id].weak] || 0) + 1;
      if (w.boss) need.suspend = (need.suspend || 0) + 3;
    }
    return need;
  }
  function spend() {
    for (let guard = 0; guard < 40; guard++) {
      const st = sim.st, need = needs();
      const have = (id) => st.towers.filter((t) => t.id === id).length;
      const nextMix = waveAt(rules, st.wave + 1).mix.concat(kind === "spread" ? waveAt(rules, st.wave + 2).mix : []);
      // A guard that can read obfuscated jailbreaks, before they show up.
      if (nextMix.some((id) => sim.creepKind[id].shield)) {
        const g = st.towers.findIndex((t) => t.id === "guard" && t.level < rules.shieldLevel);
        if (g >= 0 && !st.towers.some((t) => t.id === "guard" && t.level >= rules.shieldLevel)) {
          if (sim.upgrade(g)) continue;
          if (st.money < sim.upCost(st.towers[g])) return;
        }
      }
      // Every needed type, `per` of each, most-needed first.
      const want = Object.entries(need).sort((a, b) => b[1] - a[1]).map(([id]) => id);
      let acted = false;
      for (const id of want) {
        if (have(id) >= per) continue;
        const pad = ranked[id].find((p) => sim.towerAt(p) < 0);
        if (pad === undefined) continue;
        if (sim.build(pad, id)) { acted = true; break; }
        if (st.money < tk[id].cost) return; // save up for it
      }
      if (acted) continue;
      // Then alternate: upgrade the weakest needed tower, or add another of the top need.
      const ups = st.towers.map((t, i) => [t, i]).filter(([t]) => need[t.id] && t.level < tk[t.id].maxLevel)
        .sort((a, b) => (kind === "spread" ? (need[b[0].id] - need[a[0].id]) || a[0].level - b[0].level : a[0].level - b[0].level));
      const top = want[Math.floor(rnd() * Math.min(2, want.length))];
      const addFirst = st.towers.length < 5 + st.wave / 2 && rnd() < 0.6;
      const tryAdd = () => { const pad = top && ranked[top].find((p) => sim.towerAt(p) < 0); return pad !== undefined && top && sim.build(pad, top); };
      if (addFirst && tryAdd()) continue;
      if (ups.length && sim.upgrade(ups[0][1])) continue;
      if (!addFirst && tryAdd()) continue;
      return;
    }
  }
  let t = 0;
  while (sim.st.phase !== "over" && sim.st.wave < MAXWAVE) {
    if (sim.st.phase === "build") { spend(); sim.startWave(); }
    sim.step(1 / 30);
    if (++t % 30 === 0) spend(); // a player keeps building during a wave
    if (t > 30 * 60 * 90) break;
  }
  const st = sim.st;
  return { firstLeak: st.firstLeakWave || null, end: st.wave, won: st.won, score: st.score, lives: st.lives };
}

function seeded(seed) {
  return () => ((seed = (seed * 1664525 + 1013904223) >>> 0) / 4294967296);
}

for (const kind of ["focused", "spread"]) {
  const res = [];
  for (let i = 0; i < RUNS; i++) res.push(play(kind, seeded(i * 7919 + 1)));
  const med = (a) => { const s = a.filter((v) => v != null).sort((x, y) => x - y); return s.length ? s[Math.floor(s.length / 2)] : "-"; };
  const leakBy5 = res.filter((r) => r.firstLeak && r.firstLeak <= 5).length;
  const won = res.filter((r) => r.won).length;
  const ends = {};
  for (const r of res) ends[r.end] = (ends[r.end] || 0) + 1;
  console.log(`${kind.padEnd(6)} runs ${RUNS} · first leak median wave ${med(res.map((r) => r.firstLeak))} · leaked by wave 5: ${leakBy5}/${RUNS}` +
    ` · fell on wave median ${med(res.map((r) => r.end))} · beat the boss ${won}/${RUNS} · score median ${med(res.map((r) => r.score))}`);
  console.log("        fell on wave: " + Object.entries(ends).sort((a, b) => a[0] - b[0]).map(([w, n]) => `${w}:${n}`).join(" "));
}
