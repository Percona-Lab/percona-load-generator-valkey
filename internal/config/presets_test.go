package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestEveryPresetIsValidStandaloneAndCluster(t *testing.T) {
	if len(Presets()) != 9 {
		t.Fatalf("want the 9 presets of the plan, got %v", PresetNames())
	}
	for _, p := range Presets() {
		t.Run(p.Name, func(t *testing.T) {
			if p.Title == "" || p.Description == "" {
				t.Fatal("a preset needs a title and a description")
			}
			c, err := WithPreset(p.Name)
			if err != nil {
				t.Fatal(err)
			}
			c.Normalize()
			if c.Workload.Preset != p.Name || len(c.Workload.Mix) == 0 {
				t.Fatalf("preset not applied: %+v", c.Workload)
			}
			if err := c.Validate(); err != nil {
				t.Fatalf("standalone: %v", err)
			}
			c.Target.Mode = "cluster"
			c.Target.URIs = []string{"127.0.0.1:7001"}
			if err := c.Validate(); err != nil {
				t.Fatalf("cluster: %v", err)
			}
		})
	}
}

func TestPresetFromYAMLUnderTheUsersOwnSettings(t *testing.T) {
	c, err := Parse([]byte("workload:\n  preset: Cache\nrun:\n  duration: 5s\n"))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := WithPreset("cache")
	if c.Workload.Preset != "cache" || !reflect.DeepEqual(c.Workload.Mix, want.Workload.Mix) || c.Dataset.KeyDistribution != "zipf" {
		t.Fatalf("preset not used: %+v", c.Workload)
	}
	// Anything the file sets wins over the preset.
	c, err = Parse([]byte("workload:\n  preset: cache\n  mix: [{op: GET, weight: 1}]\ndataset:\n  value_size: 64\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Workload.Mix) != 1 || c.Dataset.ValueSize != 64 || c.Dataset.KeyDistribution != "zipf" {
		t.Fatalf("user settings should override only what they set: %+v %+v", c.Workload.Mix, c.Dataset)
	}
}

func TestPresetFlagAndErrors(t *testing.T) {
	c, err := ParseWithPreset([]byte("run:\n  duration: 3s\n"), "session")
	if err != nil || c.Workload.Preset != "session" || c.Run.Duration.Seconds() != 3 {
		t.Fatalf("%v %+v", err, c.Workload)
	}
	for _, bad := range []string{"workload:\n  preset: nope\n"} {
		if _, err := Parse([]byte(bad)); err == nil || !strings.Contains(err.Error(), "unknown preset") || !strings.Contains(err.Error(), "leaderboard") {
			t.Fatalf("want an error listing the presets, got %v", err)
		}
	}
	if _, err := ParseWithPreset(nil, "nope"); err == nil {
		t.Fatal("unknown flag value must fail")
	}
	d := Default()
	d.Workload.Preset = "nope"
	if err := d.Validate(); err == nil || !strings.Contains(err.Error(), "workload.preset") {
		t.Fatalf("Validate should name the field: %v", err)
	}
}

func TestPresetRoundTripsThroughTheUIMapForm(t *testing.T) {
	c, _ := WithPreset("leaderboard")
	m, err := c.ToMap()
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseMap(m)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.Workload, c.Workload) || back.Dataset != c.Dataset {
		t.Fatal("round trip changed the preset workload")
	}
}

func TestDefaultsHaveNoPreset(t *testing.T) {
	if Default().Workload.Preset != "" {
		t.Fatal("presets are opt-in")
	}
}
