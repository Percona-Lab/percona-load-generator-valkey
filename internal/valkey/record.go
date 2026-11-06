package valkey

import (
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/stats"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/workloadrec"
)

// RecordTap returns a Tap that writes every command a worker sends to rec.
// When the recording keeps outcomes, each command carries its status, the
// latency of its batch and, with checksums, the size and checksum of its reply.
func RecordTap(rec *workloadrec.Recorder) Tap {
	outcomes, sums := rec.WantsOutcomes(), rec.WantsChecksums()
	return func(ev Event) {
		off := rec.Offset(ev.Start)
		conn := ""
		if ev.Dedicated {
			conn = workloadrec.ConnDedicated
		}
		for i, cmd := range ev.Cmds {
			e := workloadrec.Entry{Worker: ev.Worker, Offset: off, Group: ev.Group, Conn: conn, Cmd: cmd}
			if outcomes && i < len(ev.Results) {
				e.Outcome = OutcomeOf(ev.Results[i], ev.Elapsed.Microseconds(), sums)
			}
			rec.Record(e)
		}
	}
}

// OutcomeOf converts a command's result to the outcome stored in a recording.
func OutcomeOf(r Result, latencyUS int64, checksums bool) *workloadrec.Outcome {
	o := &workloadrec.Outcome{Status: workloadrec.OutcomeOK, US: latencyUS}
	switch {
	case r.Err != nil:
		o.Status, o.Class = workloadrec.OutcomeError, stats.Classify(r.Err).String()
	case r.Miss:
		o.Status = workloadrec.OutcomeMiss
	case checksums:
		o.Len = r.Len
		o.Sum = sumHex(r.Sum)
	}
	return o
}

func sumHex(v uint64) string {
	const hexd = "0123456789abcdef"
	var b [16]byte
	for i := 15; i >= 0; i-- {
		b[i] = hexd[v&0xf]
		v >>= 4
	}
	return string(b[:])
}
