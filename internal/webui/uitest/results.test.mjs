import test from 'node:test';
import assert from 'node:assert/strict';
import { loadPage, fixture, clone, tick, click, setValue } from './harness.mjs';

const ID_A = '20261104-100000'; // with server data and a slow-commands finding
const ID_D = '20261104-130000'; // client only
const text = (doc, sel) => doc.querySelector(sel).textContent;
const rows = (doc, sel) => Array.from(doc.querySelectorAll(sel));

test('start switches to Live Metrics and shows the running run', async () => {
  let live = fixture('live_idle.json');
  const { w, doc, calls, close } = await loadPage({
    '/api/start': { status: 202, body: { started: true } },
    '/api/live': async () => ({ status: 200, body: live }),
  });
  live = fixture('live_running.json');
  click(w, doc.getElementById('startBtn'));
  await tick(30);
  assert.equal(calls.filter(c => c.url === '/api/start').length, 1);
  assert.equal(doc.getElementById('panel-live').hidden, false, 'moved to Live Metrics');
  assert.equal(text(doc, '#statusBadge'), 'running');
  assert.ok(doc.getElementById('statusBadge').classList.contains('active'));
  assert.equal(doc.getElementById('startBtn').disabled, true);
  assert.equal(doc.getElementById('stopBtn').hidden, false);
  const kpis = text(doc, '#liveKpis');
  assert.match(kpis, /51,000\/s/);
  assert.match(kpis, /150,000/);
  assert.match(kpis, /12 misses/);
  const ops = rows(doc, '#opTable tbody tr').map(r => r.cells[0].textContent);
  assert.deepEqual(ops, ['GET', 'SET']);
  assert.equal(text(doc, '#opP99Head'), 'p99 (latest slice)');
  assert.equal(w.chartLog.find(c => c.canvas.id === 'ch-throughput').data.datasets[0].data.length, 2);
  assert.ok(parseFloat(doc.getElementById('progressBar').style.width) > 0);
  assert.match(text(doc, '#phaseLine'), /Running/);
  close();
});

test('stop posts to /api/stop', async () => {
  const { w, doc, calls, close } = await loadPage({ '/api/live': fixture('live_running.json'), '/api/stop': { status: 200, body: { stopping: true } } });
  await w.pollLive();
  click(w, doc.getElementById('stopBtn'));
  await tick(10);
  assert.ok(calls.some(c => c.url === '/api/stop' && c.method === 'POST'));
  close();
});

test('when the run finishes the stored run opens with report links and an insights badge', async () => {
  const run = fixture('run_a.json');
  let live = fixture('live_running.json');
  const { w, doc, calls, close } = await loadPage({ '/api/live': async () => ({ status: 200, body: live }) });
  await w.pollLive();
  w.showTab('live'); // a started run puts the user on Live Metrics
  live = { ...fixture('live_idle.json'), state: 'done', run_id: ID_A };
  await w.pollLive();
  await tick(30);
  assert.ok(calls.some(c => c.url === '/api/runs/' + ID_A));
  assert.equal(text(doc, '#statusBadge'), 'finished');
  const banner = text(doc, '#viewBanner');
  assert.match(banner, /Showing "cache baseline"/);
  const links = rows(doc, '#viewBanner a').map(a => a.getAttribute('href'));
  assert.deepEqual(links, [`/api/runs/${ID_A}/report`, `/api/runs/${ID_A}/report?download=1`]);
  assert.equal(doc.getElementById('opP99Head').textContent, 'p99');
  const n = run.findings.filter(f => f.severity !== 'info').length;
  assert.ok(n >= 1);
  assert.equal(text(doc, '#insightCount'), String(n));
  assert.equal(doc.getElementById('insightCount').hidden, false);
  assert.match(text(doc, '#liveKpis'), new RegExp(Math.round(run.client.ops).toLocaleString('en-US')));
  close();
});

test('a failed run shows the error', async () => {
  const live = { ...fixture('live_idle.json'), state: 'error', error: 'cannot connect to 127.0.0.1:1: refused' };
  const { w, doc, close } = await loadPage({ '/api/live': live });
  await w.pollLive();
  w.showTab('live');
  assert.equal(text(doc, '#statusBadge'), 'error');
  assert.ok(doc.getElementById('statusBadge').classList.contains('error'));
  assert.match(text(doc, '#phaseLine'), /cannot connect to 127.0.0.1:1/);
  close();
});

test('a polling failure does not break the page', async () => {
  const { w, doc, close } = await loadPage({ '/api/live': { status: 500, body: { error: 'boom' } } });
  await w.pollLive();
  assert.equal(text(doc, '#statusBadge'), 'idle');
  assert.equal(doc.getElementById('panel-settings').hidden, false);
  close();
});

test('Insights tab lists findings by severity and escapes their text', async () => {
  const run = clone(fixture('run_a.json'));
  run.findings = [
    { id: 'x', severity: 'critical', title: 'Bad <b>thing</b>', evidence: '<img src=x onerror="window.__xss=1">', advice: 'Fix it' },
    { id: 'y', severity: 'info', title: 'Note', evidence: 'e', advice: 'a' },
  ];
  const { w, doc, close } = await loadPage({ ['/api/runs/' + ID_A]: run }, { hash: `#run=${ID_A}&tab=insights` });
  const cards = rows(doc, '#insBody .insight');
  assert.deepEqual(cards.map(c => c.classList.contains('critical') ? 'critical' : 'info'), ['critical', 'info']);
  assert.match(cards[0].textContent, /Bad <b>thing<\/b>/, 'shown as text');
  assert.equal(doc.querySelectorAll('#insBody b, #insBody img').length, 0, 'no markup from data');
  assert.equal(w.__xss, undefined);
  assert.equal(text(doc, '#insightCount'), '1', 'only non-info findings count');
  close();
});

test('Insights without server data says what was not checked', async () => {
  const { doc, close } = await loadPage({}, { hash: `#run=${ID_D}&tab=insights` });
  assert.match(text(doc, '#insBody'), /Server metrics were off/);
  close();
});

test('Insights with no findings says so', async () => {
  const run = clone(fixture('run_a.json'));
  run.findings = [];
  const { doc, close } = await loadPage({ ['/api/runs/' + ID_A]: run }, { hash: `#run=${ID_A}&tab=insights` });
  assert.match(text(doc, '#insBody'), /Nothing in this run needs attention/);
  close();
});

test('Server tab shows server data when the run had server metrics', async () => {
  const run = fixture('run_a.json');
  const { w, doc, close } = await loadPage({}, { hash: `#run=${ID_A}&tab=server` });
  assert.equal(doc.getElementById('srvBody').hidden, false);
  assert.equal(doc.getElementById('srvEmpty').hidden, true);
  const k = text(doc, '#srvKpis');
  assert.match(k, /9\.1\.3/);
  assert.match(k, /97\.0%/);
  assert.match(k, new RegExp('plgv sent ' + run.client.ops.toLocaleString('en-US')));
  const ids = w.chartLog.map(c => c.canvas.id);
  for (const id of ['ch-srvops', 'ch-srvmem', 'ch-srvhit', 'ch-srvconn']) assert.ok(ids.includes(id), id);
  const ops = w.chartLog.find(c => c.canvas.id === 'ch-srvops').data.datasets;
  assert.deepEqual([...ops.map(d => d.label)], ['server ops/s', 'plgv ops/s']);
  close();
});

test('Server and Slowlog tabs explain when server metrics were off, and link to the setting', async () => {
  const { w, doc, close } = await loadPage({}, { hash: `#run=${ID_D}&tab=server` });
  assert.equal(doc.getElementById('srvBody').hidden, true);
  assert.match(text(doc, '#srvEmpty'), /Server metrics are off/);
  assert.match(text(doc, '#srvEmpty'), /Collect server metrics/);
  w.showTab('slowlog');
  assert.match(text(doc, '#slowEmpty'), /Server metrics are off/);
  click(w, doc.querySelector('#slowEmpty button'));
  assert.equal(doc.getElementById('panel-settings').hidden, false, 'the button goes to Settings');
  close();
});

test('Slowlog tab shows entries (escaped) and latency events', async () => {
  const { w, doc, close } = await loadPage({}, { hash: `#run=${ID_A}&tab=slowlog` });
  const r = rows(doc, '#slowTable tbody tr');
  assert.equal(r.length, 1);
  assert.match(r[0].textContent, /12\.4 ms|12 ms/);
  assert.equal(r[0].querySelector('code').textContent, 'HGETALL <b>big</b>');
  assert.equal(doc.querySelectorAll('#slowTable b').length, 0);
  assert.match(text(doc, '#latEvents'), /command/);
  close();
});

test('Slowlog tab with no entries says so', async () => {
  const run = clone(fixture('run_a.json'));
  run.server.slowlog = []; run.server.latency_events = [];
  const { doc, close } = await loadPage({ ['/api/runs/' + ID_A]: run }, { hash: `#run=${ID_A}&tab=slowlog` });
  assert.match(text(doc, '#slowTable tbody'), /No slow commands/);
  assert.match(text(doc, '#latEvents'), /latency-monitor-threshold/);
  close();
});

test('Latency Analysis shows percentiles, per-operation rows and the latency mode note', async () => {
  const run = clone(fixture('run_a.json'));
  run.client.coordinated_omission = true; run.client.target_ops_per_sec = 50000;
  const { w, doc, close } = await loadPage({ ['/api/runs/' + ID_A]: run }, { hash: `#run=${ID_A}&tab=latency` });
  assert.equal(doc.getElementById('latBody').hidden, false);
  assert.match(text(doc, '#latKpis'), /p99/);
  assert.match(text(doc, '#latKpis'), /p99\.9/);
  assert.match(text(doc, '#latNote'), /coordinated-omission latency/);
  assert.equal(rows(doc, '#latTable tbody tr').length, run.per_op.length);
  assert.deepEqual([...w.chartLog.find(c => c.canvas.id === 'ch-lat2').data.datasets.map(d => d.label)], ['p50', 'p95', 'p99', 'max']);
  close();
});

test('Latency Analysis before any run explains itself', async () => {
  const { w, doc, close } = await loadPage();
  w.showTab('latency');
  assert.equal(doc.getElementById('latEmpty').hidden, false);
  assert.equal(doc.getElementById('latBody').hidden, true);
  close();
});

test('labels from stored data are shown as text, never as markup', async () => {
  const runs = clone(fixture('runs.json'));
  runs.runs[0].label = '<img src=x onerror="window.__xss=1">';
  runs.runs[0].mix = '<script>window.__xss=2</script>';
  const { w, doc, close } = await loadPage({ '/api/runs': runs }, { hash: '#tab=history' });
  await tick(20);
  assert.match(text(doc, '#histTable tbody'), /<img src=x/);
  assert.equal(doc.querySelectorAll('#histTable img, #histTable script').length, 0);
  assert.equal(w.__xss, undefined);
  close();
});

test('Slowlog tab shows at most 100 entries and says how many there were', async () => {
  const run = clone(fixture('run_a.json'));
  run.server.slowlog = Array.from({ length: 250 }, (_, i) => ({ id: i, unix_time: 1700000000 + i, duration_ms: 1 + i / 100, command: 'SMEMBERS k' + i }));
  run.server.slowlog_new = 900;
  const { doc, close } = await loadPage({ ['/api/runs/' + ID_A]: run }, { hash: `#run=${ID_A}&tab=slowlog` });
  const trs = rows(doc, '#slowTable tbody tr');
  assert.equal(trs.length, 101, '100 entries plus the note');
  assert.match(trs[100].textContent, /first 100 of 250 stored entries \(900 slow commands in total\)/);
  close();
});

test('opening an older run shows its own length on the clock, not the last run\'s planned length', async () => {
  const live = clone(fixture('live_idle.json'));
  live.state = 'done'; live.duration_seconds = 4; live.elapsed_seconds = 4;
  const run = fixture('run_a.json');
  const { w, doc, close } = await loadPage({ '/api/live': live }, { hash: '#run=20261104-100000&tab=live' });
  await w.pollLive();
  await tick(30);
  const mm = s => String(Math.floor(s / 60)).padStart(2, '0') + ':' + String(Math.round(s % 60)).padStart(2, '0');
  const len = mm(run.client.elapsed_seconds);
  assert.equal(doc.getElementById('runClock').textContent, `${len} / ${len}`);
  close();
});
