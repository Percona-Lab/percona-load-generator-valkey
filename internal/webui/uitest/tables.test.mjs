import test from 'node:test';
import assert from 'node:assert/strict';
import { loadPage, fixture } from './harness.mjs';

// A number column's header must line up with its values (both right-aligned).
// jsdom's getComputedStyle ignores selector specificity, so it cannot catch the
// bug where `table.data th { text-align:left }` beat `th.num { text-align:right }`.
// Instead, require a rule that is at least as specific as the left-align rule.
test('numeric table headers have a right-align rule that beats the table header rule', async () => {
  const { doc, close } = await loadPage();
  const rules = [];
  for (const sheet of Array.from(doc.styleSheets)) for (const r of Array.from(sheet.cssRules)) if (r.selectorText) rules.push(r);
  const leftHeader = rules.find(r => r.selectorText.split(',').map(s => s.trim()).includes('table.data th') && r.style.getPropertyValue('text-align') === 'left');
  assert.ok(leftHeader, 'the table header rule exists');
  const selectors = rules.filter(r => r.style.getPropertyValue('text-align') === 'right').flatMap(r => r.selectorText.split(',').map(s => s.trim()));
  assert.ok(selectors.includes('table.data th.num'), 'th.num inside table.data is right-aligned, so headers line up with their numbers');
  assert.ok(selectors.includes('table.data td.num'));
  close();
});

// Every column whose cells are numbers must have a numeric header, on every tab
// that has tables (Live Metrics, Latency Analysis, Server, Run History, Compare).
const VIEWS = [
  ['live', '#run=20261104-100000&tab=live'],
  ['latency', '#run=20261104-100000&tab=latency'],
  ['server (cluster nodes)', '#run=20261104-200000&tab=server', { '/api/runs/20261104-200000': fixture('run_cluster.json') }],
  ['history', '#tab=history'],
];
for (const [name, hash, routes] of VIEWS) {
  test(`${name}: number columns have number headers, so they line up`, async () => {
    const { doc, close } = await loadPage(routes || {}, { hash });
    await new Promise(r => setTimeout(r, 40));
    let columns = 0;
    for (const t of doc.querySelectorAll('table.data')) {
      const heads = Array.from(t.querySelectorAll('thead th'));
      for (const tr of t.querySelectorAll('tbody tr')) {
        Array.from(tr.children).forEach((td, i) => {
          if (td.classList.contains('num') && heads[i]) {
            columns++;
            assert.ok(heads[i].classList.contains('num'), `${name}/${t.id}: column ${i} ("${heads[i].textContent}") has number cells but a left-aligned header`);
          }
        });
      }
    }
    assert.ok(columns > 0, 'the view has numeric columns to check');
    close();
  });
}
