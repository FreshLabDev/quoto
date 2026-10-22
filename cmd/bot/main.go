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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/quoto/internal/ai"
	"github.com/FreshLabDev/quoto/internal/bot"
	"github.com/FreshLabDev/quoto/internal/buildinfo"
	"github.com/FreshLabDev/quoto/internal/config"
	"github.com/FreshLabDev/quoto/internal/db"
	"github.com/FreshLabDev/quoto/internal/deeplink"
	"github.com/FreshLabDev/quoto/internal/health"
	qlog "github.com/FreshLabDev/quoto/internal/log"
	"github.com/FreshLabDev/quoto/internal/media"
	"github.com/FreshLabDev/quoto/internal/scheduler"
	"github.com/FreshLabDev/quoto/internal/telegram"
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

	redactor := qlog.NewRedactor(cfg.Secrets())

	tgOpts := []tg.Option{tg.WithLogger(log), tg.WithAllowedUpdates(bot.AllowedUpdates...)}
	if cfg.TelegramAPIBase != "" {
		tgOpts = append(tgOpts, tg.WithAPIBase(cfg.TelegramAPIBase), tg.WithLocalFiles(cfg.BotAPIFilesDir))
	}
	tgClient := tg.New(cfg.BotToken, tgOpts...)
	// sendRichMessage is not required: the bot probes it itself and falls back
	// to HTML, which is what an api.telegram.org deployment always gets.
	me, err := tgClient.Preflight(ctx, tg.Needs{Files: tgClient.IsLocalServer(), Wait: 30 * time.Second})
	if err != nil {
		return fmt.Errorf("telegram: %w", err)
	}
	if !strings.EqualFold(me.Username, cfg.BotUsername) {
		log.Warn("BOT_USERNAME differs from the bot's real username; deep links will point at the wrong bot",
			"configured", cfg.BotUsername, "actual", me.Username)
	}
	log.Info("telegram connected", "bot", me.Username, "self_hosted", tgClient.IsLocalServer())

	adapter := telegram.New(tgClient, telegram.Config{DeveloperIDs: cfg.DeveloperIDs, Redact: redactor.Scrub}, log)
	signer := deeplink.NewSigner(cfg.BotToken)

	aiService := ai.New(ai.Config{
		AnthropicKey:            cfg.AnthropicKey,
		AnthropicEvalModel:      cfg.AnthropicEvalModel,
		AnthropicEvalEffort:     cfg.AnthropicEvalEffort,
		AnthropicMediaModel:     cfg.AnthropicMediaModel,
		AnthropicMediaEffort:    cfg.AnthropicMediaEffort,
		OpenRouterKey:           cfg.OpenRouterKey,
		OpenRouterEvalModel:     cfg.OpenRouterEvalModel,
		OpenRouterEvalFallback:  cfg.OpenRouterEvalFallback,
		OpenRouterMediaModel:    cfg.OpenRouterMediaModel,
		OpenRouterMediaFallback: cfg.OpenRouterMediaFallback,
		OpenRouterEvalEffort:    cfg.OpenRouterEvalEffort,
		OpenRouterMediaEffort:   cfg.OpenRouterMediaEffort,
		BotUsername:             cfg.BotUsername,
		AuditDir:                cfg.AuditLogDir,
	}, redactor.Scrub, log)

	mediaPipeline := media.New(media.Config{AnalysisEnabled: cfg.MediaAnalysis}, media.Deps{
		Store:     media.NewPGStore(store.Pool()),
		Files:     tgClient,
		Describer: aiService,
		Log:       log,
	})

	// The report names the OpenRouter evaluation model when a failed run has no
	// model of its own to record.
	sched := scheduler.New(scheduler.Config{BotUsername: cfg.BotUsername, PremiumChats: cfg.PremiumChatIDs}, scheduler.Deps{
		Groups:    store,
		Messages:  store,
		Quotes:    store,
		Core:      store,
		Reports:   scheduler.NewPGReports(store.Pool(), scheduler.WithFallbackModel(cfg.OpenRouterEvalModel), scheduler.WithReportsLogger(log)),
		Evaluator: aiService,
		Sender:    adapter,
		Signer:    signer,
		Log:       log,
	})

	telegramBot := bot.New(bot.Config{
		BotUsername:          cfg.BotUsername,
		MediaAnalysisEnabled: cfg.MediaAnalysis,
		SelfHosted:           tgClient.IsLocalServer(),
		Version:              buildinfo.Version,
	}, bot.Deps{
		TG:        tgClient,
		Adapter:   adapter,
		Core:      store,
		Groups:    store,
		Messages:  store,
		Reactions: store,
		Quotes:    store,
		Stats:     store,
		Media:     mediaPipeline,
		CatchUp:   sched,
		Signer:    signer,
		Log:       log,
	})

	startedAt := time.Now()
	mux := http.NewServeMux()
	mux.Handle("/healthz", health.New(
		health.Func("db", store.Ping),
		health.Fresh("polling", telegramBot.LastPoll, 2*time.Minute, startedAt),
		health.Fresh("scheduler", sched.LastTick, 3*time.Minute, startedAt),
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

	start("bot", telegramBot.Run)
	start("scheduler", sched.Run)
	start("media", mediaPipeline.Run)

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
