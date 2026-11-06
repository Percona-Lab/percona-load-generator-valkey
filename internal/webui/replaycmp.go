package webui

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/multitarget"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/replay"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/replaycompare"
)

// maxReplayCompareBody bounds the upload of replay summaries (they hold totals, not commands).
const maxReplayCompareBody = 16 << 20

// handleReplayCompare compares replay summaries the page uploaded. It reads no
// file from the server's disk and stores nothing: the summaries come in the
// request body, the comparison goes out in the response.
//
// Body: {"files": [<replay summary or multi-target file>, ...], "tolerance": 10}.
func (s *Server) handleReplayCompare(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, 405, "POST the replay summaries")
		return
	}
	var body struct {
		Files     []json.RawMessage `json:"files"`
		Tolerance float64           `json:"tolerance"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxReplayCompareBody))
	if err := dec.Decode(&body); err != nil {
		fail(w, 400, "the request is not valid JSON, or is larger than 16 MB: "+err.Error())
		return
	}
	var all []*replay.Summary
	for i, f := range body.Files {
		got, err := replaycompare.Parse(f)
		if err != nil {
			fail(w, 400, fmt.Sprintf("file %d: %v", i+1, err))
			return
		}
		all = append(all, got...)
	}
	if len(all) < 2 {
		fail(w, 400, "give two or more replays: files from plgv replay -summary, or one file from -targets")
		return
	}
	if len(all) > multitarget.MaxTargets {
		fail(w, 400, fmt.Sprintf("%d replays: at most %d can be compared at once", len(all), multitarget.MaxTargets))
		return
	}
	if body.Tolerance < 0 || body.Tolerance > 1000 {
		fail(w, 400, "tolerance is a percent between 0 and 1000")
		return
	}
	replaycompare.Uniquify(all)
	writeJSON(w, 200, map[string]any{
		"summaries":   all,
		"comparisons": replaycompare.CompareAll(all, replaycompare.Options{Tolerance: body.Tolerance / 100}),
	})
}
