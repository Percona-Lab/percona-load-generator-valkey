//go:build integration_cluster

package webui

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
)

// A cluster run started from the web API stores the per-node view.
func TestIntegrationWebClusterRun(t *testing.T) {
	addr := os.Getenv("PLGV_IT_CLUSTER_ADDR")
	if addr == "" {
		addr = "127.0.0.1:7001"
	}
	s, err := New(Options{DataDir: t.TempDir(), Version: "it"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	e := &env{t: t, srv: s, ts: ts}
	t.Cleanup(func() {
		c, err := valkey.NewClient(valkey.LibValkeyGo, valkey.Options{Addrs: []string{addr}, Workers: 1})
		if err != nil {
			return
		}
		defer c.Close()
		for i := 0; i < 1000; i++ {
			c.Exec(context.Background(), []valkey.Cmd{{"UNLINK", fmt.Sprintf("it:wcl:{%d}:s:%d", i%8, i)}})
		}
	})
	body := e.defaultsBody(func(c map[string]any) {
		tg := c["target"].(map[string]any)
		tg["uris"] = []any{addr}
		tg["mode"] = "cluster"
		run := c["run"].(map[string]any)
		run["duration"] = "2s"
		run["concurrency"] = 8
		d := c["dataset"].(map[string]any)
		d["key_count"] = 1000
		d["key_prefix"] = "it:wcl:"
		d["hash_tag_groups"] = 8
		c["server_metrics"].(map[string]any)["enabled"] = true
		c["server_metrics"].(map[string]any)["interval"] = "500ms"
		c["output"].(map[string]any)["sample_interval"] = "500ms"
	})
	if code, _, raw := e.do("POST", "/api/start", body, nil); code != 202 {
		t.Fatalf("%d %s", code, raw)
	}
	var sawNodes bool
	for i := 0; i < 400; i++ {
		_, live, _ := e.do("GET", "/api/live", nil, nil)
		if srv, ok := live["server"].(map[string]any); ok && srv["mode"] == "cluster" {
			if n, _ := srv["nodes"].([]any); len(n) > 0 {
				sawNodes = true
			}
		}
		if live["state"] == StateDone {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !sawNodes {
		t.Error("the live view never showed the cluster nodes")
	}
	done := e.waitStateFor(StateDone, 20*time.Second)
	_, sum, _ := e.do("GET", "/api/runs/"+done["run_id"].(string), nil, nil)
	client := sum["client"].(map[string]any)
	if client["target_mode"] != "cluster" || len(client["nodes"].([]any)) != 3 {
		t.Fatalf("client %v", client)
	}
	srv := sum["server"].(map[string]any)
	if len(srv["nodes"].([]any)) != 6 || srv["topology"] == nil {
		t.Fatalf("server %v", srv["topology"])
	}
	_, list, _ := e.do("GET", "/api/runs", nil, nil)
	if list["runs"].([]any)[0].(map[string]any)["target_mode"] != "cluster" {
		t.Fatalf("%v", list)
	}
}
