package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// ToMap returns the configuration as nested maps shaped exactly like the YAML
// (durations as strings such as "10s"), ready to send as JSON.
func (c Config) ToMap() (map[string]any, error) {
	b, err := c.Marshal()
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// ParseMap builds a configuration from nested maps (decoded JSON) on top of the
// defaults, with the same strict rules as YAML files. Decode JSON with
// UseNumber so integers stay integers.
func ParseMap(m map[string]any) (Config, error) {
	b, err := yaml.Marshal(fixNumbers(m))
	if err != nil {
		return Config{}, err
	}
	return Parse(b)
}

// DecodeJSONMap decodes a JSON object keeping integers exact.
func DecodeJSONMap(raw []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

func fixNumbers(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = fixNumbers(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = fixNumbers(e)
		}
		return out
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		f, _ := x.Float64()
		return f
	}
	return v
}

// Fingerprint identifies the workload shape of a run: two runs with the same
// fingerprint are comparable. The server address, duration, seed, output and
// server-metrics settings are not part of it, so the same workload can be
// compared across servers and across runs of different length.
func (c Config) Fingerprint() string {
	c.Target.URIs = nil
	c.Target.TLS = TLS{}
	c.Run.Duration = 0
	c.Run.Seed = 0
	c.Run.OpBudget, c.Run.Deterministic = 0, false
	c.Run.Sweep = Sweep{Kind: "none", Values: []float64{}}
	c.Output = Output{}
	c.Server = ServerMetrics{}
	c.KeySpace = KeySpace{}
	c.Record = Record{}
	c.Target.Username, c.Target.Password = "", ""
	b, err := c.Marshal()
	if err != nil {
		return fmt.Sprintf("unhashable-%v", err)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])[:12]
}
