// SPDX-License-Identifier: Apache-2.0

// Command polipan-agent runs on every node. This is the M0 skeleton: it starts,
// reads its config, builds the node info of the agent protocol and waits for a stop signal.
// The server connection, reconciler and the other loops come in M1.
//
// Usage:
//
//	polipan-agent [-server host:port] [-data-dir dir] [-log-level level]
//	polipan-agent version
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/polipan/polipan/agent/internal/config"
	"github.com/polipan/polipan/agent/internal/logging"
	"github.com/polipan/polipan/agent/internal/version"
	"github.com/polipan/polipan/proto/agentv1"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "polipan-agent:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg := config.FromEnv(os.Getenv)
	fs := flag.NewFlagSet("polipan-agent", flag.ContinueOnError)
	fs.StringVar(&cfg.Server, "server", cfg.Server, "address of the server gateway (host:port)")
	fs.StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "folder for the agent state")
	fs.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "debug, info, warn or error")
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

	log := logging.New(os.Stderr, cfg.LogLevel)
	info := nodeInfo()
	log.Info("agent started", "version", version.Version, "hostname", info.GetHostname(), "arch", info.GetArch())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	log.Info("agent stopped")
	return nil
}

// nodeInfo describes this node in the format of the agent protocol.
func nodeInfo() *agentv1.NodeInfo {
	host, _ := os.Hostname()
	return &agentv1.NodeInfo{
		Hostname:     host,
		Os:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		AgentVersion: version.Version,
	}
}
