// Cluster Defense: the WebSocket client shared by the phone page and the big screen.
//
// connect(url, onSnap, opts) opens /cluster-defense/ws, calls onSnap(snap) for every
// snapshot, and reconnects with a 1 s, 2 s, 4 s, 8 s backoff (reset once a snapshot
// arrives). Positions are drawn a little in the past and interpolated between the two
// snapshots around that moment, so the 15 Hz stream looks smooth.
//
// The delay adapts to the network: 1.5x the p95 gap between snapshot arrivals over the
// last ~4 s, clamped to 100-250 ms (the public tunnel delivers 15 Hz with gaps of up to
// ~250 ms under load, measured 2026-11-02). If the buffer still runs dry, positions are
// extrapolated along their last velocity for up to 120 ms instead of freezing.

const TICK = 1000 / 15; // server tick, ms
const KEEP = 12; // snapshots kept for interpolation
const MIN_DELAY = 100, MAX_DELAY = 250, EXTRAPOLATE = 120;

// connect returns {send(obj), pos(kind, id), latest(), stats(), close()}.
// opts.onMsg(msg) gets every frame that is not a snapshot or a pong. opts.onStatus(s)
// gets "open", "closed" (retrying), "full" or "unauthorized" (no arcade cookie: the
// caller should send the player to join first; no retry). opts.ping: ms between pings
// (0 = never), for the round-trip time in stats(). opts.cookie: false for a big screen
// (?screen=1 needs no player), which never asks /api/me and always retries.
export function connect(url, onSnap, { onMsg = () => {}, onStatus = () => {}, ping = 0, cookie = true } = {}) {
  let ws = null, backoff = 1000, timer = 0, closed = false, opened = false;
  let buf = []; // {wt, maps, snap}, oldest first
  let offset = null; // performance.now() - world time (ms) for the fastest recent packet
  let delay = MIN_DELAY;
  const gaps = []; // recent arrival gaps, ms
  let lastAt = 0, rtt = 0, pingSeq = 0;
  const pings = new Map();

  function open() {
    opened = false;
    ws = new WebSocket(url);
    // The backoff resets on the first snapshot, not on open: a full city accepts the
    // socket, says {"t":"full"} and closes, and must not be retried every second.
    ws.onopen = () => { opened = true; onStatus("open"); };
    ws.onmessage = (ev) => {
      let m;
      try { m = JSON.parse(ev.data); } catch { return; }
      if (m.t === "snap") { backoff = 1000; take(m); onSnap(m); return; }
      if (m.t === "pong") {
        const t0 = pings.get(m.id);
        if (t0) { pings.delete(m.id); rtt = rtt ? rtt * 0.7 + (performance.now() - t0) * 0.3 : performance.now() - t0; }
        return;
      }
      if (m.t === "full") onStatus("full");
      onMsg(m);
    };
    ws.onclose = async () => {
      ws = null;
      if (closed) return;
      if (!opened && cookie) {
        // A refused upgrade hides its status; ask the arcade whether we have a player.
        const me = await fetch("/api/me").catch(() => null);
        if (me && me.status === 401) { onStatus("unauthorized"); return; }
      }
      onStatus("closed");
      timer = setTimeout(open, backoff);
      backoff = Math.min(backoff * 2, 8000);
    };
  }

  function take(s) {
    const at = performance.now();
    // now is rounded to 10 ms on the wire; snap it back onto the 1/15 s tick grid.
    const wt = Math.round((s.now * 1000) / TICK) * TICK;
    const last = buf[buf.length - 1];
    // A reset restarts world time; a long stall jumps it. Either way start over.
    if (last && (wt < last.wt - 500 || wt > last.wt + 3000)) { buf = []; offset = null; gaps.length = 0; lastAt = 0; }
    if (last && wt === last.wt) return; // a duplicate tick adds nothing
    if (lastAt) { gaps.push(at - lastAt); if (gaps.length > 60) gaps.shift(); }
    lastAt = at;
    const o = at - wt;
    // Take a smaller offset at once (a fast packet), drift up slowly (lag grows).
    offset = offset === null || o < offset ? o : offset + (o - offset) * 0.01;
    if (gaps.length >= 10) {
      const sorted = [...gaps].sort((a, b) => a - b);
      const want = Math.min(MAX_DELAY, Math.max(MIN_DELAY, 1.5 * sorted[Math.floor(sorted.length * 0.95)]));
      delay += (want - delay) * 0.05; // ease, so the drawn time never jumps
    }
    const maps = {};
    for (const kind of ["ships", "agents"]) {
      const m = new Map();
      for (const e of s[kind] || []) m.set(e.id, e);
      maps[kind] = m;
    }
    buf.push({ wt, maps, snap: s });
    if (buf.length > KEEP) buf.shift();
  }

  const renderTime = () => performance.now() - offset - delay;

  // pos is where an entity is drawn now: {x, y}, or null if the newest snapshot lacks it.
  function pos(kind, id) {
    if (!buf.length) return null;
    const newest = buf[buf.length - 1];
    const e = newest.maps[kind].get(id);
    if (!e) return null;
    const rt = renderTime();
    if (rt >= newest.wt) {
      // Buffer ran dry: carry on along the last step for a moment.
      const prev = buf.length > 1 ? buf[buf.length - 2] : null, p = prev && prev.maps[kind].get(id);
      if (!p || Math.abs(p.x - e.x) + Math.abs(p.y - e.y) > 8) return { x: e.x, y: e.y };
      const f = Math.min(rt - newest.wt, EXTRAPOLATE) / (newest.wt - prev.wt || TICK);
      return { x: e.x + (e.x - p.x) * f, y: e.y + (e.y - p.y) * f };
    }
    let i = buf.length - 1;
    while (i > 0 && buf[i - 1].wt > rt) i--;
    if (i === 0) { const o = buf[0].maps[kind].get(id) || e; return { x: o.x, y: o.y }; }
    const a = buf[i - 1].maps[kind].get(id), b = buf[i].maps[kind].get(id);
    if (!a || !b) { const o = b || e; return { x: o.x, y: o.y }; }
    // A jump (respawn, reset) is not slid across the map.
    if (Math.abs(a.x - b.x) + Math.abs(a.y - b.y) > 8) return { x: b.x, y: b.y };
    const f = (rt - buf[i - 1].wt) / (buf[i].wt - buf[i - 1].wt || TICK);
    return { x: a.x + (b.x - a.x) * f, y: a.y + (b.y - a.y) * f };
  }

  // stats is what the debug overlay shows.
  function stats() {
    const n = gaps.length, avg = n ? gaps.reduce((a, b) => a + b, 0) / n : 0;
    const sorted = [...gaps].sort((a, b) => a - b);
    const rt = buf.length ? renderTime() : 0;
    return {
      gapAvg: avg, gapMax: n ? sorted[n - 1] : 0, gapP95: n ? sorted[Math.floor(n * 0.95)] : 0,
      delay, depth: buf.filter((b) => b.wt > rt).length, // snapshots still ahead of the drawn time
      dry: buf.length > 0 && rt >= buf[buf.length - 1].wt, rtt,
    };
  }

  if (ping > 0) {
    setInterval(() => {
      if (!ws || ws.readyState !== 1) return;
      const id = String(++pingSeq);
      pings.set(id, performance.now());
      if (pings.size > 5) pings.delete(pings.keys().next().value);
      ws.send(JSON.stringify({ t: "ping", code: id }));
    }, ping);
  }

  open();
  return {
    send(obj) { if (ws && ws.readyState === 1) { ws.send(JSON.stringify(obj)); return true; } return false; },
    pos,
    stats,
    latest: () => (buf.length ? buf[buf.length - 1].snap : null),
    close() { closed = true; clearTimeout(timer); if (ws) ws.close(); },
  };
}
