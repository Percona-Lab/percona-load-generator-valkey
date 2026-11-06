import test from 'node:test';
import assert from 'node:assert/strict';
import { openTask, openCat, loadPage, fixture, clone, tick, click, setValue, startCall } from './harness.mjs';

const visible = (doc, sel) => !doc.querySelector(sel).closest('[hidden]');
const text = (doc, sel) => doc.querySelector(sel).textContent;
const rows = (doc, sel) => Array.from(doc.querySelectorAll(sel));
const START = { '/api/start': { status: 202, body: { started: true } } };
const CL = '20261104-200000';
const SN = '20261104-210000';
const clusterRoutes = {
  ['/api/runs/' + CL]: fixture('run_cluster.json'),
  ['/api/runs/' + SN]: fixture('run_sentinel.json'),
  '/api/runs': fixture('runs_cluster.json'),
};

test('defaults: auto mode, no replica reads, no hash tags, no sentinel master', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  assert.equal(doc.getElementById('f-mode').value, 'auto');
  assert.equal(doc.getElementById('f-replicas').checked, false);
  click(w, doc.getElementById('startBtn'));
  await tick(20);
  const cfg = startCall(calls).body.config;
  assert.equal(cfg.target.mode, 'auto');
  assert.equal(cfg.target.read_from_replicas, false);
  assert.equal(cfg.target.sentinel.master_name, '');
  assert.equal(cfg.dataset.hash_tag_groups, 0);
  assert.deepEqual(cfg.target.uris, ['valkey://127.0.0.1:6379']);
  close();
});

test('the address field takes several addresses and changes its label with the deployment', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  const label = () => text(doc, '#uriLabel');
  assert.match(label(), /Server address \(or cluster seed nodes\)/);
  setValue(w, doc.getElementById('f-mode'), 'cluster');
  assert.equal(label(), 'Seed nodes');
  setValue(w, doc.getElementById('f-uri'), '127.0.0.1:7001, valkey://127.0.0.1:7002;127.0.0.1:7003');
  assert.match(text(doc, '#launchSummary'), /cluster 127\.0\.0\.1:7001, valkey:\/\/127\.0\.0\.1:7002, 127\.0\.0\.1:7003/);
  setValue(w, doc.getElementById('f-mode'), 'sentinel');
  assert.equal(label(), 'Sentinel addresses');
  setValue(w, doc.getElementById('f-mode'), 'standalone');
  assert.equal(label(), 'Server address');
  setValue(w, doc.getElementById('f-mode'), 'cluster');
  click(w, doc.getElementById('startBtn'));
  await tick(20);
  assert.deepEqual(startCall(calls).body.config.target.uris, ['127.0.0.1:7001', 'valkey://127.0.0.1:7002', '127.0.0.1:7003']);
  close();
});

test('the sentinel master name and replica reads appear only where they apply', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  openTask(w, doc);
  const master = () => !doc.getElementById('f-smaster').closest('[hidden]');
  const replicas = () => !doc.getElementById('f-replicas').closest('[hidden]');
  assert.deepEqual([master(), replicas()], [false, true], 'auto: replica reads may apply');
  setValue(w, doc.getElementById('f-mode'), 'standalone');
  assert.deepEqual([master(), replicas()], [false, false]);
  setValue(w, doc.getElementById('f-mode'), 'sentinel');
  assert.deepEqual([master(), replicas()], [true, true]);
  setValue(w, doc.getElementById('f-smaster'), 'mymaster');
  setValue(w, doc.getElementById('f-replicas'), true);
  click(w, doc.getElementById('startBtn'));
  await tick(20);
  const t = startCall(calls).body.config.target;
  assert.deepEqual([t.mode, t.sentinel.master_name, t.read_from_replicas], ['sentinel', 'mymaster', true]);
  assert.match(text(doc, '#serverNote'), /cluster.*routes every command by its key's slot/);
  assert.match(text(doc, '#serverNote'), /sentinel.*follows the master through failovers/);
  close();
});

test('hash tag groups are an Advanced setting with an explanation', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  openTask(w, doc);
  assert.ok(!visible(doc, '#f-tags'));
  setValue(w, doc.getElementById('lvlAdvanced'), true);
  assert.ok(!visible(doc, '#f-tags'), 'dataset details are in their own category');
  openCat(w, doc, 'data');
  assert.ok(visible(doc, '#f-tags'));
  assert.match(text(doc, '#tagNote'), /CROSSSLOT/);
  setValue(w, doc.getElementById('f-tags'), '16');
  click(w, doc.getElementById('startBtn'));
  await tick(20);
  assert.equal(startCall(calls).body.config.dataset.hash_tag_groups, 16);
  close();
});

test('cluster errors from the server are shown on the right fields', async () => {
  const errors = [
    { path: 'target.uris[1]', message: 'unsupported scheme "http"' },
    { path: 'target.sentinel.master_name', message: 'required for mode sentinel' },
    { path: 'dataset.hash_tag_groups', message: 'must be >= 0' },
    { path: 'workload.mix[0]', message: 'MGET touches several keys (CROSSSLOT)' },
  ];
  const { w, doc, close } = await loadPage({ '/api/start': { status: 400, body: { error: 'x', errors } } });
  click(w, doc.getElementById('startBtn'));
  await tick(20);
  assert.equal(doc.getElementById('f-uri').getAttribute('aria-invalid'), 'true');
  assert.match(doc.getElementById('f-uri').nextElementSibling.textContent, /unsupported scheme/);
  assert.equal(doc.getElementById('f-smaster').getAttribute('aria-invalid'), 'true');
  assert.equal(doc.getElementById('f-tags').getAttribute('aria-invalid'), 'true');
  assert.ok(doc.querySelector('[data-cat-btn="data"] .dot'), 'the category of the hash tag setting is marked');
  openCat(w, doc, 'data');
  assert.ok(visible(doc, '#f-tags'), 'the hidden Advanced card is reachable');
  assert.match(text(doc, '#startErrors'), /MGET touches several keys/, 'an error with no single field is listed');
  close();
});

test('the Server tab of a cluster run shows nodes, shares, roles and the failover', async () => {
  const run = fixture('run_cluster.json');
  const { w, doc, close } = await loadPage(clusterRoutes, { hash: `#run=${CL}&tab=server` });
  assert.equal(doc.getElementById('srvTopology').hidden, false);
  const trs = rows(doc, '#nodeTable tbody tr');
  assert.equal(trs.length, 4);
  assert.deepEqual(trs.map(r => r.dataset.node), run.server.nodes.map(n => n.addr));
  const first = trs[0].textContent;
  assert.match(first, /127\.0\.0\.1:7001/);
  assert.match(first, /primary/);
  assert.match(first, /5,461/);
  assert.match(first, /33\.3%/);
  const replica = trs[3].textContent;
  assert.match(replica, /replica/);
  assert.match(replica, /-/, 'a replica has no slots and no plgv ops');
  assert.match(trs[1].textContent, /4/, 'client errors per node');
  const ev = rows(doc, '#topoEvents [data-event]');
  assert.equal(ev.length, 1);
  assert.match(ev[0].textContent, /failover at 4\.0s/);
  assert.match(ev[0].textContent, /from 127\.0\.0\.1:7001 to 127\.0\.0\.1:7004/);
  close();
});

test('topology events are drawn as markers on the time charts', async () => {
  const { w, close } = await loadPage(clusterRoutes, { hash: `#run=${CL}&tab=server` });
  const chart = id => w.chartLog.find(c => c.canvas.id === id);
  for (const id of ['ch-srvops', 'ch-nodeops', 'ch-clientnodes']) {
    assert.deepEqual(JSON.parse(JSON.stringify(chart(id).$markers)), [{ x: 4, label: 'failover' }], id);
  }
  assert.ok(chart('ch-nodeops').updates >= 1, 'a chart created with markers must be redrawn once the markers are set, or the browser shows none');
  assert.equal(chart('ch-nodeops').data.datasets.length, 4);
  assert.match(chart('ch-nodeops').data.datasets[3].label, /7004 \(replica\)/);
  assert.equal(chart('ch-clientnodes').data.datasets.length, 4);
  // plgv's per-node series is rate per second: 3000 ops in a 1 s slice.
  assert.equal(chart('ch-clientnodes').data.datasets[0].data[0].y, 3000);
  w.showTab('live');
  for (const id of ['ch-throughput', 'ch-latency', 'ch-errors']) {
    assert.equal(chart(id).$markers.length, 1, id + ' shows the failover too');
  }
  w.showTab('latency');
  assert.equal(chart('ch-lat2').$markers.length, 1);
  close();
});

test('the failover is in the Insights and the run list marks the deployment', async () => {
  const { w, doc, close } = await loadPage(clusterRoutes, { hash: `#run=${CL}&tab=insights` });
  assert.match(text(doc, '#insBody'), /failover\(s\) happened during the run/);
  w.showTab('history');
  await tick(30);
  const chips = rows(doc, '#histTable [data-mode-chip]').map(c => c.textContent).sort();
  assert.deepEqual(chips, ['cluster', 'sentinel']);
  close();
});

test('a standalone run shows no node table and no markers', async () => {
  const { w, doc, close } = await loadPage({}, { hash: '#run=20261104-100000&tab=server' });
  assert.equal(doc.getElementById('srvTopology').hidden, true);
  assert.deepEqual(JSON.parse(JSON.stringify(w.chartLog.find(c => c.canvas.id === 'ch-srvops').$markers)), []);
  close();
});

test('a sentinel run shows the master move but no node table', async () => {
  const { doc, close } = await loadPage(clusterRoutes, { hash: `#run=${SN}&tab=server` });
  assert.equal(doc.getElementById('srvTopology').hidden, false);
  assert.equal(doc.getElementById('nodeTable').hidden, true, 'sentinel reports the master, not per-node tables');
  const ev = rows(doc, '#topoEvents [data-event]');
  assert.equal(ev.length, 1);
  assert.match(ev[0].textContent, /moved from 127\.0\.0\.1:6380 to 127\.0\.0\.1:6381/);
  close();
});

test('a quiet cluster says nothing changed', async () => {
  const run = clone(fixture('run_cluster.json'));
  run.server.topology.events = [];
  const { doc, close } = await loadPage({ ...clusterRoutes, ['/api/runs/' + CL]: run }, { hash: `#run=${CL}&tab=server` });
  assert.match(text(doc, '#topoEvents'), /No failovers, reshards or node changes/);
  run.server.topology.cluster_down = true;
  close();
});

test('the live view of a cluster run shows nodes and events while it runs', async () => {
  const { w, doc, close } = await loadPage({ '/api/live': fixture('live_cluster.json') });
  await w.pollLive();
  w.showTab('server');
  assert.equal(doc.getElementById('srvTopology').hidden, false);
  assert.equal(rows(doc, '#nodeTable tbody tr').length, 4);
  assert.equal(rows(doc, '#topoEvents [data-event]').length, 1);
  assert.match(text(doc, '#nodeTable tbody tr'), /-/, 'plgv per-node counts are known once the run ends');
  close();
});
