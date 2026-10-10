// SPDX-License-Identifier: Apache-2.0

// Package server wires the parts together and runs the HTTP server and the job workers.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/polipan/polipan/server/internal/api"
	"github.com/polipan/polipan/server/internal/config"
	"github.com/polipan/polipan/server/internal/db"
	"github.com/polipan/polipan/server/internal/queue"
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
	handler, err := api.NewRouter(api.Deps{
		Log: log, DB: pool, Clock: clk, RateLimit: cfg.APIRateLimit, Web: web.Handler(),
	})
	if err != nil {
		return fmt.Errorf("server: %w", err)
	}

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

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
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
