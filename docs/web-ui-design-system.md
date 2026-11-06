# Web UI design system

The web UI is one page, `internal/webui/static/index.html`, embedded in the Go binary. It uses plain
HTML, CSS and JavaScript with no build step and no runtime network access: fonts use the system
stack and `chart.js` is bundled next to the page ([THIRD_PARTY.md](../THIRD_PARTY.md)). The
stylesheet is token based, with the same tokens and component look as the Percona Load Generator
for MongoDB so the tools feel alike.

## Tokens (`:root`)

| Group | Tokens |
|---|---|
| Surfaces | `--bg`, `--surface`, `--surface-muted`, `--surface-sunken`, `--border`, `--border-strong` |
| Text | `--text-strong`, `--text`, `--text-secondary`, `--text-muted` |
| Interaction | `--accent`, `--accent-hover`, `--accent-soft`, `--accent-border`, `--focus`, `--focus-ring` |
| Status | `--ok*`, `--warn*`, `--danger*` (text, `-soft` background, `-border`) |
| Type | `--font-sans`, `--font-mono`, `--fs-xs` 11 to `--fs-2xl` 24 |
| Space and shape | `--space-1` to `--space-6` (4 to 32 px), `--radius-sm/md/lg`, `--control-h`, `--elevation-1/2` |
| Chart colors | `--p-sky`, `--p-sunrise`, `--p-fire`, `--p-forest`, `--p-amethyst`, `--p-stone` |

Components use only these tokens; page-specific values should be the exception.

## Field widths

Inputs are sized by what they accept, not by the current value, so nothing moves while you type:
`f-xs` (6.5rem: counts), `f-sm` (9.5rem: durations, rates, large counts), `f-md` (16rem: names),
`f-lg` (26rem), `f-xl` (46rem: addresses). Every control keeps `max-width: 100%`.

## Components

| Component | Classes | Notes |
|---|---|---|
| Buttons | `.btn`, `.btn-primary`, `.btn-danger`, `.btn-small` | One primary action per area. `aria-busy` shows progress. |
| Cards | `.card`, `.chart-card`, `.kpi` | `--radius-lg`, 1px border. |
| Header | `.top-card`, `.brand`, `.run-clock`, `.status-badge` | Brand (home), status, run clock, Completed Runs, Exit. The section tabs sit in a second card and appear once a workload is chosen. |
| Task cards | `.task-card`, `.task-main`, `.task-defaults`, `.task-icon` | The front page: one per workload, an icon, a title, one sentence and **Use defaults**. Dashed cards go to results. |
| Settings level | `.tier-bar`, `.seg`; cards carry `data-level` | A segmented control: Basic is always on; Advanced and Expert toggle (real checkboxes, so keyboard and screen readers work). |
| Categories | `.cat-nav`, `.settings-layout.has-cats`; cards carry `data-cat` | A list on the left, shown with Advanced or Expert; one category at a time; a red dot marks one with a problem. |
| Launch bar | `.launch-bar` | Sticky at the bottom of Settings: summary and the Start/Stop buttons. |
| Notes | `.settings-note`, `.note-box` | Short guidance stays visible; the server metrics note is always shown. |
| Field errors | `.field-error`, `aria-invalid` | Placed under the control; removed when the control changes. A hidden level is revealed to show one. |
| Tables | `table.data` | Muted header row, hover row, numbers right-aligned. |
| Findings | `.insight.critical/.warning/.info`, `.chip` | Status is never shown by color alone: each has text. |

## States

Focus shows a 2px outline on keyboard focus; disabled controls are dimmed and not clickable;
invalid controls get a danger border; the `hidden` attribute always wins.

## Safety

All text that comes from data (labels, commands, error messages, findings) is inserted with
`textContent`, never as HTML. A test checks that a label containing markup is shown as text.

## Screenshots

`node scripts/screenshots.mjs` regenerates the web screenshots in `images/` from a real `plgv web` and a real
Valkey (Node 22+, Chrome, `make valkey-up`); pass names to make only some (`node scripts/screenshots.mjs home settings`).

## Checks

`make test-ui` runs the DOM tests (jsdom) against fixtures that are the real API output; see
[TESTING.md](../TESTING.md).
