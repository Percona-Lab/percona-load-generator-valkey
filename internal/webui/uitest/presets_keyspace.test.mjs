import test from 'node:test';
import assert from 'node:assert/strict';
import { openTask, openCat, loadPage, fixture, clone, tick, click, setValue, startCall } from './harness.mjs';

const START = { '/api/start': { status: 202, body: { started: true } } };
const KS = '20261104-220000';
const KSRUN = { ['/api/runs/' + KS]: fixture('run_keyspace.json') };
const q = (doc, sel) => doc.querySelector(sel);
const qa = (doc, sel) => Array.from(doc.querySelectorAll(sel));
const visible = (doc, sel) => !doc.querySelector(sel).closest('[hidden]');
const cfgOf = async (w, doc, calls) => { click(w, doc.getElementById('startBtn')); await tick(20); return startCall(calls).body.config; };

test('the preset list offers every preset, none chosen by default', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  const sel = doc.getElementById('presetSel');
  assert.equal(sel.value, '');
  const names = qa(doc, '#presetSel option').map(o => o.value).filter(Boolean);
  assert.deepEqual(names, ['cache', 'session', 'leaderboard', 'queue', 'rate-limiter', 'pubsub-fanout', 'counters', 'big-keys', 'mixed']);
  const cfg = await cfgOf(w, doc, calls);
  assert.equal(cfg.workload.preset, undefined, 'presets are opt-in');
  assert.equal(cfg.keyspace_analysis.enabled, false, 'key-space analysis is opt-in');
  close();
});

test('choosing a preset fills the mix, dataset and pub/sub, and sends the preset name', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  setValue(w, doc.getElementById('presetSel'), 'leaderboard', 'change');
  assert.deepEqual(qa(doc, '#mixBody [data-mix-field="op"]').map(s => s.value), ['ZADD', 'ZINCRBY', 'ZRANK', 'ZSCORE', 'ZRANGE', 'ZCARD']);
  assert.equal(doc.getElementById('f-dist') ? doc.getElementById('f-dist').value : 'zipf', 'zipf');
  assert.match(q(doc, '#presetNote').textContent, /Sorted-set ranking/);
  const cfg = await cfgOf(w, doc, calls);
  assert.equal(cfg.workload.preset, 'leaderboard');
  assert.equal(cfg.dataset.key_distribution, 'zipf');
  assert.equal(cfg.dataset.collection_size, 1000);
  assert.equal(cfg.run.concurrency, 64, 'a preset that does not set workers leaves the user value');
  close();
});

test('a preset with subscribers fills the pub/sub settings; big-keys sets the workers', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  setValue(w, doc.getElementById('presetSel'), 'pubsub-fanout', 'change');
  let cfg = await cfgOf(w, doc, calls);
  assert.deepEqual(cfg.workload.pubsub, { subscribers: 8, channels: 32, sharded: false });
  setValue(w, doc.getElementById('presetSel'), 'big-keys', 'change');
  assert.equal(doc.getElementById('f-conc') ? doc.getElementById('f-conc').value : '16', '16');
  cfg = await cfgOf(w, doc, calls);
  assert.equal(cfg.run.concurrency, 16);
  assert.equal(cfg.workload.pubsub.subscribers, 0, 'the previous preset is fully replaced');
  close();
});

test('editing the operations turns the preset off, and None keeps the mix', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  setValue(w, doc.getElementById('presetSel'), 'cache', 'change');
  setValue(w, q(doc, '[data-mix-row="0"] [data-mix-field="weight"]'), '5');
  assert.equal(doc.getElementById('presetSel').value, '');
  let cfg = await cfgOf(w, doc, calls);
  assert.equal(cfg.workload.preset, undefined);
  assert.equal(cfg.workload.mix[0].weight, 5);
  setValue(w, doc.getElementById('presetSel'), 'session', 'change');
  click(w, doc.getElementById('mixAdd'));
  assert.equal(doc.getElementById('presetSel').value, '', 'adding an operation also turns it off');
  setValue(w, doc.getElementById('presetSel'), 'session', 'change');
  setValue(w, doc.getElementById('presetSel'), '', 'change');
  cfg = await cfgOf(w, doc, calls);
  assert.equal(cfg.workload.preset, undefined);
  assert.ok(cfg.workload.mix.length > 1, 'choosing None keeps the operations');
  close();
});

test('key-space analysis is an Advanced setting with an explanation, off by default', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  openTask(w, doc);
  assert.ok(!visible(doc, '#f-ks'));
  setValue(w, doc.getElementById('lvlAdvanced'), true);
  openCat(w, doc, 'metrics');
  assert.ok(visible(doc, '#f-ks'));
  assert.equal(doc.getElementById('f-ks').checked, false);
  assert.ok(!visible(doc, '#f-kssamples'), 'options appear once it is on');
  assert.match(q(doc, '#ksSetNote').textContent, /only reads/);
  assert.match(q(doc, '#ksSetNote').textContent, /Off by default/);
  setValue(w, doc.getElementById('f-ks'), true);
  assert.ok(visible(doc, '#f-kssamples'));
  setValue(w, doc.getElementById('f-kssamples'), '2000');
  setValue(w, doc.getElementById('f-kstop'), '5');
  setValue(w, doc.getElementById('f-ksmatch'), 'plgv:h:*');
  const cfg = await cfgOf(w, doc, calls);
  assert.deepEqual(cfg.keyspace_analysis, { enabled: true, samples: 2000, top: 5, match: 'plgv:h:*' });
  close();
});

test('the Key Space tab shows types, sizes, expiry and the biggest keys of a run', async () => {
  const { doc, close } = await loadPage(KSRUN, { hash: `#run=${KS}&tab=keyspace` });
  await tick(30);
  assert.ok(visible(doc, '#ksBody'));
  const kp = q(doc, '#ksKpis').textContent;
  assert.match(kp, /200/);
  assert.match(kp, /118\.0 MiB/);
  assert.match(kp, /1\.4 MiB/);
  assert.match(kp, /50 keys over 1 MiB/);
  assert.match(q(doc, '#ksNote').textContent, /All 200 keys matching plgv:\*/);
  const types = qa(doc, '#ksTypes tbody tr').map(r => r.children[0].textContent);
  assert.deepEqual(types, ['list', 'zset', 'set', 'hash']);
  assert.equal(qa(doc, '#ksSizes .hist-row').length, 7);
  assert.equal(qa(doc, '#ksTTL .hist-row').length, 6);
  assert.equal(q(doc, '#ksSizes .hist-row:nth-child(5) .hist-fill').style.width, '100%');
  const big = qa(doc, '#ksBig tbody tr');
  assert.equal(big.length, 3);
  assert.match(big[0].textContent, /plgv:l:0.*list.*1\.4 MiB.*10,?000.*never/);
  assert.match(big[2].textContent, /1\.0 h/);
  close();
});

test('the big-keys finding is listed in Insights', async () => {
  const { doc, close } = await loadPage(KSRUN, { hash: `#run=${KS}&tab=insights` });
  await tick(30);
  assert.match(q(doc, '#insBody').textContent, /Big keys found: the largest is 1\.4 MiB/);
  close();
});

test('a run without the analysis says so and points to Settings; no run says to run one', async () => {
  const a = await loadPage({}, { hash: '#run=20261104-100000&tab=keyspace' });
  await tick(30);
  assert.ok(!visible(a.doc, '#ksEmpty') === false);
  assert.match(q(a.doc, '#ksEmpty').textContent, /Key-space analysis was off for this run/);
  click(a.w, q(a.doc, '#ksEmpty button'));
  assert.ok(visible(a.doc, '#f-ks'), 'the button opens the category that holds the Advanced setting');
  assert.equal(q(a.doc, '[data-cat-btn="metrics"]').getAttribute('aria-current'), 'true');
  a.close();
  const b = await loadPage({}, { hash: '#tab=keyspace' });
  assert.match(q(b.doc, '#ksEmpty').textContent, /after a run/);
  b.close();
});

test('a failed analysis shows the reason instead of empty tables', async () => {
  const run = clone(fixture('run_keyspace.json'));
  run.keyspace = { requested: 10000, sampled: 0, match: 'plgv:*', errors: 1, first_error: 'NOPERM this user has no permissions to run the memory command', types: [], big_keys: [], sizes: [], ttl: [] };
  run.findings = [];
  const { doc, close } = await loadPage({ ['/api/runs/' + KS]: run }, { hash: `#run=${KS}&tab=keyspace` });
  await tick(30);
  assert.ok(!visible(doc, '#ksBody') || q(doc, '#ksBody').hidden);
  assert.match(q(doc, '#ksEmpty').textContent, /No keys were analyzed matching plgv:\*/);
  assert.match(q(doc, '#ksEmpty').textContent, /NOPERM/);
  close();
});

test('key names are shown as text, never as markup', async () => {
  const run = clone(fixture('run_keyspace.json'));
  run.keyspace.big_keys[0].key = '<img src=x onerror=alert(1)>';
  const { doc, close } = await loadPage({ ['/api/runs/' + KS]: run }, { hash: `#run=${KS}&tab=keyspace` });
  await tick(30);
  assert.equal(qa(doc, '#ksBig img').length, 0);
  assert.match(q(doc, '#ksBig').textContent, /<img src=x/);
  close();
});

test('a #preset= link opens the settings with the preset applied', async () => {
  const { doc, close } = await loadPage({}, { hash: '#preset=queue' });
  await tick(30);
  assert.equal(doc.getElementById('presetSel').value, 'queue');
  assert.ok(qa(doc, '#mixBody [data-mix-field="op"]').some(s => s.value === 'XCONSUME'));
  close();
  const bad = await loadPage({}, { hash: '#preset=nope' });
  await tick(30);
  assert.equal(bad.doc.getElementById('presetSel').value, '', 'an unknown preset is ignored');
  bad.close();
});
