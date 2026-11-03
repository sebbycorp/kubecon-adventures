// Grand Theft Agent: the WebSocket client shared by the phone page and the big screen.
//
// connect(url, onSnap, opts) opens /grand-theft-agent/ws, calls onSnap(snap) for every
// snapshot, and reconnects with a 1 s, 2 s, 4 s, 8 s backoff. Positions are drawn 100 ms
// in the past and interpolated between the two snapshots around that moment, so the
// 15 Hz stream looks smooth; pos(kind, id) answers that for avatars, targets and allies.

const DELAY = 100; // ms behind the newest snapshot
const KEEP = 8; // snapshots kept for interpolation

// connect returns {send(obj), pos(kind, id), latest(), close()}.
// opts.onMsg(msg) gets every frame that is not a snapshot ({"t":"squad"}, "err", "full").
// opts.onStatus(s) gets "open", "closed" (retrying), "full" or "unauthorized" (no
// arcade cookie: the caller should send the player to join first; no retry).
export function connect(url, onSnap, { onMsg = () => {}, onStatus = () => {} } = {}) {
  let ws = null, backoff = 1000, timer = 0, closed = false, opened = false;
  // Each buffered snapshot keeps its world time and per-kind position maps.
  let buf = [];
  let offset = null; // performance.now() - world time (ms), smoothed

  function open() {
    opened = false;
    ws = new WebSocket(url);
    ws.onopen = () => { opened = true; backoff = 1000; onStatus("open"); };
    ws.onmessage = (ev) => {
      let m;
      try { m = JSON.parse(ev.data); } catch { return; }
      if (m.t === "snap") { take(m); onSnap(m); return; }
      if (m.t === "full") onStatus("full");
      onMsg(m);
    };
    ws.onclose = async () => {
      ws = null;
      if (closed) return;
      if (!opened) {
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
    const at = performance.now(), wt = s.now * 1000;
    const last = buf[buf.length - 1];
    // A reset restarts world time; a long stall jumps it. Either way start over.
    if (last && (wt < last.wt - 500 || wt > last.wt + 3000)) { buf = []; offset = null; }
    const o = at - wt;
    // Take a smaller offset at once (a fast packet), drift up slowly (lag grows).
    offset = offset === null || o < offset ? o : offset + (o - offset) * 0.02;
    const maps = {};
    for (const kind of ["avatars", "targets", "allies"]) {
      const m = new Map();
      for (const e of s[kind] || []) m.set(e.id, e);
      maps[kind] = m;
    }
    buf.push({ wt, maps, snap: s });
    if (buf.length > KEEP) buf.shift();
  }

  // pos is where an entity is drawn now: {x, y}, or null if the newest snapshot lacks it.
  function pos(kind, id) {
    if (!buf.length) return null;
    const newest = buf[buf.length - 1].maps[kind].get(id);
    if (!newest) return null;
    const rt = performance.now() - offset - DELAY;
    let i = buf.length - 1;
    while (i > 0 && buf[i - 1].wt > rt) i--;
    if (i === 0 || buf[i].wt <= rt) {
      const e = (i === 0 && buf[0].wt > rt ? buf[0] : buf[buf.length - 1]).maps[kind].get(id) || newest;
      return { x: e.x, y: e.y };
    }
    const a = buf[i - 1].maps[kind].get(id), b = buf[i].maps[kind].get(id);
    if (!a || !b) return { x: (b || newest).x, y: (b || newest).y };
    const f = (rt - buf[i - 1].wt) / (buf[i].wt - buf[i - 1].wt || 1);
    // A jump (respawn, reset) is not slid across the map.
    if (Math.abs(a.x - b.x) + Math.abs(a.y - b.y) > 4) return { x: b.x, y: b.y };
    return { x: a.x + (b.x - a.x) * f, y: a.y + (b.y - a.y) * f };
  }

  open();
  return {
    send(obj) { if (ws && ws.readyState === 1) { ws.send(JSON.stringify(obj)); return true; } return false; },
    pos,
    latest: () => (buf.length ? buf[buf.length - 1].snap : null),
    close() { closed = true; clearTimeout(timer); if (ws) ws.close(); },
  };
}
