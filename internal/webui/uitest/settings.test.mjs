import test from 'node:test';
import assert from 'node:assert/strict';
import { openTask, openCat, loadPage, fixture, clone, tick, click, setValue, startCall } from './harness.mjs';

const visible = (doc, sel) => !doc.querySelector(sel).closest('[hidden]');

test('opens on the front page: a card per workload, and no section tabs yet', async () => {
  const { doc, errors, close } = await loadPage();
  assert.equal(doc.getElementById('home').hidden, false);
  assert.equal(doc.getElementById('taskView').hidden, true);
  assert.equal(doc.getElementById('mainTabs').hidden, true, 'the front page shows only the header and the cards');
  assert.match(doc.querySelector('#home h2').textContent, /What do you want to do/);
  const ids = sel => Array.from(doc.querySelectorAll(sel + ' [data-task]')).map(c => c.dataset.task);
  assert.deepEqual(ids('#taskGrid'), ['standard', 'custom', 'raw', 'sweep', 'record', 'replay']);
  assert.deepEqual(ids('#presetGrid'), ['preset:cache', 'preset:session', 'preset:leaderboard', 'preset:queue', 'preset:rate-limiter', 'preset:pubsub-fanout', 'preset:counters', 'preset:big-keys', 'preset:mixed']);
  for (const c of doc.querySelectorAll('.task-grid .task-card[data-task]')) {
    assert.ok(c.querySelector('strong').textContent && c.querySelector('.d').textContent, 'a title and a description');
    assert.equal(c.querySelector('.task-defaults').textContent, 'Use defaults');
  }
  assert.deepEqual(Array.from(doc.querySelectorAll('#exploreGrid [data-explore]')).map(c => c.dataset.explore), ['history', 'compare', 'replay', 'trends']);
  assert.equal(doc.getElementById('statusBadge').textContent, 'idle');
  assert.equal(doc.getElementById('runClock').textContent, '00:00 / 00:00');
  assert.deepEqual(errors, []);
  close();
});

test('choosing a workload opens its settings: only the Basic cards, defaults selected', async () => {
  const { w, doc, errors, close } = await loadPage();
  openTask(w, doc);
  assert.equal(doc.getElementById('home').hidden, true);
  assert.equal(doc.getElementById('mainTabs').hidden, false, 'the section tabs appear');
  assert.equal(doc.querySelector('[data-tab="settings"]').getAttribute('aria-selected'), 'true');
  assert.equal(doc.getElementById('panel-settings').hidden, false);
  assert.equal(doc.getElementById('taskTitle').textContent, 'Standard workload');
  assert.equal(doc.getElementById('catNav').hidden, true, 'no category list while only Basic is on');
  for (const id of ['f-uri', 'f-duration', 'f-conc', 'f-keys', 'f-srv', 'savedSelect']) assert.ok(visible(doc, '#' + id), id);
  for (const id of ['f-pipe', 'f-rate', 'f-co', 'f-prefix', 'f-ttlmode', 'f-sample', 'f-client', 'f-slowN', 'f-seed', 'f-tls']) assert.ok(!visible(doc, '#' + id), id + ' is above Basic');
  assert.deepEqual(errors, []);
  close();
});

test('settings levels add a category list; each category shows its own settings; hidden values still apply', async () => {
  const { w, doc, calls, close } = await loadPage({ '/api/start': { status: 202, body: { started: true } } });
  openTask(w, doc);
  const adv = doc.getElementById('lvlAdvanced');
  assert.match(doc.getElementById('levelNote').textContent, /hidden settings still apply/);
  setValue(w, adv, true);
  assert.equal(doc.getElementById('catNav').hidden, false, 'a category list appears with Advanced or Expert');
  const cats = Array.from(doc.querySelectorAll('[data-cat-btn]')).map(b => b.dataset.catBtn);
  assert.deepEqual(cats, ['basic', 'connection', 'execution', 'data', 'seed', 'metrics']);
  assert.equal(doc.querySelector('[data-cat-btn="basic"]').getAttribute('aria-current'), 'true');
  assert.ok(visible(doc, '#f-uri') && !visible(doc, '#f-pipe'), 'Basic settings are shown first');
  openCat(w, doc, 'execution');
  assert.ok(visible(doc, '#f-pipe') && !visible(doc, '#f-uri'), 'one category at a time');
  assert.equal(adv.getAttribute('aria-expanded'), 'true');
  assert.ok(!doc.querySelector('[data-cat-btn="connection"]') || !visible(doc, '#f-client'), 'Expert is still hidden');
  setValue(w, doc.getElementById('f-pipe'), '16');
  setValue(w, adv, false); // hide Advanced again
  assert.equal(doc.getElementById('catNav').hidden, true);
  assert.ok(!visible(doc, '#f-pipe'));
  click(w, doc.getElementById('startBtn'));
  await tick(20);
  assert.equal(startCall(calls).body.config.target.pool.pipeline_depth, 16, 'a hidden setting keeps its value');
  w.showTab('settings'); // a started run switches to Live Metrics
  setValue(w, doc.getElementById('lvlExpert'), true);
  openCat(w, doc, 'connection');
  assert.ok(visible(doc, '#f-client'));
  assert.match(doc.getElementById('levelNote').textContent, /Advanced settings are hidden/);
  assert.match(doc.getElementById('levelNote').textContent, /Showing basic and expert settings/);
  close();
});

test('the levels choice is remembered', async () => {
  const { w, doc, close } = await loadPage();
  setValue(w, doc.getElementById('lvlExpert'), true);
  assert.equal(JSON.parse(w.localStorage.getItem('plgv.levels')).expert, true);
  close();
});

test('server metrics: unchecked by default, with the explanatory note', async () => {
  const { w, doc, calls, close } = await loadPage({ '/api/start': { status: 202, body: {} } });
  openTask(w, doc);
  const cb = doc.getElementById('f-srv');
  assert.equal(cb.checked, false, 'off by default');
  assert.ok(visible(doc, '#f-srv'), 'the checkbox is a Basic setting');
  const note = doc.querySelector('[data-testid="server-metrics-note"]');
  assert.ok(visible(doc, '[data-testid="server-metrics-note"]'), 'the note is always visible, not behind a hover');
  for (const phrase of ['read-only', 'INFO', 'SLOWLOG GET', 'LATENCY LATEST', 'never changes server settings', 'Off by default', 'permission']) {
    assert.ok(note.textContent.includes(phrase), 'note mentions ' + phrase);
  }
  assert.equal(doc.getElementById('srvOptions').hidden, true, 'sub-options appear only when enabled');
  assert.match(doc.getElementById('launchSummary').textContent, /server metrics off/);

  click(w, doc.getElementById('startBtn'));
  await tick(20);
  assert.equal(startCall(calls).body.config.server_metrics.enabled, false, 'a default run sends enabled: false');

  setValue(w, cb, true);
  assert.equal(doc.getElementById('srvOptions').hidden, false);
  assert.ok(doc.getElementById('f-srvint') && doc.getElementById('f-srvslow') && doc.getElementById('f-srvlat'));
  assert.match(doc.getElementById('launchSummary').textContent, /server metrics on/);
  click(w, doc.getElementById('startBtn'));
  await tick(20);
  assert.equal(startCall(calls).body.config.server_metrics.enabled, true);
  close();
});

test('operation mix builder: defaults, shares, parameters, add and remove', async () => {
  const { w, doc, calls, close } = await loadPage({ '/api/start': { status: 202, body: {} } });
  const rows = () => Array.from(doc.querySelectorAll('#mixBody tr'));
  assert.equal(rows().length, 2);
  assert.deepEqual(rows().map(r => r.querySelector('.mix-share').textContent), ['80%', '20%']);

  // Changing a weight updates the shares.
  setValue(w, rows()[1].querySelector('[data-mix-field="weight"]'), '80');
  assert.deepEqual(rows().map(r => r.querySelector('.mix-share').textContent), ['50%', '50%']);

  // HSET takes a "fields" parameter; MGET takes "count"; plain GET takes none.
  click(w, doc.getElementById('mixAdd'));
  let r = rows()[2];
  setValue(w, r.querySelector('[data-mix-field="op"]'), 'HSET');
  r = rows()[2];
  assert.ok(r.querySelector('[data-mix-field="fields"]'));
  setValue(w, r.querySelector('[data-mix-field="fields"]'), '4');
  setValue(w, r.querySelector('[data-mix-field="op"]'), 'MGET');
  r = rows()[2];
  assert.ok(r.querySelector('[data-mix-field="count"]') && !r.querySelector('[data-mix-field="fields"]'));
  setValue(w, r.querySelector('[data-mix-field="op"]'), 'DEL');
  r = rows()[2];
  assert.ok(r.querySelector('[data-mix-field="type"]'), 'key ops take a key type');
  setValue(w, r.querySelector('[data-mix-field="type"]'), 'hash');
  setValue(w, r.querySelector('[data-mix-field="op"]'), 'GET');
  assert.equal(rows()[2].querySelectorAll('[data-mix-field="type"], [data-mix-field="count"], [data-mix-field="fields"]').length, 0);

  // Parameters from an operation that no longer takes them are dropped from what is sent.
  setValue(w, rows()[2].querySelector('[data-mix-field="op"]'), 'HSET');
  setValue(w, rows()[2].querySelector('[data-mix-field="fields"]'), '3');
  click(w, rows()[0].querySelector('[data-mix-remove]'));
  assert.equal(rows().length, 2);
  click(w, doc.getElementById('startBtn'));
  await tick(20);
  const mix = startCall(calls).body.config.workload.mix;
  assert.deepEqual(mix.map(m => m.op), ['SET', 'HSET']);
  assert.equal(mix[1].fields, 3);
  assert.equal(mix[0].count, undefined);

  // Every catalog operation is offered, grouped by family.
  const opts = Array.from(rows()[0].querySelectorAll('option')).map(o => o.value);
  assert.ok(opts.length >= 68 && opts.includes('ZRANGEBYSCORE') && opts.includes('SRANDMEMBER'));
  assert.equal(rows()[0].querySelectorAll('optgroup').length, 14, 'one group per family, including the phase 7 families');
  close();
});

test('removing every operation says so', async () => {
  const { w, doc, close } = await loadPage();
  while (doc.querySelector('[data-mix-remove]')) click(w, doc.querySelector('[data-mix-remove]'));
  assert.match(doc.getElementById('mixErrors').textContent, /at least one operation/i);
  close();
});

test('validation errors from the server land on the fields, revealing hidden levels', async () => {
  const errors = [
    { path: 'run.concurrency', message: 'must be >= 1, got 0' },
    { path: 'target.pool.pipeline_depth', message: 'must be >= 1, got 0' },
    { path: 'workload.mix[0].count', message: 'not valid for GET' },
    { path: '', message: 'something general' },
  ];
  const { w, doc, close } = await loadPage({ '/api/start': { status: 400, body: { error: 'the configuration is not valid', errors } } });
  openTask(w, doc);
  assert.ok(!visible(doc, '#f-pipe'));
  click(w, doc.getElementById('startBtn'));
  await tick(20);
  const conc = doc.getElementById('f-conc');
  assert.equal(conc.getAttribute('aria-invalid'), 'true');
  assert.match(conc.nextElementSibling.textContent, /must be >= 1/);
  assert.equal(conc.nextElementSibling.getAttribute('role'), 'alert');
  assert.ok(visible(doc, '#f-conc'), 'the category of the first problem is opened');
  assert.equal(doc.querySelector('[data-cat-btn="basic"]').getAttribute('aria-current'), 'true');
  assert.ok(doc.querySelector('[data-cat-btn="execution"] .dot'), 'a category with a problem is marked in the list');
  assert.ok(!doc.querySelector('[data-cat-btn="data"] .dot'), 'and one without a problem is not');
  openCat(w, doc, 'execution');
  assert.ok(visible(doc, '#f-pipe'), 'the Advanced level was turned on so the field can be reached');
  assert.equal(doc.getElementById('f-pipe').getAttribute('aria-invalid'), 'true');
  assert.equal(doc.querySelector('[data-mix-index="0"][data-mix-field="weight"]').getAttribute('aria-invalid'), null);
  const box = doc.getElementById('startErrors');
  assert.equal(box.hidden, false);
  assert.match(box.textContent, /something general/);
  // Editing a field clears its error.
  setValue(w, conc, '8');
  assert.equal(conc.getAttribute('aria-invalid'), null);
  assert.ok(!conc.nextElementSibling || !conc.nextElementSibling.classList.contains('field-error'));
  close();
});

test('saved settings: list, save, load and delete', async () => {
  const saved = clone(fixture('config.json').defaults);
  saved.run.concurrency = 7;
  saved.dataset.key_count = 5000;
  let names = ['nightly'];
  const { w, doc, calls, close } = await loadPage({
    '/api/settings': async (url, init) => {
      if (init.method === 'PUT') { names.push(decodeURIComponent(url.split('/').pop())); return { status: 200, body: { saved: true } }; }
      if (init.method === 'DELETE') { names = []; return { status: 200, body: { deleted: true } }; }
      if (url === '/api/settings') return { status: 200, body: { settings: names } };
      return { status: 200, body: { name: 'nightly', config: saved } };
    },
  });
  const sel = doc.getElementById('savedSelect');
  assert.deepEqual(Array.from(sel.options).map(o => o.value), ['nightly']);
  click(w, doc.getElementById('savedLoad'));
  await tick(20);
  assert.equal(doc.getElementById('f-conc').value, '7');
  assert.equal(doc.getElementById('f-keys').value, '5000');
  assert.match(doc.getElementById('savedMsg').textContent, /Loaded "nightly"/);

  setValue(w, doc.getElementById('savedName'), 'my test');
  click(w, doc.getElementById('savedSave'));
  await tick(20);
  const put = calls.find(c => c.method === 'PUT');
  assert.equal(put.url, '/api/settings/my%20test');
  assert.equal(put.body.config.run.concurrency, 7);
  assert.match(doc.getElementById('savedMsg').textContent, /Credentials are never saved/);
  assert.ok(Array.from(sel.options).some(o => o.value === 'my test'));

  click(w, doc.getElementById('savedDelete'));
  await tick(20);
  assert.ok(calls.some(c => c.method === 'DELETE'));
  close();
});

test('saving with no name asks for one', async () => {
  const { w, doc, calls, close } = await loadPage();
  click(w, doc.getElementById('savedSave'));
  await tick(10);
  assert.match(doc.getElementById('savedMsg').textContent, /Enter a name/);
  assert.ok(!calls.some(c => c.method === 'PUT'));
  close();
});

test('the launch bar summarizes the run and follows edits', async () => {
  const { w, doc, close } = await loadPage();
  const s = () => doc.getElementById('launchSummary').textContent;
  assert.match(s(), /64 workers for 10s/);
  assert.match(s(), /2 operations/);
  setValue(w, doc.getElementById('f-conc'), '8');
  setValue(w, doc.getElementById('f-duration'), '30s');
  assert.match(s(), /8 workers for 30s/);
  close();
});

test('page is keyboard and screen-reader friendly: roles, labels, tab state', async () => {
  const { doc, close } = await loadPage();
  assert.equal(doc.querySelector('nav.tabs').getAttribute('role'), 'tablist');
  const tabs = Array.from(doc.querySelectorAll('[role="tab"]'));
  assert.equal(tabs.length, 11);
  assert.equal(tabs.filter(t => t.getAttribute('aria-selected') === 'true').length, 1);
  for (const inp of doc.querySelectorAll('#panel-settings input[data-path], #panel-settings select[data-path]')) {
    const labelled = inp.id && doc.querySelector(`label[for="${inp.id}"]`);
    assert.ok(labelled, `${inp.dataset.path} has a label`);
  }
  close();
});
