package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"runtime"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/webui"
)

// webCmd implements `plgv web`.
func webCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("web", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		addr, dataDir     string
		allowedHosts      string
		keep              int
		open, allowRemote bool
	)
	fs.StringVar(&addr, "addr", "127.0.0.1:8080", "listen address")
	fs.StringVar(&dataDir, "data-dir", "plgv-data", "directory for stored runs and saved settings")
	fs.IntVar(&keep, "keep-runs", 100, "number of finished runs to keep (0 = keep all)")
	fs.BoolVar(&open, "open", false, "open the UI in the default browser")
	fs.BoolVar(&allowRemote, "allow-remote", false, "allow listening on a non-loopback address (the UI has no login)")
	fs.StringVar(&allowedHosts, "allowed-host", "", "comma-separated host names the UI answers to (the Host header); use with -allow-remote, for example localhost,127.0.0.1 behind a port published on loopback only")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if !allowRemote && !isLoopback(addr) {
		fmt.Fprintf(stderr, "plgv web: %q is not a loopback address. The UI has no login and can run load against any server it can reach.\n"+
			"Use 127.0.0.1, or pass -allow-remote if you protect access another way.\n", addr)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	srv, err := webui.New(webui.Options{DataDir: dataDir, Version: version, KeepRuns: keep, Shutdown: stop, LoopbackOnly: !allowRemote, FreePaths: !allowRemote, AllowedHosts: splitAddrs(allowedHosts)})
	if err != nil {
		fmt.Fprintln(stderr, "plgv web:", err)
		return 1
	}
	url := "http://" + addr
	fmt.Fprintf(stdout, "plgv web UI: %s  (data in %s; Ctrl-C to stop)\n", url, dataDir)
	if open {
		openBrowser(url)
	}
	if err := srv.ListenAndServe(ctx, addr); err != nil {
		fmt.Fprintln(stderr, "plgv web:", err)
		return 1
	}
	return 0
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func openBrowser(url string) {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", url)
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		c = exec.Command("xdg-open", url)
	}
	_ = c.Start()
}
