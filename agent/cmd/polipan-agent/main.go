// SPDX-License-Identifier: Apache-2.0

// Command polipan-agent runs on every node.
//
// Usage:
//
//	polipan-agent [flags]                                  run the agent
//	polipan-agent join --server H:P --token T --ca-fingerprint FP [--data-dir D]
//	polipan-agent version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/polipan/polipan/agent/internal/config"
	"github.com/polipan/polipan/agent/internal/dns"
	"github.com/polipan/polipan/agent/internal/docker"
	"github.com/polipan/polipan/agent/internal/firewall"
	"github.com/polipan/polipan/agent/internal/ingress"
	"github.com/polipan/polipan/agent/internal/join"
	"github.com/polipan/polipan/agent/internal/logging"
	"github.com/polipan/polipan/agent/internal/reconcile"
	"github.com/polipan/polipan/agent/internal/state"
	"github.com/polipan/polipan/agent/internal/stream"
	"github.com/polipan/polipan/agent/internal/version"
	"github.com/polipan/polipan/proto/agentv1"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "polipan-agent:", err)
		os.Exit(1)
	}
}

func commonFlags(fs *flag.FlagSet, cfg *config.Config) {
	fs.StringVar(&cfg.Server, "server", cfg.Server, "address of the server gateway (host:port)")
	fs.StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "folder for the agent state")
	fs.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "debug, info, warn or error")
	fs.StringVar(&cfg.Token, "token", cfg.Token, "join token (used when the agent has no identity)")
	fs.StringVar(&cfg.CAFingerprint, "ca-fingerprint", cfg.CAFingerprint, "sha256 hex of the server CA certificate")
	fs.StringVar(&cfg.DockerSocket, "docker-socket", cfg.DockerSocket, "Docker unix socket")
}

func run(args []string) error {
	cfg := config.FromEnv(os.Getenv)
	if len(args) > 0 {
		switch args[0] {
		case "version":
			fmt.Printf("polipan-agent %s (%s)\n", version.Version, version.Commit)
			return nil
		case "join":
			fs := flag.NewFlagSet("join", flag.ContinueOnError)
			commonFlags(fs, &cfg)
			if err := fs.Parse(args[1:]); err != nil {
				return err
			}
			log := logging.New(os.Stderr, cfg.LogLevel)
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			nf, err := doJoin(ctx, cfg, log)
			if err != nil {
				return err
			}
			fmt.Printf("joined as %s\n", nf.NodeID)
			return nil
		}
	}
	fs := flag.NewFlagSet("polipan-agent", flag.ContinueOnError)
	commonFlags(fs, &cfg)
	fs.StringVar(&cfg.Network, "network", cfg.Network, "docker network of the workloads")
	fs.BoolVar(&cfg.Firewall, "firewall", cfg.Firewall, "manage nftables rules")
	fs.StringVar(&cfg.ACMECAURL, "acme-ca-url", cfg.ACMECAURL, "ACME directory URL override")
	fs.StringVar(&cfg.CaddyImage, "caddy-image", cfg.CaddyImage, "Caddy image")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.Arg(0) == "version" {
		fmt.Printf("polipan-agent %s (%s)\n", version.Version, version.Commit)
		return nil
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	return runAgent(cfg)
}

func newIngress(cfg config.Config, d docker.Client) *ingress.Manager {
	return &ingress.Manager{Config: ingress.Config{DataDir: cfg.DataDir, Image: cfg.CaddyImage,
		Network: cfg.Network, ACMECAURL: cfg.ACMECAURL, ACMETrustedRoots: cfg.ACMETrustedRoots}, Docker: d}
}

func doJoin(ctx context.Context, cfg config.Config, log *slog.Logger) (*join.NodeFile, error) {
	d := docker.New(cfg.DockerSocket)
	d.IPNetwork = cfg.Network
	dv, err := d.Ping(ctx)
	if err != nil {
		log.Warn("docker is not reachable", "error", err)
	}
	info := join.NodeInfo(ctx, dv, newIngress(cfg, d).Version(ctx), cfg.DataDir, nil)
	return join.Run(ctx, join.Options{Server: cfg.Server, Token: cfg.Token,
		CAFingerprint: cfg.CAFingerprint, DataDir: cfg.DataDir, Info: info})
}

func runAgent(cfg config.Config) error {
	log := logging.New(os.Stderr, cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}
	if cfg.Server == "" {
		return errors.New("server address is required (--server or POLIPAN_AGENT_SERVER)")
	}
	if !join.Exists(cfg.DataDir) {
		if cfg.Token == "" || cfg.CAFingerprint == "" {
			return errors.New("this node has not joined: give --token and --ca-fingerprint, or run `polipan-agent join`")
		}
		nf, err := doJoin(ctx, cfg, log)
		if err != nil {
			return err
		}
		log.Info("joined the server", "node_id", nf.NodeID)
	}

	d := docker.New(cfg.DockerSocket)
	d.IPNetwork = cfg.Network
	store, err := state.New(cfg.DataDir)
	if err != nil {
		return err
	}
	ing := newIngress(cfg, d)
	dnsSrv := &dns.Server{}
	fw := &firewall.Firewall{Enabled: cfg.Firewall, AgentPort: serverPort(cfg.Server), Log: log}

	eng := reconcile.New(d, store, cfg.Network, log)
	eng.Ingress, eng.DNS, eng.Firewall = ing, dnsSrv, fw
	dnsStarted := false
	eng.Infra = func(ctx context.Context) error {
		gw, err := d.EnsureNetwork(ctx, cfg.Network, cfg.Subnet)
		if err != nil {
			return err
		}
		if !dnsStarted {
			addr, err := dnsSrv.Start(ctx, gw)
			if err != nil {
				log.Warn("cannot start the DNS server", "error", err)
			} else {
				log.Info("DNS server started", "addr", addr)
				if host, _, _ := net.SplitHostPort(addr); host == gw {
					eng.DNSServers = []string{gw}
					fw.DNSAddr = gw
				}
			}
			dnsStarted = true
		}
		return ing.Start(ctx)
	}
	if err := eng.Load(); err != nil {
		log.Error("cannot load the saved state", "error", err)
	}

	sc := &stream.Client{Server: cfg.Server, DataDir: cfg.DataDir, Handler: eng, Docker: d, Log: log,
		NodeInfo: func(ctx context.Context) *agentv1.NodeInfo {
			dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			dv, _ := d.Ping(dctx)
			return join.NodeInfo(ctx, dv, ing.Version(dctx), cfg.DataDir, nil)
		}}
	eng.Sink = sc
	log.Info("agent started", "version", version.Version)
	go eng.Run(ctx)
	sc.Run(ctx)
	log.Info("agent stopped")
	return nil
}

func serverPort(addr string) int {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 9443
	}
	n, _ := strconv.Atoi(p)
	return n
}
