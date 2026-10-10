// SPDX-License-Identifier: Apache-2.0

// Command polipan-server is the Polipan control plane.
//
// Usage:
//
//	polipan-server [-config file]              run the server (default)
//	polipan-server [-config file] migrate      apply the migrations and exit
//	polipan-server [-config file] healthcheck  call /healthz of a running server (for container health checks)
//	polipan-server [-config file] join-token --name <node> [--label k=v]...  make a node join token
//	polipan-server [-config file] ca-fingerprint  print the CA fingerprint
//	polipan-server [-config file] setup-url   print the one-time setup URL for the first user
//	polipan-server [-config file] admin rotate-kek            make a new master key and re-wrap all data keys
//	polipan-server [-config file] admin rotate-dek --project <id or slug>  re-encrypt the secrets of a project
//	polipan-server [-config file] admin backup-password --policy <id>  print the restic password and the repositories of a policy
//	polipan-server [-config file] admin reset-password <email>  print a one-hour link to set a new password
//	polipan-server [-config file] admin disable-2fa <email>  turn two-factor login off for a user
//	polipan-server [-config file] admin audit-verify         check the audit hash chain
//	polipan-server version                    print the version
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/polipan/polipan/server/internal/admin"
	"github.com/polipan/polipan/server/internal/backup"
	"github.com/polipan/polipan/server/internal/config"
	"github.com/polipan/polipan/server/internal/control"
	"github.com/polipan/polipan/server/internal/db"
	"github.com/polipan/polipan/server/internal/logging"
	"github.com/polipan/polipan/server/internal/pki"
	"github.com/polipan/polipan/server/internal/secrets"
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
	case "join-token":
		return control.JoinTokenCommand(ctx, cfg, flag.Args()[1:], os.Stdout)
	case "setup-url":
		kek, err := pki.LoadKEK(cfg.MasterKeyFile, os.Getenv)
		if err != nil {
			return err
		}
		fmt.Printf("%s/setup?token=%s\n", strings.TrimRight(cfg.ExternalURL, "/"), pki.SetupToken(kek))
		return nil
	case "ca-fingerprint":
		return control.CAFingerprintCommand(ctx, cfg, os.Stdout)
	case "admin":
		switch flag.Arg(1) {
		case "rotate-kek":
			return secrets.RotateKEKCommand(ctx, cfg, os.Getenv, os.Stdout)
		case "rotate-dek":
			return secrets.RotateDEKCommand(ctx, cfg, flag.Args()[2:], os.Getenv, os.Stdout)
		case "backup-password":
			return backup.PasswordCommand(ctx, cfg, flag.Args()[2:], os.Getenv, os.Stdout)
		case "reset-password":
			return admin.ResetPassword(ctx, cfg, flag.Args()[2:], os.Stdout)
		case "disable-2fa":
			return admin.DisableTOTP(ctx, cfg, flag.Args()[2:], os.Stdout)
		case "audit-verify":
			return admin.AuditVerify(ctx, cfg, os.Stdout)
		}
		return fmt.Errorf("unknown admin command %q (rotate-kek, rotate-dek, backup-password, reset-password, disable-2fa, audit-verify)", flag.Arg(1))
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
