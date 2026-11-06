import test from 'node:test';
import assert from 'node:assert/strict';
import { openTask, openCat, loadPage, fixture, clone, tick, click, setValue, startCall } from './harness.mjs';

const RECS = fixture('recordings.json');
const INFO = fixture('recording_info.json');
const STORED = fixture('replay_stored.json');
const START = { '/api/start': { status: 202, body: { started: true } }, '/api/replay': { status: 202, body: { ok: true } } };
const q = (doc, sel) => doc.querySelector(sel);
const qa = (doc, sel) => Array.from(doc.querySelectorAll(sel));
const shown = (doc, sel) => !q(doc, sel).closest('[hidden]');
const last = (calls, url) => calls.filter(c => c.url === url).pop();
const tickN = () => tick(30);
const useReplay = async (opts = {}) => {
  const page = await loadPage({ ...START, ...opts });
  openTask(page.w, page.doc, 'replay');
  await tickN();
  return page;
};

test('the front page offers Record a workload and Replay a recorded workload', async () => {
  const { doc, close } = await loadPage();
  const card = id => q(doc, `[data-task="${id}"]`);
  assert.equal(card('record').querySelector('strong').textContent, 'Record a workload');
  assert.equal(card('replay').querySelector('strong').textContent, 'Replay a recorded workload');
  assert.match(card('record').querySelector('.d').textContent, /save every command/);
  assert.match(card('replay').querySelector('.d').textContent, /Send a saved recording/);
  close();
});

test('Record a workload: its card is on, with defaults, and the start says to record', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  openTask(w, doc, 'record');
  assert.equal(q(doc, '#taskTitle').textContent, 'Record a workload');
  assert.ok(shown(doc, '#recordCard'), 'the Record card is shown');
  assert.match(q(doc, '#recNote').textContent, /treat it like production data/);
  assert.equal(q(doc, '#f-rcomp').value, 'zstd');
  assert.equal(q(doc, '#f-rbp').value, 'fail');
  assert.equal(q(doc, '#f-rpath').value, '', 'a blank path means a new file for every run');
  assert.equal(q(doc, '#startBtn').textContent, 'Start recording');
  assert.ok(shown(doc, '#mixCard'), 'the workload to record is chosen here');
  assert.ok(!shown(doc, '#recordingCard') && !shown(doc, '#replayOptsCard'), 'no replay cards');
  assert.deepEqual(Array.from(doc.querySelectorAll('#mixBody [data-mix-field="op"]')).map(s => s.value), ['GET', 'SET']);
  click(w, q(doc, '#startBtn')); await tickN();
  const body = last(calls, '/api/start').body;
  assert.equal(body.record, true);
  assert.equal(body.config.record.compression, 'zstd');
  assert.equal(body.config.record.path, '');
  assert.equal(body.config.replay, undefined, 'replay settings never go to the run');
  close();
});

test('Record options reach the request, and a path error shows under its field', async () => {
  const { w, doc, calls, close } = await loadPage({ '/api/start': { status: 400, body: { error: 'x', errors: [{ path: 'record.path', message: 'the folder /nope does not exist or cannot be read' }] } } });
  openTask(w, doc, 'record');
  setValue(w, q(doc, '#f-rout'), true); setValue(w, q(doc, '#f-rsum'), true);
  setValue(w, q(doc, '#f-rbp'), 'block'); setValue(w, q(doc, '#f-rmax'), '64'); setValue(w, q(doc, '#f-rcomp'), 'gzip');
  setValue(w, q(doc, '#f-rpath'), '/nope/mine.plgvrec');
  click(w, q(doc, '#startBtn')); await tickN();
  const rec = last(calls, '/api/start').body.config.record;
  assert.deepEqual([rec.outcomes, rec.checksums, rec.backpressure, rec.max_mb, rec.compression, rec.path], [true, true, 'block', 64, 'gzip', '/nope/mine.plgvrec']);
  assert.equal(q(doc, '#f-rpath').getAttribute('aria-invalid'), 'true');
  assert.match(q(doc, '#f-rpath').nextElementSibling.textContent, /does not exist/);
  close();
});

test('other workloads never record, and leaving Record clears its settings', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  openTask(w, doc, 'record');
  setValue(w, q(doc, '#f-rpath'), '/tmp/x.plgvrec');
  q(doc, '#taskSel').value = 'standard'; q(doc, '#taskSel').dispatchEvent(new w.Event('change', { bubbles: true }));
  assert.ok(!shown(doc, '#recordCard'));
  click(w, q(doc, '#startBtn')); await tickN();
  const body = last(calls, '/api/start').body;
  assert.equal(body.record, false);
  assert.equal(body.config.record.path, '');
  close();
});

test('Replay: only the connection and replay cards, the newest recording chosen, defaults selected', async () => {
  const { w, doc, calls, close } = await useReplay();
  assert.equal(q(doc, '#taskTitle').textContent, 'Replay a recorded workload');
  assert.equal(q(doc, '#startBtn').textContent, 'Start replay');
  for (const sel of ['#recordingCard', '#replayOptsCard']) assert.ok(shown(doc, sel), sel);
  for (const sel of ['#mixCard', '#metricsCard', '#recordCard']) assert.ok(!shown(doc, sel), sel + ' is hidden');
  assert.ok(qa(doc, '[data-group="target"]').every(c => !c.hidden), 'the Server card stays');
  assert.ok(qa(doc, '[data-group="run"], [data-group="dataset"], [data-group="saved"]').every(c => c.hidden), 'run settings do not apply');
  const opts = qa(doc, '#rpList option');
  assert.equal(opts.length, RECS.recordings.length);
  assert.equal(q(doc, '#rpList').value, RECS.recordings[0].path, 'the newest recording is selected');
  assert.equal(q(doc, '#rpPath').value, RECS.recordings[0].path);
  assert.equal(q(doc, '#f-rpspeed').value, '1');
  assert.equal(q(doc, '#f-rpiter').value, '1');
  assert.equal(q(doc, '#f-rpreads').checked, false);
  assert.match(q(doc, '#launchSummary').textContent, /^Replay · import-.* · recorded pace · all commands · /);
  close();
});

test('Replay start sends the replay settings apart from the server settings', async () => {
  const { w, doc, calls, close } = await useReplay();
  setValue(w, q(doc, '#f-rpspeed'), '0'); setValue(w, q(doc, '#f-rpiter'), '3'); setValue(w, q(doc, '#f-rpdelay'), '2s'); setValue(w, q(doc, '#f-rpreads'), true);
  setValue(w, q(doc, '#f-uri'), '10.0.0.9:6379');
  click(w, q(doc, '#startBtn')); await tickN();
  const r = last(calls, '/api/replay').body;
  assert.equal(r.replay.path, RECS.recordings[0].path);
  assert.deepEqual([r.replay.speed, r.replay.iterations, r.replay.iteration_delay, r.replay.reads_only], [0, 3, '2s', true]);
  assert.deepEqual(r.config.target.uris, ['10.0.0.9:6379']);
  assert.equal(r.config.replay, undefined);
  assert.equal(startCall(calls), undefined, 'a replay is not a workload run');
  assert.equal(q(doc, '#panel-live').hidden, false, 'the page moves to Live Metrics');
  close();
});

test('Advanced replay: several servers, checks and the import card sit in categories', async () => {
  const { w, doc, calls, close } = await useReplay();
  click(w, q(doc, '#lvlAdvanced')); setValue(w, q(doc, '#lvlAdvanced'), true);
  const cats = qa(doc, '[data-cat-btn]').map(b => b.dataset.catBtn);
  assert.deepEqual(cats, ['basic', 'connection', 'execution', 'data'], 'no seed or metrics categories for a replay');
  openCat(w, doc, 'execution');
  assert.ok(shown(doc, '#replayTargetsCard') && shown(doc, '#replayChecksCard'));
  openCat(w, doc, 'data');
  assert.ok(shown(doc, '#importCard'), 'the import card is under Data and commands');
  openCat(w, doc, 'execution');
  setValue(w, q(doc, '#f-rptargets'), 'current=10.0.0.5:6379\n new=10.0.0.6:6379\n');
  setValue(w, q(doc, '#f-rpconc'), true); setValue(w, q(doc, '#f-rptol'), '25'); setValue(w, q(doc, '#f-rpreq'), true);
  assert.match(q(doc, '#launchSummary').textContent, /2 servers/);
  click(w, q(doc, '#startBtn')); await tickN();
  const r = last(calls, '/api/replay').body.replay;
  assert.deepEqual(r.targets, ['current=10.0.0.5:6379', 'new=10.0.0.6:6379']);
  assert.deepEqual([r.concurrent, r.tolerance, r.require_data, r.allow_dangerous], [true, 25, true, false]);
  close();
});

test('a replay problem is shown under its field and its category opens', async () => {
  const bad = { '/api/replay': { status: 400, body: { error: 'x', errors: [{ path: 'replay.targets', message: 'needs at least two servers' }, { path: 'replay.path', message: 'choose a recording' }] } } };
  const { w, doc, close } = await useReplay(bad);
  click(w, q(doc, '#startBtn')); await tickN();
  assert.equal(q(doc, '#f-rptargets').getAttribute('aria-invalid'), 'true', 'the Advanced level was turned on to reach it');
  assert.match(q(doc, '#startErrors').textContent, /replay was not started/i);
  close();
});

test('recording details come from the server', async () => {
  const { w, doc, calls, close } = await useReplay({ '/api/recordings/info': INFO });
  click(w, q(doc, '#rpInfoBtn')); await tickN();
  assert.match(last(calls, '/api/recordings/info?path=' + encodeURIComponent(RECS.recordings[0].path)) ? 'ok' : '', /ok/);
  const kv = q(doc, '[data-testid="recording-info"]');
  assert.match(kv.textContent, /Status\s*complete/);
  assert.match(kv.textContent, /Commands\s*3/);
  close();
});

test('Delete removes only a recording from the folder, after asking', async () => {
  const { w, doc, calls, close } = await useReplay({ '/api/recordings/delete': { ok: true } });
  const name = RECS.recordings[0].name;
  click(w, q(doc, '#rpDelete')); await tickN();
  const del = calls.find(c => c.method === 'DELETE');
  assert.equal(del.url, '/api/recordings/delete?name=' + encodeURIComponent(name));
  assert.equal(calls.filter(c => c.url === '/api/recordings').length, 2, 'the list is read again after a delete');
  // A path outside the folder cannot be deleted from here.
  setValue(w, q(doc, '#rpPath'), '/elsewhere/other.plgvrec');
  assert.equal(q(doc, '#rpDelete').disabled, true);
  close();
});

test('Upload sends the file as bytes and chooses the stored copy', async () => {
  const stored = clone(RECS.recordings[1]);
  const { w, doc, calls, close } = await useReplay({ '/api/recordings/upload': { recording: stored } });
  const input = q(doc, '#rpUpload');
  const file = { name: 'mine.plgvrec.zst', size: 5 };
  Object.defineProperty(input, 'files', { value: [file], configurable: true });
  input.dispatchEvent(new w.Event('change', { bubbles: true })); await tickN();
  const up = calls.find(c => c.method === 'PUT');
  assert.equal(up.url, '/api/recordings/upload?name=mine.plgvrec.zst');
  assert.equal(up.headers['Content-Type'], 'application/octet-stream');
  assert.equal(up.body, file);
  assert.equal(q(doc, '#rpPath').value, stored.path);
  assert.match(q(doc, '#rpListMsg').textContent, /Uploaded mine\.plgvrec\.zst/);
  close();
});

test('Import converts pasted traffic and chooses the new recording', async () => {
  const result = fixture('import_result.json');
  const { w, doc, calls, close } = await useReplay({ '/api/import-traffic': result });
  click(w, q(doc, '#lvlAdvanced')); setValue(w, q(doc, '#lvlAdvanced'), true); openCat(w, doc, 'data');
  setValue(w, q(doc, '#impText'), '1.0 [0 a:1] "SET" "k" "v"\n'); setValue(w, q(doc, '#impFormat'), 'monitor'); setValue(w, q(doc, '#impComp'), 'gzip'); setValue(w, q(doc, '#impDanger'), true);
  click(w, q(doc, '#impGo')); await tickN();
  const sent = last(calls, '/api/import-traffic').body;
  assert.deepEqual([sent.format, sent.compression, sent.allow_dangerous, sent.text], ['monitor', 'gzip', true, '1.0 [0 a:1] "SET" "k" "v"\n']);
  assert.match(q(doc, '#impMsg').textContent, /Converted \d+ commands/);
  assert.equal(q(doc, '#rpPath').value, result.recording.path);
  close();
});

test('with typed paths off, the path and server-file fields are hidden', async () => {
  const locked = { ...RECS, free_paths: false };
  const { doc, close } = await useReplay({ '/api/recordings': locked });
  assert.equal(q(doc, '#rpFreeRow').hidden, true);
  assert.equal(q(doc, '#impPathRow').hidden, true);
  assert.match(q(doc, '#rpPathNote').textContent, /off because plgv web accepts remote connections/);
  close();
});

test('no recordings yet: the card says how to get one', async () => {
  const { doc, close } = await useReplay({ '/api/recordings': { dir: '/d/recordings', free_paths: true, recordings: [] } });
  assert.match(q(doc, '#rpListMsg').textContent, /No recordings yet/);
  assert.equal(q(doc, '#rpPath').value, '');
  close();
});

test('a running replay shows each server, its state and progress', async () => {
  const { w, doc, close } = await loadPage({ '/api/live': fixture('live_replay_running.json') });
  assert.equal(q(doc, '#panel-live').hidden, false, 'a page opened during a replay shows it');
  assert.equal(q(doc, '#statusBadge').textContent, 'running');
  assert.equal(q(doc, '#replayLive').hidden, false);
  assert.equal(q(doc, '#liveBody').hidden, true);
  const rows = qa(doc, '#rlTargets tbody tr');
  assert.deepEqual(rows.map(r => r.dataset.target), ['valkey 8.0', 'valkey 9.1', 'valkey 9.2']);
  assert.deepEqual(rows.map(r => r.querySelector('.chip').textContent), ['done', 'running', 'waiting']);
  assert.match(rows[1].textContent, /21,000/);
  assert.match(rows[1].textContent, /35%/, '21,000 of 60,000 recorded commands');
  assert.equal(w.chartLog.length > 0, true, 'a throughput chart is drawn');
  assert.match(q(doc, '#runClock').textContent, /^00:02$/, 'a replay has no planned duration');
  assert.equal(q(doc, '#stopBtn').hidden, false);
  close();
});

test('a finished replay shows its result and the comparison', async () => {
  const { w, doc, close } = await loadPage({ '/api/live': fixture('live_replay_done.json') });
  w.showTab('live');
  assert.match(q(doc, '[data-testid="replay-banner"]').textContent, /Replay of .*sequential/);
  assert.equal(qa(doc, '#rlResult #rpSummaries tbody tr').length, 2);
  assert.ok(q(doc, '#rlResult .rp-compare'), 'the comparison is drawn');
  assert.match(q(doc, '#rlPhase').textContent, /Finished/);
  assert.equal(q(doc, '#statusBadge').textContent, 'finished');
  close();
});

test('a failed replay says why', async () => {
  const live = { ...fixture('live_replay_done.json'), state: 'error', error: 'old: connection refused', replay_id: undefined };
  const { w, doc, close } = await loadPage({ '/api/live': live });
  w.showTab('live');
  assert.equal(q(doc, '#statusBadge').textContent, 'error');
  assert.match(q(doc, '#rlPhase').textContent, /The replay failed\. old: connection refused/);
  close();
});

test('a recorded run offers to replay its recording', async () => {
  const run = fixture('run_recorded.json');
  const { w, doc, close } = await loadPage({ '/api/runs/rec1': run });
  await w.openRun('rec1'); await tickN();
  const b = q(doc, '[data-testid="recording-banner"]');
  assert.match(b.textContent, /Recorded 1 commands to rec-.*\.plgvrec\.zst/);
  assert.match(b.textContent, /zstd, complete/);
  click(w, q(doc, '#replayThisBtn')); await tickN();
  assert.equal(q(doc, '#taskTitle').textContent, 'Replay a recorded workload');
  assert.equal(q(doc, '#rpPath').value, run.recording.path);
  close();
});

test('Replay Comparison lists your replays and compares the ones you tick', async () => {
  const { w, doc, calls, close } = await loadPage({ '/api/replay-compare': fixture('replay_compare.json') });
  click(w, q(doc, '#tab-replay')); await tickN();
  const rows = qa(doc, '#rpStored tbody tr');
  assert.equal(rows.length, 1);
  assert.match(rows[0].textContent, /session\.jsonl\.zst/);
  assert.match(rows[0].textContent, /valkey 8\.0, valkey 9\.1/);
  click(w, rows[0].querySelector('input'));
  rows[0].querySelector('input').checked = true;
  click(w, q(doc, '#rpStoredCompare')); await tickN();
  const sent = last(calls, '/api/replay-compare').body;
  assert.equal(sent.files.length, 2);
  assert.equal(q(doc, '#rpBody').hidden, false);
  close();
});

test('Open shows a stored replay on the Live tab', async () => {
  const { w, doc, close } = await loadPage();
  click(w, q(doc, '#tab-replay')); await tickN();
  click(w, q(doc, '#rpStored tbody button'));
  await tickN();
  assert.equal(q(doc, '#panel-live').hidden, false);
  assert.equal(qa(doc, '#rlResult #rpSummaries tbody tr').length, STORED.targets.length);
  close();
});

test('the #task= link opens the Record and Replay tasks', async () => {
  for (const t of ['record', 'replay']) {
    const { doc, close } = await loadPage({}, { hash: '#task=' + t });
    assert.equal(q(doc, '#taskView').hidden, false);
    assert.equal(q(doc, '#taskSel').value, t);
    close();
  }
});
