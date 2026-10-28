package main

// pageHTML is Mission Control. The server fills in what it knows about this
// request; static/app.js brings it to life by polling /api/stats.
const pageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark light">
<title>Mission Control · hello-web</title>
<link rel="stylesheet" href="/static/app.css?v={{.Version}}">
<link rel="stylesheet" href="/theme.css?v={{.Version}}">
<script src="/static/app.js?v={{.Version}}" defer></script>
</head>
<body>
<header class="top">
  <div class="brand">
    <span class="dot" aria-hidden="true"></span>
    <h1>Mission Control</h1>
    <span class="env env-{{.Environment}}">{{.Environment}}</span>
  </div>
  <p class="tagline">A live look inside the Cloud Run service that served this page.</p>
  <div class="controls">
    <button id="pause" type="button" aria-pressed="false">Pause</button>
    <span id="poll-status" class="muted">connecting…</span>
  </div>
</header>

<main id="app"
  data-version="{{.Version}}" data-revision="{{.Revision}}" data-instance="{{.Instance}}"
  data-env="{{.Environment}}" data-trace="{{.TraceID}}" data-project="{{.Project}}">

  <section class="card hero" aria-labelledby="h-you">
    <h2 id="h-you">You were served by</h2>
    <div class="hero-grid">
      <div><span class="label">Instance</span><span class="big" id="you-instance">{{.Instance}}</span><span class="sub" id="you-name"></span></div>
      <div><span class="label">Revision</span><span class="big mono">{{.Revision}}</span></div>
      <div><span class="label">Version</span><span class="big mono swatch">{{.Version}}</span></div>
      <div><span class="label">Served at</span><span class="big mono">{{.ServedAt}}</span><span class="sub">{{.ServedDate}}</span></div>
    </div>
  </section>

  <section class="card" aria-labelledby="h-journey">
    <h2 id="h-journey">Your request's journey</h2>
    <p class="muted">Measured by your browser, split using the <code>Server-Timing</code> header the app sends back.</p>
    <ol id="journey" class="journey"><li class="muted">Measuring…</li></ol>
    <p class="muted small" id="trace"></p>
  </section>

  <section class="card" aria-labelledby="h-swarm">
    <h2 id="h-swarm">Summon the swarm</h2>
    <p class="muted">Fire parallel requests that each take a moment (<code>/work?ms=…</code>) and watch the load spread across instances. Each instance takes up to 80 at once before Cloud Run adds another.</p>
    <div class="swarm-controls">
      <label>Requests <select id="swarm-n"><option>20</option><option selected>50</option><option>100</option></select></label>
      <label>Each takes <select id="swarm-ms"><option value="200">200 ms</option><option value="500" selected>500 ms</option><option value="1000">1 s</option></select></label>
      <button id="swarm-go" type="button" class="primary">Launch</button>
    </div>
    <div id="swarm-bar" class="progress" hidden><span></span></div>
    <div id="swarm-result" aria-live="polite"></div>
  </section>

  <section class="card wide" aria-labelledby="h-radar">
    <h2 id="h-radar">Rollout radar</h2>
    <p class="muted">Which revision answered your last <span id="radar-n">0</span> requests. During a canary this bar moves 10% → 50% → 100%.</p>
    <div id="radar" class="radar" role="img" aria-label="Share of responses by revision"></div>
    <ul id="radar-legend" class="legend"></ul>
  </section>

  <section class="card wide" aria-labelledby="h-fleet">
    <h2 id="h-fleet">Instance fleet <span class="count" id="fleet-count">0</span></h2>
    <p class="muted">Every instance that has answered you. Cloud Run adds them under load and removes idle ones; a card goes to sleep when it hasn't been seen for a while.</p>
    <div id="fleet" class="fleet"></div>
  </section>


  <section class="card wide" aria-labelledby="h-badges">
    <h2 id="h-badges">Achievements</h2>
    <ul id="badges" class="badges"></ul>
  </section>

  <section class="card wide" aria-labelledby="h-feed">
    <h2 id="h-feed">Live request feed</h2>
    <p class="muted">Recent requests handled by the instances you've reached. The page's own polling is hidden.</p>
    <div class="table-wrap"><table class="feed"><thead><tr><th>Time</th><th>Instance</th><th>Request</th><th>Status</th><th>ms</th></tr></thead><tbody id="feed"></tbody></table></div>
  </section>

  <section class="card wide" aria-labelledby="h-log">
    <h2 id="h-log">Instance logs</h2>
    <p class="muted">The app's own log lines, as sent to Cloud Logging (trace IDs and addresses removed).</p>
    <ol id="log" class="log"></ol>
  </section>

  <section class="card wide" aria-labelledby="h-how">
    <h2 id="h-how">How this page got here</h2>
    <ol class="pipeline">
      <li><strong>Push to main</strong><span>GitHub Actions tests the Go code and builds one container image.</span></li>
      <li><strong>Dev</strong><span>That exact image (by digest) goes to the dev project and gets smoke-tested.</span></li>
      <li><strong>Prod canary</strong><span>10% of traffic, then 50%, then 100%, checking the new revision's error rate at each step. A failure rolls back automatically.</span></li>
      <li><strong>Serving</strong><span>Cloud Run runs it on 0–10 instances, scales to zero when idle, and keeps the previous revision ready for an instant rollback.</span></li>
    </ol>
  </section>

  <noscript><p class="card">Mission Control needs JavaScript for the live view. The details above are from this request.</p></noscript>
</main>

<footer class="muted small">
  <span>{{.Service}} · Go on Cloud Run · polls <code>/api/stats</code> every 2 s and pauses when this tab is hidden</span>
</footer>
</body>
</html>
`
