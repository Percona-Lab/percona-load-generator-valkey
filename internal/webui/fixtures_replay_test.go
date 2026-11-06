package webui

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/replaycompare"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/runsummary"
)

// replayFixtures builds the recording and replay fixtures from the real handlers.
// Paths and times are made stable so the files do not change between runs.
func replayFixtures(t *testing.T, base time.Time) map[string][]byte {
	t.Helper()
	pretty := func(v any) []byte {
		b, _ := json.MarshalIndent(v, "", "  ")
		return append(b, '\n')
	}
	e := setupWith(t, nil, func(o *Options) { o.FreePaths = true })
	dir := e.recDir()
	stable := func(b []byte) []byte {
		b = bytes.ReplaceAll(b, []byte(filepath.Dir(dir)), []byte("/home/user/plgv-data"))
		b = regexp.MustCompile(`"created_at": ?"[^"]*"`).ReplaceAll(b, []byte(`"created_at": "2026-11-04T10:00:00Z"`))
		b = regexp.MustCompile(`"modified_at": ?"[^"]*"`).ReplaceAll(b, []byte(`"modified_at": "2026-11-04T10:00:00Z"`))
		return b
	}
	out := map[string][]byte{}

	// Recordings in the folder: one plain, one compressed, one imported.
	writeRec(t, filepath.Join(dir, "rec-20261104T100000Z-a1b2c3.plgvrec"), "")
	writeRec(t, filepath.Join(dir, "rec-20261103T090000Z-d4e5f6.plgvrec.zst"), "zstd")
	_, _, raw := e.do("POST", "/api/import-traffic", map[string]any{"text": monitorSample, "compression": "zstd"}, nil)
	out["import_result.json"] = stable([]byte(raw))
	_, _, raw = e.do("GET", "/api/recordings", nil, nil)
	out["recordings.json"] = stable([]byte(raw))
	_, _, raw = e.do("GET", "/api/recordings/info?path=rec-20261104T100000Z-a1b2c3.plgvrec", nil, nil)
	out["recording_info.json"] = stable([]byte(raw))

	// A run that was recorded.
	f := newFake()
	f.started = base
	close(f.release)
	e.srv.mgr.run = f.run
	body := e.defaultsBody(func(c map[string]any) {
		c["record"].(map[string]any)["compression"] = "zstd"
		c["record"].(map[string]any)["outcomes"] = true
	})
	body["record"] = true
	body["label"] = "recorded"
	e.do("POST", "/api/start", body, nil)
	id := e.waitState(StateDone)["run_id"].(string)
	_, _, raw = e.do("GET", "/api/runs/"+id, nil, nil)
	out["run_recorded.json"] = stable([]byte(raw))

	// A stored multi-target replay, saved directly so its id is fixed.
	sums := sampleReplaySummaries()
	sr := &StoredReplay{Label: "upgrade test", FinishedAt: base.Add(time.Hour), Mode: "sequential", Speed: 0, ReadsOnly: false, Iterations: 1,
		Recording: sums[0].Recording, Targets: sums, Comparisons: replaycompare.CompareAll(sums, replaycompare.Options{Tolerance: 0.1})}
	rid, err := e.srv.replays.Save(sr)
	if err != nil {
		t.Fatal(err)
	}
	_, _, raw = e.do("GET", "/api/replays/"+rid, nil, nil)
	out["replay_stored.json"] = []byte(raw)
	_, _, raw = e.do("GET", "/api/replays", nil, nil)
	out["replays.json"] = []byte(raw)

	// Live views of a replay.
	lr := &LiveReplay{Recording: "rec-20261104T100000Z-a1b2c3.plgvrec", RecordedOps: 60000, Iterations: 1, Speed: 0,
		Targets: []LiveReplayTarget{
			{Name: "valkey 8.0", Addr: "10.0.0.5:6379", State: TargetDone, Ops: 60000, ElapsedSec: 1.2, Series: []runsummary.Point{{OffsetSeconds: 1, OpsPerSec: 50000, P50Ms: 0.2, P95Ms: 0.4, P99Ms: 0.8, MaxMs: 2}}},
			{Name: "valkey 9.1", Addr: "10.0.0.6:6379", State: TargetRunning, Ops: 21000, ElapsedSec: 0.6, Series: []runsummary.Point{{OffsetSeconds: 1, OpsPerSec: 36000, P50Ms: 0.3, P95Ms: 0.5, P99Ms: 0.9, MaxMs: 2}}},
			{Name: "valkey 9.2", Addr: "10.0.0.7:6379", State: TargetWaiting, Series: []runsummary.Point{}}}}
	out["live_replay_running.json"] = pretty(Live{State: StateRunning, Kind: "replay", Replay: lr, StartedAt: base, ElapsedSec: 2, Series: []runsummary.Point{}, PerOp: []LiveOp{}})
	out["live_replay_done.json"] = pretty(Live{State: StateDone, Kind: "replay", ReplayID: rid, Replay: lr, StartedAt: base, ElapsedSec: 4, Series: []runsummary.Point{}, PerOp: []LiveOp{}})
	_ = os.Remove
	return out
}
