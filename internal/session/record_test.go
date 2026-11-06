package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/workloadrec"
)

// infoStub answers INFO server and nothing else.
type infoStub struct{ valkey.Client }

func (infoStub) Query(context.Context, valkey.Cmd) (any, error) {
	return "# Server\r\nvalkey_version:9.1.3\r\n", nil
}

func TestServerVersion(t *testing.T) {
	cases := map[string]string{
		"# Server\r\nredis_version:7.2.4\r\nvalkey_version:9.1.3\r\nos:Linux\r\n": "9.1.3",
		"# Server\r\nredis_version:7.2.4\r\n":                                     "7.2.4",
		"nothing":                                                                 "",
	}
	for in, want := range cases {
		if got := serverVersion(in); got != want {
			t.Errorf("serverVersion(%q) = %q, want %q", in, got, want)
		}
	}
	if serverVersion(42) != "" {
		t.Error("a reply that is not text has no version")
	}
}

func TestStartRecordingWritesAHeaderWithoutTargetOrCredentials(t *testing.T) {
	cfg := config.Default()
	cfg.Target.URIs = []string{"secret-host.example:6379"}
	cfg.Target.Username, cfg.Target.Password = "admin", "hunter2"
	cfg.Run.Seed = 3
	cfg.Run.Concurrency = 5
	cfg.Record = config.Record{Path: filepath.Join(t.TempDir(), "r.jsonl"), Compression: "none", Backpressure: "fail"} // no fingerprint: no server here
	rec, err := startRecording(context.Background(), infoStub{}, cfg, "standalone", "v-test")
	if err != nil {
		t.Fatal(err)
	}
	if res := rec.Close(); res.Status != workloadrec.StatusComplete {
		t.Fatalf("%+v", res)
	}
	b, _ := os.ReadFile(cfg.Record.Path)
	for _, secret := range []string{"secret-host", "hunter2", "admin"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("the recording holds %q: %s", secret, b)
		}
	}
	info, err := workloadrec.Scan(cfg.Record.Path)
	if err != nil || info.Header.Workers != 5 || info.Header.Seed != 3 || info.Header.PLGVVersion != "v-test" || info.Header.DataFingerprint != nil || info.Header.ServerVersion != "9.1.3" {
		t.Fatalf("header = %+v, err = %v", info.Header, err)
	}
}
