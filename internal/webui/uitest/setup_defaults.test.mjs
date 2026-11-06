import test from 'node:test';
import assert from 'node:assert/strict';
import { loadPage, fixture } from './harness.mjs';

// The defaults the page receives come from the Go API; opt-in features must be off.
test('the defaults from the API leave every opt-in feature off', async () => {
  const cfg = fixture('config.json').defaults;
  assert.equal(cfg.server_metrics.enabled, false);
  assert.equal(cfg.run.coordinated_omission, false);
  assert.equal(cfg.run.target_ops_per_sec, 0);
  assert.equal(cfg.output.summary_file, '');
  assert.equal(cfg.target.pool.pipeline_depth, 1);
});

test('the page offers every operation the server reports', async () => {
  const fx = fixture('config.json');
  const { doc, close } = await loadPage();
  const offered = new Set(Array.from(doc.querySelectorAll('#mixBody tr:first-child option')).map(o => o.value));
  for (const op of fx.op_names) assert.ok(offered.has(op), op + ' is offered');
  close();
});

test('the version from the API is shown', async () => {
  const { doc, close } = await loadPage();
  assert.equal(doc.getElementById('version').textContent, fixture('config.json').version);
  close();
});
