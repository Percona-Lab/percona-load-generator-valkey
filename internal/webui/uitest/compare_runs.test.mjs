import test from 'node:test';
import assert from 'node:assert/strict';
import { loadPage, fixture, clone, tick, click, setValue } from './harness.mjs';

const q = (doc, sel) => doc.querySelector(sel);
const qa = (doc, sel) => Array.from(doc.querySelectorAll(sel));
const IDS = ['20261104-100000', '20261104-110000', '20261104-120000', '20261104-130000'];
const chart = (w, id) => w.chartLog.filter(c => c.canvas.id === id).pop();
const ticked = doc => qa(doc, '#cmpPicker input[data-key]').filter(b => b.checked).map(b => b.dataset.key);

async function open(overrides = {}, hash = '#tab=compare') {
  const p = await loadPage({ '/api/compare-settings': fixture('compare_settings.json'), ...overrides }, { hash });
  await tick(60);
  return p;
}

test('opens with the two newest runs ticked, one box per run, and three overlay charts', async () => {
  const { w, doc, close } = await open();
  assert.deepEqual(ticked(doc), [IDS[3], IDS[2]]);
  assert.equal(qa(doc, '#cmpPicker fieldset').length, 4);
  assert.match(q(doc, '#cmpPicker fieldset').textContent, /127\.0\.0\.1:6379/, 'the server address is shown');
  assert.match(q(doc, '#cmpSub').textContent, /2 lines/);
  assert.equal(qa(doc, '#cmpLegend .ms-legend-item').length, 2);
  for (const id of ['cmpThroughputChart', 'cmpLatencyChart', 'cmpTailChart']) {
    assert.equal(chart(w, id).data.datasets.length, 2, id);
  }
  assert.equal(doc.getElementById('cmpCharts').hidden, false);
  close();
});

test('each line starts at zero and uses minutes since the run started', async () => {
  const { w, doc, close } = await open();
  const run = fixture('run_d.json');
  const ds = chart(w, 'cmpThroughputChart').data.datasets.find(d => d.label.includes('127.0.0.1')) || chart(w, 'cmpThroughputChart').data.datasets[0];
  const pts = run.series;
  assert.ok(ds.data.length > 0);
  assert.equal(ds.data[0].x, pts[0].offset_s / 60);
  assert.equal(ds.data[0].y, pts[0].ops_per_sec);
  assert.equal(chart(w, 'cmpThroughputChart').options.scales.x.min, 0);
  assert.match(chart(w, 'cmpThroughputChart').options.scales.x.title.text, /Minutes since each run started/);
  close();
});

test('lines differ by colour and line style, so two runs never look alike', async () => {
  const { w, close } = await open();
  const [a, b] = chart(w, 'cmpTailChart').data.datasets;
  assert.notEqual(a.baseColor, b.baseColor);
  assert.notDeepEqual(a.borderDash, b.borderDash);
  close();
});

test('the p99 and p95 buttons switch the tail latency chart', async () => {
  const { w, doc, close } = await open();
  const tail = () => chart(w, 'cmpTailChart');
  assert.equal(tail().options.scales.y.title.text, 'p99 latency (ms)');
  const first = fixture('run_d.json').series[0];
  assert.ok(tail().data.datasets.some(d => d.data[0] && d.data[0].y === first.p99_ms));
  click(w, q(doc, '#cmpTailMetric [data-metric="p95"]'));
  assert.equal(tail().options.scales.y.title.text, 'p95 latency (ms)');
  assert.equal(q(doc, '#cmpTailMetric [data-metric="p95"]').getAttribute('aria-checked'), 'true');
  assert.equal(q(doc, '#cmpTailMetric [data-metric="p99"]').getAttribute('aria-checked'), 'false');
  assert.ok(tail().data.datasets.some(d => d.data[0] && d.data[0].y === first.p95_ms));
  close();
});

test('Clear removes every line, Latest two runs brings the newest two back, and ticking adds one', async () => {
  const { w, doc, close } = await open();
  click(w, doc.getElementById('cmpClear'));
  assert.deepEqual(ticked(doc), []);
  assert.equal(doc.getElementById('cmpCharts').hidden, true);
  assert.match(q(doc, '#cmpHint').textContent, /Tick at least one run/);
  setValue(w, q(doc, `#cmpPicker input[data-key="${IDS[0]}"]`), true);
  assert.deepEqual(ticked(doc), [IDS[0]]);
  assert.match(q(doc, '#cmpSub').textContent, /1 line$/);
  click(w, doc.getElementById('cmpLatestTwo'));
  assert.deepEqual(ticked(doc), [IDS[3], IDS[2]]);
  await tick(40);
  close();
});

test('at most 8 lines at a time, and it says why', async () => {
  const runs = clone(fixture('runs.json'));
  const base = runs.runs[0];
  for (let i = 0; i < 8; i++) runs.runs.push({ ...base, id: `20261103-0${i}0000`, started_at: `2026-11-03T0${i}:00:00Z` });
  const { w, doc, close } = await open({ '/api/runs': runs });
  for (const b of qa(doc, '#cmpPicker input[data-key]')) if (!b.checked && ticked(doc).length < 8) setValue(w, b, true);
  assert.equal(ticked(doc).length, 8);
  const extra = qa(doc, '#cmpPicker input[data-key]').find(b => !b.checked);
  setValue(w, extra, true);
  assert.equal(ticked(doc).length, 8, 'the ninth tick is refused');
  assert.equal(extra.checked, false);
  assert.match(q(doc, '#cmpHint').textContent, /Up to 8 lines/);
  await tick(40);
  close();
});

test('"Settings that differ" lists what changed between the drawn runs', async () => {
  const { doc, calls, close } = await open();
  assert.ok(calls.some(c => c.url.startsWith('/api/compare-settings?ids=')));
  assert.match(q(doc, '#cmpDiff').textContent, /^Settings that differ: server_metrics\.enabled: true vs false/);
  close();
});

test('with exactly two runs the metric table and details stay below the charts', async () => {
  const { doc, calls, close } = await open({ '/api/compare': fixture('compare_same.json') });
  assert.ok(calls.some(c => c.url.startsWith('/api/compare?a=')));
  assert.equal(doc.getElementById('cmpDetailsHeading').hidden, false);
  assert.ok(qa(doc, '#cmpMetrics tbody tr').length > 0);
  click(doc.defaultView, doc.getElementById('cmpClear'));
  assert.equal(doc.getElementById('cmpDetailsHeading').hidden, true, 'no details when two runs are not drawn');
  close();
});

test('a running workload is a line named "Running now" that is drawn against the finished runs', async () => {
  const { w, doc, close } = await open({ '/api/live': fixture('live_running.json') });
  await w.pollLive();
  await tick(30);
  const live = q(doc, '#cmpPicker fieldset.cmp-live');
  assert.ok(live, 'the running workload is listed');
  assert.match(live.textContent, /Running now/);
  assert.match(live.textContent, /keeps growing while it runs/);
  assert.ok(ticked(doc).includes('live'), 'and drawn');
  assert.ok(chart(w, 'cmpThroughputChart').data.datasets.some(d => d.borderWidth === 3), 'with a heavier line');
  close();
});

test('opening a sweep draws its runs on the charts and shows the two sweep figures', async () => {
  const { w, doc, close } = await open({ '/api/runs': fixture('runs_sweep.json') }, '#sweep=swtest');
  await tick(60);
  assert.equal(ticked(doc).length, 4, 'every run of the sweep is ticked');
  assert.equal(chart(w, 'cmpThroughputChart').data.datasets.length, 4);
  const figs = qa(doc, '#sweepFigs svg');
  assert.equal(figs.length, 2);
  assert.match(q(doc, '#sweepFigs').textContent, /Throughput by worker count/);
  assert.match(q(doc, '#sweepFigs').textContent, /More workers stop helping/);
  assert.ok(q(doc, '#sweepOnCharts'), 'the button that draws the sweep');
  click(w, doc.getElementById('cmpClear'));
  click(w, doc.getElementById('sweepOnCharts'));
  assert.equal(ticked(doc).length, 4);
  close();
});

test('with no stored runs the picker explains itself', async () => {
  const { doc, close } = await open({ '/api/runs': { runs: [] } });
  assert.match(q(doc, '#cmpPicker').textContent, /No runs to compare yet/);
  assert.equal(doc.getElementById('cmpCharts').hidden, true);
  close();
});
