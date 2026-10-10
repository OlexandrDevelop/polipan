// SPDX-License-Identifier: Apache-2.0

// Package server wires the parts together and runs the HTTP server and the job workers.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/polipan/polipan/server/internal/api"
	"github.com/polipan/polipan/server/internal/audit"
	"github.com/polipan/polipan/server/internal/backup"
	"github.com/polipan/polipan/server/internal/config"
	"github.com/polipan/polipan/server/internal/control"
	"github.com/polipan/polipan/server/internal/db"
	"github.com/polipan/polipan/server/internal/gateway"
	"github.com/polipan/polipan/server/internal/pki"
	"github.com/polipan/polipan/server/internal/queue"
	"github.com/polipan/polipan/server/internal/secrets"
	"github.com/polipan/polipan/server/internal/shared/clock"
	"github.com/polipan/polipan/server/internal/web"
)

// Run starts the server and blocks until ctx ends.
// It applies the migrations first, then serves HTTP and runs the job worker.
func Run(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	applied, err := db.Migrate(ctx, pool)
	if err != nil {
		return err
	}
	log.Info("migrations done", "applied", applied)

	clk := clock.Real{}
	kek, err := pki.LoadKEK(cfg.MasterKeyFile, os.Getenv)
	if err != nil {
		return err
	}
	ca, err := pki.Load(ctx, pool, kek)
	if err != nil {
		return err
	}
	// The gateway sends commands for the control side, and the control side gives the
	// desired state to the gateway, so the gateway is created first and gets its state later.
	gw, err := gateway.New(gateway.Deps{DB: pool, CA: ca, Config: cfg, Clock: clk, Log: log})
	if err != nil {
		return fmt.Errorf("server: %w", err)
	}
	sec, err := secrets.NewManager(pool, kek)
	if err != nil {
		return err
	}
	builds := control.SetupBuilds(ctx, pool, ca, kek, os.Getenv, log) // git builds and registry (S-B)
	ctl := control.New(control.Deps{DB: pool, Clock: clk, Log: log, Config: cfg, CA: ca, Sender: gw, Secrets: sec, Build: builds})
	gw.State = ctl
	gw.Health = ctl

	bk := backup.New(backup.Deps{DB: pool, Secrets: sec, Sender: gw, Orch: ctl, Clock: clk, Log: log, SSRF: ctl.SSRFPolicy})
	handler, err := api.NewRouter(api.Deps{
		Log: log, DB: pool, Pool: pool, Clock: clk, RateLimit: cfg.APIRateLimit, Web: web.Handler(),
		Orchestrator: ctl, ExternalURL: cfg.ExternalURL, TrustedProxies: cfg.TrustedProxies, AgentListen: cfg.AgentListen,
		Secrets: sec, Exec: gw, Backup: bk, KEK: kek,
		Git:        &api.GitDeps{Keys: builds.Keys, Policy: ctl.SSRFPolicy},
		SetupToken: pki.SetupToken(kek), SessionTTL: cfg.SessionTTL, SessionAbsoluteTTL: cfg.SessionAbsoluteTTL,
	})
	if err != nil {
		return fmt.Errorf("server: %w", err)
	}

	gwListener, err := net.Listen("tcp", cfg.AgentListen)
	if err != nil {
		return fmt.Errorf("server: agent gateway: %w", err)
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	gwDone := make(chan struct{})
	go func() {
		defer close(gwDone)
		if err := gw.Serve(runCtx, gwListener); err != nil {
			log.Error("agent gateway stopped", "error", err)
		}
	}()
	bkDone := make(chan struct{})
	go func() {
		defer close(bkDone)
		if err := bk.Run(runCtx); err != nil {
			log.Error("backup service stopped", "error", err)
		}
	}()
	auditDone := make(chan struct{})
	go func() {
		defer close(auditDone)
		audit.Run(runCtx, pool, log, audit.ArchiveKey(kek))
	}()
	ctlDone := make(chan struct{})
	go func() {
		defer close(ctlDone)
		if err := ctl.Run(runCtx); err != nil {
			log.Error("control stopped", "error", err)
		}
	}()
	defer func() {
		cancelRun()
		<-gwDone
		<-ctlDone
		<-bkDone
		<-auditDone
	}()

	host, _ := os.Hostname()
	worker := &queue.Worker{
		Queue:    queue.New(pool, clk),
		ID:       fmt.Sprintf("%s-%d", host, os.Getpid()),
		Handlers: map[string]queue.Handler{}, // job kinds are added by later milestones
		Interval: time.Second,
		Log:      log,
	}
	workerCtx, stopWorker := context.WithCancel(ctx)
	defer stopWorker()
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		worker.Run(workerCtx)
	}()

	srv := api.NewHTTPServer(cfg.Listen, handler)
	errCh := make(chan error, 1)
	go func() {
		log.Info("server listening", "addr", cfg.Listen)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		stopWorker()
		<-workerDone
		return err
	case <-ctx.Done():
	}
	log.Info("server stopping")
	shutCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	err = srv.Shutdown(shutCtx)
	stopWorker()
	<-workerDone
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
