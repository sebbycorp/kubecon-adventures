// Arcade-wide bits any page can load (served at /static/arcade.js): a game's live
// leaderboard from GET /api/boards, the call-to-action buttons for kagent.dev and
// agentgateway.dev, and the share-my-score buttons for a game's end screen. Classic
// script: it defines window.Arcade.
"use strict";
(() => {
  const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
  const COLORS = { green: "#5fd068", blue: "#60a5fa", red: "#f87171", purple: "#a78bfa", gold: "#fbbf24", pink: "#f472b6" };
  const color = (c) => COLORS[c] || (/^#[0-9a-f]{6}$/i.test(c || "") ? c : "#8a8f94");

  // One stylesheet for both widgets. They take the page's font and text colour; --ab-accent
  // sets the highlight (scores, borders).
  const css = `
.ab-board { display: grid; gap: 4px; font-variant-numeric: tabular-nums; }
.ab-row { display: grid; grid-template-columns: 1.6em 10px 1fr auto; gap: 8px; align-items: center; padding: 5px 8px; border-radius: 8px; background: rgba(255,255,255,.04); }
.ab-row.ab-top { background: color-mix(in srgb, var(--ab-accent, #5fd068) 14%, transparent); }
.ab-rank { opacity: .7; font-weight: 700; }
.ab-dot { width: 9px; height: 9px; border-radius: 50%; }
.ab-who { min-width: 0; overflow: hidden; }
.ab-who b { display: block; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; font-weight: 600; }
.ab-who small { display: block; opacity: .62; font-size: .82em; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.ab-score { font-weight: 800; color: var(--ab-accent, #5fd068); text-align: right; }
.ab-score small { display: block; font-weight: 400; opacity: .62; font-size: .72em; color: inherit; }
.ab-empty { opacity: .62; padding: 6px 2px; }
.ab-row.ab-me { outline: 2px solid var(--ab-accent, #5fd068); outline-offset: -2px; }
.ab-medal { font-size: 1.15em; line-height: 1; }
.ab-block { margin: 12px 0 4px; }
.ab-block > .ab-h { font-size: 11px; letter-spacing: .12em; opacity: .7; margin: 0 0 6px; text-transform: uppercase; }
.ab-block .ab-ctas { margin-top: 2px; }
.ab-ctas { display: flex; flex-wrap: wrap; gap: 8px; }
.ab-cta { display: inline-flex; align-items: center; gap: 8px; text-decoration: none; border-radius: 999px; font: 600 13px Inter, system-ui, sans-serif; padding: 8px 14px 8px 10px;
  border: 1px solid #3d7a45; color: #e8e8e8; background: #111412; transition: border-color .15s, box-shadow .15s; letter-spacing: 0; text-transform: none; }
.ab-cta img { width: 18px; height: 18px; }
.ab-cta.ab-primary { background: #5fd068; border-color: #5fd068; color: #06140a; }
.ab-cta:hover { border-color: #5fd068; box-shadow: 0 0 22px #5fd06833; }
.ab-ctas.ab-sm .ab-cta { font-size: 11.5px; padding: 5px 10px 5px 7px; gap: 6px; }
.ab-ctas.ab-sm .ab-cta img { width: 14px; height: 14px; }
.ab-ctas.ab-lg .ab-cta { font-size: 15px; padding: 11px 18px 11px 13px; }
.ab-ctas.ab-lg .ab-cta img { width: 22px; height: 22px; }
.ab-share { display: grid; gap: 8px; margin: 10px 0 4px; }
.ab-share-main { width: 100%; border: 0; border-radius: 12px; padding: 13px 16px; font: inherit; font-weight: 800; letter-spacing: .06em; cursor: pointer;
  background: var(--ab-accent, #5fd068); color: #071012; box-shadow: 0 0 24px color-mix(in srgb, var(--ab-accent, #5fd068) 35%, transparent); }
.ab-share-row { display: grid; grid-template-columns: repeat(4, 1fr); gap: 6px; }
.ab-share-row button { border: 1px solid color-mix(in srgb, var(--ab-accent, #5fd068) 45%, #2a2d30); border-radius: 10px; padding: 8px 4px; font: inherit; font-size: .82em; font-weight: 700;
  background: rgba(0,0,0,.35); color: inherit; cursor: pointer; white-space: nowrap; }
.ab-share-row button:active, .ab-share-main:active { transform: scale(.97); }
.ab-share-note { min-height: 1.2em; font-size: .8em; opacity: .75; text-align: center; }`;
  const style = document.createElement("style");
  style.textContent = css;
  document.head.appendChild(style);

  // ctas renders the two buttons into el; size is "sm", "md" or "lg".
  function ctas(el, size = "md") {
    if (!el) return;
    el.classList.add("ab-ctas", "ab-" + size);
    el.innerHTML =
      `<a class="ab-cta ab-primary" href="https://kagent.dev" target="_blank" rel="noopener"><img src="/agent-flow/static/kagent-icon.svg" alt="">kagent.dev</a>` +
      `<a class="ab-cta" href="https://agentgateway.dev" target="_blank" rel="noopener"><img src="/agent-flow/static/agentgateway-icon.svg" alt="">agentgateway.dev</a>`;
  }

  // Boards are fetched once for every widget on the page, every 5 s.
  const widgets = [];
  let last = null, timer = null, myNick = null;
  fetch("/api/me").then((r) => (r.ok ? r.json() : null)).then((m) => { myNick = m && m.nick; widgets.forEach(draw); }).catch(() => {});
  const MEDALS = ["🥇", "🥈", "🥉"];
  async function refresh() {
    try {
      last = await (await fetch("/api/boards", { cache: "no-store" })).json();
    } catch {
      return;
    }
    // Widgets whose element left the page (a TV page turn) are dropped.
    for (let i = widgets.length - 1; i >= 0; i--) if (!widgets[i].el.isConnected) widgets.splice(i, 1);
    for (const w of widgets) draw(w);
  }
  function draw(w) {
    const b = (last || []).find((x) => x.path === w.path);
    if (!b) return;
    const rows = (b.rows || []).slice(0, w.n);
    if (!rows.length) { w.el.innerHTML = `<div class="ab-empty">${esc(w.empty)}</div>`; return; }
    w.el.innerHTML = rows.map((r, i) =>
      `<div class="ab-row${i === 0 ? " ab-top" : ""}${myNick && r.nick === myNick ? " ab-me" : ""}"><span class="ab-rank">${w.medals && i < 3 ? `<span class="ab-medal">${MEDALS[i]}</span>` : i + 1}</span><i class="ab-dot" style="background:${color(r.color)}"></i>` +
      `<span class="ab-who"><b>${esc(r.nick)}</b>${w.detail && r.detail ? `<small>${esc(r.detail)}</small>` : ""}</span>` +
      `<span class="ab-score">${Number(r.score).toLocaleString()}${w.unit ? `<small>${esc(b.unit)}</small>` : ""}</span></div>`).join("");
  }
  // board keeps el showing the top n of the game at path ("/duck-hunt"), live.
  function board(el, path, { n = 5, detail = true, unit = true, medals = true, empty = "No scores yet. Be the first." } = {}) {
    if (!el) return;
    el.classList.add("ab-board");
    // One widget per element: calling board again on the same element updates it.
    for (let i = widgets.length - 1; i >= 0; i--) if (widgets[i].el === el) widgets.splice(i, 1);
    const w = { el, path, n, detail, unit, medals, empty };
    widgets.push(w);
    if (last) draw(w);
    if (!timer) { refresh(); timer = setInterval(refresh, 5000); }
  }

  // ---- share my score ----
  // Post text for each game: a score line, then a line on what is real underneath, using the
  // run's own numbers where the game has them. A few wordings each, picked at random, so the
  // posts don't all read the same. Only the nickname and the run's numbers go in.
  const SITE = "https://kubecon.agentpwned.ai";
  const TAGS = ["#kagent #AgentSubstrate #agentgateway #KubeCon", "#kagent #AgentSubstrate #KubeCon", "#kagent #AgentSubstrate"];
  const pick = (a) => a[Math.floor(Math.random() * a.length)];
  const n = (v) => Number(v || 0).toLocaleString("en-US");
  const has = (v) => v != null && v !== "" && Number(v) > 0;
  const pl = (v, one, many) => `${n(v)} ${Number(v) === 1 ? one : many}`;
  let density = null; // {actors, workers} from /api/stats, fetched when a share widget is drawn
  const SCORES = {
    "/substrate-siege": (o, f) => [
      `I scored ${n(o.score)} in Substrate Siege${has(f.freezes) ? `, freezing ${pl(f.freezes, "AI agent", "AI agents")}` : ""}${has(f.takedowns) ? ` and ${pl(f.takedowns, "player", "players")}` : ""} 🧊`,
      `${n(o.score)} points in Substrate Siege: ${pl(f.freezes, "agent", "agents")} frozen${has(f.booked) ? `, ${n(f.booked)} booked into snapshots` : ""}${has(f.takedowns) ? `, ${pl(f.takedowns, "player", "players")} iced` : ""}.`,
      `Just hunted rogue AI agents through a night city in Substrate Siege: ${n(o.score)} points 🏙️`],
    "/cluster-defense": (o, f) => [
      `I kept the cluster alive to wave ${n(f.wave)} in Cluster Defense${has(f.suspends) ? `, suspending ${n(f.suspends)} subagents` : ""} 🚀`,
      `Cluster Defense: ${n(o.score)} points, wave ${n(f.wave)}${has(f.seconds) ? `, ${n(f.seconds)}s before the workers filled up` : ""}.`,
      `Flew a ship, shot down ${n(f.suspends || o.score)} subagents before they filled the cluster 🚀`],
    "/gateway-defense": (o, f) => [
      `Wave ${n(f.wave)} in Gateway Defense, ${n(o.score)} points 🛡️`,
      `I held off rogue AI traffic to wave ${n(f.wave)} in Gateway Defense: ${n(o.score)} points.`,
      `${n(o.score)} points in Gateway Defense, my towers are real agentgateway controls 🛡️`],
    "/flappy": (o, f) => [
      `My octopus swam through ${n(f.gaps)} gaps in Flappy Substrate: ${n(o.score)} points 🐙`,
      `${n(o.score)} points in Flappy Substrate! I was the agent, and I kept it swimming 🐙`,
      `Flappy Substrate: ${n(f.gaps)} gaps, ${n(o.score)} points before my octopus hit the coral 🐙`],
    "/pod-packer": (o, f) => [
      `I packed ${n(f.lines)} rows of AI agent pods in Pod Packer: ${n(o.score)} points 🧱`,
      `${n(o.score)} points in Pod Packer${has(f.tetrises) ? `, ${pl(f.tetrises, "tetris", "tetrises")}` : ""}: Tetris where every block is a real AI agent 🧱`,
      `Pod Packer: ${n(f.lines)} lines, level ${n(f.level)}, ${n(o.score)} points before my cluster filled up 🧱`],
    "/agent-flow": (o, f) => [
      `My own AI agent just ${f.task || "finished a task"}${has(f.answerMs) ? ` in ${(f.answerMs / 1000).toFixed(1)}s` : ""} 🤖`,
      `I gave an AI agent a job at the booth: it ${f.task || "did it"}${has(f.answerMs) ? ` in ${(f.answerMs / 1000).toFixed(1)}s` : ""}.`],
    "/duck-hunt": (o, f) => [
      `${n(o.score)} points in Duck Hunt${has(f.hits) ? `: ${n(f.hits)} agents shot out of the sky` : ""} 🦆`,
      `I shot ${n(f.hits || o.score)} AI agents out of the sky in Duck Hunt 🦆`],
  };
  // facts is the "why it's amazing" line, most specific first.
  function factLines(o, f) {
    const out = [];
    const g = o.path;
    if (g === "/gateway-defense" && (has(f.b403) || has(f.b429) || has(f.suspends)))
      out.push(`Every block was a real agentgateway call: ${n(f.b403)} prompt-guard 403s, ${n(f.b429)} rate-limit 429s, ${n(f.suspends)} Agent Substrate suspends.`);
    if (g === "/pod-packer" && has(f.snaps))
      out.push(`Every row I cleared snapshotted real agents: ${n(f.snaps)} kagent + Agent Substrate snapshots${has(f.snapshotMs) ? `, ${n(f.snapshotMs)} ms each` : ""}.`);
    if (has(f.snapshotMs) && has(f.wakeMs)) out.push(`My agent was snapshotted in ${n(f.snapshotMs)} ms and woke in ${n(f.wakeMs)} ms on kagent + Agent Substrate.`);
    if (has(f.hosted) && has(f.slots)) out.push(`kagent + Agent Substrate hosted ${n(f.hosted)} agents on ${n(f.slots)} workers by snapshotting the idle ones.`);
    if (has(f.restoreMs)) out.push(`It woke from a snapshot in ${n(f.restoreMs)} ms on kagent + Agent Substrate.`);
    if (has(f.snapshotMs)) out.push(`Every ${g === "/substrate-siege" ? "freeze" : "hit"} is a real kagent + Agent Substrate snapshot (${n(f.snapshotMs)} ms).`);
    if (density && density.actors && density.workers)
      out.push(`${n(density.actors)} real AI agents on ${n(density.workers)} sandboxes, thanks to kagent + Agent Substrate.`);
    out.push(pick(["Every character is a real AI agent on kagent + Agent Substrate.", "Every character is a real AI agent, running on kagent + Agent Substrate.",
      "Real AI agents, real snapshots: kagent + Agent Substrate."]));
    return out;
  }
  // shareText builds the post: under 280 characters with the link, for X.
  function shareText(o) {
    const f = o.facts || {}, url = SITE + (o.path || "/");
    const scores = (SCORES[o.path] || ((o) => [`I scored ${n(o.score)} ${o.unit || "points"} in ${o.game}.`]))(o, f);
    const score = pick(scores), facts = factLines(o, f);
    for (const fact of facts) for (const tags of TAGS) {
      const text = `${score}\n${fact}`, full = `${text}\n${url}\n${tags}`;
      if (full.length <= 280) return { text: `${text}\n${tags}`, url, full };
    }
    const text = score.slice(0, 280 - url.length - 30);
    return { text: `${text}\n${TAGS[2]}`, url, full: `${text}\n${url}\n${TAGS[2]}` };
  }
  const open = (u) => window.open(u, "_blank", "noopener");
  async function copy(t) {
    try { await navigator.clipboard.writeText(t); return true; } catch {}
    try { const a = document.createElement("textarea"); a.value = t; document.body.appendChild(a); a.select(); const ok = document.execCommand("copy"); a.remove(); return ok; } catch { return false; }
  }
  // share renders SHARE MY SCORE (the phone's own share sheet where there is one) and
  // X, LinkedIn, Bluesky and Copy into el. o: {game, score, unit, path, facts, label}.
  function share(el, o) {
    if (!el) return;
    if (!density) fetch("/api/stats").then((r) => (r.ok ? r.json() : null)).then((s) => { if (s) density = { actors: s.actors, workers: s.workers }; }).catch(() => {});
    el.classList.add("ab-share");
    el.innerHTML = `<button type="button" class="ab-share-main">📣 ${esc(o.label || "SHARE MY SCORE")}</button>
      <div class="ab-share-row"><button type="button" data-to="x">𝕏 Post</button><button type="button" data-to="li">LinkedIn</button><button type="button" data-to="bsky">🦋 Bluesky</button><button type="button" data-to="copy">⧉ Copy</button></div>
      <div class="ab-share-note" aria-live="polite"></div>`;
    const note = (t) => { el.querySelector(".ab-share-note").textContent = t; };
    el.querySelector(".ab-share-main").onclick = async () => {
      const p = shareText(o);
      if (navigator.share) {
        try { await navigator.share({ title: `${o.game} · kagent + Agent Substrate`, text: p.text, url: p.url }); return; } catch (e) { if (e && e.name === "AbortError") return; }
      }
      note((await copy(p.full)) ? "Post text copied. Pick a network below, or paste it anywhere." : "Pick a network below.");
    };
    el.querySelectorAll("[data-to]").forEach((b) => (b.onclick = async () => {
      const p = shareText(o);
      switch (b.dataset.to) {
        case "x": open(`https://twitter.com/intent/tweet?text=${encodeURIComponent(p.text)}&url=${encodeURIComponent(p.url)}`); break;
        case "li": await copy(p.full); note("Post text copied, paste it on LinkedIn."); open(`https://www.linkedin.com/sharing/share-offsite/?url=${encodeURIComponent(p.url)}`); break;
        case "bsky": open(`https://bsky.app/intent/compose?text=${encodeURIComponent(p.full)}`); break;
        case "copy": note((await copy(p.full)) ? "Copied. Paste it anywhere." : "Couldn't copy on this browser."); break;
      }
    }));
  }
  window.Arcade = { board, ctas, share, shareText };
  // Markup can ask for them too: data-ab-board="/duck-hunt" (data-ab-n, data-ab-detail="0",
  // data-ab-unit="0", data-ab-empty) and data-ab-cta="sm|md|lg".
  function mount() {
    document.querySelectorAll("[data-ab-board]").forEach((el) => board(el, el.dataset.abBoard, {
      n: +el.dataset.abN || 5, detail: el.dataset.abDetail !== "0", unit: el.dataset.abUnit !== "0",
      empty: el.dataset.abEmpty || "No scores yet. Be the first." }));
    document.querySelectorAll("[data-ab-cta]").forEach((el) => ctas(el, el.dataset.abCta || "md"));
  }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", mount); else mount();
})();
