import test from 'node:test';
import assert from 'node:assert/strict';
import { loadPage, tick, click, setValue, startCall } from './harness.mjs';

const START = { '/api/start': { status: 202, body: { started: true } } };
const q = (doc, sel) => doc.querySelector(sel);

test('TLS is off by default, an Advanced setting, and its options are hidden', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  const card = q(doc, '#tlsCard');
  assert.equal(card.dataset.level, 'advanced');
  assert.equal(q(doc, '#f-tls').checked, false);
  assert.equal(q(doc, '#tlsOptions').hidden, true);
  click(w, doc.getElementById('startBtn'));
  await tick(20);
  const t = startCall(calls).body.config.target.tls;
  assert.equal(t.enabled, false);
  assert.equal(t.insecure_skip_verify, false, 'verification is on unless asked off');
  close();
});

test('turning TLS on shows the file fields and sends paths, not contents', async () => {
  const { w, doc, calls, close } = await loadPage(START);
  setValue(w, q(doc, '#f-tls'), true);
  assert.equal(q(doc, '#tlsOptions').hidden, false);
  setValue(w, q(doc, '#f-tlsca'), '/etc/plgv/ca.crt');
  setValue(w, q(doc, '#f-tlscert'), '/etc/plgv/client.crt');
  setValue(w, q(doc, '#f-tlskey'), '/etc/plgv/client.key');
  setValue(w, q(doc, '#f-tlsname'), 'valkey.internal');
  click(w, doc.getElementById('startBtn'));
  await tick(20);
  assert.deepEqual(startCall(calls).body.config.target.tls,
    { enabled: true, ca_file: '/etc/plgv/ca.crt', cert_file: '/etc/plgv/client.crt', key_file: '/etc/plgv/client.key', server_name: 'valkey.internal', insecure_skip_verify: false });
  setValue(w, q(doc, '#f-tls'), false);
  assert.equal(q(doc, '#tlsOptions').hidden, true);
  close();
});

test('the TLS note says where credentials come from and warns about skipping verification', async () => {
  const { doc, close } = await loadPage(START);
  const note = q(doc, '#tlsNote').textContent;
  assert.match(note, /PLGV_USERNAME/);
  assert.match(note, /PLGV_PASSWORD/);
  assert.match(note, /man in the middle/);
  assert.equal(q(doc, '#f-tlsinsecure').checked, false);
  close();
});
