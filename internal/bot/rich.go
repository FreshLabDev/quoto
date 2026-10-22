// SPDX-License-Identifier: Apache-2.0

package bot

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/quoto/internal/agreement"
)

// Rich Markdown (Bot API 10.3) renders the agreement as a real document. Only a
// self-hosted Bot API server has it; Telegram's own answers 404 "method not
// found". The bot therefore keeps one of three states, and only richOn sends it:
//
//   - richUnknown: the probe could not tell (the server was busy, unreachable,
//     or refused the credentials). The HTML path is correct and the rich one
//     might not be, so unknown reads as no.
//   - richOn: the server has sendRichMessage.
//   - richOff: it has not, or lost it while running.
const (
	richUnknown int32 = iota
	richOn
	richOff
)

type richState struct{ v atomic.Int32 }

func (r *richState) available() bool { return r.v.Load() == richOn }
func (r *richState) set(s int32)     { r.v.Store(s) }

// probeRich asks the server once at startup and logs which mode the bot got and
// why. A server without the method is a degraded mode, not a dead bot, so
// nothing here stops the start.
func (b *Bot) probeRich(ctx context.Context) {
	if !b.cfg.SelfHosted {
		b.rich.set(richOff)
		b.log.Info("rich markdown off: the Bot API server is Telegram's own and has no sendRichMessage; the agreement is sent as HTML")
		return
	}
	missing, err := b.d.TG.Probe(ctx, "sendRichMessage")
	switch {
	case err != nil:
		if ctx.Err() != nil {
			return
		}
		// Not an answer about the method: the server was busy, in conflict,
		// unreachable or refused the credentials. Stay on HTML.
		b.rich.set(richUnknown)
		b.log.Warn("rich markdown off: could not tell whether the server has sendRichMessage; the agreement is sent as HTML", "error", err)
		if !errors.As(err, new(*tg.APIError)) {
			b.d.Adapter.Alert(ctx, "richmd:preflight-failed", "sendRichMessage preflight was inconclusive: "+err.Error())
		}
	case len(missing) > 0:
		b.rich.set(richOff)
		b.log.Warn("rich markdown off: the Bot API server has no sendRichMessage (Bot API 10.3); the agreement is sent as HTML")
		b.d.Adapter.Alert(ctx, "richmd:unsupported",
			"Bot API server has no sendRichMessage (Bot API 10.3): the user agreement is rendered as plain HTML.")
	default:
		b.rich.set(richOn)
		b.log.Info("rich markdown on: the user agreement renders natively")
	}
}

// richLost switches the rich path off for good: the server answered the probe
// and has since lost the method.
func (b *Bot) richLost(ctx context.Context, err error) {
	if !b.rich.available() {
		return
	}
	b.rich.set(richOff)
	b.log.Warn("rich markdown off: sendRichMessage disappeared while running", "error", err)
	b.d.Adapter.Alert(ctx, "richmd:lost", "Rich Markdown stopped working at runtime: "+err.Error())
}

// rejected reports a 400 or a 404 from Telegram: the edit was refused, which says
// something about this edit and, unless it is "method not found", nothing about
// the server.
func rejected(err error) bool {
	var api *tg.APIError
	if !errors.As(err, &api) {
		return false
	}
	code := api.ErrorCode
	if code == 0 {
		code = api.StatusCode
	}
	return code == 400 || code == 404
}

// editDocument puts the agreement into a panel: as rich Markdown when the server
// has it, else, or when the server refuses, as the HTML the document is also
// written in. A "message is not modified" answer means the panel already shows
// it.
//
// Only a 404 "method not found" turns the rich path off. A 400 (a deleted panel,
// a message that can no longer be edited) says nothing about the server, so it
// costs this one edit its rich rendering and nothing more.
func (b *Bot) editDocument(ctx context.Context, chatID, messageID int64, doc agreement.Document) error {
	if b.rich.available() {
		err := b.d.TG.EditMessageRichMarkdown(ctx, chatID, messageID, doc.Markdown, doc.Keyboard, tg.WithEntityDetection())
		switch {
		case err == nil, tg.IsMessageNotModified(err):
			return nil
		case tg.IsMethodNotFound(err):
			b.richLost(ctx, err)
		case rejected(err):
			b.log.Warn("rich edit refused, falling back to HTML for this edit", "chat", chatID, "error", err)
		default:
			// Flood wait, server error, network: HTML would meet the same.
			return err
		}
	}
	return b.d.TG.EditMessageText(ctx, chatID, messageID, doc.HTML, doc.Keyboard)
}
