package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writePKI writes a CA, and a client certificate and key it signed, into dir.
func writePKI(t *testing.T, dir string) (ca, cert, key string) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, _ := x509.ParseCertificate(caDER)
	clKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	clTmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "client"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	clDER, err := x509.CreateCertificate(rand.Reader, clTmpl, caCert, &clKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(clKey)
	w := func(name, typ string, der []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	return w("ca.pem", "CERTIFICATE", caDER), w("client.pem", "CERTIFICATE", clDER), w("client.key", "EC PRIVATE KEY", keyDER)
}

func TestTLSIsOffByDefault(t *testing.T) {
	c := Default()
	if c.Target.TLSEnabled() || c.Target.TLS.Enabled || c.Target.TLS.InsecureSkipVerify {
		t.Fatalf("%+v", c.Target.TLS)
	}
	if cfg, err := c.Target.TLSConfig(); cfg != nil || err != nil {
		t.Fatalf("no TLS config without TLS: %v %v", cfg, err)
	}
}

func TestSecureSchemeTurnsTLSOn(t *testing.T) {
	for _, u := range []string{"valkeys://h:6379", "rediss://h:6379", " VALKEYS://h:6379"} {
		c := Default()
		c.Target.URIs = []string{u}
		if !c.Target.TLSEnabled() {
			t.Errorf("%q must enable TLS", u)
		}
		if err := c.Validate(); err != nil {
			t.Errorf("%q: %v", u, err)
		}
		if a, _ := c.Target.Addrs(); len(a) != 1 || a[0] != "h:6379" {
			t.Errorf("%q: addrs %v", u, a)
		}
	}
	c := Default()
	c.Target.URIs = []string{"http://h:6379"}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "valkeys://") {
		t.Fatalf("err = %v", err)
	}
}

func TestTLSValidation(t *testing.T) {
	cases := map[string]func(*Config){
		"cert_file and key_file go together": func(c *Config) { c.Target.TLS = TLS{Enabled: true, CertFile: "c.pem"} },
		"TLS is off":                         func(c *Config) { c.Target.TLS = TLS{CAFile: "ca.pem"} },
		"ca_file pointless":                  func(c *Config) { c.Target.TLS = TLS{Enabled: true, InsecureSkipVerify: true, CAFile: "ca.pem"} },
	}
	for want, edit := range cases {
		c := Default()
		edit(&c)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", want, err)
		}
	}
	c := Default()
	c.Target.TLS = TLS{Enabled: true, CertFile: "c", KeyFile: "k", CAFile: "ca", ServerName: "x"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestTLSConfigReadsTheFiles(t *testing.T) {
	ca, cert, key := writePKI(t, t.TempDir())
	tg := Target{TLS: TLS{Enabled: true, CAFile: ca, CertFile: cert, KeyFile: key, ServerName: "valkey.example"}}
	cfg, err := tg.TLSConfig()
	if err != nil || cfg == nil {
		t.Fatalf("%v", err)
	}
	if cfg.RootCAs == nil || len(cfg.Certificates) != 1 || cfg.ServerName != "valkey.example" || cfg.InsecureSkipVerify || cfg.MinVersion < 0x0303 {
		t.Fatalf("%+v", cfg)
	}
	if _, err := (Target{TLS: TLS{Enabled: true, InsecureSkipVerify: true}}).TLSConfig(); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "junk.pem")
	_ = os.WriteFile(bad, []byte("not a certificate"), 0o600)
	for name, tl := range map[string]TLS{
		"missing CA":  {Enabled: true, CAFile: filepath.Join(t.TempDir(), "nope.pem")},
		"junk CA":     {Enabled: true, CAFile: bad},
		"junk pair":   {Enabled: true, CertFile: bad, KeyFile: bad},
		"cert no key": {Enabled: true, CertFile: cert, KeyFile: filepath.Join(t.TempDir(), "nokey")},
	} {
		if _, err := (Target{TLS: tl}).TLSConfig(); err == nil || !strings.Contains(err.Error(), "target.tls") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestTLSFromEnvironmentAndFingerprint(t *testing.T) {
	env := map[string]string{"PLGV_TLS": "true", "PLGV_TLS_CA_FILE": "/ca", "PLGV_TLS_CERT_FILE": "/c", "PLGV_TLS_KEY_FILE": "/k", "PLGV_TLS_SERVER_NAME": "n"}
	c := Default()
	c.ApplyEnv(func(k string) string { return env[k] })
	if (c.Target.TLS != TLS{Enabled: true, CAFile: "/ca", CertFile: "/c", KeyFile: "/k", ServerName: "n"}) {
		t.Fatalf("%+v", c.Target.TLS)
	}
	if c.Fingerprint() != Default().Fingerprint() {
		t.Fatal("TLS settings are where the run connects, not what it does: not part of the workload fingerprint")
	}
	// The YAML form has paths only.
	y, _ := c.Marshal()
	if !strings.Contains(string(y), "ca_file: /ca") || strings.Contains(string(y), "BEGIN") {
		t.Fatalf("%s", y)
	}
	if _, err := Parse([]byte("target:\n  tls:\n    enabled: true\n    ca_fil: x\n")); err == nil {
		t.Fatal("a misspelled TLS key must be rejected")
	}
}

func TestResolveSecretsFromFiles(t *testing.T) {
	dir := t.TempDir()
	pw := filepath.Join(dir, "pw")
	user := filepath.Join(dir, "user")
	_ = os.WriteFile(pw, []byte("s3cret\n"), 0o600)
	_ = os.WriteFile(user, []byte("bench\r\n"), 0o600)
	env := map[string]string{"PLGV_PASSWORD": "from-env", "PLGV_PASSWORD_FILE": pw, "PLGV_USERNAME_FILE": user}
	c := Default()
	if err := c.ApplyEnvStrict(func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	if c.Target.Password != "s3cret" || c.Target.Username != "bench" {
		t.Fatalf("a mounted secret wins and loses its newline: %q %q", c.Target.Password, c.Target.Username)
	}
	env["PLGV_PASSWORD_FILE"] = filepath.Join(dir, "missing")
	c = Default()
	if err := c.ApplyEnvStrict(func(k string) string { return env[k] }); err == nil || !strings.Contains(err.Error(), "PLGV_PASSWORD_FILE") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(c.Target.Password, "s3cret") {
		t.Fatal("nothing read")
	}
}

func TestCredentialsNeverInTheSavedConfig(t *testing.T) {
	c := Default()
	c.Target.Username, c.Target.Password = "bench", "hunter2-password"
	y, _ := c.Marshal()
	j, _ := c.ToMap()
	if strings.Contains(string(y), "hunter2") || strings.Contains(string(y), "bench") || strings.Contains(strings.ToLower(jsonOf(j)), "hunter2") {
		t.Fatalf("credentials leaked:\n%s", y)
	}
}

func jsonOf(m map[string]any) string {
	b, _ := json.Marshal(m)
	return string(b)
}
