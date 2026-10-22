// SPDX-License-Identifier: Apache-2.0

// Package telegram is the part of Quoto's Bot API traffic that only Quoto does:
// silent sends, copyMessage, pinChatMessage, getChat, developer alerts, and the
// questions the scheduler asks about a failed send ("may it have been delivered
// anyway?", "is the chat gone?"). Everything else goes through github.com/FreshLabDev/tg
// directly. Client implements domain.Sender.
//
// Quoto's Python bot set disable_notification and a disabled link preview as
// bot-wide defaults. tg sends neither, so this package adds them to every
// message the bot posts, and the callers never have to remember.
package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/quoto/internal/agreement"
	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/i18n"
)

const (
	// floodAttempts is how many times a send is tried when Telegram answers 429.
	floodAttempts = 3
	// maxFloodWait caps the wait Telegram asks for; one second is added on top.
	maxFloodWait = 60 * time.Second

	// alertCooldown is how long the same alert key stays quiet.
	alertCooldown = 10 * time.Minute
	// maxAlertKeys bounds the dedupe map. Keys are error kinds, not payloads,
	// so the bound is only reached by a bug that makes a new key each time.
	maxAlertKeys = 512
	// maxAlertRunes keeps an alert below Telegram's 4096-character limit, which
	// would otherwise reject the whole message and lose the alert.
	maxAlertRunes = 3500
)

// Variables so tests can shorten them.
var (
	// attemptBudget bounds one send attempt. tg replays a POST once when
	// Telegram answers 429 with retry_after and sleeps that long under the
	// caller's context, which is unbounded: a flood wait of an hour would hold
	// the scheduler for an hour. With the budget the attempt ends, and the loop
	// below applies its own min(retry_after, 60)+1 wait.
	attemptBudget = 45 * time.Second
	// alertTimeout bounds delivery of one alert to one developer.
	alertTimeout = 30 * time.Second
)

// Config is what the adapter needs besides the Bot API client.
type Config struct {
	// DeveloperIDs receive alerts. Empty switches alerts off.
	DeveloperIDs []int64
	// Redact removes secrets from text before it leaves the process
	// (log.Redactor.Scrub). Nil leaves text as it is.
	Redact func(string) string
	// Now and Sleep are injected by tests. Nil means the real clock.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
}

// Client is the Telegram adapter. It is safe for concurrent use.
type Client struct {
	tg    *tg.Client
	cfg   Config
	log   *slog.Logger
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error

	mu      sync.Mutex
	alerted map[string]time.Time
}

var _ domain.Sender = (*Client)(nil)

// New returns the adapter over c.
func New(c *tg.Client, cfg Config, log *slog.Logger) *Client {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	a := &Client{tg: c, cfg: cfg, log: log, now: cfg.Now, sleep: cfg.Sleep, alerted: map[string]time.Time{}}
	if a.now == nil {
		a.now = time.Now
	}
	if a.sleep == nil {
		a.sleep = sleepContext
	}
	return a
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

func (c *Client) scrub(s string) string {
	if c.cfg.Redact == nil {
		return s
	}
	return c.cfg.Redact(s)
}

// SendParams describes one message the bot posts. Text is Telegram HTML.
type SendParams struct {
	ChatID int64
	// ThreadID is message_thread_id. The caller sets it only for a message that
	// is in a forum topic: Telegram rejects the field in an ordinary reply chain.
	ThreadID int
	Text     string
	Markup   *tg.InlineKeyboardMarkup
}

type sendMessageRequest struct {
	ChatID              int64                    `json:"chat_id"`
	ThreadID            int                      `json:"message_thread_id,omitempty"`
	Text                string                   `json:"text"`
	ParseMode           string                   `json:"parse_mode"`
	DisableNotification bool                     `json:"disable_notification"`
	LinkPreviewOptions  tg.LinkPreviewOptions    `json:"link_preview_options"`
	ReplyMarkup         *tg.InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

// Send posts one silent HTML message without a link preview, once. A handler
// that answers a person uses it: the person can press the button again, so a
// flood wait is reported rather than waited out.
func (c *Client) Send(ctx context.Context, p SendParams) (tg.Message, error) {
	var resp struct {
		OK     bool       `json:"ok"`
		Result tg.Message `json:"result"`
	}
	err := c.tg.Call(ctx, "sendMessage", sendMessageRequest{
		ChatID:              p.ChatID,
		ThreadID:            p.ThreadID,
		Text:                p.Text,
		ParseMode:           "HTML",
		DisableNotification: true,
		LinkPreviewOptions:  tg.LinkPreviewOptions{IsDisabled: true},
		ReplyMarkup:         p.Markup,
	}, &resp)
	if err != nil {
		return tg.Message{}, err
	}
	if !resp.OK {
		return tg.Message{}, fmt.Errorf("%w: sendMessage carried no ok", tg.ErrUnexpectedResult)
	}
	return resp.Result, nil
}

// SendHTML implements domain.Sender.
func (c *Client) SendHTML(ctx context.Context, chatID int64, html string) (int64, error) {
	var id int64
	err := c.withFloodRetry(ctx, func(ctx context.Context) error {
		msg, err := c.Send(ctx, SendParams{ChatID: chatID, Text: html})
		id = msg.MessageID
		return err
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

type copyMessageRequest struct {
	ChatID              int64  `json:"chat_id"`
	FromChatID          int64  `json:"from_chat_id"`
	MessageID           int64  `json:"message_id"`
	Caption             string `json:"caption"`
	ParseMode           string `json:"parse_mode"`
	DisableNotification bool   `json:"disable_notification"`
}

// CopyWithCaption implements domain.Sender.
func (c *Client) CopyWithCaption(ctx context.Context, chatID, messageID int64, captionHTML string) (int64, error) {
	var id int64
	err := c.withFloodRetry(ctx, func(ctx context.Context) error {
		var resp struct {
			OK     bool `json:"ok"`
			Result struct {
				MessageID int64 `json:"message_id"`
			} `json:"result"`
		}
		err := c.tg.Call(ctx, "copyMessage", copyMessageRequest{
			ChatID:              chatID,
			FromChatID:          chatID,
			MessageID:           messageID,
			Caption:             captionHTML,
			ParseMode:           "HTML",
			DisableNotification: true,
		}, &resp)
		if err != nil {
			return err
		}
		if !resp.OK || resp.Result.MessageID == 0 {
			return fmt.Errorf("%w: copyMessage carried no message_id", tg.ErrUnexpectedResult)
		}
		id = resp.Result.MessageID
		return nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

type pinChatMessageRequest struct {
	ChatID              int64 `json:"chat_id"`
	MessageID           int64 `json:"message_id"`
	DisableNotification bool  `json:"disable_notification"`
}

// Pin implements domain.Sender.
func (c *Client) Pin(ctx context.Context, chatID, messageID int64) error {
	var resp struct {
		OK bool `json:"ok"`
	}
	return c.tg.Call(ctx, "pinChatMessage", pinChatMessageRequest{
		ChatID: chatID, MessageID: messageID, DisableNotification: true,
	}, &resp)
}

// SendAgreementReminder implements domain.Sender. The keyboard is the one the
// welcome message carries, so the button works the same wherever it appears.
func (c *Client) SendAgreementReminder(ctx context.Context, chatID int64, lang string, outdated bool) error {
	lang = i18n.OrDefault(lang)
	key := "agreement.reminder"
	if outdated {
		key = "agreement.updated_reminder"
	}
	_, err := c.Send(ctx, SendParams{
		ChatID: chatID,
		Text:   i18n.T(lang, key),
		Markup: agreement.WelcomeKeyboard(lang),
	})
	return err
}

// GetChat reads a chat. The group migration uses it to learn the new
// supergroup's title and type.
func (c *Client) GetChat(ctx context.Context, chatID int64) (tg.Chat, error) {
	var resp struct {
		OK     bool    `json:"ok"`
		Result tg.Chat `json:"result"`
	}
	values := url.Values{"chat_id": {strconv.FormatInt(chatID, 10)}}
	if err := c.tg.CallGet(ctx, "getChat", values, &resp); err != nil {
		return tg.Chat{}, err
	}
	if !resp.OK || resp.Result.ID == 0 {
		return tg.Chat{}, fmt.Errorf("%w: getChat carried no chat", tg.ErrUnexpectedResult)
	}
	return resp.Result, nil
}

// withFloodRetry runs op up to floodAttempts times while Telegram answers 429,
// waiting min(retry_after, 60)+1 seconds between attempts. Any other error, and
// the last 429, is returned as it is. The caller's context ends the wait.
func (c *Client) withFloodRetry(ctx context.Context, op func(ctx context.Context) error) error {
	var err error
	for attempt := 1; attempt <= floodAttempts; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, attemptBudget)
		err = op(attemptCtx)
		cancel()
		if err == nil {
			return nil
		}
		wait, flood := floodDelay(err)
		if !flood || attempt == floodAttempts || ctx.Err() != nil {
			return err
		}
		c.log.Warn("telegram flood wait, retrying", "attempt", attempt, "wait", wait.String())
		if sleepErr := c.sleep(ctx, wait); sleepErr != nil {
			return errors.Join(err, sleepErr)
		}
	}
	return err
}

// floodDelay is how long to wait before repeating a request that Telegram
// refused with 429, and whether err is such a refusal at all. It is looked up
// by error type, so an error joined with a context error (tg does that when its
// own wait was cut short) is still a flood wait.
func floodDelay(err error) (time.Duration, bool) {
	if !tg.IsTooManyRequests(err) {
		return 0, false
	}
	return min(tg.RetryAfter(err), maxFloodWait) + time.Second, true
}

// errorCode is the number Telegram's answer means. Telegram sends the same
// number as the HTTP status and as error_code; behind a proxy that returns 200
// around an ok=false body only error_code is set.
func errorCode(e *tg.APIError) int {
	if e.ErrorCode != 0 {
		return e.ErrorCode
	}
	return e.StatusCode
}

// OutcomeUnknown implements domain.Sender. A send that failed this way may have
// been delivered: the answer never arrived, was unreadable, or came from a
// server that was failing. A definite refusal (4xx, including 429) was not.
func (c *Client) OutcomeUnknown(err error) bool {
	if err == nil {
		return false
	}
	var api *tg.APIError
	if errors.As(err, &api) {
		return errorCode(api) >= 500
	}
	// Not an answer from Telegram: a timeout, a dropped connection, a cancelled
	// context, or a 2xx whose body could not be read (tg.ErrUnexpectedResult).
	// The request may have reached the server in each of them.
	return true
}

// ChatNotFound implements domain.Sender. It matches the description, as the
// Python scheduler did, because Telegram answers 400 for many unrelated causes.
func (c *Client) ChatNotFound(err error) bool {
	var api *tg.APIError
	if !errors.As(err, &api) || errorCode(api) != 400 {
		return false
	}
	return strings.Contains(strings.ToLower(api.Description), "chat not found")
}

// Alert implements domain.Sender. Alerts are plain text with notification
// sound, so no stray character of an error message can break the markup of the
// alert that reports it.
func (c *Client) Alert(ctx context.Context, key, text string) {
	if len(c.cfg.DeveloperIDs) == 0 {
		return
	}
	if key == "" {
		key = text
	}
	if !c.allowAlert(key) {
		return
	}
	text = c.scrub(text)
	if r := []rune(text); len(r) > maxAlertRunes {
		text = string(r[:maxAlertRunes]) + "…"
	}
	for _, id := range c.cfg.DeveloperIDs {
		sendCtx, cancel := context.WithTimeout(ctx, alertTimeout)
		_, err := c.tg.SendPlainText(sendCtx, id, text)
		cancel()
		if err != nil {
			c.log.Warn("developer alert not delivered", "developer", id, "error", c.scrub(err.Error()))
		}
	}
}

// allowAlert records key and reports whether it was quiet for alertCooldown.
func (c *Client) allowAlert(key string) bool {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if last, ok := c.alerted[key]; ok && now.Sub(last) < alertCooldown {
		return false
	}
	if len(c.alerted) >= maxAlertKeys {
		c.pruneAlerts(now)
	}
	c.alerted[key] = now
	return true
}

// pruneAlerts drops the keys that are quiet again, and when that is not enough
// the oldest one, so the map cannot grow without bound.
func (c *Client) pruneAlerts(now time.Time) {
	var oldestKey string
	var oldest time.Time
	found := false
	for k, t := range c.alerted {
		if now.Sub(t) >= alertCooldown {
			delete(c.alerted, k)
			continue
		}
		if !found || t.Before(oldest) {
			oldestKey, oldest, found = k, t, true
		}
	}
	if found && len(c.alerted) >= maxAlertKeys {
		delete(c.alerted, oldestKey)
	}
}
