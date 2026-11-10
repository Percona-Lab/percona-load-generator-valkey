import test from 'node:test';
import assert from 'node:assert/strict';
import { openTask, openCat, loadPage, fixture, clone, tick, setValue, startCall, click } from './harness.mjs';

const START = { '/api/start': { status: 202, body: { started: true } } };
const SC = '20261104-230000';
const SCRUN = { ['/api/runs/' + SC]: fixture('run_srvconfig.json') };
const q = (doc, sel) => doc.querySelector(sel);
const qa = (doc, sel) => Array.from(doc.querySelectorAll(sel));
const visible = (doc, sel) => !doc.querySelector(sel).closest('[hidden]');
const cfgOf = async (w, doc, calls) => { click(w, doc.getElementById('startBtn')); await tick(20); return startCall(calls).body.config; };

test('server configuration is off by default, explained, and sends the name when on', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  openTask(w, doc);
  setValue(w, doc.getElementById('lvlAdvanced'), true);
  openCat(w, doc, 'metrics');
  assert.ok(visible(doc, '#f-sc'));
  assert.equal(doc.getElementById('f-sc').checked, false);
  assert.ok(!visible(doc, '#f-scname'), 'the name field appears once it is on');
  const note = q(doc, '#scNote').textContent;
  assert.match(note, /CONFIG GET/);
  assert.match(note, /never changes a setting/);
  assert.match(note, /Passwords are hidden/);
  assert.match(note, /Off by default/);
  const cfg = await cfgOf(w, doc, calls);
  assert.ok(!(cfg.server_config && cfg.server_config.enabled), 'opt-in: nothing is sent while it is off');
  close();
});

test('turning server configuration on shows the name field and sends both settings', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  openTask(w, doc);
  setValue(w, doc.getElementById('lvlAdvanced'), true);
  openCat(w, doc, 'metrics');
  setValue(w, doc.getElementById('f-sc'), true);
  assert.ok(visible(doc, '#f-scname'));
  setValue(w, doc.getElementById('f-scname'), 'valkey-9.1-io4');
  const cfg = await cfgOf(w, doc, calls);
  assert.deepEqual(cfg.server_config, { enabled: true, name: 'valkey-9.1-io4' });
  close();
});

test('the Server tab shows the recorded settings, with masked secrets, a node picker and filters', async () => {
  const { doc, close } = await loadPage(SCRUN, { hash: `#run=${SC}&tab=server` });
  await tick(30);
  assert.ok(visible(doc, '#scView'));
  assert.match(q(doc, '#scTitle').textContent, /valkey-9\.1-io4/);
  assert.match(q(doc, '#scMsg').textContent, /1 setting\(s\) differ between servers/);
  assert.equal(qa(doc, '#scNode option').length, 2);
  const rows = () => qa(doc, '#scTable tbody tr').map(r => r.textContent);
  assert.equal(rows().length, 5);
  assert.ok(rows().some(r => /requirepass\*\*\*/.test(r)), 'the secret stays masked');
  const fsel = doc.getElementById('scFilter');
  fsel.value = 'changed'; fsel.dispatchEvent(new doc.defaultView.Event('change', { bubbles: true }));
  assert.equal(rows().length, 1);
  assert.match(rows()[0], /hz.*changed to 20 during the run/);
  fsel.value = 'differ'; fsel.dispatchEvent(new doc.defaultView.Event('change', { bubbles: true }));
  assert.match(rows().join(' '), /maxmemory0/);
  fsel.value = 'all';
  const search = doc.getElementById('scSearch');
  search.value = 'io-thr'; search.dispatchEvent(new doc.defaultView.Event('input', { bubbles: true }));
  assert.equal(rows().length, 1);
  close();
});

test('a run without recorded settings shows no server configuration card', async () => {
  const { doc, close } = await loadPage({}, { hash: '#run=20261104-100000&tab=server' });
  await tick(30);
  assert.equal(doc.getElementById('scView').hidden, true);
  close();
});

test('a blocked CONFIG is explained in plain words', async () => {
  const run = clone(fixture('run_srvconfig.json'));
  run.server_config = { name: 'locked-down', status: 'not_permitted', nodes: [{ addr: '127.0.0.1:6379', status: 'not_permitted', error: 'NOPERM' }] };
  const { doc, close } = await loadPage({ ['/api/runs/' + SC]: run }, { hash: `#run=${SC}&tab=server` });
  await tick(30);
  assert.match(q(doc, '#scMsg').textContent, /does not allow CONFIG GET/);
  assert.match(q(doc, '#scMsg').textContent, /run itself was not affected/);
  assert.equal(doc.getElementById('scTools').hidden, true);
  close();
});

test('Run History shows the server setup and filters by it', async () => {
  const runs = clone(fixture('runs.json'));
  const list = runs.runs || runs;
  list[0].server_setup = 'setup-a'; list[1].server_setup = 'setup-b';
  const { w, doc, close } = await loadPage({ '/api/runs': runs }, { hash: '#tab=history' });
  await tick(30);
  const sel = doc.getElementById('histSetup');
  assert.ok(visible(doc, '#histSetup'));
  assert.deepEqual(qa(doc, '#histSetup option').map(o => o.value), ['', 'setup-a', 'setup-b']);
  const total = qa(doc, '#histTable tbody tr').length;
  setValue(w, sel, 'setup-a', 'change');
  const left = qa(doc, '#histTable tbody tr');
  assert.equal(left.length, 1);
  assert.match(left[0].textContent, /setup-a/);
  assert.ok(total > 1);
  close();
});

test('Run History hides the setup filter when no run has a setup name', async () => {
  const { doc, close } = await loadPage({}, { hash: '#tab=history' });
  await tick(30);
  assert.ok(!visible(doc, '#histSetup'));
  close();
});

test('Compare Runs lists the server settings that differ, or says why it cannot', async () => {
  const cmp = clone(fixture('compare_different.json'));
  cmp.A.server_config = true; cmp.B.server_config = true; cmp.A.server_setup = 'one'; cmp.B.server_setup = 'four';
  cmp.server_config_diffs = [{ key: 'io-threads', a: '1', b: '4' }];
  const IDS = ['20261104-100000', '20261104-130000'];
  let p = await loadPage({ '/api/compare': cmp }, { hash: `#compare=${IDS[0]},${IDS[1]}` });
  await tick(30);
  const rows = qa(p.doc, '#cmpSrvDiffs tbody tr');
  assert.equal(rows.length, 1);
  assert.match(rows[0].textContent, /io-threads14/);
  assert.match(q(p.doc, '#cmpSrvDiffs thead').textContent, /A \(one\).*B \(four\)/);
  p.close();
  const none = clone(fixture('compare_different.json'));
  p = await loadPage({ '/api/compare': none }, { hash: `#compare=${IDS[0]},${IDS[1]}` });
  await tick(30);
  assert.match(q(p.doc, '#cmpSrvNote').textContent, /Neither run recorded/);
  p.close();
});
