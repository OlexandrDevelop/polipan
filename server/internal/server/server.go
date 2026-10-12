// SPDX-License-Identifier: Apache-2.0

// Package server wires the parts together and runs the HTTP server and the job workers.
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/polipan/polipan/server/internal/alert"
	"github.com/polipan/polipan/server/internal/api"
	"github.com/polipan/polipan/server/internal/audit"
	"github.com/polipan/polipan/server/internal/backup"
	"github.com/polipan/polipan/server/internal/build"
	"github.com/polipan/polipan/server/internal/config"
	"github.com/polipan/polipan/server/internal/control"
	"github.com/polipan/polipan/server/internal/db"
	"github.com/polipan/polipan/server/internal/gateway"
	"github.com/polipan/polipan/server/internal/git"
	"github.com/polipan/polipan/server/internal/obs"
	"github.com/polipan/polipan/server/internal/pki"
	"github.com/polipan/polipan/server/internal/preview"
	"github.com/polipan/polipan/server/internal/queue"
	"github.com/polipan/polipan/server/internal/secrets"
	"github.com/polipan/polipan/server/internal/shared/clock"
	"github.com/polipan/polipan/server/internal/store"
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
	ctl := control.New(control.Deps{DB: pool, Clock: clk, Log: log, Config: cfg, CA: ca, Sender: gw, Secrets: sec, Build: builds,
		DiskReserveBytes: cfg.DiskReserveBytes})
	// M8: the metrics and log stores, the ingest of the agents and the alert engine. Without the store URLs
	// monitoring is off: the canary gate checks health only and metric rules do not run.
	var backends *obs.Backends
	if cfg.VictoriaMetricsURL != "" || cfg.VictoriaLogsURL != "" {
		backends = obs.NewBackends(cfg.VictoriaMetricsURL, cfg.VictoriaLogsURL, cfg.VictoriaUser, cfg.VictoriaPassword)
		ctl.Gates = backends
	}
	alerts := &alert.Engine{
		DB: pool, Store: &alert.Store{DB: pool, Secrets: sec}, Clock: clk, Log: log, Backends: backends,
		ExternalURL: cfg.ExternalURL, RepeatDefault: cfg.AlertRepeatInterval, TelegramAPI: cfg.TelegramAPI,
		Client: func(ctx context.Context, orgID string) *http.Client {
			return ctl.SSRFPolicy(ctx, orgID).Client(20 * time.Second)
		},
		Dialer: func(ctx context.Context, orgID string) *net.Dialer {
			return ctl.SSRFPolicy(ctx, orgID).Dialer(15 * time.Second)
		},
	}
	gw.State = ctl
	gw.Health = ctl
	gw.Joined = ctl.NodeJoined

	bk := backup.New(backup.Deps{DB: pool, Secrets: sec, Sender: gw, Orch: ctl, Clock: clk, Log: log, SSRF: ctl.SSRFPolicy})
	ctl.Mover = bk
	// M9: the preview environments. They post the commit status and the comment through the Git connection of the service.
	st := store.New(pool)
	gitDeps := &api.GitDeps{Keys: builds.Keys, Policy: ctl.SSRFPolicy}
	previews := preview.New(preview.Deps{Pool: pool, Store: st, Orch: ctl, Secrets: sec, Clock: clk, Log: log, ExternalURL: cfg.ExternalURL,
		Provider: func(ctx context.Context, orgID, connectionID string) (git.Provider, error) {
			c, err := st.GetGitConnection(ctx, orgID, connectionID)
			if err != nil {
				return nil, err
			}
			return gitDeps.ProviderOf(ctx, c)
		}})
	gitDeps.Previews = previews
	handler, err := api.NewRouter(api.Deps{
		Log: log, DB: pool, Pool: pool, Clock: clk, RateLimit: cfg.APIRateLimit, Web: web.Handler(),
		Orchestrator: ctl, ExternalURL: cfg.ExternalURL, TrustedProxies: cfg.TrustedProxies, AgentListen: cfg.AgentListen,
		Secrets: sec, Exec: gw, Backup: bk, KEK: kek, Obs: backends, Alerts: alerts, Monitoring: api.MonitoringSettings{
			MetricsRetentionDays: cfg.StorageMetricsRetentionDays, LogsRetentionDays: cfg.StorageLogsRetentionDays},
		Git:        gitDeps,
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
	alertDone := make(chan struct{})
	go func() {
		defer close(alertDone)
		alerts.Run(runCtx)
	}()
	previewDone := make(chan struct{})
	go func() {
		defer close(previewDone)
		previews.Run(runCtx)
	}()
	// The BuildKit cache is pruned by policy; a high BuildKit disk starts it sooner (12-observability section 7.1).
	if bkit, ok := builds.Builder.(*build.BuildKit); ok {
		cc := &build.CacheCleaner{BuildKit: bkit, KeepFor: cfg.BuildCacheKeep, MaxMB: cfg.BuildCacheMaxMB, Log: log}
		if backends != nil {
			cc.High = func(ctx context.Context) bool {
				vs, err := backends.Query(ctx, `max(polipan_node_fs_used_bytes{roles=~".*buildkit.*"} / (polipan_node_fs_used_bytes{roles=~".*buildkit.*"} + polipan_node_fs_avail_bytes{roles=~".*buildkit.*"}) * 100)`, time.Time{})
				return err == nil && len(vs) > 0 && vs[0].Value >= 85
			}
		}
		go cc.Run(runCtx)
	}
	var ingestSrv *http.Server
	if backends != nil && cfg.IngestListen != "" {
		ingest := &obs.Ingest{DB: pool, Backends: backends, Log: log, Clock: clk}
		ingestSrv = &http.Server{Addr: cfg.IngestListen, Handler: ingest.Handler(), TLSConfig: gw.IngestTLSConfig(),
			ReadHeaderTimeout: 10 * time.Second, ReadTimeout: time.Minute, WriteTimeout: time.Minute}
		go func() {
			l, err := tls.Listen("tcp", cfg.IngestListen, ingestSrv.TLSConfig)
			if err != nil {
				log.Error("ingest listener", "error", err)
				return
			}
			if err := ingestSrv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("ingest stopped", "error", err)
			}
		}()
	}
	defer func() {
		cancelRun()
		if ingestSrv != nil {
			_ = ingestSrv.Close()
		}
		<-alertDone
		<-previewDone
		<-gwDone
		<-ctlDone
		<-bkDone
		<-auditDone
	}()

	host, _ := os.Hostname()
	jobs := queue.New(pool, clk)
	jobs.Backoffs = map[string][]time.Duration{alert.JobKind: alert.DeliveryBackoffs}
	worker := &queue.Worker{
		Queue:    jobs,
		ID:       fmt.Sprintf("%s-%d", host, os.Getpid()),
		Handlers: map[string]queue.Handler{alert.JobKind: alerts.Handler()},
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
