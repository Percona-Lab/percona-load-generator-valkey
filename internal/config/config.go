// Package config defines the plgv YAML configuration: schema, defaults,
// validation, environment overrides, and load/save.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/loadprofile"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/rawcmd"
	"gopkg.in/yaml.v3"
)

// Config is the full run configuration.
type Config struct {
	Target   Target        `yaml:"target"`
	Run      Run           `yaml:"run"`
	Dataset  Dataset       `yaml:"dataset"`
	Workload Workload      `yaml:"workload"`
	Server   ServerMetrics `yaml:"server_metrics"`
	KeySpace KeySpace      `yaml:"keyspace_analysis"`
	Record   Record        `yaml:"record"`
	Output   Output        `yaml:"output"`
}

// KeySpace controls the key-space analysis run after the workload: a sample of
// keys is inspected (type, size, expiry) and the biggest are listed. It only
// reads, and is off by default.
type KeySpace struct {
	Enabled bool   `yaml:"enabled"`
	Samples int    `yaml:"samples"` // keys to inspect
	Top     int    `yaml:"top"`     // biggest keys to list
	Match   string `yaml:"match"`   // SCAN MATCH pattern; empty = the dataset's key prefix
}

// Record writes every command the workers send to a file, so the run can be
// replayed (plgv replay). It is off unless Path is set. It is a CLI feature:
// the web server refuses it, because a request must not choose a file to write.
type Record struct {
	Path string `yaml:"path"` // recording file; empty = not recording
	// Compression is auto (by the file extension: .zst, .gz), none, zstd or gzip.
	Compression  string `yaml:"compression"`
	MaxMB        int    `yaml:"max_mb"`       // stop at this many uncompressed MB; 0 = no limit
	Backpressure string `yaml:"backpressure"` // fail (default) or block, when the writer cannot keep up
	Outcomes     bool   `yaml:"outcomes"`     // keep each command's status and latency
	Checksums    bool   `yaml:"checksums"`    // also keep a checksum of each reply (implies outcomes)
	// DataFingerprint stores the key count and a key digest at the start, so a
	// replay can tell whether its target holds the same data.
	DataFingerprint bool `yaml:"data_fingerprint"`
}

// ServerMetrics controls polling of the server under test (INFO, SLOWLOG,
// LATENCY). It is off by default; it only reads.
type ServerMetrics struct {
	Enabled        bool          `yaml:"enabled"`
	Interval       time.Duration `yaml:"interval"`
	Slowlog        bool          `yaml:"slowlog"`         // poll SLOWLOG GET for new entries
	SlowlogEntries int           `yaml:"slowlog_entries"` // entries fetched per poll
	Latency        bool          `yaml:"latency"`         // poll LATENCY LATEST
}

// Output controls what a run records.
type Output struct {
	SampleInterval time.Duration `yaml:"sample_interval"` // time-series slice length
	SummaryFile    string        `yaml:"summary_file"`    // write a JSON run summary here; empty = none
}

// Target says which server to load. Credentials never live in the YAML file:
// they come from PLGV_USERNAME and PLGV_PASSWORD.
type Target struct {
	// URIs: one server (standalone); one or more seed nodes (cluster); one or
	// more sentinel addresses (sentinel).
	URIs []string `yaml:"uris"`
	// Mode: auto (detect standalone or cluster after connecting), standalone,
	// cluster or sentinel. Sentinel is never detected: set it.
	Mode   string `yaml:"mode"`
	Client string `yaml:"client"` // valkey-go | go-redis (go-redis: standalone only)
	// Sentinel: the master set name to follow (mode sentinel).
	Sentinel Sentinel `yaml:"sentinel"`
	// ReadFromReplicas sends read-only commands to replicas (cluster or sentinel).
	ReadFromReplicas bool `yaml:"read_from_replicas"`
	Pool             Pool `yaml:"pool"`
	// TLS encrypts the connection (see tls.go). Off by default.
	TLS      TLS    `yaml:"tls"`
	Username string `yaml:"-" json:"-"`
	Password string `yaml:"-" json:"-"`
}

// Sentinel names the master set a sentinel deployment monitors.
type Sentinel struct {
	MasterName string `yaml:"master_name"`
}

// Pool tunes the connection behavior.
type Pool struct {
	PipelineDepth int `yaml:"pipeline_depth"` // operations per pipelined batch; 1 = none
}

// Run controls duration, concurrency and randomness.
type Run struct {
	Duration    time.Duration `yaml:"duration"`
	Concurrency int           `yaml:"concurrency"`
	Seed        int64         `yaml:"seed"` // 0 = pick a random seed and report it
	// OpBudget, when above 0, replaces the duration: the run sends exactly this
	// many operations, split between the workers, and stops. Needs a closed loop
	// and a fixed number of workers.
	OpBudget int64 `yaml:"op_budget"`
	// Deterministic makes the generated traffic reproducible: the same seed,
	// op_budget and workload always send the same commands, and the run reports
	// a traffic fingerprint to prove it. Needs a seed and op_budget, and refuses
	// operations whose commands depend on the clock or on server replies.
	Deterministic bool `yaml:"deterministic"`
	// TargetOpsPerSec paces the run to this total rate; 0 = as fast as possible.
	TargetOpsPerSec float64 `yaml:"target_ops_per_sec"`
	// CoordinatedOmission measures latency from each batch's intended start
	// time instead of its actual send time. Needs target_ops_per_sec > 0.
	CoordinatedOmission bool `yaml:"coordinated_omission"`
	// Loop is "closed" (each worker sends its next batch when the previous one
	// finishes, the default) or "open" (batches arrive at target_ops_per_sec
	// whether or not the server keeps up; latency includes queueing).
	Loop string `yaml:"loop"`
	// LoadProfile shapes the number of active workers over time (fixed, ramp,
	// step, spike or sine). Fixed uses run.concurrency.
	LoadProfile loadprofile.Config `yaml:"load_profile"`
	// Sweep repeats the run once per level (worker counts or target rates).
	Sweep Sweep `yaml:"sweep"`
}

// Sweep runs the same workload several times, one level per run.
type Sweep struct {
	Kind   string        `yaml:"kind"`   // none | workers | rate
	Values []float64     `yaml:"values"` // worker counts, or target ops/s
	Pause  time.Duration `yaml:"pause"`  // wait between two runs
}

// Sweep limits.
const (
	MinSweepLevels  = 2
	MaxSweepLevels  = 8
	MaxSweepWorkers = 10000
)

// Dataset describes the key space and values.
type Dataset struct {
	KeyCount        int64   `yaml:"key_count"` // keys per type
	KeyPrefix       string  `yaml:"key_prefix"`
	KeyDistribution string  `yaml:"key_distribution"` // uniform | zipf
	ZipfS           float64 `yaml:"zipf_s"`           // > 1; 0 = 1.1
	ValueSize       int     `yaml:"value_size"`       // bytes
	CollectionSize  int     `yaml:"collection_size"`  // elements per list, set, zset
	// HashTagGroups > 0 puts a {group} hash tag in every key so keys of the same
	// group share a cluster slot and multi-key commands (MGET, MSET) work. 0 = no tags.
	HashTagGroups int `yaml:"hash_tag_groups"`
	HashFields    int `yaml:"hash_fields"` // fields per hash
	// BitmapBits is the size of each bitmap key in bits.
	BitmapBits int `yaml:"bitmap_bits"`
	// StreamLength is the number of entries preloaded into each stream, and the
	// length XTRIM keeps (XADD trims to ten times this, approximately).
	StreamLength int  `yaml:"stream_length"`
	TTL          TTL  `yaml:"ttl"`
	Preload      bool `yaml:"preload"`
}

// TTL is the expiry model for string writes (SET) and for the explicit expiry
// ops (SETEX, EXPIRE, GETEX).
type TTL struct {
	Mode       string  `yaml:"mode"` // none | fixed | random
	Seconds    int     `yaml:"seconds"`
	MinSeconds int     `yaml:"min_seconds"`
	MaxSeconds int     `yaml:"max_seconds"`
	Fraction   float64 `yaml:"fraction"` // share of SET writes that get a TTL, 0-1
}

// Workload is the operation mix.
type Workload struct {
	// Preset names a built-in starting point (see Presets). It is applied under
	// the rest of the file, so any setting written here overrides it.
	Preset string    `yaml:"preset,omitempty"`
	Mix    []MixItem `yaml:"mix"`
	// PubSub starts subscribers so PUBLISH/SPUBLISH operations are received and
	// their delivery latency measured. 0 subscribers = publish only.
	PubSub PubSub `yaml:"pubsub"`
	// AllowDangerous lets RAW commands use commands that are blocked by default
	// (FLUSHALL, DEBUG, SHUTDOWN, CONFIG SET, ...).
	AllowDangerous bool `yaml:"allow_dangerous"`
}

// PubSub configures the subscribers.
type PubSub struct {
	Subscribers int  `yaml:"subscribers"`
	Channels    int  `yaml:"channels"` // channels published to and subscribed to
	Sharded     bool `yaml:"sharded"`  // SSUBSCRIBE/SPUBLISH instead of SUBSCRIBE/PUBLISH
}

// MixItem is one weighted operation.
type MixItem struct {
	Op     string `yaml:"op"`
	Weight int    `yaml:"weight"`
	Type   string `yaml:"type,omitempty"`   // key ops only: string|hash|list|set|zset
	Count  int    `yaml:"count,omitempty"`  // MGET/MSET keys, LRANGE/ZRANGE/... elements, SCAN page size
	Fields int    `yaml:"fields,omitempty"` // HSET fields written per call
	// Timeout: blocking ops wait at most this long for data (default 1s).
	Timeout string `yaml:"timeout,omitempty"`
	// Script names a built-in Lua script or function (rate_limit, incr_cap, cas);
	// Source is inline Lua for EVAL/EVALSHA (it receives one key); Args are
	// extra string arguments.
	Script string   `yaml:"script,omitempty"`
	Source string   `yaml:"source,omitempty"`
	Args   []string `yaml:"args,omitempty"`
	// Steps are the commands of a TXN or PIPELINE (their weights are ignored).
	Steps []MixItem `yaml:"steps,omitempty"`
	// Command is the text of a RAW command, for example "GET {key}".
	Command string `yaml:"command,omitempty"`
}

// Default returns the configuration used when no file is given: an 80/20
// GET/SET workload on 100,000 keys, preloaded.
func Default() Config {
	return Config{
		Target: Target{
			URIs: []string{"valkey://127.0.0.1:6379"}, Mode: "auto", Client: "valkey-go", Sentinel: Sentinel{MasterName: ""},
			Pool: Pool{PipelineDepth: 1},
		},
		Run: Run{Duration: 10 * time.Second, Concurrency: 64, Seed: 1, Loop: "closed",
			LoadProfile: loadprofile.Config{Kind: "fixed", Steps: []loadprofile.Stage{}}, Sweep: Sweep{Kind: "none", Values: []float64{}}},
		Dataset: Dataset{
			KeyCount: 100000, KeyPrefix: "plgv:", KeyDistribution: "uniform",
			ValueSize: 256, CollectionSize: 100, HashFields: 8, BitmapBits: 10000, StreamLength: 20, Preload: true,
			TTL: TTL{Mode: "none", Seconds: 300, MinSeconds: 60, MaxSeconds: 3600, Fraction: 1},
		},
		Server:   ServerMetrics{Interval: time.Second, Slowlog: true, SlowlogEntries: 128, Latency: true},
		KeySpace: KeySpace{Samples: 10000, Top: 10},
		Record:   Record{Compression: "auto", Backpressure: "fail", DataFingerprint: true},
		Output:   Output{SampleInterval: time.Second},
		Workload: Workload{Mix: []MixItem{{Op: "GET", Weight: 80}, {Op: "SET", Weight: 20}}, PubSub: PubSub{Channels: 16}},
	}
}

// Load reads a YAML file on top of the defaults. Unknown fields are errors.
// Environment overrides are not applied here; call ApplyEnv.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	return Parse(b)
}

// Parse decodes YAML on top of the defaults.
func Parse(b []byte) (Config, error) { return ParseWithPreset(b, "") }

// ParseWithPreset is Parse with a preset chosen by the caller (for example a
// command-line flag). The file's own workload.preset is used when preset is empty.
func ParseWithPreset(b []byte, preset string) (Config, error) {
	c := Default()
	if preset == "" {
		preset, _ = presetOf(b)
	}
	if preset != "" {
		p, ok := FindPreset(preset)
		if !ok {
			return Config{}, unknownPreset(preset)
		}
		p.Apply(&c)
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	// An empty file (io.EOF) means "all defaults".
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	c.Normalize()
	return c, nil
}

// Marshal renders the configuration as YAML (credentials are never included).
func (c Config) Marshal() ([]byte, error) { return yaml.Marshal(c) }

// Save writes the configuration as YAML.
func (c Config) Save(path string) error {
	b, err := c.Marshal()
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// ApplyEnv applies environment overrides: PLGV_URI (or the older PLGV_ADDR),
// PLGV_USERNAME and PLGV_PASSWORD.
func (c *Config) ApplyEnv(getenv func(string) string) {
	if v := getenv("PLGV_URI"); v != "" {
		c.Target.URIs = []string{v}
	} else if v := getenv("PLGV_ADDR"); v != "" {
		c.Target.URIs = []string{v}
	}
	if v := getenv("PLGV_USERNAME"); v != "" {
		c.Target.Username = v
	}
	if v := getenv("PLGV_PASSWORD"); v != "" {
		c.Target.Password = v
	}
	if v := strings.ToLower(getenv("PLGV_TLS")); v == "true" || v == "1" || v == "yes" {
		c.Target.TLS.Enabled = true
	}
	if v := getenv("PLGV_TLS_CA_FILE"); v != "" {
		c.Target.TLS.CAFile = v
	}
	if v := getenv("PLGV_TLS_CERT_FILE"); v != "" {
		c.Target.TLS.CertFile = v
	}
	if v := getenv("PLGV_TLS_KEY_FILE"); v != "" {
		c.Target.TLS.KeyFile = v
	}
	if v := getenv("PLGV_TLS_SERVER_NAME"); v != "" {
		c.Target.TLS.ServerName = v
	}
}

// ApplyEnvStrict is ApplyEnv plus PLGV_USERNAME_FILE and PLGV_PASSWORD_FILE
// (mounted secrets); it fails when such a file cannot be read.
func (c *Config) ApplyEnvStrict(getenv func(string) string) error {
	c.ApplyEnv(getenv)
	return c.ResolveSecrets(getenv)
}

// ApplyCredentialsEnv applies only PLGV_USERNAME and PLGV_PASSWORD. The web UI
// uses it so an address typed in the form is never replaced by PLGV_URI.
func (c *Config) ApplyCredentialsEnv(getenv func(string) string) {
	if v := getenv("PLGV_USERNAME"); v != "" {
		c.Target.Username = v
	}
	if v := getenv("PLGV_PASSWORD"); v != "" {
		c.Target.Password = v
	}
}

// Addrs returns the host:port of every address in target.uris.
func (t Target) Addrs() ([]string, error) {
	if len(t.URIs) == 0 {
		return nil, errors.New("target.uris: give at least one address")
	}
	out := make([]string, 0, len(t.URIs))
	for i, raw0 := range t.URIs {
		raw := strings.TrimSpace(raw0)
		if !strings.Contains(raw, "://") {
			raw = "valkey://" + raw
		}
		u, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("target.uris[%d]: %w", i, err)
		}
		switch u.Scheme {
		case "valkey", "redis":
		case "valkeys", "rediss":
		default:
			return nil, fmt.Errorf("target.uris[%d]: unsupported scheme %q (use valkey://, valkeys:// or host:port)", i, u.Scheme)
		}
		if u.User != nil {
			return nil, fmt.Errorf("target.uris[%d]: do not put credentials in the URI; use PLGV_USERNAME and PLGV_PASSWORD", i)
		}
		if u.Hostname() == "" || u.Port() == "" {
			return nil, fmt.Errorf("target.uris[%d]: want host:port, got %q", i, raw0)
		}
		out = append(out, u.Host)
	}
	return out, nil
}

// Addr returns the first address; for a standalone target it is the only one.
func (t Target) Addr() (string, error) {
	a, err := t.Addrs()
	if err != nil {
		return "", err
	}
	return a[0], nil
}

// Validate checks the whole configuration and returns every problem found.
func (c Config) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if c.Workload.Preset != "" {
		if _, ok := FindPreset(c.Workload.Preset); !ok {
			add("workload.preset: unknown preset %q (one of %s)", c.Workload.Preset, strings.Join(PresetNames(), ", "))
		}
	}
	c.Target.validateTLS(add)
	addrs, addrErr := c.Target.Addrs()
	if addrErr != nil {
		errs = append(errs, addrErr)
	}
	switch c.Target.Mode {
	case "auto", "standalone", "cluster", "sentinel":
	default:
		add("target.mode: unknown %q (want auto, standalone, cluster or sentinel)", c.Target.Mode)
	}
	if addrErr == nil && c.Target.Mode == "standalone" && len(addrs) != 1 {
		add("target.uris: a standalone target has exactly one address, got %d (use mode cluster or sentinel for several)", len(addrs))
	}
	if c.Target.Mode == "sentinel" && strings.TrimSpace(c.Target.Sentinel.MasterName) == "" {
		add("target.sentinel.master_name: required for mode sentinel (the master set the sentinels monitor)")
	}
	if c.Target.Mode != "sentinel" && c.Target.Sentinel.MasterName != "" {
		add("target.sentinel.master_name: only used with mode sentinel")
	}
	if c.Target.ReadFromReplicas && c.Target.Mode == "standalone" {
		add("target.read_from_replicas: needs a cluster or sentinel target, not standalone")
	}
	if (c.Target.Mode == "cluster" || c.Target.Mode == "sentinel") && c.Target.Client == "go-redis" {
		add("target.client: go-redis supports standalone targets only; use valkey-go for %s", c.Target.Mode)
	}
	switch c.Target.Client {
	case "valkey-go", "go-redis":
	default:
		add("target.client: unknown %q (want valkey-go or go-redis)", c.Target.Client)
	}
	if c.Target.Pool.PipelineDepth < 1 {
		add("target.pool.pipeline_depth: must be >= 1, got %d", c.Target.Pool.PipelineDepth)
	}
	if c.Run.Duration <= 0 {
		add("run.duration: must be > 0, got %v", c.Run.Duration)
	}
	if c.Run.Concurrency < 1 {
		add("run.concurrency: must be >= 1, got %d", c.Run.Concurrency)
	}
	if c.Run.Seed < 0 {
		add("run.seed: must be >= 0, got %d", c.Run.Seed)
	}

	if c.Run.TargetOpsPerSec < 0 {
		add("run.target_ops_per_sec: must be >= 0, got %v", c.Run.TargetOpsPerSec)
	}
	if c.Run.CoordinatedOmission && c.Run.TargetOpsPerSec <= 0 {
		add("run.coordinated_omission: needs run.target_ops_per_sec > 0 (there is no intended start time without a target rate)")
	}
	if c.Output.SampleInterval < 100*time.Millisecond {
		add("output.sample_interval: must be >= 100ms, got %v", c.Output.SampleInterval)
	}
	if c.KeySpace.Samples < 100 || c.KeySpace.Samples > 1_000_000 {
		add("keyspace_analysis.samples: must be 100-1000000, got %d", c.KeySpace.Samples)
	}
	if c.KeySpace.Top < 1 || c.KeySpace.Top > 100 {
		add("keyspace_analysis.top: must be 1-100, got %d", c.KeySpace.Top)
	}
	if c.Record.Path != "" || c.Record.Outcomes || c.Record.Checksums {
		if c.Record.Path == "" {
			add("record.path: is required when record.outcomes or record.checksums is set")
		}
		switch c.Record.Compression {
		case "auto", "none", "zstd", "gzip":
		default:
			add("record.compression: must be auto, none, zstd or gzip, got %q", c.Record.Compression)
		}
		switch c.Record.Backpressure {
		case "fail", "block":
		default:
			add("record.backpressure: must be fail or block, got %q", c.Record.Backpressure)
		}
		if c.Run.Sweep.Kind != "" && c.Run.Sweep.Kind != "none" {
			add("record.path: a sweep runs several times and would overwrite the recording; record one run at a time")
		}
		if c.Record.MaxMB < 0 {
			add("record.max_mb: must be >= 0, got %d", c.Record.MaxMB)
		}
	}
	if c.Server.Interval < 100*time.Millisecond {
		add("server_metrics.interval: must be >= 100ms, got %v", c.Server.Interval)
	}
	if c.Server.SlowlogEntries < 1 || c.Server.SlowlogEntries > 10000 {
		add("server_metrics.slowlog_entries: must be 1-10000, got %d", c.Server.SlowlogEntries)
	}

	c.validateRunShape(add)

	d := c.Dataset
	if d.KeyCount < 1 {
		add("dataset.key_count: must be >= 1, got %d", d.KeyCount)
	}
	switch d.KeyDistribution {
	case "uniform":
	case "zipf":
		if d.ZipfS != 0 && d.ZipfS <= 1 {
			add("dataset.zipf_s: must be > 1 (or 0 for the default), got %v", d.ZipfS)
		}
	default:
		add("dataset.key_distribution: unknown %q (want uniform or zipf)", d.KeyDistribution)
	}
	if d.BitmapBits < 8 || d.BitmapBits > 1<<30 {
		add("dataset.bitmap_bits: must be 8 to %d, got %d", 1<<30, d.BitmapBits)
	}
	if d.StreamLength < 1 || d.StreamLength > 100000 {
		add("dataset.stream_length: must be 1 to 100000, got %d", d.StreamLength)
	}
	if d.HashTagGroups < 0 {
		add("dataset.hash_tag_groups: must be >= 0, got %d", d.HashTagGroups)
	}
	if d.HashTagGroups > 0 && int64(d.HashTagGroups) > d.KeyCount {
		add("dataset.hash_tag_groups: %d groups exceed dataset.key_count (%d)", d.HashTagGroups, d.KeyCount)
	}
	if c.Target.Mode == "cluster" && d.HashTagGroups == 0 {
		for i, it := range c.Workload.Mix {
			if (it.Op == "MGET" || it.Op == "MSET") && it.Count != 1 {
				add("workload.mix[%d]: %s touches several keys, which fail with CROSSSLOT in a cluster unless they share a slot; set dataset.hash_tag_groups >= 1 (or count: 1)", i, it.Op)
			}
		}
	}
	if d.ValueSize < 1 {
		add("dataset.value_size: must be >= 1, got %d", d.ValueSize)
	}
	if d.CollectionSize < 1 {
		add("dataset.collection_size: must be >= 1, got %d", d.CollectionSize)
	}
	if d.HashFields < 1 {
		add("dataset.hash_fields: must be >= 1, got %d", d.HashFields)
	}
	switch d.TTL.Mode {
	case "none":
	case "fixed":
		if d.TTL.Seconds < 1 {
			add("dataset.ttl.seconds: must be >= 1 for mode fixed, got %d", d.TTL.Seconds)
		}
	case "random":
		if d.TTL.MinSeconds < 1 || d.TTL.MaxSeconds < d.TTL.MinSeconds {
			add("dataset.ttl: random mode needs 1 <= min_seconds <= max_seconds, got %d..%d", d.TTL.MinSeconds, d.TTL.MaxSeconds)
		}
	default:
		add("dataset.ttl.mode: unknown %q (want none, fixed or random)", d.TTL.Mode)
	}
	if d.TTL.Fraction < 0 || d.TTL.Fraction > 1 {
		add("dataset.ttl.fraction: must be between 0 and 1, got %v", d.TTL.Fraction)
	}

	if len(c.Workload.Mix) == 0 {
		add("workload.mix: needs at least one operation")
	}
	for i, it := range c.Workload.Mix {
		c.validateItem(add, fmt.Sprintf("workload.mix[%d]", i), it, false)
	}
	c.validatePubSub(add)
	return errors.Join(errs...)
}

// validateItem checks one mix entry (or one step of a TXN/PIPELINE).
func (c Config) validateItem(add func(string, ...any), p string, it MixItem, step bool) {
	d := c.Dataset
	info, ok := Ops[it.Op]
	if !ok {
		add("%s.op: unknown %q (supported: %s)", p, it.Op, strings.Join(OpNames(), ", "))
		return
	}
	if !step && it.Weight < 1 {
		add("%s.weight: must be >= 1, got %d", p, it.Weight)
	}
	if it.Count < 0 || (it.Count > 0 && !info.Count) {
		add("%s.count: not valid for %s", p, it.Op)
	}
	if it.Fields < 0 || (it.Fields > 0 && !info.Fields) {
		add("%s.fields: not valid for %s", p, it.Op)
	}
	if it.Fields > d.HashFields {
		add("%s.fields: %d exceeds dataset.hash_fields (%d)", p, it.Fields, d.HashFields)
	}
	if it.Type != "" {
		valid := false
		for _, t := range AllTypes {
			if it.Type == t {
				valid = true
			}
		}
		switch {
		case !info.Type:
			add("%s.type: not valid for %s", p, it.Op)
		case !valid:
			add("%s.type: unknown %q (want %s)", p, it.Type, strings.Join(AllTypes, ", "))
		}
	}
	if it.Timeout != "" {
		if !info.Timeout {
			add("%s.timeout: not valid for %s", p, it.Op)
		} else if dur, err := time.ParseDuration(it.Timeout); err != nil || dur < 10*time.Millisecond || dur > time.Minute {
			add("%s.timeout: want a duration from 10ms to 1m (for example 500ms), got %q", p, it.Timeout)
		}
	}
	if (it.Script != "" || it.Source != "" || len(it.Args) > 0) && !info.Script {
		add("%s: script, source and args are only valid for EVAL, EVALSHA and FCALL", p)
	}
	if info.Script {
		builtin := false
		for _, n := range BuiltinScripts {
			if it.Script == n {
				builtin = true
			}
		}
		switch {
		case it.Script != "" && it.Source != "":
			add("%s: give either script (a built-in) or source (inline Lua), not both", p)
		case it.Script == "" && it.Source == "":
			add("%s.script: %s needs script (one of %s) or source (inline Lua)", p, it.Op, strings.Join(BuiltinScripts, ", "))
		case it.Script != "" && !builtin:
			add("%s.script: unknown %q (built-ins: %s)", p, it.Script, strings.Join(BuiltinScripts, ", "))
		case it.Source != "" && it.Op == "FCALL":
			add("%s.source: FCALL calls a function by name; use script", p)
		}
	}
	if len(it.Steps) > 0 && !info.Steps {
		add("%s.steps: only valid for TXN and PIPELINE", p)
	}
	if info.Steps {
		if step {
			add("%s: %s cannot be nested inside another TXN or PIPELINE", p, it.Op)
		}
		if len(it.Steps) < 1 || len(it.Steps) > 32 {
			add("%s.steps: give 1 to 32 steps, got %d", p, len(it.Steps))
		}
		for j, st := range it.Steps {
			sp := fmt.Sprintf("%s.steps[%d]", p, j)
			si, sok := Ops[st.Op]
			if sok && (si.Kind != "" && si.Kind != KindSimple || si.Family == "raw" || si.Family == "pubsub") {
				add("%s.op: %s cannot be a step (blocking, composite, raw and pub/sub operations run on their own)", sp, st.Op)
				continue
			}
			c.validateItem(add, sp, st, true)
		}
		if c.Target.Mode == "cluster" && len(it.Steps) > 1 && d.HashTagGroups == 0 && it.Op == "TXN" {
			add("%s: a transaction over several keys fails with CROSSSLOT in a cluster unless its keys share a slot; set dataset.hash_tag_groups >= 1", p)
		}
	}
	if it.Op == "WATCH_INCR" && c.Target.Mode == "cluster" && c.Target.Client == "go-redis" {
		add("%s: WATCH_INCR needs valkey-go", p)
	}
	if it.Command != "" && !info.Command {
		add("%s.command: only valid for RAW", p)
	}
	if info.Command {
		if step {
			add("%s: RAW cannot be a step", p)
		} else if strings.TrimSpace(it.Command) == "" {
			add("%s.command: RAW needs a command, for example \"GET {key}\"", p)
		} else if t, err := rawcmd.Parse(it.Command); err != nil {
			add("%s.command: %v", p, err)
		} else if err := rawcmd.Check(t, c.Workload.AllowDangerous); err != nil {
			add("%s.command: %v", p, err)
		}
	}
}

// validatePubSub checks the pub/sub settings against the operations that use them.
func (c Config) validatePubSub(add func(string, ...any)) {
	ps := c.Workload.PubSub
	if ps.Subscribers < 0 {
		add("workload.pubsub.subscribers: must be >= 0, got %d", ps.Subscribers)
	}
	if ps.Channels < 1 {
		add("workload.pubsub.channels: must be >= 1, got %d", ps.Channels)
	}
	uses := map[string]bool{}
	for _, it := range c.Workload.Mix {
		uses[it.Op] = true
	}
	if ps.Sharded && uses["PUBLISH"] {
		add("workload.pubsub.sharded: subscribers use SSUBSCRIBE, so publish with SPUBLISH (PUBLISH does not reach them)")
	}
	if !ps.Sharded && uses["SPUBLISH"] && ps.Subscribers > 0 {
		add("workload.pubsub.sharded: SPUBLISH only reaches sharded subscribers; set sharded: true")
	}
	if ps.Subscribers > 0 && !uses["PUBLISH"] && !uses["SPUBLISH"] {
		add("workload.pubsub.subscribers: %d subscribers but the mix never publishes (add PUBLISH or SPUBLISH)", ps.Subscribers)
	}
	if ps.Subscribers > 0 && c.Target.Client == "go-redis" && ps.Sharded {
		add("workload.pubsub.sharded: needs valkey-go")
	}
}

// Normalize upper-cases operation names so YAML may use any case.
func (c *Config) Normalize() {
	c.Workload.Preset = strings.ToLower(strings.TrimSpace(c.Workload.Preset))
	c.Record.Compression = strings.ToLower(strings.TrimSpace(c.Record.Compression))
	c.Record.Backpressure = strings.ToLower(strings.TrimSpace(c.Record.Backpressure))
	var norm func(it *MixItem)
	norm = func(it *MixItem) {
		it.Op = strings.ToUpper(strings.TrimSpace(it.Op))
		it.Type = strings.ToLower(strings.TrimSpace(it.Type))
		it.Script = strings.ToLower(strings.TrimSpace(it.Script))
		for j := range it.Steps {
			norm(&it.Steps[j])
		}
	}
	for i := range c.Workload.Mix {
		norm(&c.Workload.Mix[i])
	}
}

// validateRunShape checks the loop, load profile and sweep settings together.
func (c Config) validateRunShape(add func(string, ...any)) {
	r := c.Run
	switch r.Loop {
	case "closed", "open":
	default:
		add("run.loop: unknown %q (want closed or open)", r.Loop)
	}
	if r.Loop == "open" && r.TargetOpsPerSec <= 0 {
		add("run.loop: open needs run.target_ops_per_sec > 0 (an open loop sends at a fixed rate)")
	}
	kind := r.LoadProfile.NormalizedKind()
	if _, err := loadprofile.Compile(r.LoadProfile, r.Concurrency); err != nil {
		add("run.load_profile: %v", err)
	}
	if kind != loadprofile.KindFixed && r.Loop != "open" && r.TargetOpsPerSec > 0 {
		add("run.target_ops_per_sec: pacing a changing number of workers needs run.loop: open")
	}

	if r.OpBudget < 0 {
		add("run.op_budget: must be >= 0, got %d", r.OpBudget)
	}
	if r.OpBudget > 0 {
		if r.Loop == "open" {
			add("run.op_budget: needs run.loop: closed (an open loop sends at a rate, not a count)")
		}
		if kind != loadprofile.KindFixed {
			add("run.op_budget: needs a fixed load profile (a changing worker count has no fixed split of the budget)")
		}
	}
	if r.Deterministic {
		if r.Seed == 0 {
			add("run.deterministic: needs run.seed (a fixed non-zero seed)")
		}
		if r.OpBudget <= 0 {
			add("run.deterministic: needs run.op_budget > 0 (a fixed number of operations)")
		}
		if r.Sweep.Kind != "" && r.Sweep.Kind != "none" {
			add("run.deterministic: a sweep changes the number of workers, which changes the traffic")
		}
		for _, op := range c.nonDeterministicOps() {
			add("workload.mix: %s", op)
		}
	}

	sw := r.Sweep
	switch sw.Kind {
	case "none", "":
		return
	case "workers", "rate":
	default:
		add("run.sweep.kind: unknown %q (want none, workers or rate)", sw.Kind)
		return
	}
	if n := len(sw.Values); n < MinSweepLevels || n > MaxSweepLevels {
		add("run.sweep.values: give %d to %d levels, got %d", MinSweepLevels, MaxSweepLevels, n)
	}
	seen := map[float64]bool{}
	for i, v := range sw.Values {
		switch {
		case sw.Kind == "workers" && (v != float64(int(v)) || v < 1 || v > MaxSweepWorkers):
			add("run.sweep.values[%d]: a worker count must be a whole number from 1 to %d, got %v", i, MaxSweepWorkers, v)
		case sw.Kind == "rate" && v <= 0:
			add("run.sweep.values[%d]: a target rate must be > 0, got %v", i, v)
		case seen[v]:
			add("run.sweep.values[%d]: %v is listed twice", i, v)
		}
		seen[v] = true
	}
	if sw.Pause < 0 {
		add("run.sweep.pause: must be >= 0, got %v", sw.Pause)
	}
	if kind != loadprofile.KindFixed {
		add("run.load_profile: a sweep runs fixed levels; use the fixed profile")
	}
	if sw.Kind == "rate" && r.TargetOpsPerSec > 0 {
		add("run.target_ops_per_sec: a rate sweep sets the rate for each level; leave it at 0")
	}
}

// nonDeterministicOps lists, for deterministic mode, the mix entries whose
// commands depend on the clock or on what the server replied.
func (c Config) nonDeterministicOps() []string {
	var out []string
	var visit func(it MixItem)
	visit = func(it MixItem) {
		switch {
		case it.Op == "PUBLISH" || it.Op == "SPUBLISH":
			out = append(out, it.Op+" puts the send time in the message, so the traffic changes on every run")
		case it.Op == "XCONSUME":
			out = append(out, "XCONSUME acknowledges the ids the server returned, so the traffic depends on the server")
		case it.Op == "WATCH_INCR":
			out = append(out, "WATCH_INCR writes the value it read, so the traffic depends on the server")
		case it.Op == "RAW" && strings.Contains(it.Command, "{now}"):
			out = append(out, "RAW with {now} puts the current time in the command")
		}
		for _, st := range it.Steps {
			visit(st)
		}
	}
	for _, it := range c.Workload.Mix {
		visit(it)
	}
	return out
}
