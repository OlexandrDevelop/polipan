// SPDX-License-Identifier: Apache-2.0

// Command polipan-server is the Polipan control plane.
//
// Usage:
//
//	polipan-server [-config file]              run the server (default)
//	polipan-server [-config file] migrate      apply the migrations and exit
//	polipan-server [-config file] healthcheck  call /healthz of a running server (for container health checks)
//	polipan-server version                     print the version
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/polipan/polipan/server/internal/config"
	"github.com/polipan/polipan/server/internal/db"
	"github.com/polipan/polipan/server/internal/logging"
	"github.com/polipan/polipan/server/internal/server"
	"github.com/polipan/polipan/server/internal/version"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "polipan-server:", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", config.DefaultPath, "path of the config file")
	flag.Parse()
	cmd := flag.Arg(0)

	if cmd == "version" {
		fmt.Printf("polipan-server %s (%s)\n", version.Version, version.Commit)
		return nil
	}

	cfg, err := config.Load(*configPath, os.Getenv)
	if err != nil {
		return err
	}
	log := logging.New(os.Stderr, cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "", "serve":
		log.Info("starting", "version", version.Version, "commit", version.Commit)
		return server.Run(ctx, cfg, log)
	case "migrate":
		pool, err := db.Open(ctx, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		defer pool.Close()
		applied, err := db.Migrate(ctx, pool)
		log.Info("migrations done", "applied", applied)
		return err
	case "healthcheck":
		return healthcheck(ctx, cfg.Listen)
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
}

// healthcheck calls /healthz on the local server. It fails when the answer is not 200.
func healthcheck(ctx context.Context, listen string) error {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	url := "http://" + net.JoinHostPort(host, port) + "/healthz"
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz answered %d", resp.StatusCode)
	}
	return nil
}
