// Shared jsdom harness for the web UI DOM tests. Fixtures are the real API
// output written by TestUIFixturesMatchAPI in the Go tests.
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { JSDOM, VirtualConsole } from 'jsdom';

const here = dirname(fileURLToPath(import.meta.url));
const pageHTML = readFileSync(join(here, '..', 'static', 'index.html'), 'utf8');
export const fixture = name => JSON.parse(readFileSync(join(here, '..', 'testdata', 'ui', name), 'utf8'));
export const clone = v => structuredClone(v);

// routes maps a URL prefix to a value (the JSON body) or an async function
// (url, init) => { status, body }. The longest matching prefix wins.
export function defaultRoutes() {
  return {
    '/api/config': fixture('config.json'),
    '/api/live': fixture('live_idle.json'),
    '/api/settings': { settings: [] },
    '/api/runs/20261104-100000': fixture('run_a.json'),
    '/api/runs/20261104-110000': fixture('run_b.json'),
    '/api/runs/20261104-120000': fixture('run_c.json'),
    '/api/runs/20261104-130000': fixture('run_d.json'),
    '/api/runs': fixture('runs.json'),
    '/api/trends': fixture('trends.json'),
    '/api/sweeps/swtest': fixture('sweep_workers.json'),
    '/api/sweeps': fixture('sweeps.json'),
    '/api/recordings': fixture('recordings.json'),
    '/api/replays/20261104-110000': fixture('replay_stored.json'),
    '/api/replays': fixture('replays.json'),
  };
}

export async function loadPage(overrides = {}, { hash = '' } = {}) {
  const routes = { ...defaultRoutes(), ...overrides };
  const errors = [];
  const vc = new VirtualConsole();
  vc.on('jsdomError', e => { if (!String(e.message).includes('Not implemented')) errors.push(e); });
  const dom = new JSDOM(pageHTML, {
    runScripts: 'dangerously',
    pretendToBeVisual: true,
    url: 'http://localhost/' + hash,
    virtualConsole: vc,
    beforeParse(window) {
      // No background polling in tests: they call w.pollLive() when they want a poll,
      // and a failing test must not keep the process alive.
      window.setInterval = () => 0;
      window.chartLog = [];
      window.Chart = class {
        constructor(canvas, cfg) { this.canvas = canvas; this.data = cfg.data; this.cfg = cfg; this.options = cfg.options; window.chartLog.push(this); }
        update() { this.updates = (this.updates || 0) + 1; }
        resize() {}
      };
      window.confirm = () => true;
      window.fetchCalls = [];
      window.fetch = async (url, init = {}) => {
        let sent; try { sent = init.body ? JSON.parse(init.body) : undefined; } catch (e) { sent = init.body; } // an upload is a file, not JSON
        const call = { url: String(url), method: (init.method || 'GET').toUpperCase(), body: sent, headers: init.headers || {} };
        window.fetchCalls.push(call);
        const keys = Object.keys(routes).filter(k => call.url.startsWith(k)).sort((a, b) => b.length - a.length);
        if (!keys.length) return { ok: false, status: 404, json: async () => ({ error: 'no route ' + call.url }) };
        let r = routes[keys[0]];
        if (typeof r === 'function') r = await r(call.url, call);
        // A route value is either a full response { status, body } or just the JSON body.
        const full = r && typeof r === 'object' && 'status' in r && 'body' in r;
        const { status = 200, body = {} } = full ? r : { body: r };
        return { ok: status < 400, status, json: async () => clone(body) };
      };
    },
  });
  await new Promise(r => dom.window.addEventListener('load', r));
  const w = dom.window;
  await w.plgvInit;
  return { dom, w, doc: w.document, errors, calls: w.fetchCalls, close: () => dom.window.close() };
}

export const tick = (ms = 0) => new Promise(r => setTimeout(r, ms));
export const click = (w, el) => el.dispatchEvent(new w.MouseEvent('click', { bubbles: true }));
export function setValue(w, el, value, type = 'input') {
  if (el.type === 'checkbox') el.checked = value; else el.value = value;
  el.dispatchEvent(new w.Event(type, { bubbles: true }));
  if (el.tagName === 'SELECT' || el.type === 'checkbox') el.dispatchEvent(new w.Event('change', { bubbles: true }));
}
export const startCall = calls => calls.filter(c => c.url === '/api/start').pop();

// The front page lists a card per workload. Picking one opens its settings with its defaults.
export function openTask(w, doc, id = 'standard') {
  const btn = doc.querySelector(`[data-task="${id}"] .task-main`);
  if (!btn) throw new Error('no task card ' + id);
  click(w, btn);
}
// With Advanced or Expert on, a category list picks which settings are shown.
export function openCat(w, doc, cat) {
  const btn = doc.querySelector(`[data-cat-btn="${cat}"]`);
  if (!btn) throw new Error('no category ' + cat + ' (is Advanced or Expert on?)');
  click(w, btn);
}
