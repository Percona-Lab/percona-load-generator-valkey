package main

import (
	"flag"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
)

// tlsFlags are the connection-security flags shared by run, replay and import-traffic.
type tlsFlags struct {
	on                  bool
	ca, cert, key, name string
	insecure            bool
}

func (t *tlsFlags) bind(fs *flag.FlagSet) {
	fs.BoolVar(&t.on, "tls", false, "connect over TLS (also implied by a valkeys:// address); see docs/security-tls-acl.md")
	fs.StringVar(&t.ca, "tls-ca", "", "PEM file of the certificate authorities that sign the server's certificate (default: the system's)")
	fs.StringVar(&t.cert, "tls-cert", "", "client certificate (PEM) for mutual TLS; needs -tls-key")
	fs.StringVar(&t.key, "tls-key", "", "client private key (PEM) for mutual TLS")
	fs.StringVar(&t.name, "tls-server-name", "", "check the server certificate against this name instead of the address's host")
	fs.BoolVar(&t.insecure, "tls-insecure", false, "do not verify the server certificate (throwaway tests only: it removes the protection against a man in the middle)")
}

// apply copies the flags the user set onto the configuration.
func (t *tlsFlags) apply(fs *flag.FlagSet, c *config.Config) {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["tls"] {
		c.Target.TLS.Enabled = t.on
	}
	if set["tls-ca"] {
		c.Target.TLS.CAFile = t.ca
	}
	if set["tls-cert"] {
		c.Target.TLS.CertFile = t.cert
	}
	if set["tls-key"] {
		c.Target.TLS.KeyFile = t.key
	}
	if set["tls-server-name"] {
		c.Target.TLS.ServerName = t.name
	}
	if set["tls-insecure"] {
		c.Target.TLS.InsecureSkipVerify = t.insecure
	}
	// A TLS file or option implies TLS, so `-tls-ca ca.pem` alone does what it says.
	if !set["tls"] && (t.ca != "" || t.cert != "" || t.key != "" || t.name != "" || t.insecure) {
		c.Target.TLS.Enabled = true
	}
}
