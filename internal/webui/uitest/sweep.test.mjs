import test from 'node:test';
import assert from 'node:assert/strict';
import { openTask, openCat, loadPage, fixture, clone, tick, click, setValue, startCall } from './harness.mjs';

const visible = (doc, sel) => !doc.querySelector(sel).closest('[hidden]');
const text = (doc, sel) => doc.querySelector(sel).textContent;
const rows = (doc, sel) => Array.from(doc.querySelectorAll(sel));
const START = { '/api/start': { status: 202, body: { started: true } } };

async function advanced(overrides = {}, opts) {
  const p = await loadPage({ ...START, ...overrides }, opts);
  openTask(p.w, p.doc);
  setValue(p.w, p.doc.getElementById('lvlAdvanced'), true);
  openCat(p.w, p.doc, 'execution');
  return p;
}

test('the defaults leave load profile, loop and sweep off', async () => {
  const { w, doc, calls, close } = await advanced();
  click(w, doc.getElementById('startBtn'));
  await tick(20);
  const run = startCall(calls).body.config.run;
  assert.equal(run.load_profile.kind, 'fixed');
  assert.equal(run.loop, 'closed');
  assert.equal(run.sweep.kind, 'none');
  assert.deepEqual(run.sweep.values, []);
  assert.match(text(doc, '#launchSummary'), /64 workers for 10s/);
  close();
});

test('the load profile card shows only the fields of the chosen profile', async () => {
  const { w, doc, close } = await loadPage();
  openTask(w, doc);
  assert.ok(!visible(doc, '#f-pkind'), 'load profile is an Advanced setting');
  setValue(w, doc.getElementById('lvlAdvanced'), true);
  openCat(w, doc, 'execution');
  const shown = () => rows(doc, '[data-profile]').filter(n => !n.hidden).map(n => n.dataset.profile);
  assert.deepEqual(shown(), []);
  for (const [kind, ids] of Object.entries({ ramp: ['f-rs', 're'.replace('re', 'f-re'), 'f-ro'], spike: ['f-sb', 'f-sp', 'f-sa', 'f-sf'], sine: ['f-nmin', 'f-nmax', 'f-nper'] })) {
    setValue(w, doc.getElementById('f-pkind'), kind);
    assert.deepEqual(shown(), [kind]);
    for (const id of ids) assert.ok(visible(doc, '#' + id), `${kind}: ${id}`);
  }
  setValue(w, doc.getElementById('f-pkind'), 'step');
  assert.deepEqual(shown(), ['step']);
  assert.ok(visible(doc, '#stepAdd'));
  setValue(w, doc.getElementById('f-pkind'), 'fixed');
  assert.deepEqual(shown(), []);
  assert.match(text(doc, '#profileNote'), /open loop/);
  close();
});

test('a ramp profile is sent with its settings and shown in the summary', async () => {
  const { w, doc, calls, close } = await advanced();
  setValue(w, doc.getElementById('f-pkind'), 'ramp');
  setValue(w, doc.getElementById('f-rs'), '1');
  setValue(w, doc.getElementById('f-re'), '32');
  setValue(w, doc.getElementById('f-ro'), '20s');
  assert.match(text(doc, '#launchSummary'), /ramp profile for 10s/);
  click(w, doc.getElementById('startBtn'));
  await tick(20);
  const lp = startCall(calls).body.config.run.load_profile;
  assert.deepEqual([lp.kind, lp.start_workers, lp.end_workers, lp.ramp_over], ['ramp', 1, 32, '20s']);
  close();
});

test('step stages can be added, edited and removed', async () => {
  const { w, doc, calls, close } = await advanced();
  setValue(w, doc.getElementById('f-pkind'), 'step');
  assert.equal(rows(doc, '#stepBody tr').length, 0);
  click(w, doc.getElementById('stepAdd'));
  click(w, doc.getElementById('stepAdd'));
  click(w, doc.getElementById('stepAdd'));
  assert.equal(rows(doc, '#stepBody tr').length, 3);
  setValue(w, doc.querySelector('[data-step-index="0"][data-step-field="workers"]'), '2');
  setValue(w, doc.querySelector('[data-step-index="0"][data-step-field="duration"]'), '5s');
  setValue(w, doc.querySelector('[data-step-index="1"][data-step-field="workers"]'), '16');
  click(w, doc.querySelector('[data-step-remove="2"]'));
  assert.equal(rows(doc, '#stepBody tr').length, 2);
  click(w, doc.getElementById('startBtn'));
  await tick(20);
  const steps = startCall(calls).body.config.run.load_profile.steps;
  assert.deepEqual(steps, [{ workers: 2, duration: '5s' }, { workers: 16, duration: '10s' }]);
  close();
});

test('the loop choice: closed by default, open explained and sent', async () => {
  const { w, doc, calls, close } = await advanced();
  assert.equal(doc.getElementById('f-loop').value, 'closed');
  assert.match(text(doc, '#loopNote'), /Open loop:.*whether or not the server keeps up/);
  assert.match(text(doc, '#loopNote'), /needs a target rate/);
  setValue(w, doc.getElementById('f-loop'), 'open');
  setValue(w, doc.getElementById('f-rate'), '50000');
  assert.match(text(doc, '#launchSummary'), /open loop/);
  click(w, doc.getElementById('startBtn'));
  await tick(20);
  const run = startCall(calls).body.config.run;
  assert.equal(run.loop, 'open');
  assert.equal(run.target_ops_per_sec, 50000);
  close();
});

test('sweep card: levels appear when a sweep is chosen and are sent as numbers', async () => {
  const { w, doc, calls, close } = await advanced();
  const sel = doc.getElementById('f-swkind');
  assert.equal(sel.value, 'none');
  assert.ok(doc.getElementById('f-swvals').closest('[hidden]'), 'no level fields while the sweep is off');
  setValue(w, sel, 'workers');
  assert.ok(visible(doc, '#f-swvals') && visible(doc, '#f-swpause'));
  assert.equal(text(doc, '#swValsLabel'), 'Worker counts');
  setValue(w, doc.getElementById('f-swvals'), '4, 8;16  32');
  assert.match(text(doc, '#launchSummary'), /sweep of 4 runs by worker count, 10s each/);
  setValue(w, doc.getElementById('f-swpause'), '2s');
  click(w, doc.getElementById('startBtn'));
  await tick(20);
  const sw = startCall(calls).body.config.run.sweep;
  assert.deepEqual([sw.kind, sw.values, sw.pause], ['workers', [4, 8, 16, 32], '2s']);
  assert.ok(sw.values.every(v => typeof v === 'number'));

  w.showTab('settings');
  setValue(w, sel, 'rate');
  assert.equal(text(doc, '#swValsLabel'), 'Target rates (ops/s)');
  setValue(w, doc.getElementById('f-swvals'), '10000, 20000');
  assert.match(text(doc, '#launchSummary'), /sweep of 2 runs by target rate/);
  assert.match(text(doc, '#sweepNote'), /Duration.*length of each run/);
  close();
});

test('sweep and profile errors from the server are shown on their fields', async () => {
  const errors = [
    { path: 'run.sweep.values', message: 'give 2 to 8 levels, got 1' },
    { path: 'run.load_profile.steps[0].duration', message: 'invalid duration' },
    { path: 'run.loop', message: 'open needs run.target_ops_per_sec > 0' },
  ];
  const { w, doc, close } = await advanced({ '/api/start': { status: 400, body: { error: 'x', errors } } });
  setValue(w, doc.getElementById('f-pkind'), 'step');
  click(w, doc.getElementById('stepAdd'));
  click(w, doc.getElementById('startBtn'));
  await tick(20);
  assert.equal(doc.getElementById('f-swvals').getAttribute('aria-invalid'), 'true');
  assert.match(doc.getElementById('f-swvals').nextElementSibling.textContent, /2 to 8 levels/);
  assert.equal(doc.querySelector('[data-step-index="0"][data-step-field="duration"]').getAttribute('aria-invalid'), 'true');
  assert.equal(doc.getElementById('f-loop').getAttribute('aria-invalid'), 'true');
  close();
});

test('the live view shows sweep progress and the active workers chart', async () => {
  const { w, doc, close } = await loadPage({ '/api/live': fixture('live_sweep.json') });
  await w.pollLive();
  w.showTab('live');
  assert.match(text(doc, '[data-testid="sweep-banner"]'), /Sweep of 4 runs by worker count\. Level 2 of 4: 8 workers\./);
  assert.ok(w.chartLog.some(c => c.canvas.id === 'ch-workers'), 'active workers chart');
  const data = w.chartLog.find(c => c.canvas.id === 'ch-workers').data.datasets[0].data;
  assert.equal(data[0].y, 8);
  close();
});

test('when a sweep finishes, its last level opens with a button to the results', async () => {
  let live = clone(fixture('live_sweep.json'));
  const last = clone(fixture('run_a.json'));
  last.sweep = { id: 'swtest', kind: 'workers', index: 4, total: 4, value: 32 };
  const { w, doc, calls, close } = await loadPage({ '/api/live': async () => ({ status: 200, body: live }), '/api/runs/20261104-120300': last });
  await w.pollLive();
  w.showTab('live');
  live = { ...fixture('live_idle.json'), state: 'done', run_id: '20261104-120300', sweep: { id: 'swtest', kind: 'workers', index: 4, total: 4, value: 32, run_ids: [] } };
  await w.pollLive();
  await tick(30);
  assert.match(text(doc, '[data-testid="sweep-banner"]'), /level 4 of 4 of a sweep by worker count \(32 workers\)/);
  click(w, doc.getElementById('sweepCompareBtn'));
  await tick(60);
  assert.equal(doc.getElementById('panel-compare').hidden, false);
  assert.ok(calls.some(c => c.url === '/api/sweeps/swtest'));
  assert.equal(doc.getElementById('sweepBody').hidden, false);
  close();
});

test('Compare Runs shows the sweep results table with the plateau reading', async () => {
  const fx = fixture('sweep_workers.json');
  const { w, doc, close } = await loadPage({}, { hash: '#sweep=swtest' });
  await tick(60);
  const trs = rows(doc, '#sweepTable tbody tr');
  assert.equal(trs.length, fx.rows.length);
  assert.deepEqual(rows(doc, '#sweepTable th').map(t => t.textContent).slice(0, 2), ['Workers', 'ops/s']);
  assert.match(rows(doc, '#sweepTable th').map(t => t.textContent).join(' '), /Scaling/);
  assert.equal(trs.filter(r => r.classList.contains('peak')).length, 1);
  assert.equal(trs.find(r => r.classList.contains('peak')).dataset.run, fx.peak_run_id);
  assert.match(text(doc, '#sweepBody'), /More workers stopped helping after 16 workers/);
  assert.match(text(doc, '#sweepBody'), /p99 latency reached twice/);
  assert.match(trs[1].textContent, /98%|97%/, 'scaling efficiency is shown');
  assert.equal(rows(doc, '#sweepFigs svg').length, 2, 'a worker sweep shows the two figures');
  assert.ok(!w.chartLog.some(c => c.canvas.id === 'ch-sw-ops'), 'and not the older line charts');
  close();
});

test('a rate sweep shows served share and dropped operations', async () => {
  const sw = clone(fixture('sweep_workers.json'));
  sw.kind = 'rate';
  sw.rows.forEach((r, i) => { r.value = 10000 * (i + 1); r.dropped_ops = i === 3 ? 5000 : 0; r.efficiency = i === 3 ? 0.6 : 1; });
  sw.notes = ['The server (or plgv) could not serve 40000 ops/s: it reached 24000 ops/s. The highest fully served level is shown in the table.'];
  const { doc, close } = await loadPage({ '/api/sweeps/swtest': sw }, { hash: '#sweep=swtest' });
  await tick(60);
  const heads = rows(doc, '#sweepTable th').map(t => t.textContent);
  assert.ok(heads.includes('Target ops/s') && heads.includes('Dropped') && heads.includes('Served'));
  assert.match(text(doc, '#sweepBody'), /could not serve 40000/);
  assert.ok(doc.querySelector('#sweepBody .banner.warn'), 'a saturation note is a warning');
  assert.match(rows(doc, '#sweepTable tbody tr')[3].textContent, /60%/);
  assert.equal(rows(doc, '#sweepFigs').length, 0, 'a rate sweep has no worker figures');
  assert.ok(doc.getElementById('ch-sw-ops'), 'a rate sweep keeps its throughput and latency charts');
  close();
});

test('a sweep that did not finish says so', async () => {
  const sw = clone(fixture('sweep_workers.json'));
  sw.complete = false; sw.rows = sw.rows.slice(0, 2);
  sw.notes = ['This sweep did not finish all of its levels, so the reading below covers only the levels that ran.'];
  const { doc, close } = await loadPage({ '/api/sweeps/swtest': sw }, { hash: '#sweep=swtest' });
  await tick(60);
  assert.ok(doc.querySelector('#sweepBody .banner.warn'));
  assert.match(text(doc, '#sweepBody'), /2 of 4 runs/);
  close();
});

test('with no sweeps the section explains how to run one', async () => {
  const { doc, close } = await loadPage({ '/api/sweeps': { sweeps: [] } }, { hash: '#tab=compare' });
  await tick(40);
  assert.equal(doc.getElementById('sweepMsg').hidden, false);
  assert.match(text(doc, '#sweepMsg'), /No sweeps yet/);
  close();
});

test('Run History marks the runs of a sweep and links to the results', async () => {
  const { w, doc, calls, close } = await loadPage({ '/api/runs': fixture('runs_sweep.json') }, { hash: '#tab=history' });
  await tick(40);
  const chips = rows(doc, '#histTable [data-sweep-chip]').map(c => c.textContent).sort();
  assert.deepEqual(chips, ['sweep 1/4', 'sweep 2/4', 'sweep 3/4', 'sweep 4/4']);
  click(w, rows(doc, '#histTable [data-compare-sweep]')[0]);
  await tick(60);
  assert.equal(doc.getElementById('panel-compare').hidden, false);
  assert.ok(calls.some(c => c.url === '/api/sweeps/swtest'));
  close();
});

test('opening one run of a sweep offers its sweep', async () => {
  const run = clone(fixture('run_a.json'));
  run.sweep = { id: 'swtest', kind: 'workers', index: 2, total: 4, value: 8 };
  const { doc, close } = await loadPage({ '/api/runs/20261104-100000': run }, { hash: '#run=20261104-100000&tab=live' });
  assert.match(text(doc, '[data-testid="sweep-banner"]'), /level 2 of 4 of a sweep by worker count \(8 workers\)/);
  close();
});

test('Time Machine: pick a time range, see its statistics, reset it', async () => {
  const run = clone(fixture('run_a.json'));
  run.series = Array.from({ length: 10 }, (_, i) => ({ offset_s: i + 1, ops_per_sec: 1000 * (i + 1), p50_ms: 0.3, p95_ms: 1, p99_ms: i === 6 ? 9 : 1.5, max_ms: 10, errors: i === 6 ? 2 : 0, workers: 4 }));
  const { w, doc, close } = await loadPage({ '/api/runs/20261104-100000': run }, { hash: '#run=20261104-100000&tab=live' });
  assert.match(text(doc, '#tm-label-live'), /whole run \(10s\)/);
  assert.equal(doc.getElementById('tm-reset-live').hidden, true);
  const thr = () => w.chartLog.find(c => c.canvas.id === 'ch-throughput');
  assert.equal(thr().options.scales.x.min, undefined);

  setValue(w, doc.getElementById('tm-from-live'), '5', 'change');
  setValue(w, doc.getElementById('tm-to-live'), '8', 'change');
  assert.match(text(doc, '#tm-label-live'), /5s to 8s of 10s/);
  assert.equal(thr().options.scales.x.min, 5);
  assert.equal(thr().options.scales.x.max, 8);
  assert.match(text(doc, '#tm-stats-live'), /average 6,500\/s/);
  assert.match(text(doc, '#tm-stats-live'), /worst p99 9\.00 ms/);
  assert.match(text(doc, '#tm-stats-live'), /2 errors/);
  assert.match(text(doc, '#tm-stats-live'), /4 slices/);
  assert.equal(doc.getElementById('tm-reset-live').hidden, false);

  // The range follows to the other tabs that show charts of the same run.
  w.showTab('latency');
  assert.equal(w.chartLog.find(c => c.canvas.id === 'ch-lat2').options.scales.x.min, 5);

  click(w, doc.getElementById('tm-reset-latency'));
  assert.equal(w.chartLog.find(c => c.canvas.id === 'ch-lat2').options.scales.x.min, undefined);
  assert.match(text(doc, '#tm-label-latency'), /whole run/);
  close();
});

test('Time Machine never lets From pass To, and a new run clears the range', async () => {
  const run = clone(fixture('run_a.json'));
  run.series = Array.from({ length: 10 }, (_, i) => ({ offset_s: i + 1, ops_per_sec: 100, p50_ms: 1, p95_ms: 1, p99_ms: 1, max_ms: 1, errors: 0, workers: 1 }));
  const { w, doc, close } = await loadPage({ '/api/runs/20261104-100000': run }, { hash: '#run=20261104-100000&tab=live' });
  setValue(w, doc.getElementById('tm-to-live'), '4', 'change');
  setValue(w, doc.getElementById('tm-from-live'), '9', 'change');
  const lbl = text(doc, '#tm-label-live');
  const m = /(\d+)s to (\d+)s/.exec(lbl);
  assert.ok(m && Number(m[1]) < Number(m[2]), lbl);
  w.showTab('history');
  await tick(30);
  click(w, doc.querySelector('#histTable [data-open]'));
  await tick(40);
  assert.match(text(doc, '#tm-label-live'), /whole run/, 'opening another run starts from the whole run');
  close();
});

test('a #set= link pre-fills settings, with lists separated by semicolons', async () => {
  const { doc, close } = await loadPage({}, { hash: '#tab=settings&levels=advanced&cat=execution&set=run.load_profile.kind:ramp,run.load_profile.end_workers:32,run.loop:open,server_metrics.enabled:true,target.uris:a:1;b:2,run.sweep.values:4;8' });
  assert.equal(doc.getElementById('f-uri').value, 'a:1, b:2');
  assert.equal(doc.getElementById('f-swvals').value, '4, 8');
  assert.equal(doc.getElementById('f-pkind').value, 'ramp');
  assert.equal(doc.getElementById('f-re').value, '32');
  assert.equal(doc.getElementById('f-loop').value, 'open');
  assert.equal(doc.getElementById('f-srv').checked, true);
  assert.ok(visible(doc, '#f-re'), 'the ramp fields are shown');
  close();
});

test('a #range= link opens the Time Machine on that range', async () => {
  const run = clone(fixture('run_a.json'));
  run.series = Array.from({ length: 10 }, (_, i) => ({ offset_s: i + 1, ops_per_sec: 100, p50_ms: 1, p95_ms: 1, p99_ms: 1, max_ms: 1, errors: 0, workers: 1 }));
  const { w, doc, close } = await loadPage({ '/api/runs/20261104-100000': run }, { hash: '#run=20261104-100000&tab=live&range=3-7' });
  assert.match(text(doc, '#tm-label-live'), /3s to 7s of 10s/);
  const ch = w.chartLog.find(c => c.canvas.id === 'ch-throughput');
  assert.equal(ch.options.scales.x.min, 3);
  assert.ok(ch.updates >= 1, 'a chart created on a zoomed range must be updated, or the browser keeps the full axis');
  close();
});

test('the "No sweeps yet" note is not shown when sweeps exist', async () => {
  const { doc, close } = await loadPage({}, { hash: '#tab=compare' });
  await tick(60);
  assert.ok(doc.querySelectorAll('#sweepSelect option').length > 0, 'the fixture has a sweep');
  assert.equal(doc.getElementById('sweepMsg').hidden, true);
  assert.equal(doc.getElementById('sweepBar').hidden, false);
  close();
});
