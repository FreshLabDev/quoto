// SPDX-License-Identifier: Apache-2.0

// Package bot is Quoto's Telegram front: the update loop, the handlers behind
// every update type, the /start panels and their callbacks, the agreement, and
// the collection of the day's group messages.
//
// The product logic that decides what a screen says lives in internal/menu and
// internal/agreement; the Bot API calls only Quoto makes live in
// internal/telegram. This package decides which update does what, in which order,
// and what to say when a call fails.
package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/quoto/internal/deeplink"
	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/telegram"
)

// AllowedUpdates is what cmd/bot passes to tg.WithAllowedUpdates. Telegram keeps
// the last list set for a token, and by default it leaves out the reaction
// updates, so the list is always explicit. Reactions arrive only while the bot
// is an administrator of the chat.
var AllowedUpdates = []string{
	"message", "edited_message", "callback_query", "my_chat_member",
	"message_reaction", "message_reaction_count",
}

const (
	// pollTimeoutSeconds is Telegram's own long-poll duration.
	pollTimeoutSeconds = 10
	// workers bounds the updates handled at once. A handler waits on the
	// database and on Telegram, so the bound is about not exhausting the pool
	// of connections, not about CPU.
	workers = 16
)

// handlerTimeout bounds one update's handler. It is a variable so tests can
// shorten it. The handler's context is detached from Run's, so a shutdown lets
// the updates already accepted finish instead of cutting them in half.
var handlerTimeout = 2 * time.Minute

// Config is what the bot learns from the environment.
type Config struct {
	BotUsername          string
	MediaAnalysisEnabled bool   // MEDIA_ANALYSIS_ENABLED
	SelfHosted           bool   // TELEGRAM_BOT_API_BASE_URL is set (rich Markdown possible)
	Version              string // buildinfo.Version for the About card
}

// Adapter is what the bot uses of internal/telegram. *telegram.Client is the
// implementation; the narrow interface lets tests watch the alerts.
type Adapter interface {
	Send(ctx context.Context, p telegram.SendParams) (tg.Message, error)
	GetChat(ctx context.Context, chatID int64) (tg.Chat, error)
	Alert(ctx context.Context, key, text string)
}

// MediaQueue hands a stored media message to the analysis pipeline. Submit does
// not block.
type MediaQueue interface {
	Submit(messageDBID int64)
}

// CatchUp publishes the quote of a group that has just accepted the agreement,
// if its cutoff has already passed. The scheduler decides whether it has.
type CatchUp interface {
	CatchUp(ctx context.Context, chatID int64)
}

// Deps are the collaborators. Media and CatchUp may be nil.
type Deps struct {
	TG        *tg.Client
	Adapter   Adapter
	Core      domain.Core
	Groups    domain.Groups
	Messages  domain.Messages
	Reactions domain.Reactions
	Quotes    domain.Quotes
	Stats     domain.StatsReader
	Media     MediaQueue
	CatchUp   CatchUp
	Signer    deeplink.Signer
	Log       *slog.Logger
}

// Bot handles updates. Create it with New and run it with Run.
type Bot struct {
	cfg Config
	d   Deps
	log *slog.Logger

	// username is the bot's own name, lower-case and without "@", for the
	// /command@name check.
	username string

	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error

	lastPoll atomic.Int64 // unix nanoseconds of the last successful getUpdates

	rich     richState
	throttle callbackThrottle
	panels   panelRegistry
	wg       sync.WaitGroup

	runMu  sync.Mutex
	runCtx context.Context
}

// New returns a bot. It performs no I/O.
func New(cfg Config, deps Deps) *Bot {
	cfg.BotUsername = strings.TrimPrefix(strings.TrimSpace(cfg.BotUsername), "@")
	b := &Bot{
		cfg:      cfg,
		d:        deps,
		log:      deps.Log,
		username: strings.ToLower(cfg.BotUsername),
		now:      time.Now,
		sleep:    sleepContext,
		runCtx:   context.Background(),
	}
	if b.log == nil {
		b.log = slog.New(slog.DiscardHandler)
	}
	b.throttle.init()
	b.panels.init()
	return b
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// LastPoll is the time of the last successful getUpdates, for /healthz. It is
// the zero time before the first one.
func (b *Bot) LastPoll() time.Time {
	n := b.lastPoll.Load()
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

// Run probes rich Markdown, publishes the commands and long-polls until ctx is
// done. It returns after the updates already accepted have been handled.
func (b *Bot) Run(ctx context.Context) error {
	b.runMu.Lock()
	b.runCtx = ctx
	b.runMu.Unlock()

	b.probeRich(ctx)
	b.registerCommands(ctx)

	offset := b.poll(ctx)
	b.wg.Wait()
	b.confirm(ctx, offset)
	return nil
}

// poll long-polls until ctx is done and returns the offset of the first update
// it has not accepted.
func (b *Bot) poll(ctx context.Context) (offset int64) {
	slots := make(chan struct{}, workers)
	failures := 0
	for ctx.Err() == nil {
		updates, err := b.d.TG.GetUpdates(ctx, offset, pollTimeoutSeconds)
		if err != nil {
			if ctx.Err() != nil {
				return offset
			}
			failures++
			delay := min(time.Duration(failures)*time.Second, 30*time.Second)
			if failures <= 3 || failures%10 == 0 {
				b.log.Error("telegram polling failed", "failures", failures, "retry_in", delay.String(), "error", err)
			}
			if b.sleep(ctx, delay) != nil {
				return offset
			}
			continue
		}
		if failures > 0 {
			b.log.Info("telegram polling recovered", "after_failures", failures)
			failures = 0
		}
		b.lastPoll.Store(b.now().UnixNano())
		for _, u := range updates {
			// Taking a slot is accepting the update. Only then does the offset
			// move past it, so an update that was never started is delivered
			// again after a restart.
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return offset
			}
			b.wg.Add(1)
			go func(u tg.Update) {
				defer func() { <-slots; b.wg.Done() }()
				b.handle(ctx, u)
			}(u)
			offset = u.UpdateID + 1
		}
	}
	return offset
}

// confirm tells Telegram the accepted updates are done. Telegram drops an update
// only when a later getUpdates carries a higher offset; without this call every
// restart would deliver the last few again, and a repeated reaction update would
// be counted twice.
func (b *Bot) confirm(ctx context.Context, offset int64) {
	if offset == 0 {
		return
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := b.d.TG.GetUpdates(cctx, offset, 0); err != nil {
		b.log.Warn("could not confirm the last updates", "offset", offset, "error", err)
	}
}

// background is the context for work that outlives one update, such as the
// catch-up after an acceptance. It ends when Run's does.
func (b *Bot) background() context.Context {
	b.runMu.Lock()
	defer b.runMu.Unlock()
	return b.runCtx
}

// handle runs one update. A failure never stops polling: it is logged and sent
// to the developers once per update type per ten minutes.
func (b *Bot) handle(ctx context.Context, u tg.Update) {
	kind := updateKind(u)
	if kind == "" {
		return
	}
	hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), handlerTimeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			b.fail(hctx, kind, fmt.Errorf("panic: %v", r))
		}
	}()
	if err := b.dispatch(hctx, kind, u); err != nil {
		b.fail(hctx, kind, err)
	}
}

func (b *Bot) fail(ctx context.Context, kind string, err error) {
	b.log.Error("update failed", "update", kind, "error", err)
	b.d.Adapter.Alert(ctx, "update:"+kind, fmt.Sprintf("Update error (%s): %v", kind, err))
}

// updateKind names the field of the update that is set, which is the name
// Telegram uses in allowed_updates. An update of a kind the bot does not ask for
// has none.
func updateKind(u tg.Update) string {
	switch {
	case u.Message != nil:
		return "message"
	case u.EditedMessage != nil:
		return "edited_message"
	case u.Callback != nil:
		return "callback_query"
	case u.MyChatMember != nil:
		return "my_chat_member"
	case u.MessageReaction != nil:
		return "message_reaction"
	case u.MessageReactionCount != nil:
		return "message_reaction_count"
	}
	return ""
}

func (b *Bot) dispatch(ctx context.Context, kind string, u tg.Update) error {
	switch kind {
	case "message":
		return b.onMessage(ctx, u.Message)
	case "edited_message":
		return b.onEditedMessage(ctx, u.EditedMessage)
	case "callback_query":
		return b.onCallback(ctx, u.Callback)
	case "my_chat_member":
		return b.onMyChatMember(ctx, u.MyChatMember)
	case "message_reaction":
		return b.onReaction(ctx, u.MessageReaction)
	case "message_reaction_count":
		return b.onReactionCount(ctx, u.MessageReactionCount)
	}
	return nil
}

func isGroupChat(c tg.Chat) bool {
	return c.Type == tg.ChatGroup || c.Type == tg.ChatSupergroup
}

func personFrom(u *tg.User) domain.Person {
	if u == nil {
		return domain.Person{}
	}
	return domain.Person{
		ID: u.ID, Username: u.Username, FirstName: u.FirstName, LastName: u.LastName,
		LanguageCode: u.LanguageCode, IsBot: u.IsBot,
	}
}

func chatInfo(c tg.Chat) domain.ChatInfo {
	return domain.ChatInfo{ID: c.ID, Type: c.Type, Title: c.Title, Username: c.Username}
}
