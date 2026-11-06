import test from 'node:test';
import assert from 'node:assert/strict';
import { loadPage, fixture, clone, tick, click, setValue } from './harness.mjs';

const text = (doc, sel) => doc.querySelector(sel).textContent;
const rows = (doc, sel) => Array.from(doc.querySelectorAll(sel));
const IDS = ['20261104-100000', '20261104-110000', '20261104-120000', '20261104-130000'];

async function openHistory(overrides = {}) {
  const p = await loadPage(overrides, { hash: '#tab=history' });
  await tick(30);
  return p;
}

test('Run History lists stored runs with their results and finding chips', async () => {
  const { doc, close } = await openHistory();
  const fx = fixture('runs.json').runs;
  const r = rows(doc, '#histTable tbody tr');
  assert.equal(r.length, fx.length);
  assert.equal(doc.getElementById('histEmpty').hidden, true);
  const cached = r.find(x => x.textContent.includes('cache baseline'));
  assert.match(cached.textContent, /warning/);
  const clientOnly = r.find(x => x.textContent.includes('client only'));
  assert.match(clientOnly.textContent, /client only/);
  assert.equal(r[0].querySelector('a[href$="/report"]').getAttribute('target'), '_blank');
  close();
});

test('Run History is empty-state friendly', async () => {
  const { doc, close } = await openHistory({ '/api/runs': { runs: [] } });
  assert.equal(doc.getElementById('histEmpty').hidden, false);
  assert.equal(doc.getElementById('histTable').hidden, true);
  close();
});

test('selecting two runs enables Compare, a third replaces the oldest selection', async () => {
  const { w, doc, close } = await openHistory();
  const boxes = () => rows(doc, '#histTable tbody input[type=checkbox]');
  assert.equal(doc.getElementById('histCompare').disabled, true);
  setValue(w, boxes()[0], true);
  assert.equal(doc.getElementById('histCompare').disabled, true);
  assert.match(text(doc, '#histNote'), /1 selected/);
  setValue(w, boxes()[1], true);
  assert.equal(doc.getElementById('histCompare').disabled, false);
  setValue(w, boxes()[2], true);
  const checked = boxes().map(b => b.checked);
  assert.deepEqual(checked.filter(Boolean).length, 2, 'never more than two');
  assert.equal(checked[0], false, 'the oldest selection was dropped');
  close();
});

test('Open loads the stored run into the result tabs', async () => {
  const { w, doc, calls, close } = await openHistory();
  click(w, rows(doc, '#histTable [data-open]')[0]);
  await tick(30);
  assert.ok(calls.some(c => c.url.startsWith('/api/runs/2026')));
  assert.equal(doc.getElementById('panel-live').hidden, false);
  assert.match(text(doc, '#viewBanner'), /Showing/);
  close();
});

test('Delete asks, calls the API and refreshes the list', async () => {
  let list = clone(fixture('runs.json'));
  const handler = async (url, init) => {
    if (init.method === 'DELETE') { list.runs.shift(); return { status: 200, body: { deleted: true } }; }
    return { status: 200, body: list };
  };
  const routes = { '/api/runs': handler };
  for (const id of IDS) routes['/api/runs/' + id] = handler; // the longest matching prefix wins
  const { w, doc, calls, close } = await openHistory(routes);
  const before = rows(doc, '#histTable tbody tr').length;
  click(w, rows(doc, '#histTable [data-delete]')[0]);
  await tick(40);
  assert.ok(calls.some(c => c.method === 'DELETE'));
  assert.equal(rows(doc, '#histTable tbody tr').length, before - 1);
  close();
});

test('Compare Runs shows the metric table with better/worse verdicts', async () => {
  const cmp = fixture('compare_same.json');
  const { w, doc, calls, close } = await loadPage({ '/api/compare': cmp }, { hash: `#compare=${IDS[0]},${IDS[2]}` });
  await tick(30);
  assert.ok(calls.some(c => c.url === `/api/compare?a=${IDS[0]}&b=${IDS[2]}`));
  const trs = rows(doc, '#cmpMetrics tbody tr');
  assert.equal(trs.length, cmp.metrics.length);
  for (const [i, m] of cmp.metrics.entries()) {
    const verdict = trs[i].cells[4];
    const want = m.better === 'same' ? 'about the same' : m.better === 'b' ? 'B is better' : 'A is better';
    assert.equal(verdict.textContent, want, m.name);
    assert.equal(verdict.className, m.better === 'same' ? '' : m.better === 'b' ? 'better' : 'worse');
  }
  assert.equal(doc.querySelector('#cmpBody .banner.warn'), null, 'comparable runs get no warning');
  assert.deepEqual(w.chartLog.filter(c => c.canvas.id.startsWith('ch-cmp')).map(c => c.canvas.id).sort(), ['ch-cmp-ops', 'ch-cmp-p99']);
  assert.match(text(doc, '#cmpBody'), /By operation/);
  close();
});

test('Compare Runs warns when the workloads differ and lists the setting differences', async () => {
  const cmp = fixture('compare_different.json');
  assert.equal(cmp.comparable, false);
  const { doc, close } = await loadPage({ '/api/compare': cmp }, { hash: `#compare=${IDS[0]},${IDS[3]}` });
  await tick(30);
  assert.match(text(doc, '#cmpBody .banner.warn'), /different workloads/);
  const diffs = rows(doc, '#cmpDiffs tbody tr');
  assert.equal(diffs.length, cmp.config_diffs.length);
  assert.ok(diffs.length > 0 && diffs[0].querySelector('code').textContent.includes('.'));
  close();
});

test('Compare rejects the same run twice and surfaces API errors', async () => {
  const { w, doc, close } = await loadPage({ '/api/compare': { status: 404, body: { error: 'run not found' } } }, { hash: '#tab=compare' });
  await tick(30);
  setValue(w, doc.getElementById('cmpB'), doc.getElementById('cmpA').value);
  click(w, doc.getElementById('cmpGo'));
  await tick(20);
  assert.match(text(doc, '#cmpMsg'), /two different runs/);
  setValue(w, doc.getElementById('cmpB'), IDS[1]);
  click(w, doc.getElementById('cmpGo'));
  await tick(30);
  assert.match(text(doc, '#cmpMsg'), /run not found/);
  close();
});

test('the history Compare button opens the comparison for the two selected runs', async () => {
  const { w, doc, calls, close } = await openHistory({ '/api/compare': fixture('compare_same.json') });
  const boxes = rows(doc, '#histTable tbody input[type=checkbox]');
  setValue(w, boxes[0], true); setValue(w, boxes[1], true);
  click(w, doc.getElementById('histCompare'));
  await tick(60);
  assert.equal(doc.getElementById('panel-compare').hidden, false);
  assert.ok(calls.some(c => c.url.startsWith('/api/compare?a=')));
  close();
});

test('Trends: groups, regression flags and charts', async () => {
  const tr = clone(fixture('trends.json'));
  const enough = tr.groups.find(g => g.enough);
  enough.flags = [{ metric: 'p99_ms', baseline: 2, latest: 3, change: 0.5, message: 'p99 latency is 50% higher than the median of the previous runs' }];
  const { w, doc, close } = await loadPage({ '/api/trends': tr }, { hash: '#tab=trends' });
  await tick(30);
  assert.ok(doc.getElementById('trendGroup').options.length >= 1);
  assert.equal(doc.getElementById('trendBody').hidden, false);
  assert.match(text(doc, '#trendFlags'), /regression: p99_ms/);
  assert.match(text(doc, '#trendFlags'), /50% higher/);
  const ids = w.chartLog.map(c => c.canvas.id);
  for (const id of ['ch-tr-ops', 'ch-tr-p99', 'ch-tr-hit']) assert.ok(ids.includes(id), id);
  const ops = w.chartLog.find(c => c.canvas.id === 'ch-tr-ops');
  assert.equal(ops.data.datasets[0].data.length, enough.runs.length);
  close();
});

test('Trends: a quiet group says it is in line, a short group says more runs are needed', async () => {
  const tr = clone(fixture('trends.json'));
  tr.groups.find(g => g.enough).flags = [];
  const { w, doc, close } = await loadPage({ '/api/trends': tr }, { hash: '#tab=trends' });
  await tick(30);
  assert.match(text(doc, '#trendFlags'), /in line with the previous runs/);
  // Switch to the group with too few runs.
  const short = tr.groups.find(g => !g.enough);
  assert.ok(short, 'fixture has a group with too few runs');
  setValue(w, doc.getElementById('trendGroup'), short.fingerprint);
  assert.equal(doc.getElementById('trendBody').hidden, true);
  assert.match(text(doc, '#trendEmpty'), /at least 3/);
  close();
});

test('Trends with no runs explains what to do', async () => {
  const { doc, close } = await loadPage({ '/api/trends': { groups: [], min_runs: 3, baseline_runs: 5 } }, { hash: '#tab=trends' });
  await tick(30);
  assert.match(text(doc, '#trendEmpty'), /Run the same workload a few times/);
  close();
});
