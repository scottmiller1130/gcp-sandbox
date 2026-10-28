// Mission Control: polls /api/stats and shows what the service is doing.
// Every value from the server is rendered with textContent, never as HTML.
"use strict";

(() => {
  const POLL_MS = 2000;
  const RADAR_WINDOW = 40; // responses shown in the rollout radar
  const SLEEP_AFTER_MS = 45000; // instance card dims after this long unseen
  const SWARM_COOLDOWN_MS = 8000;

  const $ = (id) => document.getElementById(id);
  const root = $("app");
  const here = root.dataset;

  const state = {
    paused: false,
    polls: 0,
    failures: 0,
    timer: null,
    responses: [], // {revision, version, at}
    instances: new Map(), // id -> latest stats + bookkeeping
    feed: new Map(), // key -> request record
    events: new Map(),
    revisions: new Map(), // revision -> {version, color}
    sawError: false,
    swarmed: false,
    scaledOut: false,
    lastSwarm: 0,
  };

  // ---------------------------------------------------------------- helpers

  function el(tag, props = {}, ...children) {
    const node = document.createElement(tag);
    for (const [k, v] of Object.entries(props)) {
      if (k === "text") node.textContent = v;
      else if (k === "class") node.className = v;
      else if (k === "style") for (const [p, val] of Object.entries(v)) node.style.setProperty(p, val);
      else node.setAttribute(k, v);
    }
    for (const c of children) if (c != null) node.append(c);
    return node;
  }

  const fmtMs = (ms) => (ms < 1 ? ms.toFixed(2) : ms < 10 ? ms.toFixed(1) : Math.round(ms).toString());
  const clock = (iso) => new Date(iso).toLocaleTimeString([], { hour12: false });

  function ago(ms) {
    const s = Math.round(ms / 1000);
    if (s < 60) return `${s}s`;
    if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
    return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
  }

  // Same colour per version as the server's accent: hue from the first byte of sha256(version).
  const hueCache = new Map();
  async function hueFor(version) {
    if (hueCache.has(version)) return hueCache.get(version);
    let byte = 0;
    try {
      const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(version));
      byte = new Uint8Array(digest)[0];
    } catch {
      for (const ch of version) byte = (byte * 31 + ch.charCodeAt(0)) & 0xff;
    }
    const hue = Math.floor((byte * 360) / 256);
    hueCache.set(version, hue);
    return hue;
  }

  async function colorFor(revision, version) {
    if (!state.revisions.has(revision)) {
      const hue = await hueFor(version || revision);
      // Several revisions of one version (re-deploys) get different lightness.
      const siblings = [...state.revisions.values()].filter((r) => r.version === version).length;
      state.revisions.set(revision, { version, color: `hsl(${hue} 75% ${58 - siblings * 12}%)` });
    }
    return state.revisions.get(revision).color;
  }

  // Friendly, stable names for instance IDs.
  const ADJ = ["brave", "calm", "swift", "lucky", "sunny", "witty", "noble", "zesty", "cosmic", "plucky",
    "gentle", "spry", "bold", "quiet", "jolly", "keen", "nimble", "proud", "rapid", "snappy"];
  const ANIMALS = ["otter", "falcon", "panda", "lynx", "badger", "heron", "koala", "marten", "ibex", "gecko",
    "puffin", "walrus", "bison", "orca", "lemur", "raven", "tapir", "yak", "quokka", "narwhal"];
  function nameFor(id) {
    const n = parseInt(id.slice(0, 8), 16) || 0;
    return `${ADJ[n % ADJ.length]}-${ANIMALS[Math.floor(n / ADJ.length) % ANIMALS.length]}`;
  }

  // ------------------------------------------------------------ achievements

  const BADGES = [
    { id: "contact", icon: "📡", title: "First contact", desc: "Opened Mission Control", test: () => true },
    { id: "crowd", icon: "👯", title: "Crowd", desc: "Met two or more instances", test: () => state.instances.size >= 2 },
    { id: "canary", icon: "🐤", title: "Canary spotter", desc: "Saw two revisions serving at once", test: () => new Set(state.responses.map((r) => r.revision)).size >= 2 },
    { id: "fresh", icon: "🧊", title: "Cold start", desc: "Caught an instance under a minute old", test: () => [...state.instances.values()].some((i) => i.firstUptime < 60) },
    { id: "swarm", icon: "🐝", title: "Swarm master", desc: "Launched a swarm", test: () => state.swarmed },
    { id: "scaled", icon: "📈", title: "Scaled out", desc: "A swarm was answered by 2+ instances", test: () => state.scaledOut },
    { id: "chaos", icon: "🔥", title: "Chaos witness", desc: "Saw a 5xx or a revision rigged to fail", test: () => state.sawError },
    { id: "watch", icon: "🌙", title: "Night watch", desc: "Kept watching for 5 minutes", test: () => state.polls >= 150 },
    { id: "signed", icon: "✍️", title: "Logged it", desc: "Signed the flight log, for good", test: () => flight.signed },
  ];

  let earned = new Set();
  try {
    earned = new Set(JSON.parse(localStorage.getItem("mc-badges") || "[]"));
  } catch {
    /* storage blocked: badges just won't persist */
  }

  function checkBadges() {
    let changed = false;
    for (const b of BADGES) {
      if (!earned.has(b.id) && b.test()) {
        earned.add(b.id);
        changed = true;
        if (b.id !== "contact") toast(`${b.icon} Achievement: ${b.title}`);
      }
    }
    if (changed) {
      try { localStorage.setItem("mc-badges", JSON.stringify([...earned])); } catch { /* ignore */ }
      renderBadges();
    }
  }

  function renderBadges() {
    $("badges").replaceChildren(...BADGES.map((b) =>
      el("li", { class: earned.has(b.id) ? "earned" : "" },
        el("span", { class: "ic", text: b.icon, "aria-hidden": "true" }),
        el("span", { class: "t", text: b.title }),
        el("span", { class: "d", text: earned.has(b.id) ? b.desc : `Locked: ${b.desc.toLowerCase()}` }))));
  }

  function toast(text) {
    const t = el("div", { class: "toast", role: "status", text });
    document.body.append(t);
    setTimeout(() => t.remove(), 3500);
  }

  // ------------------------------------------------------------------ polling

  async function poll() {
    try {
      const res = await fetch("/api/stats", { cache: "no-store" });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      await record(await res.json());
      state.failures = 0;
      setStatus(`live · ${state.polls} polls`);
      document.body.classList.remove("offline");
    } catch (err) {
      state.failures++;
      setStatus(`connection trouble (${err.message}), retrying…`);
      document.body.classList.add("offline");
    }
  }

  function schedule() {
    clearTimeout(state.timer);
    if (state.paused || document.hidden) return;
    state.timer = setTimeout(async () => {
      await poll();
      schedule();
    }, state.failures ? Math.min(POLL_MS * 2 ** state.failures, 30000) : POLL_MS);
  }

  function setStatus(text) {
    $("poll-status").textContent = state.paused ? "paused" : document.hidden ? "paused while hidden" : text;
  }

  async function record(s) {
    state.polls++;
    const now = Date.now();
    await colorFor(s.revision, s.version);
    state.responses.push({ revision: s.revision, version: s.version, at: now });
    if (state.responses.length > RADAR_WINDOW) state.responses.shift();

    const prev = state.instances.get(s.instance);
    state.instances.set(s.instance, {
      ...s,
      seenAt: now,
      firstUptime: prev ? prev.firstUptime : s.uptime_s,
      pulse: true,
    });

    for (const r of s.recent || []) {
      state.feed.set(`${s.instance}|${r.at}|${r.method}|${r.path}`, { ...r, instance: s.instance, revision: s.revision });
      if (r.status >= 500) state.sawError = true;
    }
    for (const e of s.events || []) {
      state.events.set(`${s.instance}|${e.at}|${e.message}`, { ...e, instance: s.instance, revision: s.revision });
    }
    if (s.error_rate > 0 || (s.by_status && s.by_status["5xx"] > 0)) state.sawError = true;
    trim(state.feed, 80);
    trim(state.events, 80);

    render();
    checkBadges();
  }

  function trim(map, n) {
    if (map.size <= n) return;
    const sorted = [...map.entries()].sort((a, b) => (a[1].at < b[1].at ? 1 : -1));
    map.clear();
    for (const [k, v] of sorted.slice(0, n)) map.set(k, v);
  }

  // ---------------------------------------------------------------- rendering

  function render() {
    renderRadar();
    renderFleet();
    renderFeed();
    renderLog();
  }

  function renderRadar() {
    const counts = new Map();
    for (const r of state.responses) counts.set(r.revision, (counts.get(r.revision) || 0) + 1);
    const total = state.responses.length;
    $("radar-n").textContent = total;
    const entries = [...counts.entries()].sort((a, b) => b[1] - a[1]);
    $("radar").replaceChildren(...entries.map(([rev, n]) => {
      const pct = Math.round((n / total) * 100);
      return el("span", { style: { "flex-grow": String(n), background: state.revisions.get(rev).color }, title: `${rev}: ${pct}%`, text: pct >= 12 ? `${pct}%` : "" });
    }));
    $("radar-legend").replaceChildren(...entries.map(([rev, n]) =>
      el("li", { style: { "--c": state.revisions.get(rev).color }, text: `${rev} (${state.revisions.get(rev).version}) · ${Math.round((n / total) * 100)}%${rev === here.revision ? " · served this page" : ""}` })));
  }

  function renderFleet() {
    const now = Date.now();
    const list = [...state.instances.values()].sort((a, b) => b.seenAt - a.seenAt);
    $("fleet-count").textContent = list.length;
    $("fleet").replaceChildren(...list.map((i) => {
      const asleep = now - i.seenAt > SLEEP_AFTER_MS;
      const color = state.revisions.get(i.revision)?.color;
      const cold = i.firstUptime < 60;
      const card = el("article", { class: `inst${asleep ? " asleep" : ""}${i.pulse ? " pulse" : ""}`, style: color ? { "--c": color } : {} },
        el("div", { class: "nm", text: nameFor(i.instance) }),
        el("div", { class: "id", text: `${i.instance} · ${i.revision}` }),
        asleep ? el("span", { class: "tag zzz", text: "zzz" }) : cold ? el("span", { class: "tag cold", text: "fresh" }) : null,
        el("dl", {},
          el("dt", { text: "Uptime" }), el("dd", { text: ago(i.uptime_s * 1000 + (now - i.seenAt)) }),
          el("dt", { text: "Requests" }), el("dd", { text: i.requests.toLocaleString() }),
          el("dt", { text: "In flight" }), el("dd", { text: `${Math.max(i.in_flight, 0)} / 80` }),
          el("dt", { text: "p50 / p95" }), el("dd", { text: `${fmtMs(i.latency_ms.p50)} / ${fmtMs(i.latency_ms.p95)} ms` }),
          el("dt", { text: "2xx / 4xx / 5xx" }), el("dd", { text: `${i.by_status["2xx"]} / ${i.by_status["4xx"]} / ${i.by_status["5xx"]}` }),
          el("dt", { text: "Heap" }), el("dd", { text: `${i.runtime.heap_mib.toFixed(1)} MiB` }),
          el("dt", { text: "Goroutines" }), el("dd", { text: i.runtime.goroutines }),
          el("dt", { text: "Boot time" }), el("dd", { text: `${fmtMs(i.ready_ms)} ms` }),
          i.error_rate > 0 ? el("dt", { text: "Rigged" }) : null,
          i.error_rate > 0 ? el("dd", { text: `fails ${Math.round(i.error_rate * 100)}% on purpose` }) : null),
        el("div", { class: "meter", title: "In-flight requests out of 80 per instance" },
          el("span", { style: { width: `${Math.min(100, (Math.max(i.in_flight, 0) / 80) * 100)}%` } })),
        asleep ? el("p", { class: "muted small", text: `Not seen for ${ago(now - i.seenAt)}: probably scaled in.` }) : null);
      i.pulse = false;
      return card;
    }));
  }

  let seenFeed = new Set();
  function renderFeed() {
    const rows = [...state.feed.entries()].sort((a, b) => (a[1].at < b[1].at ? 1 : -1)).slice(0, 25);
    const next = new Set();
    $("feed").replaceChildren(...rows.map(([key, r]) => {
      next.add(key);
      const tr = el("tr", { class: seenFeed.size && !seenFeed.has(key) ? "new" : "" },
        el("td", { text: clock(r.at) }),
        el("td", {}, el("span", { class: "chip", style: { "--c": state.revisions.get(r.revision)?.color || "var(--muted)" } }), nameFor(r.instance)),
        el("td", { text: `${r.method} ${r.path}` }),
        el("td", { class: `s${String(r.status)[0]}`, text: r.status }),
        el("td", { text: fmtMs(r.ms) }));
      return tr;
    }));
    if (!rows.length) $("feed").replaceChildren(el("tr", {}, el("td", { colspan: "5", class: "muted", text: "Waiting for requests…" })));
    seenFeed = next;
  }

  function renderLog() {
    const rows = [...state.events.values()].sort((a, b) => (a.at < b.at ? 1 : -1)).slice(0, 30);
    $("log").replaceChildren(...rows.map((e) => el("li", {},
      el("span", { class: "chip", style: { "--c": state.revisions.get(e.revision)?.color || "var(--muted)" } }),
      `${clock(e.at)} `,
      el("span", { class: `lv ${e.level}`, text: e.level }),
      ` ${nameFor(e.instance)}: ${e.message} `,
      e.attrs ? el("span", { class: "attrs", text: Object.entries(e.attrs).map(([k, v]) => `${k}=${v}`).join(" ") }) : null)));
  }

  // ------------------------------------------------------- the request journey

  function journey() {
    const nav = performance.getEntriesByType("navigation")[0];
    if (!nav || !nav.responseEnd) return setTimeout(journey, 200);
    const app = (nav.serverTiming || []).find((t) => t.name === "app");
    const appMs = app ? app.duration : 0;
    const steps = [
      ["DNS lookup", nav.domainLookupStart, nav.domainLookupEnd, ""],
      ["Connect (TCP + TLS)", nav.connectStart, nav.connectEnd, ""],
      ["Google front end + network", nav.requestStart, nav.responseStart - appMs, ""],
      ["Cloud Run: the app's code", nav.responseStart - appMs, nav.responseStart, "app"],
      ["Download the page", nav.responseStart, nav.responseEnd, ""],
    ];
    const end = nav.responseEnd;
    const rows = steps.map(([name, from, to, cls]) => {
      const dur = Math.max(to - from, 0);
      const reused = dur === 0 && (name.startsWith("DNS") || name.startsWith("Connect"));
      return el("li", {},
        el("span", { class: "name", text: name }),
        el("span", { class: "track" }, el("span", { class: `bar ${cls}`, style: { left: `${(from / end) * 100}%`, width: `${(dur / end) * 100}%` } })),
        el("span", { class: "ms", text: reused ? "reused" : `${fmtMs(dur)} ms` }));
    });
    rows.push(el("li", { class: "total" }, el("span", { class: "name", text: "Total" }), el("span", {}), el("span", { class: "ms", text: `${fmtMs(end)} ms` })));
    $("journey").replaceChildren(...rows);
    if (!app) $("journey").append(el("li", { class: "muted small", text: "Your browser didn't share Server-Timing, so the app's share couldn't be split out." }));

    if (here.trace) {
      const t = $("trace");
      t.append(`Cloud Run traced this request: ${here.trace}. `);
      if (here.project) {
        t.append(el("a", { href: `https://console.cloud.google.com/traces/list?project=${encodeURIComponent(here.project)}&tid=${encodeURIComponent(here.trace)}`, rel: "noopener", text: "Open in Cloud Trace" }),
          " (project owners only).");
      }
    }
  }

  // ------------------------------------------------------------------ swarm

  async function swarm() {
    const since = Date.now() - state.lastSwarm;
    if (since < SWARM_COOLDOWN_MS) return toast(`Let the fleet rest ${Math.ceil((SWARM_COOLDOWN_MS - since) / 1000)}s`);
    state.lastSwarm = Date.now();
    const n = Math.min(parseInt($("swarm-n").value, 10) || 50, 100);
    const ms = Math.min(parseInt($("swarm-ms").value, 10) || 500, 1000);
    const btn = $("swarm-go");
    const bar = $("swarm-bar");
    btn.disabled = true;
    bar.hidden = false;
    bar.firstElementChild.style.width = "0%";
    $("swarm-result").replaceChildren(el("p", { class: "muted", text: `Launching ${n} requests…` }));

    // Peek at the fleet mid-swarm, while requests are in flight.
    setTimeout(poll, Math.min(ms / 2, 400));

    let done = 0;
    const started = performance.now();
    const results = await Promise.all(Array.from({ length: n }, async () => {
      const t0 = performance.now();
      try {
        const res = await fetch(`/work?ms=${ms}`, { cache: "no-store" });
        const body = await res.json().catch(() => ({}));
        return { ok: res.ok, status: res.status, instance: body.instance || "?", revision: body.revision || "?", version: body.version || "", ms: performance.now() - t0 };
      } catch {
        return { ok: false, status: 0, instance: "?", revision: "?", version: "", ms: performance.now() - t0 };
      } finally {
        done++;
        bar.firstElementChild.style.width = `${(done / n) * 100}%`;
      }
    }));
    const wall = performance.now() - started;
    btn.disabled = false;
    setTimeout(() => (bar.hidden = true), 800);

    const lat = results.map((r) => r.ms).sort((a, b) => a - b);
    const pct = (p) => lat[Math.min(Math.floor(p * lat.length), lat.length - 1)];
    const byInst = new Map();
    for (const r of results) byInst.set(r.instance, { n: (byInst.get(r.instance)?.n || 0) + 1, revision: r.revision, version: r.version });
    for (const [, v] of byInst) if (v.revision !== "?") await colorFor(v.revision, v.version);
    const errors = results.filter((r) => !r.ok).length;
    if (errors) state.sawError = true;
    state.swarmed = true;
    if ([...byInst.keys()].filter((k) => k !== "?").length >= 2) state.scaledOut = true;

    const out = [
      el("div", { class: "stats-line" },
        el("span", { text: `${n} requests in ${fmtMs(wall)} ms` }),
        el("span", { text: `p50 ${fmtMs(pct(0.5))} ms` }),
        el("span", { text: `p95 ${fmtMs(pct(0.95))} ms` }),
        el("span", { text: `${errors} failed` })),
      el("p", { class: "muted small", text: `Answered by ${byInst.size} instance${byInst.size === 1 ? "" : "s"}:` }),
      ...[...byInst.entries()].sort((a, b) => b[1].n - a[1].n).map(([id, v]) =>
        el("div", { class: "result-row" },
          el("span", { text: id === "?" ? "no answer" : nameFor(id) }),
          el("span", { class: "track" }, el("span", { style: { width: `${(v.n / n) * 100}%`, "--c": state.revisions.get(v.revision)?.color || "var(--bad)" } })),
          el("span", { class: "mono", text: v.n }))),
    ];
    if (wall > ms * 2.5) {
      out.push(el("p", { class: "muted small", text: "Took much longer than one request: your browser limits how many requests it sends at once, so some waited their turn before reaching Cloud Run." }));
    } else if (byInst.size === 1) {
      out.push(el("p", { class: "muted small", text: "One instance handled it all: up to 80 requests can share one instance. Try 100 requests at 1 s to push past that." }));
    }
    $("swarm-result").replaceChildren(...out);
    checkBadges();
    poll();
  }

  // -------------------------------------------------------------- flight log

  const FLIGHT_POLL_MS = 30000; // the server caches for minutes anyway
  const BACKUP_OVERDUE_MS = 36 * 3600 * 1000; // nightly, plus slack
  const flight = { pick: 0, signed: false, data: null, timer: null };

  async function loadFlight() {
    try {
      const res = await fetch("/api/flightlog", { cache: "no-store" });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      flight.data = await res.json();
    } catch (err) {
      flight.data = { enabled: false, reason: `Couldn't load the flight log (${err.message}).`, emojis: flight.data?.emojis || [] };
    }
    renderFlight();
  }

  function renderEmojis(emojis) {
    const box = $("flight-emojis");
    if (box.childElementCount === emojis.length) return;
    box.replaceChildren(...emojis.map((e, i) => {
      const b = el("button", { type: "button", role: "radio", "aria-checked": String(i === flight.pick), class: i === flight.pick ? "on" : "", text: e });
      b.addEventListener("click", () => {
        flight.pick = i;
        for (const [j, x] of [...box.children].entries()) {
          x.classList.toggle("on", j === i);
          x.setAttribute("aria-checked", String(j === i));
        }
      });
      return b;
    }));
  }

  function guard(ok, title, detail) {
    return el("li", { class: ok === true ? "ok" : ok === false ? "off" : "" },
      el("span", { class: "ic", "aria-hidden": "true", text: ok === true ? "✓" : ok === false ? "–" : "…" }),
      el("span", {}, el("strong", { text: title }), el("span", { class: "muted small", text: detail })));
  }

  // A backup that passed long ago is as bad as one that failed: the job has stopped running.
  function backupGuard(b, now) {
    const title = "Nightly backup + restore drill";
    if (!b) return guard(null, title, "No backup yet: the first one runs tonight with the maintenance job.");
    const age = now - Date.parse(b.drill_at);
    if (age > BACKUP_OVERDUE_MS) {
      return guard(false, `${title}: OVERDUE`, `Last ran ${ago(age)} ago, but it should run every night. Check the hello-web-maintenance job's executions and logs. Entries since then have no vault copy.`);
    }
    return guard(b.drill_ok, b.drill_ok ? title : `${title}: FAILED`, `${b.drill_ok ? "Passed" : "Failed"} ${ago(age)} ago. ${b.drill_message}`);
  }

  function renderFlight() {
    const d = flight.data;
    renderEmojis(d.emojis || []);
    $("flight-sign").disabled = !d.enabled;
    if (!d.enabled) {
      $("flight-total").textContent = "offline";
      $("flight-entries").replaceChildren(el("li", { class: "muted", text: d.reason }));
      $("flight-guards").replaceChildren();
      $("flight-cache").textContent = "";
      return;
    }
    $("flight-total").textContent = `${d.total.toLocaleString()} signed`;
    const now = Date.now();
    $("flight-entries").replaceChildren(...(d.entries.length ? d.entries.map((e) =>
      el("li", { class: e.id === flight.mine ? "mine" : "" },
        el("span", { class: "em", text: e.emoji }),
        el("span", {}, el("strong", { text: e.callsign }),
          el("span", { class: "muted small", text: ` · ${ago(now - Date.parse(e.created))} ago · served by ${nameFor(e.instance)} on ${e.version}` }))))
      : [el("li", { class: "muted", text: "Nobody yet. Be the first." })]));
    $("flight-cache").textContent = (d.list_error ? `${d.list_error}. ` : "") +
      `This instance refreshes the list every ${Math.round(d.cache_ttl_s / 60)} min to stay inside Firestore's free tier ` +
      `(${d.budget.reads.toLocaleString()} of its ${d.budget.reads_cap.toLocaleString()} daily reads used).`;

    const s = d.status || {};
    const b = s.backup;
    const guards = [];
    if (s.error) guards.push(guard(null, "Status unavailable", s.error));
    guards.push(
      guard(s.location === "nam5" ? true : s.location ? false : null, "Multi-region",
        s.location === "nam5" ? "nam5: every write is copied to several US regions before it's confirmed. A whole region can fail without losing a signature." : `Location: ${s.location || "unknown"}`),
      guard(s.delete_protection, "Delete protection", s.delete_protection ? "The database can't be deleted, by Terraform, the console or anyone, until this is switched off." : "Off"),
      backupGuard(b, now),
      guard(b ? true : null, "Vault in another project",
        b ? `Latest: ${b.last_object.split("/").pop()}. Write-once: the backup identity can add files, never change or delete them, and a 90-day retention lock stops anyone else.` : "Backups go to a bucket in the shared project, out of reach of this environment."),
      guard(s.pitr ? true : false, "Point-in-time recovery",
        s.pitr ? "On" : "Off on purpose: it's billed. The nightly vault copies cover it, at up to a day's loss instead of minutes."),
      guard(true, "Free-tier guard",
        `${(s.signatures_today || 0).toLocaleString()} of ${d.budget.sign_cap.toLocaleString()} signatures today. The cap is enforced in a transaction, so it holds across every instance.`),
    );
    $("flight-guards").replaceChildren(...guards);
  }

  async function signFlight() {
    const btn = $("flight-sign");
    btn.disabled = true;
    const out = $("flight-result");
    out.className = "small";
    out.textContent = "Writing to Firestore…";
    const t0 = performance.now();
    try {
      const res = await fetch("/api/flightlog", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ emoji: flight.pick }) });
      const body = await res.json().catch(() => ({}));
      if (!res.ok) throw new Error(body.error || `HTTP ${res.status}`);
      flight.signed = true;
      flight.mine = body.entry.id;
      out.className = "small good";
      out.textContent = `${body.entry.emoji} Logged as ${body.entry.callsign} in ${fmtMs(performance.now() - t0)} ms, confirmed only after it reached more than one region.`;
      if (flight.data?.enabled) {
        flight.data.entries = [body.entry, ...flight.data.entries.filter((e) => e.id !== body.entry.id)].slice(0, 10);
        flight.data.total++;
        flight.data.status.signatures_today = body.signatures_today;
        renderFlight();
      }
      checkBadges();
      poll();
    } catch (err) {
      out.className = "small bad";
      out.textContent = err.message;
    } finally {
      setTimeout(() => (btn.disabled = !flight.data?.enabled), 2000);
    }
  }

  function scheduleFlight() {
    clearTimeout(flight.timer);
    if (state.paused || document.hidden) return;
    flight.timer = setTimeout(() => loadFlight().then(scheduleFlight), FLIGHT_POLL_MS);
  }

  // ------------------------------------------------------------------ wiring

  const youName = $("you-name");
  if (youName) youName.textContent = nameFor(here.instance);

  $("pause").addEventListener("click", () => {
    state.paused = !state.paused;
    $("pause").textContent = state.paused ? "Resume" : "Pause";
    $("pause").setAttribute("aria-pressed", String(state.paused));
    document.body.classList.toggle("paused", state.paused);
    setStatus("resuming…");
    if (!state.paused) poll().then(schedule);
    else clearTimeout(state.timer);
    scheduleFlight();
  });
  document.addEventListener("visibilitychange", () => {
    document.body.classList.toggle("paused", document.hidden || state.paused);
    setStatus("resuming…");
    if (!document.hidden && !state.paused) poll().then(schedule);
    else clearTimeout(state.timer);
    scheduleFlight();
  });
  $("swarm-go").addEventListener("click", swarm);
  $("flight-sign").addEventListener("click", signFlight);
  // Keep "not seen for …" ages fresh between polls.
  setInterval(() => !document.hidden && renderFleet(), 5000);

  renderBadges();
  checkBadges();
  if (document.readyState === "complete") journey();
  else window.addEventListener("load", journey);
  poll().then(schedule);
  loadFlight().then(scheduleFlight);
})();
