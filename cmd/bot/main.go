// SPDX-License-Identifier: Apache-2.0

// Command bot is Quoto: a Telegram bot that quietly collects a group's day and
// posts its best line. It wires configuration, the database, the AI clients, the
// scheduler and the Telegram update loop, and shuts them down in order on
// SIGINT or SIGTERM.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/FreshLabDev/quoto/internal/buildinfo"
	"github.com/FreshLabDev/quoto/internal/config"
	"github.com/FreshLabDev/quoto/internal/db"
	"github.com/FreshLabDev/quoto/internal/health"
	qlog "github.com/FreshLabDev/quoto/internal/log"
	"github.com/FreshLabDev/quoto/migrations"
)

// shutdownGrace is how long in-flight work gets after a signal. The compose
// file's stop_grace_period is longer.
const shutdownGrace = 25 * time.Second

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "quoto:", err)
		os.Exit(2)
	}
	log := qlog.New(os.Stdout, cfg.LogLevel, qlog.NewRedactor(cfg.Secrets()))
	if err := run(cfg, log); err != nil {
		log.Error("exit", "error", err)
		os.Exit(1)
	}
}

func run(cfg config.Config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Info("starting", "version", buildinfo.Version, "commit", buildinfo.Commit, "license", buildinfo.License)
	if len(cfg.Obsolete) > 0 {
		log.Warn("environment variables from the Python version are ignored; remove them", "names", cfg.Obsolete)
	}

	store, err := connect(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.Migrate(ctx, migrations.FS); err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.Handle("/healthz", health.New(
		health.Func("db", store.Ping),
	))
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Every long-running part is a function that returns when ctx is done. The
	// first one to fail cancels the others, and run waits for all of them.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	start := func(name string, fn func(ctx context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(runCtx); err != nil && !errors.Is(err, context.Canceled) {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("%s: %w", name, err)
				}
				mu.Unlock()
				cancel()
			}
		}()
	}

	start("http", func(ctx context.Context) error {
		go func() {
			<-ctx.Done()
			shutdown, done := context.WithTimeout(context.Background(), 5*time.Second)
			defer done()
			_ = srv.Shutdown(shutdown)
		}()
		log.Info("http listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})

	<-runCtx.Done()
	waited := make(chan struct{})
	go func() { wg.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(shutdownGrace):
		log.Warn("shutdown grace period over; exiting with work still running")
	}
	log.Info("stopped")
	mu.Lock()
	defer mu.Unlock()
	return firstErr
}

// connect retries the first connection. Container DNS on the shared network is
// not always resolvable the instant the process starts, and exiting there turns
// a two-second blip into a restart loop.
func connect(ctx context.Context, url string, log *slog.Logger) (*db.Store, error) {
	const attempts = 8
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		store, err := db.Connect(ctx, url, log)
		if err == nil {
			return store, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		delay := time.Duration(1<<min(attempt-1, 4)) * time.Second
		log.Warn("database connect failed; retrying", "attempt", attempt, "retry_in", delay, "error", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
	return nil, lastErr
}
