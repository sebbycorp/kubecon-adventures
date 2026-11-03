// Arcade-wide bits any page can load (served at /static/arcade.js): a game's live
// leaderboard from GET /api/boards, and the call-to-action buttons for kagent.dev and
// agentgateway.dev. Classic script: it defines window.Arcade.
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
.ab-ctas.ab-lg .ab-cta img { width: 22px; height: 22px; }`;
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
  let last = null, timer = null;
  async function refresh() {
    try {
      last = await (await fetch("/api/boards", { cache: "no-store" })).json();
    } catch {
      return;
    }
    for (const w of widgets) draw(w);
  }
  function draw(w) {
    const b = (last || []).find((x) => x.path === w.path);
    if (!b) return;
    const rows = (b.rows || []).slice(0, w.n);
    if (!rows.length) { w.el.innerHTML = `<div class="ab-empty">${esc(w.empty)}</div>`; return; }
    w.el.innerHTML = rows.map((r, i) =>
      `<div class="ab-row${i === 0 ? " ab-top" : ""}"><span class="ab-rank">${i + 1}</span><i class="ab-dot" style="background:${color(r.color)}"></i>` +
      `<span class="ab-who"><b>${esc(r.nick)}</b>${w.detail && r.detail ? `<small>${esc(r.detail)}</small>` : ""}</span>` +
      `<span class="ab-score">${Number(r.score).toLocaleString()}${w.unit ? `<small>${esc(b.unit)}</small>` : ""}</span></div>`).join("");
  }
  // board keeps el showing the top n of the game at path ("/duck-hunt"), live.
  function board(el, path, { n = 5, detail = true, unit = true, empty = "No scores yet. Be the first." } = {}) {
    if (!el) return;
    el.classList.add("ab-board");
    const w = { el, path, n, detail, unit, empty };
    widgets.push(w);
    if (last) draw(w);
    if (!timer) { refresh(); timer = setInterval(refresh, 5000); }
  }
  window.Arcade = { board, ctas };
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
