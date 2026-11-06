# Reports and exports

## HTML report

Every stored run has a self-contained HTML report: one file with its own styles and inline
charts, no scripts and no external files, so it can be emailed, attached to a ticket, or printed
to PDF from the browser.

- Open it from the banner above a run, or **Report** in Run History (`/api/runs/<id>/report`).
- **Download report** saves it as `plgv-report-<id>.html`.

![HTML report](../images/web_report.png)

It contains: headline numbers (operations, throughput, p50, p99, errors, client CPU); findings
with evidence and advice; the configuration of the run; latency and throughput charts;
per-operation results; errors by class; and, when server metrics were on, the server section
(version, memory, hit ratio, evictions, charts) and the slowlog entries. All text from the run
is escaped; the report never includes credentials.

## JSON run summary

The full record of a run is a JSON file (`<data-dir>/runs/<id>.json`, or `-summary` on the
command line). It holds the configuration as YAML, client and server results, the time series,
per-operation results and the findings. See [metrics-and-server-stats.md](metrics-and-server-stats.md).
It is the file Trends and Compare Runs read, so runs can be copied between machines by copying
the files into another data directory.
