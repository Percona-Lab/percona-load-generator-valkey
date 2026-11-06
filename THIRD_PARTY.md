# Third-party software

| Component | Version | License | Where |
|---|---|---|---|
| [Chart.js](https://www.chartjs.org) | 4.5.1 | MIT | `internal/webui/static/chart.js` (bundled so the UI needs no network) |
| [valkey-go](https://github.com/valkey-io/valkey-go) | 1.0.79 | Apache-2.0 | Go dependency |
| [go-redis](https://github.com/redis/go-redis) | 9.22.0 | BSD-2-Clause | Go dependency |
| [klauspost/compress](https://github.com/klauspost/compress) | 1.20.2 | BSD-3-Clause, Apache-2.0 | Go dependency (zstd and gzip for recordings) |
| [cespare/xxhash](https://github.com/cespare/xxhash) | 2.3.0 | MIT | Go dependency (of go-redis) |
| [uber-go/atomic](https://github.com/uber-go/atomic) | 1.11.0 | MIT | Go dependency (of valkey-go) |
| [golang.org/x/sys](https://pkg.go.dev/golang.org/x/sys) | 0.47.0 | BSD-3-Clause | Go dependency |
| [yaml.v3](https://github.com/go-yaml/yaml) | 3.0.1 | MIT and Apache-2.0 | Go dependency |
| [jsdom](https://github.com/jsdom/jsdom) | 26.x | MIT | development only (UI tests) |
