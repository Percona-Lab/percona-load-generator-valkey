package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// Preset is a named starting point: a workload mix plus the dataset and pub/sub
// settings that make it realistic. A preset is applied under the user's own
// settings, so anything written in the config (or sent by the UI) overrides it.
type Preset struct {
	Name        string
	Title       string
	Description string
	apply       func(c *Config)
}

// Apply writes the preset's workload and dataset settings onto c.
func (p Preset) Apply(c *Config) {
	p.apply(c)
	c.Workload.Preset = p.Name
}

func hashTags(c *Config) { c.Dataset.HashTagGroups = 16 } // multi-key and TXN presets also run in a cluster

var presets = []Preset{
	{Name: "cache", Title: "Cache", Description: "Read-heavy key/value cache: hot keys (zipf), values that expire, a few multi-key reads and deletes.",
		apply: func(c *Config) {
			c.Dataset.KeyDistribution, c.Dataset.ZipfS, c.Dataset.ValueSize = "zipf", 1.1, 512
			c.Dataset.TTL = TTL{Mode: "random", Seconds: 300, MinSeconds: 60, MaxSeconds: 600, Fraction: 1}
			hashTags(c)
			c.Workload.Mix = []MixItem{{Op: "GET", Weight: 80}, {Op: "SET", Weight: 10}, {Op: "MGET", Weight: 4, Count: 5}, {Op: "DEL", Weight: 3}, {Op: "EXPIRE", Weight: 3}}
		}},
	{Name: "session", Title: "Session store", Description: "User sessions as hashes with a sliding expiry: create, read a field, read all, touch.",
		apply: func(c *Config) {
			c.Dataset.HashFields, c.Dataset.ValueSize = 8, 128
			c.Workload.Mix = []MixItem{{Op: "HSET", Weight: 15, Fields: 4}, {Op: "HGET", Weight: 45}, {Op: "HGETALL", Weight: 15}, {Op: "EXPIRE", Weight: 20, Type: "hash"}, {Op: "DEL", Weight: 5, Type: "hash"}}
		}},
	{Name: "leaderboard", Title: "Leaderboard", Description: "Sorted-set ranking: score updates, rank and score lookups, top-N pages. A few boards (hot keys).",
		apply: func(c *Config) {
			c.Dataset.KeyCount, c.Dataset.KeyDistribution, c.Dataset.ZipfS, c.Dataset.CollectionSize = 1000, "zipf", 1.2, 1000
			c.Workload.Mix = []MixItem{{Op: "ZADD", Weight: 15}, {Op: "ZINCRBY", Weight: 20}, {Op: "ZRANK", Weight: 20}, {Op: "ZSCORE", Weight: 15}, {Op: "ZRANGE", Weight: 25, Count: 10}, {Op: "ZCARD", Weight: 5}}
		}},
	{Name: "queue", Title: "Work queue", Description: "Producers and consumers on a list and on a stream with a consumer group (read, then acknowledge).",
		apply: func(c *Config) {
			c.Dataset.KeyCount, c.Dataset.StreamLength, c.Dataset.CollectionSize = 100, 50, 100
			c.Workload.Mix = []MixItem{{Op: "LPUSH", Weight: 25}, {Op: "RPOP", Weight: 20}, {Op: "LLEN", Weight: 5}, {Op: "XADD", Weight: 20}, {Op: "XCONSUME", Weight: 20}, {Op: "XPENDING", Weight: 5}, {Op: "XLEN", Weight: 5}}
		}},
	{Name: "rate-limiter", Title: "Rate limiter", Description: "Fixed-window limits: a Lua script (INCR, EXPIRE on first hit, compare), plus plain counters.",
		apply: func(c *Config) {
			c.Dataset.KeyCount, c.Dataset.KeyDistribution, c.Dataset.ZipfS = 10000, "zipf", 1.1
			c.Workload.Mix = []MixItem{{Op: "EVAL", Weight: 70, Script: "rate_limit", Args: []string{"60", "1000"}}, {Op: "INCR", Weight: 25}, {Op: "TTL", Weight: 5}}
		}},
	{Name: "pubsub-fanout", Title: "Pub/sub fan-out", Description: "Publishers send to 32 channels, 8 subscribers receive every message; reports delivery latency and loss.",
		apply: func(c *Config) {
			c.Workload.PubSub = PubSub{Subscribers: 8, Channels: 32}
			c.Workload.Mix = []MixItem{{Op: "PUBLISH", Weight: 95}, {Op: "GET", Weight: 5}}
		}},
	{Name: "counters", Title: "Counters and analytics", Description: "Page-view analytics: counters, HyperLogLog unique counts and bitmap flags.",
		apply: func(c *Config) {
			c.Dataset.KeyCount, c.Dataset.CollectionSize, c.Dataset.BitmapBits = 10000, 50, 100000
			c.Workload.Mix = []MixItem{{Op: "INCR", Weight: 30}, {Op: "PFADD", Weight: 25}, {Op: "PFCOUNT", Weight: 10}, {Op: "SETBIT", Weight: 20}, {Op: "GETBIT", Weight: 10}, {Op: "BITCOUNT", Weight: 5}}
		}},
	{Name: "big-keys", Title: "Big keys stress", Description: "Few, very large collections (10,000 elements): whole-collection reads and slices. Shows what big keys do to latency.",
		apply: func(c *Config) {
			c.Dataset.KeyCount, c.Dataset.CollectionSize, c.Dataset.HashFields, c.Dataset.ValueSize = 50, 10000, 1000, 128
			c.Run.Concurrency = 16
			c.Workload.Mix = []MixItem{{Op: "HGETALL", Weight: 20}, {Op: "SMEMBERS", Weight: 15}, {Op: "LRANGE", Weight: 20, Count: 1000}, {Op: "ZRANGE", Weight: 20, Count: 1000}, {Op: "SADD", Weight: 10}, {Op: "ZADD", Weight: 10}, {Op: "HSET", Weight: 5, Fields: 4}}
		}},
	{Name: "mixed", Title: "Mixed (kitchen sink)", Description: "Every family at once: strings, hashes, lists, sets, sorted sets, streams, HyperLogLog, geo, bitmaps, scripts, transactions and pub/sub.",
		apply: func(c *Config) {
			c.Dataset.KeyCount, c.Dataset.KeyDistribution, c.Dataset.ZipfS, c.Dataset.CollectionSize = 20000, "zipf", 1.1, 50
			hashTags(c)
			c.Workload.PubSub = PubSub{Subscribers: 2, Channels: 16}
			c.Workload.Mix = []MixItem{
				{Op: "GET", Weight: 25}, {Op: "SET", Weight: 8}, {Op: "HSET", Weight: 5, Fields: 4}, {Op: "HGET", Weight: 8},
				{Op: "LPUSH", Weight: 4}, {Op: "RPOP", Weight: 4}, {Op: "SADD", Weight: 4}, {Op: "SISMEMBER", Weight: 5},
				{Op: "ZADD", Weight: 4}, {Op: "ZRANGE", Weight: 4, Count: 10}, {Op: "XADD", Weight: 4}, {Op: "XCONSUME", Weight: 3},
				{Op: "PFADD", Weight: 2}, {Op: "GEOADD", Weight: 2}, {Op: "GEOSEARCH", Weight: 2}, {Op: "SETBIT", Weight: 2},
				{Op: "EVAL", Weight: 3, Script: "rate_limit", Args: []string{"60", "100000"}},
				{Op: "TXN", Weight: 3, Steps: []MixItem{{Op: "GET"}, {Op: "SET"}}}, {Op: "WATCH_INCR", Weight: 1}, {Op: "PUBLISH", Weight: 3},
			}
		}},
}

// Presets lists the built-in presets in display order.
func Presets() []Preset { return append([]Preset(nil), presets...) }

// PresetNames lists the preset names.
func PresetNames() []string {
	out := make([]string, len(presets))
	for i, p := range presets {
		out[i] = p.Name
	}
	return out
}

// FindPreset looks a preset up by name (any case).
func FindPreset(name string) (Preset, bool) {
	for _, p := range presets {
		if strings.EqualFold(p.Name, strings.TrimSpace(name)) {
			return p, true
		}
	}
	return Preset{}, false
}

// WithPreset returns the defaults with a preset applied.
func WithPreset(name string) (Config, error) {
	c := Default()
	p, ok := FindPreset(name)
	if !ok {
		return Config{}, unknownPreset(name)
	}
	p.Apply(&c)
	return c, nil
}

func unknownPreset(name string) error {
	return fmt.Errorf("config: workload.preset: unknown preset %q (one of %s)", name, strings.Join(PresetNames(), ", "))
}

// presetOf reads workload.preset from YAML without being strict about the rest.
func presetOf(b []byte) (string, error) {
	var probe struct {
		Workload struct {
			Preset string `yaml:"preset"`
		} `yaml:"workload"`
	}
	if err := yaml.NewDecoder(bytes.NewReader(b)).Decode(&probe); err != nil && !errors.Is(err, io.EOF) {
		return "", nil // the strict decode reports the real problem
	}
	return probe.Workload.Preset, nil
}

// MatchPattern returns the SCAN pattern for the key-space analysis: the configured one,
// or every key under the dataset's prefix.
func (k KeySpace) MatchPattern(d Dataset) string {
	if strings.TrimSpace(k.Match) != "" {
		return k.Match
	}
	return d.KeyPrefix + "*"
}
