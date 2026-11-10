package config

import (
	"strings"
	"testing"
)

func TestServerConfigIsOffByDefaultAndNotInTheFingerprint(t *testing.T) {
	c := Default()
	if c.SrvConf.Enabled || c.SrvConf.Name != "" {
		t.Fatalf("server_config must default off: %+v", c.SrvConf)
	}
	d := Default()
	d.SrvConf = ServerConfig{Enabled: true, Name: "valkey-9.1-io4"}
	if c.Fingerprint() != d.Fingerprint() {
		t.Error("server_config must not change the workload fingerprint")
	}
}

func TestServerConfigNameValidation(t *testing.T) {
	c := Default()
	c.SrvConf.Name = strings.Repeat("x", 81)
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "server_config.name") {
		t.Errorf("long name: %v", err)
	}
	c.SrvConf.Name = "bad\nname"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "control characters") {
		t.Errorf("control char: %v", err)
	}
	c.SrvConf.Name = "valkey-9.1-io4"
	if err := c.Validate(); err != nil {
		t.Errorf("good name: %v", err)
	}
}
