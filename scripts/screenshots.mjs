#!/usr/bin/env node
// Regenerates the web UI screenshots in images/ from a real plgv web server and a real
// Valkey. Needs Node 22+, Google Chrome, and a Valkey on 127.0.0.1:6379 (make valkey-up).
//   node scripts/screenshots.mjs [bin/plgv] [name ...]
// With names, only those screenshots are made (for example: home settings).
import { spawn } from 'node:child_process';
import { mkdtempSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const args = process.argv.slice(2);
const BIN = args.find(a => a.includes('/')) || 'bin/plgv';
const only = args.filter(a => !a.includes('/'));
const CHROME = process.env.CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const WEB = 18095, WEBPORT = `http://127.0.0.1:${WEB}`;
const OUT = 'images';
const sleep = ms => new Promise(r => setTimeout(r, ms));
const want = n => only.length === 0 || only.includes(n);

const data = mkdtempSync(join(tmpdir(), 'plgv-shots-'));
const web = spawn(BIN, ['web', '-addr', `127.0.0.1:${WEB}`, '-data-dir', data], { stdio: 'ignore' });
for (let i = 0; i < 50; i++) { try { await fetch(WEBPORT + '/api/live'); break; } catch { await sleep(200); } }
const post = async (path, body) => (await fetch(WEBPORT + path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })).json().catch(() => ({}));
const get = async path => (await fetch(WEBPORT + path)).json();

async function runConfig(edit) {
  const d = (await get('/api/config')).defaults;
  await edit(d); return d;
}
async function run(label, edit, { wait = true } = {}) {
  const cfg = await runConfig(edit);
  const r = await fetch(WEBPORT + '/api/start', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ config: cfg, label }) });
  if (r.status !== 202) throw new Error(label + ': ' + await r.text());
  if (!wait) return;
  for (;;) { await sleep(500); const l = await get('/api/live'); if (l.state === 'done' || l.state === 'error') { if (l.state === 'error') throw new Error(label + ' failed'); return l.run_id; } }
}
const preset = async name => (await get('/api/config')).presets.find(p => p.name === name);
const usePreset = async (d, name) => { const p = await preset(name); d.workload = p.workload; d.dataset = p.dataset; if (p.concurrency !== d.run.concurrency) d.run.concurrency = p.concurrency; };

// ---------- a minimal Chrome DevTools client ----------
async function browser(width, height, fn) {
  const port = 9600 + Math.floor(Math.random() * 300);
  const dir = mkdtempSync(join(tmpdir(), 'plgv-chrome-'));
  const chrome = spawn(CHROME, ['--headless=new', '--disable-gpu', '--hide-scrollbars', `--remote-debugging-port=${port}`, `--user-data-dir=${dir}`, `--window-size=${width},${height}`, 'about:blank'], { stdio: 'ignore' });
  let list;
  for (let i = 0; i < 60; i++) { try { list = await (await fetch(`http://127.0.0.1:${port}/json`)).json(); if (list.find(t => t.type === 'page')) break; } catch {} await sleep(200); }
  const ws = new WebSocket(list.find(t => t.type === 'page').webSocketDebuggerUrl);
  await new Promise(r => ws.addEventListener('open', r));
  let id = 0; const pending = new Map();
  ws.addEventListener('message', m => { const d = JSON.parse(m.data); if (d.id && pending.has(d.id)) { pending.get(d.id)(d); pending.delete(d.id); } });
  const send = (method, params = {}) => new Promise(r => { const i = ++id; pending.set(i, r); ws.send(JSON.stringify({ id: i, method, params })); });
  await send('Page.enable'); await send('Emulation.setDeviceMetricsOverride', { width, height, deviceScaleFactor: 1, mobile: false });
  const page = {
    // A fresh load each time: a change of #hash alone would not run the page's start-up again.
    goto: async url => { await send('Page.navigate', { url: 'about:blank' }); await sleep(200); await send('Page.navigate', { url }); await sleep(2200); },
    js: async expr => { const r = await send('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true }); if (r.result?.exceptionDetails) throw new Error(JSON.stringify(r.result.exceptionDetails).slice(0, 300)); await sleep(500); return r.result?.result?.value; },
    // clip: a selector (its box, with padding), or 'page' for the whole page
    shot: async (name, clip = 'page', pad = 8) => {
      let c;
      if (clip === 'page') {
        const m = await send('Page.getLayoutMetrics'); const h = Math.ceil(m.result.cssContentSize.height);
        await send('Emulation.setDeviceMetricsOverride', { width, height: h, deviceScaleFactor: 1, mobile: false }); await sleep(400);
        c = { x: 0, y: 0, width, height: h, scale: 1 };
      } else {
        const sels = Array.isArray(clip) ? clip : [clip];
        const box = await page.js(`(() => { const rs = ${JSON.stringify(sels)}.map(s => document.querySelector(s).getBoundingClientRect()); const x0 = Math.min(...rs.map(r => r.left)), y0 = Math.min(...rs.map(r => r.top)) + scrollY, x1 = Math.max(...rs.map(r => r.right)), y1 = Math.max(...rs.map(r => r.bottom)) + scrollY; return [x0, y0, x1, y1]; })()`);
        c = { x: Math.max(0, box[0] - pad), y: Math.max(0, box[1] - pad), width: box[2] - box[0] + 2 * pad, height: box[3] - box[1] + 2 * pad, scale: 1 };
        const m = await send('Page.getLayoutMetrics'); const h = Math.ceil(m.result.cssContentSize.height);
        await send('Emulation.setDeviceMetricsOverride', { width, height: Math.max(h, Math.ceil(c.y + c.height)), deviceScaleFactor: 1, mobile: false }); await sleep(400);
      }
      const shot = await send('Page.captureScreenshot', { format: 'png', clip: c, captureBeyondViewport: true });
      writeFileSync(join(OUT, `web_${name}.png`), Buffer.from(shot.result.data, 'base64'));
      console.log('wrote', `${OUT}/web_${name}.png`);
      await send('Emulation.setDeviceMetricsOverride', { width, height, deviceScaleFactor: 1, mobile: false });
    },
  };
  try { await fn(page); } finally {
    ws.close(); chrome.kill(); await sleep(500);
    try { rmSync(dir, { recursive: true, force: true, maxRetries: 5, retryDelay: 300 }); } catch { /* a temp directory Chrome still holds is not worth failing for */ }
  }
}
const task = id => `document.querySelector('[data-task="${id}"] .task-main').click()`;

try {
  // ---------- settings screens ----------
  if (['home', 'settings', 'categories', 'settings_levels', 'presets', 'keyspace_settings', 'advanced_builder', 'raw_injector', 'tls_settings', 'load_profile', 'sweep_settings', 'server_metrics_on', 'server_off', 'cluster_settings'].some(want)) {
    await browser(1280, 900, async p => {
      await p.goto(WEBPORT + '/');
      if (want('home')) await p.shot('home');
      if (want('presets')) await p.shot('presets', ['#presetGrid']);
      await p.js(task('preset:cache'));
      if (want('settings')) await p.shot('settings');
      if (want('server_off')) await p.shot('server_off', '#metricsCard');
      await p.js(`document.getElementById('f-srv').click()`);
      if (want('server_metrics_on')) await p.shot('server_metrics_on', '#metricsCard');
      await p.js(`document.getElementById('f-srv').click()`);
      await p.js(`document.getElementById('lvlAdvanced').click()`);
      await p.js(`document.querySelector('[data-cat-btn="execution"]').click()`);
      if (want('categories')) await p.shot('categories', ['#catNav', '#settingsCards']);
      await p.js(`document.getElementById('lvlExpert').click()`);
      if (want('settings_levels')) await p.shot('settings_levels');
      await p.js(`document.querySelector('[data-cat-btn="connection"]').click(); document.getElementById('f-tls').click()`);
      await p.js(`(() => { for (const [id, v] of [['f-tlsca', '/etc/plgv/ca.crt'], ['f-tlscert', '/etc/plgv/client.crt'], ['f-tlskey', '/etc/plgv/client.key']]) { const e = document.getElementById(id); e.value = v; e.dispatchEvent(new Event('input', { bubbles: true })); } })()`);
      if (want('tls_settings')) await p.shot('tls_settings', '#tlsCard');
      await p.js(`document.getElementById('f-tls').click(); document.querySelector('[data-cat-btn="metrics"]').click(); document.getElementById('f-ks').click()`);
      if (want('keyspace_settings')) await p.shot('keyspace_settings', '#keyspaceCard');
      await p.js(`document.getElementById('f-ks').click(); document.querySelector('[data-cat-btn="execution"]').click(); (() => { const e = document.getElementById('f-pkind'); e.value = 'ramp'; e.dispatchEvent(new Event('change', { bubbles: true })); })()`);
      await p.js(`(() => { for (const [id, v] of [['f-rs', '2'], ['f-re', '48'], ['f-ro', '30s']]) { const e = document.getElementById(id); e.value = v; e.dispatchEvent(new Event('input', { bubbles: true })); } })()`);
      if (want('load_profile')) await p.shot('load_profile', ['#catNav', '#profileCard']);
      // the sweep workload
      await p.goto(WEBPORT + '/');
      await p.js(task('sweep'));
      if (want('sweep_settings')) await p.shot('sweep_settings', ['#taskView .taskbar', '#settingsLayout']);
      // your own commands
      await p.goto(WEBPORT + '/');
      await p.js(task('raw'));
      if (want('raw_injector')) await p.shot('raw_injector', '#rawCard');
      // the custom workload builder with a few families
      await p.goto(WEBPORT + '/');
      await p.js(task('custom'));
      await p.js(`(() => { S.cfg.workload.mix = [{ op: 'XADD', weight: 3 }, { op: 'XCONSUME', weight: 3 }, { op: 'GEOADD', weight: 1 }, { op: 'PFADD', weight: 1 }, { op: 'SETBIT', weight: 1 }, { op: 'TXN', weight: 1, steps: [{ op: 'SET', weight: 1 }, { op: 'INCR', weight: 1 }] }]; renderMix(); })()`);
      if (want('advanced_builder')) await p.shot('advanced_builder', '#mixCard');
      // a cluster target
      await p.goto(WEBPORT + '/');
      await p.js(task('standard'));
      await p.js(`(() => { const e = document.getElementById('f-mode'); e.value = 'cluster'; e.dispatchEvent(new Event('change', { bubbles: true })); })()`);
      if (want('cluster_settings')) await p.shot('cluster_settings', ['#taskView .card[data-group="target"]']);
    });
  }

  // ---------- results: real runs ----------
  if (['live', 'latency', 'server', 'slowlog', 'insights', 'history', 'compare', 'trends', 'keyspace'].some(want)) {
    const ids = [];
    for (const [label, workers] of [['cache baseline', 16], ['cache baseline', 16], ['cache candidate', 16], ['cache regression', 2]]) {
      ids.push(await run(label, async d => { await usePreset(d, 'cache'); d.dataset.key_count = 20000; d.run.duration = '6s'; d.run.concurrency = workers; d.server_metrics.enabled = true; }));
    }
    const session = await run('session store', async d => { await usePreset(d, 'session'); d.run.duration = '6s'; d.dataset.key_count = 20000; d.server_metrics.enabled = true; d.keyspace_analysis.enabled = true; d.keyspace_analysis.samples = 2000; });
    const big = await run('big keys', async d => { await usePreset(d, 'big-keys'); d.run.duration = '6s'; d.dataset.key_count = 40; d.server_metrics.enabled = true; d.keyspace_analysis.enabled = true; d.keyspace_analysis.samples = 200; });
    await browser(1400, 900, async p => {
      await p.goto(`${WEBPORT}/#run=${ids[0]}&tab=live`);
      if (want('live')) await p.shot('live');
      await p.goto(`${WEBPORT}/#run=${ids[0]}&tab=latency`); if (want('latency')) await p.shot('latency');
      await p.goto(`${WEBPORT}/#run=${session}&tab=server`); if (want('server')) await p.shot('server');
      await p.goto(`${WEBPORT}/#run=${session}&tab=slowlog`); if (want('slowlog')) await p.shot('slowlog');
      await p.goto(`${WEBPORT}/#run=${big}&tab=insights`); if (want('insights')) await p.shot('insights');
      await p.goto(`${WEBPORT}/#run=${big}&tab=keyspace`); if (want('keyspace')) await p.shot('keyspace');
      await p.goto(`${WEBPORT}/#tab=history`); if (want('history')) await p.shot('history');
      await p.goto(`${WEBPORT}/#compare=${ids[1]},${ids[3]}`); if (want('compare')) await p.shot('compare');
      await p.goto(`${WEBPORT}/#tab=trends`); if (want('trends')) await p.shot('trends');
    });
  }

  // ---------- record and replay ----------
  if (['record_settings', 'recorded_run', 'replay_settings', 'replay_import', 'replay_live', 'replay_result', 'replay_stored'].some(want)) {
    await browser(1280, 900, async p => {
      await p.goto(WEBPORT + '/');
      await p.js(task('record'));
      if (want('record_settings')) await p.shot('record_settings', '#taskView');
    });
    // Record a real run through the API, the way the page does it.
    const cfg = await runConfig(async d => { await usePreset(d, 'cache'); d.dataset.key_count = 20000; d.run.duration = '6s'; d.run.concurrency = 16; d.record.compression = 'zstd'; d.record.outcomes = true; d.record.backpressure = 'block'; });
    const started = await fetch(WEBPORT + '/api/start', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ config: cfg, label: 'cache to record', record: true }) });
    if (started.status !== 202) throw new Error('record: ' + await started.text());
    let recorded;
    for (;;) { await sleep(500); const l = await get('/api/live'); if (l.state === 'error') throw new Error('record failed: ' + l.error); if (l.state === 'done') { recorded = l.run_id; break; } }
    await browser(1280, 900, async p => {
      await p.goto(`${WEBPORT}/#run=${recorded}&tab=live`);
      if (want('recorded_run')) await p.shot('recorded_run', '#viewBanner');
      await p.goto(`${WEBPORT}/#task=replay`);
      if (want('replay_settings')) await p.shot('replay_settings', '#taskView');
      await p.js(`document.getElementById('lvlAdvanced').click(); document.querySelector('[data-cat-btn="data"]').click()`);
      await p.js(`(() => { const t = document.getElementById('impText'); t.value = '1700000000.000001 [0 10.0.0.1:51000] "SET" "session:1" "abc"\\n1700000000.000900 [0 10.0.0.1:51000] "GET" "session:1"\\n1700000000.001200 [0 10.0.0.2:51001] "HGETALL" "user:7"'; })()`);
      if (want('replay_import')) await p.shot('replay_import', '#importCard');
      // Two servers (the same one twice, so the example needs only one Valkey), one after the other, reads only.
      await p.js(`document.querySelector('[data-cat-btn="execution"]').click()`);
      await p.js(`(() => { const t = document.getElementById('f-rptargets'); t.value = 'before=127.0.0.1:6379\\nafter=127.0.0.1:6379'; t.dispatchEvent(new Event('input', { bubbles: true })); const r = document.getElementById('f-rpreads'); r.click(); })()`);
      await p.js(`document.getElementById('startBtn').click()`);
      await sleep(3500);
      if (want('replay_live')) await p.shot('replay_live', '#panel-live');
      for (let i = 0; i < 60; i++) { const l = await get('/api/live'); if (l.state === 'done' || l.state === 'error') break; await sleep(500); }
      await sleep(1500);
      if (want('replay_result')) await p.shot('replay_result', '#panel-live');
    });
    await browser(1280, 900, async p => {
      await p.goto(`${WEBPORT}/#tab=replay`);
      if (want('replay_stored')) await p.shot('replay_stored', '#panel-replay');
    });
  }
} finally {
  web.kill();
  rmSync(data, { recursive: true, force: true });
}
