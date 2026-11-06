package report

// reportTemplate is a self-contained HTML document (inline CSS, no JS or
// external assets) that reads well on screen and when printed to PDF.
const reportTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
  :root { --card:#fff; --ink:#172033; --muted:#5c6b80; --line:#e3e8ef; --accent:#0b5cab;
          --s1:#1486ff; --s2:#f0b336; --s3:#f24500; --s4:#059669; }
  * { box-sizing: border-box; }
  body { margin:0; background:#f4f6f9; color:var(--ink);
         font:14px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif; }
  .wrap { max-width: 980px; margin: 0 auto; padding: 24px; }
  header { display:flex; justify-content:space-between; align-items:flex-end; border-bottom:3px solid var(--accent); padding-bottom:12px; margin-bottom:20px; }
  header h1 { margin:0; font-size:22px; }
  header .meta { color:var(--muted); font-size:12px; text-align:right; }
  .cards { display:grid; grid-template-columns: repeat(auto-fit, minmax(140px, 1fr)); gap:12px; margin-bottom:20px; }
  .card { background:var(--card); border:1px solid var(--line); border-radius:10px; padding:14px; }
  .card .label { color:var(--muted); font-size:11px; text-transform:uppercase; letter-spacing:.04em; }
  .card .value { font-size:22px; font-weight:700; margin-top:4px; }
  section { background:var(--card); border:1px solid var(--line); border-radius:10px; padding:16px 18px; margin-bottom:18px; }
  section h2 { margin:0 0 12px; font-size:15px; }
  table { width:100%; border-collapse:collapse; font-size:13px; }
  th, td { text-align:left; padding:7px 8px; border-bottom:1px solid var(--line); }
  th { color:var(--muted); font-weight:600; font-size:11px; text-transform:uppercase; letter-spacing:.03em; }
  td.num, th.num { text-align:right; font-variant-numeric: tabular-nums; }
  .kv { display:grid; grid-template-columns: repeat(2, 1fr); gap:6px 24px; }
  .kv div { display:flex; justify-content:space-between; gap:12px; border-bottom:1px dotted var(--line); padding:3px 0; }
  .kv .k { color:var(--muted); }
  .muted { color:var(--muted); }
  .finding { border:1px solid var(--line); border-left-width:4px; border-radius:8px; padding:10px 12px; margin:8px 0; }
  .finding.critical { border-left-color:#b91c1c; background:#fef2f2; }
  .finding.warning { border-left-color:#d97706; background:#fff7ed; }
  .finding.info { border-left-color:#0b5cab; background:#e8f1fb; }
  .finding .sev { font-size:11px; font-weight:700; text-transform:uppercase; letter-spacing:.04em; }
  .finding .ev { color:var(--muted); font-size:12px; }
  .chart { width:100%; height:auto; background:#fff; }
  .chart .axis { stroke:#9ca3af; stroke-width:1; }
  .chart .grid { stroke:#eef0f3; stroke-width:1; }
  .chart .tick { fill:#9ca3af; font-size:10px; }
  .chart polyline { fill:none; stroke-width:2; }
  .chart .s1 { stroke:var(--s1); } .chart .s2 { stroke:var(--s2); } .chart .s3 { stroke:var(--s3); } .chart .s4 { stroke:var(--s4); }
  .legend { display:flex; gap:14px; margin-top:6px; font-size:12px; }
  .legend .k::before { content:"\2014  "; font-weight:700; }
  .legend .s1 { color:var(--s1); } .legend .s2 { color:var(--s2); } .legend .s3 { color:var(--s3); } .legend .s4 { color:var(--s4); }
  code { font-family: ui-monospace, Menlo, Consolas, monospace; font-size:12px; }
  footer { color:var(--muted); font-size:11px; text-align:center; margin-top:24px; }
  @media print { body { background:#fff; } section, .card { break-inside: avoid; } .wrap { max-width:none; } }
</style>
</head>
<body>
<div class="wrap">
  <header>
    <h1>{{.Title}}</h1>
    <div class="meta">Run started {{ts .S.StartedAt}}<br>Report generated {{ts .Generated}}</div>
  </header>

  <div class="cards">
    <div class="card"><div class="label">Operations</div><div class="value">{{.S.Client.Ops}}</div></div>
    <div class="card"><div class="label">Throughput</div><div class="value">{{f0 .S.Client.OpsPerSec}} /s</div></div>
    <div class="card"><div class="label">p50 latency</div><div class="value">{{f2 .S.Client.P50Ms}} ms</div></div>
    <div class="card"><div class="label">p99 latency</div><div class="value">{{f2 .S.Client.P99Ms}} ms</div></div>
    <div class="card"><div class="label">Errors</div><div class="value">{{.S.Client.Errors}}</div></div>
    <div class="card"><div class="label">Client CPU</div><div class="value">{{f2 .S.Client.CPUCores}} cores</div></div>
  </div>

  <section>
    <h2>Findings</h2>
    {{range .S.Findings}}
    <div class="finding {{.Severity}}">
      <div><span class="sev">{{.Severity}}</span> &middot; <strong>{{.Title}}</strong></div>
      <div class="ev">{{.Evidence}}</div>
      <div>{{.Advice}}</div>
    </div>
    {{else}}<p class="muted">No findings: nothing in this run needs attention.</p>{{end}}
    {{if not .HasServer}}<p class="muted">Server metrics were off, so findings about the server (evictions, memory, slow commands) were not checked.</p>{{end}}
  </section>

  <section>
    <h2>Configuration</h2>
    <div class="kv">{{range .Config}}<div><span class="k">{{.Key}}</span><span>{{.Value}}</span></div>{{end}}</div>
  </section>

  <section>
    <h2>Latency</h2>
    <p class="muted">Mean {{f2 .S.Client.MeanMs}} ms &middot; p95 {{f2 .S.Client.P95Ms}} ms &middot; p99.9 {{f2 .S.Client.P999Ms}} ms &middot; max {{f2 .S.Client.MaxMs}} ms</p>
    {{.LatencyC}}
  </section>

  <section>
    <h2>Throughput</h2>
    {{.ThroughputC}}
  </section>

  <section>
    <h2>Per operation</h2>
    <table>
      <thead><tr><th>Operation</th><th class="num">Ops</th><th class="num">Misses</th><th class="num">Errors</th><th class="num">p50 ms</th><th class="num">p99 ms</th><th class="num">Max ms</th></tr></thead>
      <tbody>{{range .S.PerOp}}<tr><td>{{.Op}}</td><td class="num">{{.Ops}}</td><td class="num">{{.Misses}}</td><td class="num">{{.Errors}}</td><td class="num">{{f2 .P50Ms}}</td><td class="num">{{f2 .P99Ms}}</td><td class="num">{{f2 .MaxMs}}</td></tr>{{end}}</tbody>
    </table>
  </section>

  {{if .ErrClasses}}
  <section>
    <h2>Errors by class</h2>
    <div class="kv">{{range .ErrClasses}}<div><span class="k">{{.Key}}</span><span>{{.Value}}</span></div>{{end}}</div>
    {{if .S.Client.FirstError}}<p class="muted">First error: <code>{{.S.Client.FirstError}}</code></p>{{end}}
  </section>
  {{end}}

  {{if .HasServer}}
  <section>
    <h2>Server</h2>
    <div class="kv">{{range .ServerInfo}}<div><span class="k">{{.Key}}</span><span>{{.Value}}</span></div>{{end}}</div>
    {{.ServerOpsC}}
    {{.MemoryC}}
  </section>
  {{if .S.Server.Slowlog}}
  <section>
    <h2>Slowlog entries during the run</h2>
    <table>
      <thead><tr><th>Time</th><th class="num">Duration ms</th><th>Command</th></tr></thead>
      <tbody>{{range .S.Server.Slowlog}}<tr><td>{{unix .UnixTime}}</td><td class="num">{{f2 .DurationMs}}</td><td><code>{{.Command}}</code></td></tr>{{end}}</tbody>
    </table>
  </section>
  {{end}}
  {{end}}

  {{if .S.KeySpace}}
  <section>
    <h2>Key space</h2>
    {{if .S.KeySpace.Sampled}}
    <p>{{if .S.KeySpace.Complete}}All {{.S.KeySpace.Sampled}} keys matching <code>{{.S.KeySpace.Match}}</code> were inspected.{{else}}A sample of {{.S.KeySpace.Sampled}} of the keys matching <code>{{.S.KeySpace.Match}}</code> was inspected ({{.S.KeySpace.DBSize}} keys on the server).{{end}}</p>
    <div class="kv">{{range .KeySpaceInfo}}<div><span class="k">{{.Key}}</span><span>{{.Value}}</span></div>{{end}}</div>
    <table>
      <thead><tr><th>Type</th><th class="num">Keys</th><th class="num">Avg size</th><th class="num">Max size</th><th class="num">Max elements</th></tr></thead>
      <tbody>{{range .S.KeySpace.Types}}<tr><td>{{.Type}}</td><td class="num">{{.Keys}}</td><td class="num">{{bytes .AvgBytes}}</td><td class="num">{{bytes .MaxBytes}}</td><td class="num">{{.MaxElements}}</td></tr>{{end}}</tbody>
    </table>
    <h3>Biggest keys</h3>
    <table>
      <thead><tr><th>Key</th><th>Type</th><th class="num">Size</th><th class="num">Elements</th></tr></thead>
      <tbody>{{range .S.KeySpace.Big}}<tr><td><code>{{.Key}}</code></td><td>{{.Type}}</td><td class="num">{{bytes .Bytes}}</td><td class="num">{{.Elements}}</td></tr>{{end}}</tbody>
    </table>
    {{else}}
    <p>No keys were analyzed matching <code>{{.S.KeySpace.Match}}</code>{{if .S.KeySpace.FirstError}} ({{.S.KeySpace.FirstError}}){{end}}.</p>
    {{end}}
  </section>
  {{end}}

  <footer>Generated by Percona Load Generator for Valkey (plgv) {{.S.ToolVersion}} &middot; Print this page to export as PDF.</footer>
</div>
</body>
</html>
`
