import test from 'node:test';
import assert from 'node:assert/strict';
import { openTask, openCat, loadPage, fixture, clone, tick, click, setValue, startCall } from './harness.mjs';

const START = { '/api/start': { status: 202, body: { started: true } } };
const ID_A = '20261104-100000';
const q = (doc, sel) => doc.querySelector(sel);
const qa = (doc, sel) => Array.from(doc.querySelectorAll(sel));
const visible = (doc, sel) => !doc.querySelector(sel).closest('[hidden]');
const mixOf = async (w, doc, calls) => { click(w, doc.getElementById('startBtn')); await tick(20); return startCall(calls).body.config.workload.mix; };
const rowSel = (i, f) => `[data-mix-row="${i}"] [data-mix-field="${f}"]`;

test('defaults: no pub/sub, no raw commands, dangerous commands not allowed', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  click(w, doc.getElementById('startBtn'));
  await tick(20);
  const cfg = startCall(calls).body.config;
  assert.equal(cfg.workload.pubsub.subscribers, 0);
  assert.equal(cfg.workload.allow_dangerous, false);
  assert.ok(cfg.workload.mix.every(m => m.op !== 'RAW'));
  assert.equal(doc.getElementById('f-danger').checked, false);
  close();
});

test('the operation list offers every family', async () => {
  const { doc, close } = await loadPage(START);
  const groups = qa(doc, `${rowSel(0, 'op')} optgroup`).map(g => g.label);
  for (const g of ['Streams', 'Geo', 'HyperLogLog', 'Bitmaps', 'Lua and functions', 'Pub/Sub', 'Transactions and pipelines', 'Raw command']) assert.ok(groups.includes(g), g);
  close();
});

test('choosing TXN gives it steps that can be edited, and only simple operations are offered', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  setValue(w, q(doc, rowSel(0, 'op')), 'TXN', 'change');
  assert.equal(qa(doc, '[data-mix-row="0"] [data-step-row]').length, 2);
  const stepOps = qa(doc, '[data-mix-row="0"] [data-step-row="0"] option').map(o => o.value);
  assert.ok(stepOps.includes('SET') && !stepOps.includes('TXN') && !stepOps.includes('RAW') && !stepOps.includes('PUBLISH'));
  click(w, q(doc, '[data-step-add="0"]'));
  assert.equal(qa(doc, '[data-mix-row="0"] [data-step-row]').length, 3);
  setValue(w, q(doc, '[data-mix-row="0"] [data-step-row="2"] [data-mix-field="op"]'), 'HSET', 'change');
  click(w, q(doc, '[data-step-remove="0"]'));
  const mix = await mixOf(w, doc, calls);
  assert.deepEqual(mix[0].steps.map(s => s.op), ['SET', 'HSET']);
  close();
});

test('scripts: a built-in script, then inline Lua with arguments', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  setValue(w, q(doc, rowSel(0, 'op')), 'EVAL', 'change');
  assert.equal(q(doc, rowSel(0, 'script')).value, 'rate_limit');
  setValue(w, q(doc, rowSel(0, 'script')), '', 'change');
  setValue(w, q(doc, 'textarea[data-mix-field="source"]'), "return redis.call('GET', KEYS[1])");
  setValue(w, q(doc, rowSel(0, 'args')), '5, 10');
  const mix = await mixOf(w, doc, calls);
  assert.equal(mix[0].op, 'EVAL');
  assert.equal(mix[0].script, '');
  assert.match(mix[0].source, /redis\.call/);
  assert.deepEqual(mix[0].args, ['5', '10']);
  close();
});

test('blocking and stream parameters: timeout, type list with nine types', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  setValue(w, q(doc, rowSel(0, 'op')), 'BRPOP', 'change');
  setValue(w, q(doc, rowSel(0, 'timeout')), '250ms');
  setValue(w, q(doc, rowSel(0, 'op')), 'BRPOP', 'change');
  setValue(w, q(doc, rowSel(0, 'timeout')), '250ms');
  const mix = await mixOf(w, doc, calls);
  assert.equal(mix[0].timeout, '250ms');
  setValue(w, q(doc, rowSel(0, 'op')), 'DEL', 'change');
  assert.equal(qa(doc, `${rowSel(0, 'type')} option`).length, 9);
  close();
});

test('a raw command row takes its command text', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  setValue(w, q(doc, rowSel(0, 'op')), 'RAW', 'change');
  assert.equal(q(doc, rowSel(0, 'command')).value, 'GET {key}');
  setValue(w, q(doc, rowSel(0, 'command')), 'HSET {key:hash} visits {n}');
  const mix = await mixOf(w, doc, calls);
  assert.equal(mix[0].command, 'HSET {key:hash} visits {n}');
  close();
});

test('the raw injector replaces or extends the mix from pasted lines', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  openTask(w, doc, 'custom');
  setValue(w, doc.getElementById('lvlAdvanced'), true);
  assert.ok(!visible(doc, '#rawText'), 'the raw box is in Data and commands, not in Basic settings');
  openCat(w, doc, 'data');
  assert.ok(visible(doc, '#rawText'));
  setValue(w, doc.getElementById('rawText'), '# comment\n6 GET {key}\n\n2x SET {key} {value} EX 60\nINCR counter\n');
  click(w, doc.getElementById('rawReplace'));
  assert.match(q(doc, '#rawMsg').textContent, /3 raw commands/);
  let mix = await mixOf(w, doc, calls);
  assert.deepEqual(mix.map(m => [m.op, m.weight, m.command]), [['RAW', 6, 'GET {key}'], ['RAW', 2, 'SET {key} {value} EX 60'], ['RAW', 1, 'INCR counter']]);
  setValue(w, doc.getElementById('rawText'), 'PING');
  click(w, doc.getElementById('rawAdd'));
  assert.equal(qa(doc, '[data-mix-row]').length, 4);
  setValue(w, doc.getElementById('rawText'), '  \n# nothing\n');
  click(w, doc.getElementById('rawAdd'));
  assert.match(q(doc, '#rawMsg').textContent, /at least one command/);
  assert.equal(qa(doc, '[data-mix-row]').length, 4, 'nothing changed');
  close();
});

test('the dangerous-commands box explains itself, is off, and is sent when ticked', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  setValue(w, doc.getElementById('lvlAdvanced'), true);
  assert.match(q(doc, '#dangerNote').textContent, /FLUSHALL/);
  assert.match(q(doc, '#dangerNote').textContent, /never allowed/);
  setValue(w, doc.getElementById('f-danger'), true);
  click(w, doc.getElementById('startBtn'));
  await tick(20);
  assert.equal(startCall(calls).body.config.workload.allow_dangerous, true);
  close();
});

test('pub/sub and dataset settings are Advanced and reach the config', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  assert.ok(!visible(doc, '#f-psub'));
  setValue(w, doc.getElementById('lvlAdvanced'), true);
  setValue(w, doc.getElementById('f-psub'), '3');
  setValue(w, doc.getElementById('f-pch'), '4');
  setValue(w, doc.getElementById('f-psh'), true);
  setValue(w, doc.getElementById('f-stream'), '50');
  setValue(w, doc.getElementById('f-bits'), '2048');
  click(w, doc.getElementById('startBtn'));
  await tick(20);
  const cfg = startCall(calls).body.config;
  assert.deepEqual(cfg.workload.pubsub, { subscribers: 3, channels: 4, sharded: true });
  assert.equal(cfg.dataset.stream_length, 50);
  assert.equal(cfg.dataset.bitmap_bits, 2048);
  close();
});

test('server errors land on the new fields, including inside steps', async () => {
  const errors = [
    { path: 'workload.mix[0].command', message: 'FLUSHALL is blocked' },
    { path: 'workload.mix[1].steps[0].op', message: 'MULTI is not allowed' },
    { path: 'workload.pubsub.subscribers', message: 'must be >= 0' },
  ];
  const { w, doc, close } = await loadPage({ '/api/start': { status: 400, body: { error: 'x', errors } } });
  setValue(w, q(doc, rowSel(0, 'op')), 'RAW', 'change');
  click(w, doc.getElementById('mixAdd'));
  setValue(w, q(doc, rowSel(1, 'op')), 'TXN', 'change');
  click(w, doc.getElementById('startBtn'));
  await tick(20);
  assert.equal(q(doc, rowSel(0, 'command')).getAttribute('aria-invalid'), 'true');
  assert.match(q(doc, rowSel(0, 'command')).nextElementSibling.textContent, /FLUSHALL is blocked/);
  assert.equal(q(doc, '[data-mix-row="1"] [data-mstep="0"][data-mix-field="op"]').getAttribute('aria-invalid'), 'true');
  assert.equal(doc.getElementById('f-psub').getAttribute('aria-invalid'), 'true');
  assert.ok(doc.querySelector('[data-cat-btn="execution"] .dot'), 'the category of the Advanced pub/sub card is marked');
  openCat(w, doc, 'execution');
  assert.ok(visible(doc, '#f-psub'), 'the hidden Advanced card is reachable');
  close();
});

test('command text is shown as text, never as markup', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  setValue(w, q(doc, rowSel(0, 'op')), 'RAW', 'change');
  setValue(w, q(doc, rowSel(0, 'command')), '<img src=x onerror=alert(1)>');
  click(w, doc.getElementById('mixAdd'));
  assert.equal(qa(doc, '#mixBody img').length, 0);
  assert.equal(q(doc, rowSel(0, 'command')).value, '<img src=x onerror=alert(1)>');
  close();
});

test('a run with subscribers shows the delivery result; one without hides it', async () => {
  const run = clone(fixture('run_a.json'));
  run.client.pubsub = { subscribers: 2, channels: 8, sharded: false, published: 1000, expected: 2000, delivered: 1990, delivery_ratio: 0.995, mean_ms: 0.4, p50_ms: 0.3, p95_ms: 0.8, p99_ms: 1.2, max_ms: 3.5 };
  run.series.forEach((p, i) => { p.delivered = 10; p.delivery_p50_ms = 0.3; p.delivery_p99_ms = 1.2; });
  const a = await loadPage({ ['/api/runs/' + ID_A]: run }, { hash: `#run=${ID_A}&tab=latency` });
  await tick(30);
  assert.ok(visible(a.doc, '#psBlock'));
  const t = q(a.doc, '#psKpis').textContent;
  assert.match(t, /1,?990/);
  assert.match(t, /99\.5%/);
  assert.match(t, /2 channels|8 channels/);
  a.close();
  const b = await loadPage({}, { hash: `#run=${ID_A}&tab=latency` });
  await tick(30);
  assert.ok(!visible(b.doc, '#psBlock'));
  b.close();
});

test('the per-operation table explains what a miss means for WATCH_INCR and blocking reads', async () => {
  const { doc, close } = await loadPage(START);
  assert.match(q(doc, '#opNote').textContent, /WATCH_INCR/);
  assert.match(q(doc, '#opNote').textContent, /timeout/);
  close();
});
