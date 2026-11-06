//go:build integration

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/runsummary"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
)

func TestIntegrationRunWithServerMetricsAndSummary(t *testing.T) {
	addr := os.Getenv("PLGV_IT_VALKEY_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	t.Cleanup(func() {
		c, err := valkey.NewClient(valkey.LibValkeyGo, valkey.Options{Addr: addr, Workers: 1})
		if err != nil {
			return
		}
		defer c.Close()
		for i := 0; i < 1100; i++ {
			c.Exec(context.Background(), []valkey.Cmd{{"UNLINK", "it:cli:s:" + strconv.Itoa(i)}})
		}
	})
	sum := filepath.Join(t.TempDir(), "run.json")
	var out, errb bytes.Buffer
	code := runCmd([]string{"-addr", addr, "-duration", "3s", "-workers", "8", "-pipeline", "4", "-keys", "1000",
		"-prefix", "it:cli:", "-server-metrics", "-summary", sum, "-sample-interval", "500ms"}, &out, &errb)
	t.Cleanup(func() { _ = os.Remove(sum) })
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errb.String())
	}
	for _, want := range []string{"server:", "ops processed:", "hit ratio:", "findings:", "summary written to"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	s, err := runsummary.Read(sum)
	if err != nil {
		t.Fatal(err)
	}
	if s.Server == nil || s.Server.Version == "" || s.Client.Ops == 0 || len(s.Series) < 4 || len(s.Server.Samples) < 4 {
		t.Fatalf("summary incomplete: client=%+v series=%d server=%+v", s.Client, len(s.Series), s.Server)
	}
}
