//go:build integration_tls

package session

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/replay"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/report"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/trafficimport"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/workloadrec"
)

func tlsAddr() string {
	if a := os.Getenv("PLGV_IT_TLS_ADDR"); a != "" {
		return a
	}
	return "127.0.0.1:6444"
}

func certDir() string {
	if d := os.Getenv("PLGV_IT_TLS_DIR"); d != "" {
		return d
	}
	return filepath.Join("..", "..", ".test-certs")
}

// tlsCfg is a run against the TLS + ACL server as the given user, with a client certificate.
func tlsCfg(user, pass, prefix string) config.Config {
	d := certDir()
	cfg := config.Default()
	cfg.Target.URIs = []string{tlsAddr()}
	cfg.Target.Username, cfg.Target.Password = user, pass
	cfg.Target.TLS = config.TLS{Enabled: true, CAFile: filepath.Join(d, "ca.crt"), CertFile: filepath.Join(d, "client.crt"), KeyFile: filepath.Join(d, "client.key")}
	cfg.Run.Concurrency = 4
	cfg.Run.Duration = time.Second
	cfg.Run.Seed = 5
	cfg.Dataset.KeyCount = 300
	cfg.Dataset.KeyPrefix = prefix
	cfg.Dataset.ValueSize = 32
	cfg.Workload.Mix = []config.MixItem{{Op: "GET", Weight: 3}, {Op: "SET", Weight: 2}, {Op: "HSET", Weight: 1}, {Op: "HGETALL", Weight: 1}}
	cfg.Normalize()
	return cfg
}

func cleanTLS(t *testing.T, prefix string) {
	t.Cleanup(func() {
		cfg := tlsCfg("admin", "adminpass", prefix)
		c, _, _, err := connect(context.Background(), cfg)
		if err != nil {
			return
		}
		defer c.Close()
		r, _ := c.Query(context.Background(), valkey.Cmd{"SCAN", "0", "MATCH", prefix + "*", "COUNT", "100000"})
		if arr, ok := r.([]any); ok && len(arr) == 2 {
			for _, k := range arr[1].([]any) {
				_, _ = c.Query(context.Background(), valkey.Cmd{"UNLINK", fmt.Sprint(k)})
			}
		}
	})
}

// A restricted ACL user runs a full workload over mutual TLS, with server
// metrics, key-space analysis, and both client libraries.
func TestTLSAclRunWithBothLibraries(t *testing.T) {
	for _, lib := range []string{valkey.LibValkeyGo, valkey.LibGoRedis} {
		t.Run(lib, func(t *testing.T) {
			prefix := fmt.Sprintf("plgv-tls-%s-%d:", lib, time.Now().UnixNano())
			cleanTLS(t, prefix)
			cfg := tlsCfg("bench", "benchpass", prefix)
			cfg.Target.Client = lib
			cfg.Server.Enabled = true
			cfg.KeySpace.Enabled = true
			cfg.KeySpace.Samples = 200
			out, err := Run(context.Background(), cfg, Options{Version: "it"}, Hooks{})
			if err != nil {
				t.Fatal(err)
			}
			if out.Result.Ops == 0 || out.Result.Errors != 0 {
				t.Fatalf("ops %d errors %d (%s)", out.Result.Ops, out.Result.Errors, out.Result.FirstError)
			}
			if out.Server == nil || out.Server.Version == "" || out.Summary.KeySpace == nil || out.Summary.KeySpace.Sampled == 0 {
				t.Fatalf("server metrics and key-space analysis must work for the restricted user: %+v / %+v", out.Server, out.Summary.KeySpace)
			}
		})
	}
}

func TestTLSSchemeAddressAndInsecure(t *testing.T) {
	prefix := fmt.Sprintf("plgv-tls-s-%d:", time.Now().UnixNano())
	cleanTLS(t, prefix)
	cfg := tlsCfg("bench", "benchpass", prefix)
	cfg.Target.URIs = []string{"valkeys://" + tlsAddr()}
	cfg.Target.TLS.Enabled = false // the scheme alone turns it on
	if out, err := Run(context.Background(), cfg, Options{}, Hooks{}); err != nil || out.Result.Errors != 0 {
		t.Fatalf("%v", err)
	}
	// Without the CA, verification fails; skipping it (explicitly) works.
	cfg = tlsCfg("bench", "benchpass", prefix)
	cfg.Target.TLS.CAFile = ""
	if _, err := Run(context.Background(), cfg, Options{}, Hooks{}); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("an unknown CA must be refused: %v", err)
	}
	cfg.Target.TLS.InsecureSkipVerify = true
	if _, err := Run(context.Background(), cfg, Options{}, Hooks{}); err != nil {
		t.Fatalf("insecure_skip_verify: %v", err)
	}
	// ServerName: the certificate is for localhost, not this name.
	cfg = tlsCfg("bench", "benchpass", prefix)
	cfg.Target.TLS.ServerName = "other.example"
	if _, err := Run(context.Background(), cfg, Options{}, Hooks{}); err == nil {
		t.Fatal("a wrong server name must be refused")
	}
}

func TestTLSRefusals(t *testing.T) {
	prefix := fmt.Sprintf("plgv-tls-r-%d:", time.Now().UnixNano())
	d := certDir()
	cases := map[string]struct {
		edit func(*config.Config)
		want string
	}{
		"no TLS at all":  {func(c *config.Config) { c.Target.TLS = config.TLS{} }, "require TLS"},
		"no client cert": {func(c *config.Config) { c.Target.TLS.CertFile, c.Target.TLS.KeyFile = "", "" }, "certificate required"},
		"other CA":       {func(c *config.Config) { c.Target.TLS.CAFile = filepath.Join(d, "other-ca.crt") }, "unknown authority"},
		"untrusted client": {func(c *config.Config) {
			c.Target.TLS.CertFile, c.Target.TLS.KeyFile = filepath.Join(d, "other-client.crt"), filepath.Join(d, "other-client.key")
		}, "unknown certificate authority"},
		"wrong password":      {func(c *config.Config) { c.Target.Password = "not-the-password-xyz" }, "WRONGPASS"},
		"default user is off": {func(c *config.Config) { c.Target.Username, c.Target.Password = "", "" }, "NOAUTH"},
		"missing CA file":     {func(c *config.Config) { c.Target.TLS.CAFile = filepath.Join(d, "nope.crt") }, "target.tls.ca_file"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := tlsCfg("bench", "benchpass", prefix)
			tc.edit(&cfg)
			_, err := Run(context.Background(), cfg, Options{}, Hooks{})
			if err == nil || (!strings.Contains(err.Error(), tc.want) && !(tc.want == "NOAUTH" && strings.Contains(err.Error(), "WRONGPASS"))) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "benchpass") || strings.Contains(err.Error(), "not-the-password-xyz") {
				t.Fatalf("the error leaks the password: %v", err)
			}
		})
	}
}

// ACL: the reader user may read but not write; a refused command is an error
// class, counted and reported, and does not stop the run.
func TestACLRefusalsAreCountedNotFatal(t *testing.T) {
	prefix := fmt.Sprintf("plgv-acl-%d:", time.Now().UnixNano())
	cleanTLS(t, prefix)
	cfg := tlsCfg("reader", "readerpass", prefix)
	cfg.Dataset.Preload = false
	out, err := Run(context.Background(), cfg, Options{}, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	r := out.Result
	if r.Errors == 0 || r.ErrClasses["NOPERM"] == 0 || !strings.Contains(r.FirstError, "NOPERM") {
		t.Fatalf("writes must fail with NOPERM: %+v %s", r.ErrClasses, r.FirstError)
	}
	if r.ErrClasses["NOPERM"] >= r.Ops {
		t.Fatalf("reads still work: %d NOPERM of %d ops", r.ErrClasses["NOPERM"], r.Ops)
	}
}

// Credentials never reach any output: error text, summary, report, recording, saved config.
func TestPasswordNeverAppearsInOutputs(t *testing.T) {
	prefix := fmt.Sprintf("plgv-pw-%d:", time.Now().UnixNano())
	cleanTLS(t, prefix)
	cfg := tlsCfg("bench", "benchpass", prefix)
	path := filepath.Join(t.TempDir(), "r.jsonl")
	cfg.Record = config.Record{Path: path, Compression: "none", Backpressure: "fail", Outcomes: true, Checksums: true, DataFingerprint: true}
	cfg.Server.Enabled = true
	out, err := Run(context.Background(), cfg, Options{Version: "it"}, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	sum, _ := json.Marshal(out.Summary)
	html, err := report.RenderBytes(out.Summary)
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := os.ReadFile(path)
	y, _ := cfg.Marshal()
	for name, b := range map[string][]byte{"summary": sum, "report": html, "recording": rec, "config": y} {
		if bytes.Contains(b, []byte("benchpass")) {
			t.Errorf("the %s holds the password", name)
		}
		if name != "recording" && bytes.Contains(b, []byte(`"bench"`)) && name != "config" {
			t.Errorf("the %s holds the user name", name)
		}
	}
}

// Record and replay over mutual TLS as the restricted user, with reply checksums.
func TestTLSRecordReplayAndReadsOnlyReplayAsReader(t *testing.T) {
	prefix := fmt.Sprintf("plgv-tlsrr-%d:", time.Now().UnixNano())
	cleanTLS(t, prefix)
	cfg := tlsCfg("bench", "benchpass", prefix)
	cfg.Run.Concurrency = 1
	cfg.Run.TargetOpsPerSec = 1500
	path := filepath.Join(t.TempDir(), "tls.jsonl.zst")
	cfg.Record = config.Record{Path: path, Compression: "auto", Backpressure: "block", Outcomes: true, Checksums: true, DataFingerprint: true}
	if _, err := Run(context.Background(), cfg, Options{}, Hooks{}); err != nil {
		t.Fatal(err)
	}
	info, err := workloadrec.Scan(path)
	if err != nil || info.Status != workloadrec.StatusComplete || info.Header.DataFingerprint == nil {
		t.Fatalf("%+v %v", info, err)
	}
	// The reader user replays the reads only; the data check reads with its own permissions.
	rcfg := tlsCfg("reader", "readerpass", prefix)
	out, err := Replay(context.Background(), rcfg, path, replay.Options{Speed: 0, ReadsOnly: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if out.Result.Ops == 0 || out.Result.Errors != 0 || out.Result.Skipped == 0 {
		t.Fatalf("%+v", out.Result)
	}
	// The same user cannot replay the writes.
	if out, err = Replay(context.Background(), rcfg, path, replay.Options{Speed: 0}, false); err != nil || out.Result.ErrClasses["NOPERM"] == 0 {
		t.Fatalf("%v %+v", err, out)
	}
}

// MONITOR capture over TLS, as the admin (MONITOR is a dangerous command the bench user lacks).
func TestTLSMonitorCapture(t *testing.T) {
	prefix := fmt.Sprintf("plgv-tlsmon-%d:", time.Now().UnixNano())
	cleanTLS(t, prefix)
	cfg := tlsCfg("bench", "benchpass", prefix)
	c, _, _, err := connect(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for i := 0; ctx.Err() == nil; i++ {
			c.Exec(ctx, []valkey.Cmd{{"SET", prefix + "k", fmt.Sprint(i)}})
			time.Sleep(2 * time.Millisecond)
		}
	}()
	time.Sleep(100 * time.Millisecond)
	admin := tlsCfg("admin", "adminpass", prefix)
	tc, err := admin.Target.TLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	n, err := trafficimport.Capture(context.Background(), tlsAddr(), trafficimport.CaptureOptions{Username: "admin", Password: "adminpass", TLS: tc, MaxCommands: 50, MaxDuration: 5 * time.Second}, &buf)
	if err != nil || n != 50 {
		t.Fatalf("%d %v", n, err)
	}
	if _, err := trafficimport.Capture(context.Background(), tlsAddr(), trafficimport.CaptureOptions{MaxCommands: 5}, &buf); err == nil {
		t.Fatal("plain TCP to a TLS port must fail")
	}
	// The bench user is not allowed MONITOR.
	if _, err := trafficimport.Capture(context.Background(), tlsAddr(), trafficimport.CaptureOptions{Username: "bench", Password: "benchpass", TLS: tc, MaxCommands: 5}, &buf); err == nil {
		t.Fatal("the restricted user must not be able to run MONITOR")
	}
}
