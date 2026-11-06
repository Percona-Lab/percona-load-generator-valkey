import test from 'node:test';
import assert from 'node:assert/strict';
import { openTask, openCat, loadPage, fixture, clone, tick, click, setValue, startCall } from './harness.mjs';

const START = { '/api/start': { status: 202, body: { started: true } } };
const q = (doc, sel) => doc.querySelector(sel);
const qa = (doc, sel) => Array.from(doc.querySelectorAll(sel));
const visible = (doc, sel) => !doc.querySelector(sel).closest('[hidden]');
const startCfg = async (w, doc, calls) => { click(w, doc.getElementById('startBtn')); await tick(20); return startCall(calls).body.config; };
const useDefaults = (w, doc, id) => click(w, q(doc, `[data-task="${id}"] .task-defaults`));
const ops = doc => qa(doc, '#mixBody [data-mix-field="op"]').map(s => s.value);

test('picking a workload selects its defaults: operations, dataset and preset', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  openTask(w, doc, 'preset:leaderboard');
  assert.equal(q(doc, '#taskTitle').textContent, 'Leaderboard');
  assert.match(q(doc, '#taskDesc').textContent, /Sorted-set ranking/);
  assert.equal(q(doc, '#taskSel').value, 'preset:leaderboard');
  assert.deepEqual(ops(doc), ['ZADD', 'ZINCRBY', 'ZRANK', 'ZSCORE', 'ZRANGE', 'ZCARD']);
  assert.equal(q(doc, '#f-dist').value, 'zipf');
  assert.equal(q(doc, '#f-keys').value, '1000');
  assert.equal(q(doc, '#presetSel').value, 'leaderboard');
  assert.match(q(doc, '#launchSummary').textContent, /^Leaderboard · /);
  const cfg = await startCfg(w, doc, calls);
  assert.equal(cfg.workload.preset, 'leaderboard');
  assert.equal(cfg.dataset.key_distribution, 'zipf');
  close();
});

test('the standard workload is 80% GET and 20% SET with the default dataset', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  openTask(w, doc, 'standard');
  assert.deepEqual(ops(doc), ['GET', 'SET']);
  const cfg = await startCfg(w, doc, calls);
  assert.deepEqual(cfg.workload.mix.map(m => [m.op, m.weight]), [['GET', 80], ['SET', 20]]);
  assert.equal(cfg.workload.preset, undefined, 'presets are opt-in');
  assert.equal(cfg.run.sweep.kind, 'none');
  assert.equal(cfg.run.deterministic, false);
  close();
});

test('switching workloads keeps the server, workers and duration and replaces what belongs to the workload', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  openTask(w, doc, 'preset:cache');
  setValue(w, q(doc, '#f-uri'), 'valkey://10.1.2.3:6380');
  setValue(w, q(doc, '#f-duration'), '45s');
  setValue(w, q(doc, '#f-conc'), '12');
  setValue(w, q(doc, '#f-keys'), '777');
  setValue(w, q(doc, '#taskSel'), 'preset:queue', 'change');
  assert.equal(q(doc, '#taskTitle').textContent, 'Work queue');
  assert.equal(q(doc, '#f-uri').value, 'valkey://10.1.2.3:6380');
  assert.equal(q(doc, '#f-duration').value, '45s');
  assert.equal(q(doc, '#f-conc').value, '12');
  assert.notEqual(q(doc, '#f-keys').value, '777', 'the dataset belongs to the workload');
  const cfg = await startCfg(w, doc, calls);
  assert.deepEqual([cfg.target.uris, cfg.run.duration, cfg.run.concurrency, cfg.workload.preset], [['valkey://10.1.2.3:6380'], '45s', 12, 'queue']);
  close();
});

test('Use defaults on a card starts from the defaults but keeps where the run connects', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  openTask(w, doc, 'preset:cache');
  setValue(w, q(doc, '#f-uri'), 'valkey://10.1.2.3:6380');
  setValue(w, q(doc, '#f-duration'), '45s');
  setValue(w, q(doc, '#f-conc'), '12');
  setValue(w, q(doc, '#f-srv'), true);
  setValue(w, q(doc, '#f-label'), 'my run');
  click(w, q(doc, '#brandHome'));
  useDefaults(w, doc, 'preset:session');
  assert.equal(q(doc, '#taskTitle').textContent, 'Session store');
  assert.equal(q(doc, '#f-uri').value, 'valkey://10.1.2.3:6380', 'the address is kept');
  assert.equal(q(doc, '#f-duration').value, '10s');
  assert.equal(q(doc, '#f-conc').value, '64');
  assert.equal(q(doc, '#f-srv').checked, false, 'opt-in settings are off again');
  assert.equal(q(doc, '#f-label').value, '');
  const cfg = await startCfg(w, doc, calls);
  assert.equal(cfg.workload.preset, 'session');
  close();
});

test('the worker count of a workload is undone on switching unless you changed it', async () => {
  const a = await loadPage(START);
  openTask(a.w, a.doc, 'preset:big-keys');
  assert.equal(q(a.doc, '#f-conc').value, '16');
  setValue(a.w, q(a.doc, '#taskSel'), 'preset:cache', 'change');
  assert.equal(q(a.doc, '#f-conc').value, '64', 'the default is back');
  a.close();
  const b = await loadPage(START);
  openTask(b.w, b.doc, 'preset:big-keys');
  setValue(b.w, q(b.doc, '#f-conc'), '32');
  setValue(b.w, q(b.doc, '#taskSel'), 'preset:cache', 'change');
  assert.equal(q(b.doc, '#f-conc').value, '32', 'your own value is kept');
  b.close();
});

test('Reset to defaults puts the workload back and keeps the address', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  openTask(w, doc, 'preset:counters');
  setValue(w, q(doc, '#f-uri'), 'valkey://h:1');
  setValue(w, q(doc, '#f-keys'), '5');
  setValue(w, q(doc, '[data-mix-row="0"] [data-mix-field="weight"]'), '99');
  assert.equal(q(doc, '#taskTitle').textContent, 'Custom workload', 'editing the operations turns the preset off');
  click(w, q(doc, '#taskReset'));
  assert.equal(q(doc, '#taskTitle').textContent, 'Custom workload');
  assert.equal(q(doc, '#f-uri').value, 'valkey://h:1');
  assert.deepEqual(ops(doc), ['GET', 'SET']);
  assert.equal(q(doc, '#f-keys').value, '100000');
  const cfg = await startCfg(w, doc, calls);
  assert.equal(cfg.dataset.key_count, 100000);
  close();
});

test('the sweep workload is a worker sweep with its levels already chosen, in the Advanced category that holds them', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  openTask(w, doc, 'sweep');
  assert.equal(q(doc, '#taskTitle').textContent, 'Find the limit');
  assert.equal(q(doc, '#lvlAdvanced').checked, true, 'the settings it needs are Advanced');
  assert.equal(q(doc, '[data-cat-btn="execution"]').getAttribute('aria-current'), 'true');
  assert.ok(visible(doc, '#f-swvals'));
  assert.equal(q(doc, '#f-swvals').value, '4, 16, 64, 128');
  assert.match(q(doc, '#launchSummary').textContent, /sweep of 4 runs by worker count/);
  const cfg = await startCfg(w, doc, calls);
  assert.deepEqual([cfg.run.sweep.kind, cfg.run.sweep.values], ['workers', [4, 16, 64, 128]]);
  setValue(w, q(doc, '#taskSel'), 'standard', 'change');
  const cfg2 = await startCfg(w, doc, calls);
  assert.equal(cfg2.run.sweep.kind, 'none', 'another workload turns the sweep off');
  close();
});

test('your own commands opens the command box and uses its lines as the workload', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  openTask(w, doc, 'raw');
  assert.equal(q(doc, '[data-cat-btn="data"]').getAttribute('aria-current'), 'true');
  assert.ok(visible(doc, '#rawText'));
  const cfg = await startCfg(w, doc, calls);
  assert.ok(cfg.workload.mix.length >= 2 && cfg.workload.mix.every(m => m.op === 'RAW'), 'the example commands are the workload until you change them');
  close();
});

test('the custom workload starts from the default mix and no preset', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  openTask(w, doc, 'preset:mixed');
  setValue(w, q(doc, '#taskSel'), 'custom', 'change');
  assert.equal(q(doc, '#presetSel').value, '');
  assert.deepEqual(ops(doc), ['GET', 'SET']);
  assert.equal((await startCfg(w, doc, calls)).workload.preset, undefined);
  close();
});

test('the preset selector inside the Workload card keeps the chosen workload in step', async () => {
  const { w, doc, close } = await loadPage(START);
  openTask(w, doc, 'standard');
  setValue(w, q(doc, '#presetSel'), 'cache', 'change');
  assert.equal(q(doc, '#taskTitle').textContent, 'Cache');
  assert.equal(q(doc, '#taskSel').value, 'preset:cache');
  setValue(w, q(doc, '#presetSel'), '', 'change');
  assert.equal(q(doc, '#taskTitle').textContent, 'Custom workload');
  close();
});

test('the brand and All workloads return to the front page without losing the settings', async () => {
  const { w, doc, close } = await loadPage(START);
  openTask(w, doc, 'preset:cache');
  setValue(w, q(doc, '#f-duration'), '33s');
  click(w, q(doc, '#backHome'));
  assert.equal(q(doc, '#home').hidden, false);
  assert.equal(q(doc, '#mainTabs').hidden, true);
  openTask(w, doc, 'preset:cache');
  assert.equal(q(doc, '#f-duration').value, '33s');
  click(w, q(doc, '#brandHome'));
  assert.equal(q(doc, '#home').hidden, false);
  close();
});

test('Completed Runs and the results cards open their sections', async () => {
  const { w, doc, close } = await loadPage();
  click(w, q(doc, '#histBtn'));
  await tick(30);
  assert.equal(q(doc, '#panel-history').hidden, false);
  assert.equal(q(doc, '#mainTabs').hidden, false);
  for (const [tab, panel] of [['compare', 'panel-compare'], ['replay', 'panel-replay'], ['trends', 'panel-trends'], ['history', 'panel-history']]) {
    click(w, q(doc, '#brandHome'));
    click(w, q(doc, `[data-explore="${tab}"] .task-main`));
    await tick(30);
    assert.equal(doc.getElementById(panel).hidden, false, tab);
  }
  close();
});

test('the header shows the run clock and the cards are locked while a run is going', async () => {
  const live = clone(fixture('live_running.json'));
  const { w, doc, calls, close } = await loadPage({ '/api/live': live, ...START });
  assert.equal(q(doc, '#statusBadge').textContent, 'running');
  assert.equal(q(doc, '#runClock').textContent, '00:03 / 00:10');
  assert.equal(q(doc, '#panel-live').hidden, false, 'a run in progress opens on Live Metrics');
  click(w, q(doc, '#brandHome'));
  assert.ok(qa(doc, '.task-main').every(b => b.disabled), 'the workload cards are locked');
  assert.ok(qa(doc, '.task-defaults').every(b => b.disabled));
  click(w, q(doc, '[data-task="preset:cache"] .task-main'));
  assert.equal(q(doc, '#home').hidden, false, 'a locked card does nothing');
  close();
});

test('Exit asks, stops the server and says so', async () => {
  const { w, doc, calls, close } = await loadPage({ '/api/shutdown': { status: 200, body: { stopping: true } } });
  w.confirm = () => false;
  click(w, q(doc, '#exitBtn'));
  await tick(10);
  assert.equal(calls.filter(c => c.url === '/api/shutdown').length, 0, 'declining does nothing');
  w.confirm = () => true;
  click(w, q(doc, '#exitBtn'));
  await tick(20);
  assert.equal(calls.filter(c => c.url === '/api/shutdown').length, 1);
  assert.match(doc.body.textContent, /PLGV has stopped/);
  close();
});

test('seed and deterministic traffic are Advanced settings in their own category, off by default', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  openTask(w, doc, 'standard');
  assert.ok(!visible(doc, '#f-det'));
  setValue(w, q(doc, '#lvlAdvanced'), true);
  openCat(w, doc, 'seed');
  assert.ok(visible(doc, '#f-seed') && visible(doc, '#f-budget') && visible(doc, '#f-det'));
  assert.equal(q(doc, '#f-det').checked, false);
  assert.equal(q(doc, '#f-budget').value, '0');
  assert.match(q(doc, '#detNote').textContent, /traffic fingerprint/);
  assert.match(q(doc, '#detNote').textContent, /Off by default/);
  setValue(w, q(doc, '#f-budget'), '5000');
  setValue(w, q(doc, '#f-det'), true);
  const cfg = await startCfg(w, doc, calls);
  assert.deepEqual([cfg.run.op_budget, cfg.run.deterministic], [5000, true]);
  assert.equal(typeof cfg.run.op_budget, 'number');
  close();
});

test('a #preset= link opens that workload with its defaults', async () => {
  const { doc, close } = await loadPage({}, { hash: '#preset=big-keys' });
  assert.equal(q(doc, '#home').hidden, true);
  assert.equal(q(doc, '#taskTitle').textContent, 'Big keys stress');
  assert.equal(q(doc, '#f-conc').value, '16');
  close();
});

test('the categories are keyboard reachable buttons and the current one is marked', async () => {
  const { w, doc, close } = await loadPage();
  openTask(w, doc);
  setValue(w, q(doc, '#lvlAdvanced'), true);
  const btns = qa(doc, '#catNav button');
  assert.ok(btns.length >= 5 && btns.every(b => b.tagName === 'BUTTON'));
  assert.equal(btns.filter(b => b.getAttribute('aria-current') === 'true').length, 1);
  assert.equal(q(doc, '#catNav').getAttribute('aria-label'), 'Settings categories');
  openCat(w, doc, 'data');
  assert.equal(q(doc, '[data-cat-btn="data"]').getAttribute('aria-current'), 'true');
  assert.equal(q(doc, '[data-cat-btn="basic"]').getAttribute('aria-current'), 'false');
  // Turning Advanced off while on an Advanced category falls back to Basic settings.
  setValue(w, q(doc, '#lvlAdvanced'), false);
  assert.ok(visible(doc, '#f-uri'));
  close();
});

test('after a run the clock shows the finished run\'s real length', async () => {
  const live = { state: 'done', run_id: '20261104-100000', label: 'x', duration_seconds: 10, elapsed_seconds: 0, series: [], per_op: [], ops: 0, errors: 0, misses: 0 };
  const { doc, close } = await loadPage({ '/api/live': live });
  await tick(30);
  const run = fixture('run_a.json');
  const mm = Math.round(run.client.elapsed_seconds);
  assert.equal(q(doc, '#statusBadge').textContent, 'finished');
  assert.equal(q(doc, '#runClock').textContent, `00:${String(mm).padStart(2, '0')} / 00:10`);
  close();
});
