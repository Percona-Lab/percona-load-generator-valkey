package session

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/datafp"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/multitarget"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/replay"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/workloadrec"
)

// ReplayOutcome is a finished replay and the check of the target's starting data.
type ReplayOutcome struct {
	Result *replay.Result
	// Data compares the target's data before the replay with the data the
	// recording started with. Nil when the recording has no fingerprint.
	Data *datafp.Check
	// Target is the fingerprint taken on the target before the replay.
	Target        *datafp.Fingerprint
	ServerVersion string
	Mode          string
}

// Replay sends the recording at path to the target in cfg (only cfg.Target
// matters; the workload comes from the file). It fingerprints the target first
// and reports whether it holds the data the recording started with: a
// mismatch is reported, not fatal, unless requireData is set.
func Replay(ctx context.Context, cfg config.Config, path string, opt replay.Options, requireData bool) (*ReplayOutcome, error) {
	info, err := workloadrec.Scan(path)
	if err != nil {
		return nil, err
	}
	cfg.Record.Checksums = info.Header.ResultChecksums // the client must digest replies to compare checksums
	c, mode, _, err := connect(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	opt.Checksums = info.Header.ResultChecksums

	out := &ReplayOutcome{Mode: mode}
	if v, err := c.Query(ctx, valkey.Cmd{"INFO", "server"}); err == nil {
		out.ServerVersion = serverVersion(v)
	}
	if info.Header.DataFingerprint != nil {
		srcs, err := scanSources(ctx, c, mode)
		if err != nil {
			return nil, fmt.Errorf("cannot fingerprint the target: %w", err)
		}
		fs := make([]datafp.Source, len(srcs))
		for i, s := range srcs {
			fs[i] = datafp.Source{Name: s.name, Query: s.query}
		}
		if out.Target, err = datafp.Take(ctx, fs, datafp.Options{}); err != nil {
			return nil, fmt.Errorf("cannot fingerprint the target: %w", err)
		}
		chk := datafp.Compare(info.Header.DataFingerprint, out.Target)
		out.Data = &chk
		if requireData && !chk.Match {
			return out, fmt.Errorf("the target does not hold the data the recording started with: %v", chk.Differences)
		}
	}
	res, err := replay.Run(ctx, c, path, opt)
	if err != nil {
		return out, err
	}
	out.Result = res
	return out, nil
}

// TargetReplay is one target's replay: the outcome and its summary (a failed
// target still has a summary that says why).
type TargetReplay struct {
	Target  multitarget.Target
	Outcome *ReplayOutcome
	Summary *replay.Summary
	Err     error
}

// ReplayMany replays the recording on every target, one after another or all
// at once. A target that fails does not stop the others.
func ReplayMany(ctx context.Context, cfg config.Config, targets []multitarget.Target, path string, opt replay.Options, concurrent, requireData bool, version string) []TargetReplay {
	info, scanErr := workloadrec.Scan(path)
	outs := multitarget.Run(ctx, targets, concurrent, func(ctx context.Context, t multitarget.Target) (*ReplayOutcome, error) {
		c := cfg
		c.Target.URIs = splitList(t.Addr)
		return Replay(ctx, c, path, opt, requireData)
	})
	res := make([]TargetReplay, len(outs))
	for i, o := range outs {
		tr := TargetReplay{Target: o.Target, Outcome: o.Value, Err: o.Err}
		switch {
		case o.Err == nil && o.Value != nil && o.Value.Result != nil:
			tr.Summary = replay.Summarize(o.Value.Result, o.Target.Name, o.Target.Addr, version, o.Value.ServerVersion, o.Value.Mode, o.Value.Data)
		default:
			s := &replay.Summary{Format: replay.SummaryFormat, Label: o.Target.Name, Target: o.Target.Addr, PLGVVersion: version, Failure: errText(o.Err)}
			if scanErr == nil {
				s.Recording = replay.RecordingRef{Name: filepath.Base(path), Status: info.Status, Ops: info.Ops, Workers: info.Header.Workers, CommandsHash: info.CommandsHash}
			}
			tr.Summary = s
		}
		res[i] = tr
	}
	return res
}

func errText(err error) string {
	if err == nil {
		return "no result"
	}
	return err.Error()
}

func splitList(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		out = append(out, f)
	}
	return out
}

// PreloadOnly writes the dataset described by cfg and stops, without running a
// workload. Use it to give every target of a replay the same starting data.
func PreloadOnly(ctx context.Context, cfg config.Config) (int64, error) {
	if err := cfg.Validate(); err != nil {
		return 0, err
	}
	c, _, _, err := connect(ctx, cfg)
	if err != nil {
		return 0, err
	}
	defer c.Close()
	cfg.Run.Seed = valkey.EffectiveSeed(cfg.Run.Seed)
	if err := valkey.Preload(ctx, c, cfg); err != nil {
		return 0, err
	}
	return cfg.Dataset.KeyCount, nil
}
