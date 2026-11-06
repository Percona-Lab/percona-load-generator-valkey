package config

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
)

// TLS configures an encrypted connection to the server. Only file paths live
// in the configuration, never key material.
type TLS struct {
	// Enabled turns TLS on. A valkeys:// (or rediss://) address turns it on too.
	Enabled bool `yaml:"enabled"`
	// CAFile is a PEM file of certificate authorities that sign the server's
	// certificate. Empty = the system's trusted authorities.
	CAFile string `yaml:"ca_file"`
	// CertFile and KeyFile are the client certificate and key (mutual TLS).
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	// ServerName overrides the name the server certificate is checked against
	// (default: the host of the address).
	ServerName string `yaml:"server_name"`
	// InsecureSkipVerify accepts any server certificate. It removes the
	// protection against a man in the middle: use it for throwaway tests only.
	InsecureSkipVerify bool `yaml:"insecure_skip_verify"`
}

// TLSEnabled reports whether the target is reached over TLS: tls.enabled, or
// any address that uses valkeys:// or rediss://.
func (t Target) TLSEnabled() bool {
	if t.TLS.Enabled {
		return true
	}
	for _, u := range t.URIs {
		u = strings.ToLower(strings.TrimSpace(u))
		if strings.HasPrefix(u, "valkeys://") || strings.HasPrefix(u, "rediss://") {
			return true
		}
	}
	return false
}

// TLSConfig builds the client's TLS settings, or nil when TLS is off. It reads
// the files, so a missing or malformed file is reported here with its path.
func (t Target) TLSConfig() (*tls.Config, error) {
	if !t.TLSEnabled() {
		return nil, nil
	}
	c := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: t.TLS.ServerName, InsecureSkipVerify: t.TLS.InsecureSkipVerify} //nolint:gosec // opt-in, documented
	if t.TLS.CAFile != "" {
		pem, err := os.ReadFile(t.TLS.CAFile)
		if err != nil {
			return nil, fmt.Errorf("target.tls.ca_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("target.tls.ca_file: %s holds no PEM certificate", t.TLS.CAFile)
		}
		c.RootCAs = pool
	}
	if t.TLS.CertFile != "" || t.TLS.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(t.TLS.CertFile, t.TLS.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("target.tls.cert_file / key_file: %w", err)
		}
		c.Certificates = []tls.Certificate{cert}
	}
	return c, nil
}

// validate checks the TLS settings that need no file access.
func (t Target) validateTLS(add func(string, ...any)) {
	tl := t.TLS
	if (tl.CertFile == "") != (tl.KeyFile == "") {
		add("target.tls: cert_file and key_file go together (mutual TLS needs both)")
	}
	if !t.TLSEnabled() {
		if tl.CAFile != "" || tl.CertFile != "" || tl.KeyFile != "" || tl.ServerName != "" || tl.InsecureSkipVerify {
			add("target.tls: files or options are set but TLS is off; set target.tls.enabled: true or use a valkeys:// address")
		}
		return
	}
	if tl.InsecureSkipVerify && tl.CAFile != "" {
		add("target.tls: insecure_skip_verify makes ca_file pointless; use one or the other")
	}
}

// CheckTLSFiles reads the TLS files once, so a run fails at the start with a
// clear message rather than at the first connection.
func (t Target) CheckTLSFiles() error {
	_, err := t.TLSConfig()
	return err
}

// ResolveSecrets reads PLGV_USERNAME_FILE and PLGV_PASSWORD_FILE (a mounted
// secret, for example a Kubernetes Secret) into the target credentials. A file
// wins over the plain variable. One trailing newline is removed.
func (c *Config) ResolveSecrets(getenv func(string) string) error {
	for _, s := range []struct {
		env string
		dst *string
	}{{"PLGV_USERNAME_FILE", &c.Target.Username}, {"PLGV_PASSWORD_FILE", &c.Target.Password}} {
		path := getenv(s.env)
		if path == "" {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("%s: %w", s.env, err)
		}
		*s.dst = strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
	}
	return nil
}
