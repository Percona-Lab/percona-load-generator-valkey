import test from 'node:test';
import assert from 'node:assert/strict';
import { loadPage, fixture, clone, tick, click, setValue } from './harness.mjs';

const q = (doc, sel) => doc.querySelector(sel);
const qa = (doc, sel) => Array.from(doc.querySelectorAll(sel));
const API = fixture('replay_compare.json');
const A = fixture('replay_summary_a.json');
const B = fixture('replay_summary_b.json');
const ROUTE = { '/api/replay-compare': API };
const b64 = obj => Buffer.from(JSON.stringify(obj)).toString('base64').replace(/\+/g, '-').replace(/\//g, '_');

// A File stand-in that jsdom's input accepts through a property override.
function choose(w, doc, objs) {
  const input = doc.getElementById('rpFiles');
  const files = objs.map((o, i) => ({ name: `r${i}.json`, text: async () => (typeof o === 'string' ? o : JSON.stringify(o)) }));
  Object.defineProperty(input, 'files', { value: files, configurable: true });
  input.dispatchEvent(new w.Event('change', { bubbles: true }));
}

test('the Replay Comparison tab exists and starts empty', async () => {
  const { w, doc, calls, close } = await loadPage(ROUTE);
  assert.equal(q(doc, '#tab-replay').textContent, 'Replay Comparison');
  click(w, q(doc, '#tab-replay'));
  assert.equal(q(doc, '#panel-replay').hidden, false);
  assert.match(q(doc, '#rpMsg').textContent, /Choose two or more replay summaries/);
  assert.equal(q(doc, '#rpBody').hidden, true);
  assert.equal(calls.filter(c => c.url === '/api/replay-compare').length, 0, 'nothing is sent until files are chosen');
  close();
});

test('choosing files sends them for comparison and shows the summaries and the verdict', async () => {
  const { w, doc, calls, close } = await loadPage(ROUTE);
  click(w, q(doc, '#tab-replay'));
  choose(w, doc, [A, B]);
  await tick(30);
  const call = calls.find(c => c.url === '/api/replay-compare');
  assert.equal(call.method, 'POST');
  assert.equal(call.body.files.length, 2);
  assert.equal(call.body.tolerance, 10);
  assert.equal(q(doc, '#rpBody').hidden, false);
  const rows = qa(doc, '#rpSummaries tbody tr');
  assert.equal(rows.length, 2);
  assert.match(rows[0].textContent, /valkey 8\.0.*\(baseline\)/);
  assert.match(rows[1].textContent, /9\.1\.3/);
  assert.match(rows[1].textContent, /as recorded/);
  const verdict = q(doc, '.rp-compare .rp-verdict');
  assert.equal(verdict.textContent, 'slower');
  assert.ok(verdict.classList.contains('v-slower'));
  assert.match(q(doc, '.rp-compare h3').textContent, /valkey 9\.1 against valkey 8\.0/);
  close();
});

test('metrics are marked better or worse and findings carry their severity', async () => {
  const { w, doc, close } = await loadPage(ROUTE);
  click(w, q(doc, '#tab-replay'));
  choose(w, doc, [A, B]);
  await tick(30);
  const thr = q(doc, '[data-metric="Throughput"]');
  assert.match(thr.textContent, /-28\.0%/);
  assert.ok(thr.querySelector('.worse'));
  const p99 = q(doc, '[data-metric="Latency p99"]');
  assert.ok(p99.querySelector('.worse'));
  const sev = qa(doc, '.rp-compare .insight').map(e => e.dataset.sev);
  assert.ok(sev.includes('warning') && sev.includes('critical'), sev.join());
  assert.match(q(doc, '.rp-compare').textContent, /More errors on valkey 9\.1/);
  assert.match(q(doc, '.rp-compare').textContent, /Different server versions/);
  assert.ok(qa(doc, '.rp-compare table')[1].textContent.includes('HGET'));
  close();
});

test('a different tolerance is sent again with the same files', async () => {
  const { w, doc, calls, close } = await loadPage(ROUTE);
  click(w, q(doc, '#tab-replay'));
  choose(w, doc, [A, B]);
  await tick(30);
  setValue(w, q(doc, '#rpTol'), '50', 'change');
  await tick(30);
  const sent = calls.filter(c => c.url === '/api/replay-compare');
  assert.equal(sent.length, 2);
  assert.equal(sent[1].body.tolerance, 50);
  assert.equal(sent[1].body.files.length, 2);
  close();
});

test('a file that is not JSON is explained, and a server error is shown', async () => {
  const { w, doc, close } = await loadPage({ '/api/replay-compare': { status: 400, body: { error: 'give two or more replays' } } });
  click(w, q(doc, '#tab-replay'));
  choose(w, doc, ['not json', A]);
  await tick(30);
  assert.match(q(doc, '#rpMsg').textContent, /r0\.json is not a JSON file/);
  choose(w, doc, [A]);
  await tick(30);
  assert.match(q(doc, '#rpMsg').textContent, /give two or more replays/);
  assert.equal(q(doc, '#rpBody').hidden, true);
  close();
});

test('replays that cannot be compared say so and show no metrics', async () => {
  const resp = clone(API);
  resp.comparisons[0].comparable = false; resp.comparisons[0].verdict = 'not comparable'; resp.comparisons[0].rows = []; resp.comparisons[0].commands = [];
  resp.comparisons[0].findings = [{ severity: 'critical', title: 'Different recordings', detail: 'not the same workload' }];
  resp.summaries[1].failure = 'cannot connect';
  const { w, doc, close } = await loadPage({ '/api/replay-compare': resp });
  click(w, q(doc, '#tab-replay'));
  choose(w, doc, [A, B]);
  await tick(30);
  assert.match(q(doc, '.rp-compare .banner').textContent, /cannot be compared/);
  assert.equal(qa(doc, '.rp-compare table').length, 0);
  assert.match(qa(doc, '#rpSummaries tbody tr')[1].textContent, /did not run/);
  assert.ok(q(doc, '.rp-verdict').classList.contains('v-not-comparable'));
  close();
});

test('the #replay link opens the comparison for the files in it', async () => {
  const { doc, calls, close } = await loadPage(ROUTE, { hash: '#replay=' + b64([A, B]) });
  assert.equal(q(doc, '#panel-replay').hidden, false);
  assert.equal(calls.filter(c => c.url === '/api/replay-compare')[0].body.files.length, 2);
  assert.equal(qa(doc, '#rpSummaries tbody tr').length, 2);
  close();
});
