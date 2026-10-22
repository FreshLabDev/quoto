// SPDX-License-Identifier: Apache-2.0

package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/quoto/internal/agreement"
	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/i18n"
	"github.com/FreshLabDev/quoto/internal/menu"
)

// answer is how a button press is acknowledged: nothing, a short toast, or a
// dialog the person has to dismiss. A handler returns one and the dispatcher
// sends it, so every press is answered exactly once, whatever path it took.
type answer struct {
	text  string
	alert bool
	// sent is set by a handler that has already answered, as Close does so that
	// the spinner stops before the messages are removed.
	sent bool
}

func toast(text string) answer  { return answer{text: text} }
func dialog(text string) answer { return answer{text: text, alert: true} }

// answerTimeout bounds the acknowledgement. It gets its own context because the
// press may have used up the handler's.
const answerTimeout = 10 * time.Second

func (b *Bot) respond(ctx context.Context, id string, a answer) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), answerTimeout)
	defer cancel()
	var err error
	if a.alert {
		err = b.d.TG.AnswerCallbackQueryAlert(ctx, id, a.text)
	} else {
		err = b.d.TG.AnswerCallbackQuery(ctx, id, a.text)
	}
	if err != nil {
		// A press answered after Telegram stopped waiting is common and harmless.
		b.log.Warn("callback not answered", "error", err)
	}
}

// onCallback routes a button press by the prefix of its data. Data this bot did
// not write, or wrote in a form it no longer reads, is answered with nothing: the
// press stops spinning and the panel stays as it is. Python left the spinner
// running for a prefix it did not know.
func (b *Bot) onCallback(ctx context.Context, cq *tg.CallbackQuery) error {
	var a answer
	var err error
	switch {
	case strings.HasPrefix(cq.Data, agreement.CallbackPrefix+":"):
		a, err = b.agreementCallback(ctx, cq)
	case strings.HasPrefix(cq.Data, menu.CallbackPrefix+":"):
		a, err = b.menuCallback(ctx, cq)
	}
	if !a.sent {
		// Also after a failure: a button that spins for half a minute is worse
		// than one that does nothing.
		b.respond(ctx, cq.ID, a)
	}
	return err
}

// editable: can the panel the press came from be edited? A message too old for
// the bot to see arrives without a date.
func editable(m tg.Message) bool { return m.Date != 0 && m.MessageID != 0 && m.Chat.ID != 0 }

// editPanel replaces the open panel with an HTML screen. A panel that cannot be
// edited, or an edit Telegram refuses, is logged and the press is answered all the
// same: the setting it changed is already saved.
func (b *Bot) editPanel(ctx context.Context, cq *tg.CallbackQuery, text string, markup *tg.InlineKeyboardMarkup) bool {
	p := cq.Message
	if !editable(p) {
		return false
	}
	if err := b.d.TG.EditMessageText(ctx, p.Chat.ID, p.MessageID, text, markup); err != nil {
		b.log.Warn("panel edit failed", "chat", p.Chat.ID, "error", cutRunes(err.Error(), 200))
		return false
	}
	return true
}

// editAgreement does the same for the agreement tab, which has two renderings.
func (b *Bot) editAgreement(ctx context.Context, cq *tg.CallbackQuery, doc agreement.Document) bool {
	p := cq.Message
	if !editable(p) {
		return false
	}
	if err := b.editDocument(ctx, p.Chat.ID, p.MessageID, doc); err != nil {
		b.log.Warn("agreement edit failed", "chat", p.Chat.ID, "error", cutRunes(err.Error(), 200))
		return false
	}
	return true
}

// closePanel removes a group panel and the /start message that opened it. Only the
// owner gets here. It answers first, so the spinner stops at once.
func (b *Bot) closePanel(ctx context.Context, cq *tg.CallbackQuery) answer {
	p := cq.Message
	if p.MessageID == 0 || p.Chat.ID == 0 {
		return answer{}
	}
	command, hasCommand := b.panels.take(msgRef{p.Chat.ID, p.MessageID}, b.now())
	b.respond(ctx, cq.ID, answer{})
	b.deleteQuietly(ctx, p.Chat.ID, p.MessageID)
	if hasCommand {
		b.deleteQuietly(ctx, command.chat, command.message)
	}
	return answer{sent: true}
}

func (b *Bot) deleteQuietly(ctx context.Context, chatID, messageID int64) {
	if err := b.d.TG.DeleteMessage(ctx, chatID, messageID); err != nil {
		b.log.Debug("message not deleted", "chat", chatID, "message", messageID, "error", err)
	}
}

// menuCallback runs a press on a panel button. The order is part of what people
// have seen: parse, owner, close, throttle, action known, scope, group context,
// admin gate, action.
func (b *Bot) menuCallback(ctx context.Context, cq *tg.CallbackQuery) (answer, error) {
	cb, ok := menu.Parse(cq.Data)
	if !ok {
		return answer{}, nil
	}
	// The press is answered in the language of the person who made it.
	viewer := i18n.OrDefault(cq.From.LanguageCode)
	if cq.From.ID != cb.Owner {
		return dialog(i18n.T(viewer, "menu.other_user")), nil
	}
	if cb.Action == menu.ActionClose {
		return b.closePanel(ctx, cq), nil
	}
	if b.throttle.hit(cq.Message.Chat.ID, cq.Message.MessageID, cq.From.ID, b.now()) {
		return answer{}, nil
	}
	if !menu.KnownAction(cb.Action) {
		return answer{}, nil
	}
	// Anything that is not the private scope is a group panel, which is how
	// Python read a scope it did not know.
	if cb.Scope == menu.ScopePrivate {
		return b.privateMenu(ctx, cq, cb)
	}
	return b.groupMenu(ctx, cq, cb)
}

func (b *Bot) privateMenu(ctx context.Context, cq *tg.CallbackQuery, cb menu.Callback) (answer, error) {
	user := cq.From
	if err := b.d.Core.TouchUser(ctx, personFrom(&user)); err != nil {
		return answer{}, fmt.Errorf("record user %d: %w", user.ID, err)
	}
	lang, source := b.userLanguage(ctx, user.ID, user.LanguageCode)

	show := func(section menu.Section, notice string) answer {
		text, markup := menu.PrivatePanel(menu.PrivateInput{
			Owner: cb.Owner, Lang: lang, LangSource: source,
			BotUsername: b.cfg.BotUsername, Version: b.cfg.Version, Section: section,
		})
		b.editPanel(ctx, cq, text, markup)
		return toast(notice)
	}

	switch cb.Action {
	case menu.ActionHome:
		return show(menu.SectionHome, ""), nil
	case menu.ActionPrivateLanguage:
		return show(menu.SectionLanguage, ""), nil
	case menu.ActionAbout:
		return show(menu.SectionAbout, ""), nil
	case menu.ActionAgreement:
		doc := agreement.Build(lang, agreement.Options{
			Ref: agreement.Ref{Scope: menu.ScopePrivate, Owner: cb.Owner},
			Nav: menu.NavRow(cb.Owner, menu.ScopePrivate, lang),
		})
		b.editAgreement(ctx, cq, doc)
		return answer{}, nil
	case menu.ActionSetPrivateLanguage:
		code, ok := i18n.Normalize(cb.Payload)
		if !ok {
			return dialog(i18n.T(lang, "menu.language.invalid")), nil
		}
		if err := b.d.Core.SetLanguage(ctx, domain.ScopeUser, user.ID, code, domain.SourceManual); err != nil {
			return answer{}, fmt.Errorf("set the language of user %d: %w", user.ID, err)
		}
		lang, source = code, domain.SourceManual
		return show(menu.SectionLanguage, i18n.T(lang, "menu.language.updated", "language_name", i18n.Name(lang))), nil
	case menu.ActionAutoPrivateLanguage:
		if err := b.d.Core.ClearLanguage(ctx, domain.ScopeUser, user.ID); err != nil {
			return answer{}, fmt.Errorf("clear the language of user %d: %w", user.ID, err)
		}
		// Ask core what answers now. Clearing withdraws Quoto's claim and nobody
		// else's: another bot's manual choice survives it and wins, and the screen
		// has to redraw in the language the person is about to get.
		lang, source = b.userLanguage(ctx, user.ID, user.LanguageCode)
		return show(menu.SectionLanguage, i18n.T(lang, "settings.updated")), nil
	}
	// A group action on a private panel.
	return answer{}, nil
}

// needsAdminCheck: which actions need to know whether the person is an admin. The
// panel's home draws the admin row only for admins and the agreement tab offers
// Accept only to them; the settings screens need it as a gate. The others never
// look at it, so a failing getChatMember does not stop them.
func needsAdminCheck(action string) bool {
	return menu.AdminOnly(action) || action == menu.ActionHome || action == menu.ActionAgreement
}

func (b *Bot) groupMenu(ctx context.Context, cq *tg.CallbackQuery, cb menu.Callback) (answer, error) {
	viewer := i18n.OrDefault(cq.From.LanguageCode)
	chat := cq.Message.Chat
	if chat.ID == 0 || !isGroupChat(chat) {
		return dialog(i18n.T(viewer, "menu.group_context_required")), nil
	}
	user := cq.From
	g, err := b.group(ctx, &user, chat)
	if err != nil {
		return answer{}, err
	}
	lang := i18n.OrDefault(g.Language)

	isAdmin := false
	if needsAdminCheck(cb.Action) {
		isAdmin, err = b.isAdmin(ctx, chat.ID, user.ID)
		if err != nil {
			// Doc 7.2 F5: say so, instead of leaving the button spinning.
			b.log.Warn("could not check the rights of the person who pressed a button", "chat", chat.ID, "user", user.ID, "error", err)
			return dialog(i18n.T(lang, "settings.invalid")), nil
		}
	}
	if menu.AdminOnly(cb.Action) && !isAdmin {
		return dialog(i18n.T(lang, "admin.admin_only")), nil
	}

	show := func(section menu.Section, notice string) answer {
		return b.showGroup(ctx, cq, cb.Owner, g, isAdmin, section, "", "", notice)
	}
	// reread loads the group again after a change, so the panel and the toast speak
	// the language it has now.
	reread := func() error {
		g, err = b.groupByID(ctx, chat.ID)
		lang = i18n.OrDefault(g.Language)
		return err
	}

	switch cb.Action {
	case menu.ActionHome:
		return show(menu.SectionHome, ""), nil
	case menu.ActionAbout:
		return show(menu.SectionAbout, ""), nil
	case menu.ActionAgreement:
		accepted := g.AgreementAccepted()
		doc := agreement.Build(lang, agreement.Options{
			CanAccept: isAdmin && !accepted,
			Accepted:  accepted,
			Ref:       agreement.Ref{Scope: menu.ScopeGroup, Owner: cb.Owner},
			Nav:       menu.NavRow(cb.Owner, menu.ScopeGroup, lang),
		})
		b.editAgreement(ctx, cq, doc)
		return answer{}, nil

	case menu.ActionGroupLanguage:
		return show(menu.SectionLanguage, ""), nil
	case menu.ActionSetGroupLanguage:
		code, ok := i18n.Normalize(cb.Payload)
		if !ok {
			return dialog(i18n.T(lang, "menu.language.invalid")), nil
		}
		if err := b.d.Core.SetLanguage(ctx, domain.ScopeChat, chat.ID, code, domain.SourceManual); err != nil {
			return answer{}, fmt.Errorf("set the language of group %d: %w", chat.ID, err)
		}
		if err := reread(); err != nil {
			return answer{}, err
		}
		return show(menu.SectionLanguage, i18n.T(lang, "menu.language.updated", "language_name", i18n.Name(lang))), nil
	case menu.ActionAutoGroupLanguage:
		if err := b.d.Core.ClearLanguage(ctx, domain.ScopeChat, chat.ID); err != nil {
			return answer{}, fmt.Errorf("clear the language of group %d: %w", chat.ID, err)
		}
		if err := reread(); err != nil {
			return answer{}, err
		}
		return show(menu.SectionLanguage, i18n.T(lang, "settings.group.language_auto_updated")), nil

	case menu.ActionGroupSchedule:
		return show(menu.SectionSchedule, ""), nil
	case menu.ActionGroupTimeAdjust, menu.ActionGroupMinAdjust:
		delta, ok := parseDelta(cb.Payload)
		if !ok {
			return dialog(i18n.T(lang, "settings.invalid")), nil
		}
		var err error
		if cb.Action == menu.ActionGroupTimeAdjust {
			_, err = b.d.Groups.AdjustQuoteTime(ctx, chat.ID, delta)
		} else {
			_, err = b.d.Groups.AdjustMinMessages(ctx, chat.ID, delta)
		}
		if err != nil {
			return answer{}, fmt.Errorf("change the schedule of group %d: %w", chat.ID, err)
		}
		if err := reread(); err != nil {
			return answer{}, err
		}
		return show(menu.SectionSchedule, i18n.T(lang, "settings.updated")), nil

	case menu.ActionGroupTimezone:
		return show(menu.SectionTimezone, ""), nil
	case menu.ActionSetGroupTimezone:
		// Any zone Go can load is accepted, not only the thirteen buttons, so a
		// button from an older panel keeps working.
		if !domain.ValidTimezone(cb.Payload) {
			return dialog(i18n.T(lang, "settings.invalid")), nil
		}
		changed, err := b.d.Groups.SetTimezone(ctx, chat.ID, cb.Payload)
		if err != nil {
			return answer{}, fmt.Errorf("set the time zone of group %d: %w", chat.ID, err)
		}
		if !changed {
			return dialog(i18n.T(lang, "settings.invalid")), nil
		}
		if err := reread(); err != nil {
			return answer{}, err
		}
		return show(menu.SectionTimezone, i18n.T(lang, "settings.updated")), nil

	case menu.ActionGroupBehavior:
		return show(menu.SectionBehavior, ""), nil
	case menu.ActionToggleGroupSetting:
		// An unknown key changes nothing and still reports success, as it did.
		if key, ok := domain.ParseToggle(cb.Payload); ok {
			if _, err := b.d.Groups.Toggle(ctx, chat.ID, key); err != nil {
				return answer{}, fmt.Errorf("toggle %s of group %d: %w", key, chat.ID, err)
			}
			if err := reread(); err != nil {
				return answer{}, err
			}
		}
		return show(menu.SectionBehavior, i18n.T(lang, "settings.updated")), nil

	case menu.ActionChatStats:
		stats, err := b.d.Stats.Chat(ctx, chat.ID)
		if err != nil {
			return answer{}, fmt.Errorf("read the statistics of group %d: %w", chat.ID, err)
		}
		return b.showGroup(ctx, cq, cb.Owner, g, isAdmin, menu.SectionStats, menu.ChatStatsText(lang, stats), menu.StatsChat, ""), nil
	case menu.ActionUserStats:
		stats, err := b.d.Stats.User(ctx, chat.ID, user.ID)
		if err != nil {
			return answer{}, fmt.Errorf("read the statistics of user %d in group %d: %w", user.ID, chat.ID, err)
		}
		return b.showGroup(ctx, cq, cb.Owner, g, isAdmin, menu.SectionStats, menu.UserStatsText(lang, stats), menu.StatsUser, ""), nil
	}
	// A private action on a group panel.
	return answer{}, nil
}

// showGroup draws one screen of the group panel into the open message.
func (b *Bot) showGroup(ctx context.Context, cq *tg.CallbackQuery, owner int64, g domain.GroupSettings, isAdmin bool,
	section menu.Section, statsText, statsView, notice string) answer {
	in := menu.GroupInputFor(owner, g, isAdmin, b.cfg.Version)
	in.Section, in.StatsText, in.StatsView = section, statsText, statsView
	text, markup := menu.GroupPanel(in)
	b.editPanel(ctx, cq, text, markup)
	return toast(notice)
}

// parseDelta reads the number of a +/- button. The buttons write plain integers.
func parseDelta(payload string) (int, bool) {
	if payload == "" {
		return 0, true
	}
	n, err := strconv.Atoi(strings.TrimSpace(payload))
	return n, err == nil
}

// agreementCallback runs a press on an agreement button: read it (in any language,
// from the welcome message or from a panel's tab) or accept it.
func (b *Bot) agreementCallback(ctx context.Context, cq *tg.CallbackQuery) (answer, error) {
	cb, ok := agreement.Parse(cq.Data)
	if !ok {
		return answer{}, nil
	}
	// The document speaks the language on the button, not the group's: the
	// switcher in it sends a new one.
	lang := i18n.OrDefault(cb.Lang)
	chat := cq.Message.Chat
	inGroup := chat.ID != 0 && isGroupChat(chat)
	user := cq.From

	// Opened as a tab of a panel, the document belongs to whoever opened the
	// panel. Opened from the welcome message, it belongs to nobody.
	if cb.HasOwner && user.ID != cb.Owner {
		return dialog(i18n.T(lang, "menu.other_user")), nil
	}
	ref, hasRef := cb.Ref()
	var nav []tg.InlineKeyboardButton
	if hasRef {
		nav = menu.NavRow(ref.Owner, ref.Scope, lang)
	}

	switch cb.Action {
	case agreement.ActionView:
		opts := agreement.Options{Ref: ref, Nav: nav}
		if inGroup {
			g, err := b.d.Groups.Ensure(ctx, personFrom(&user), chatInfo(chat))
			if err != nil {
				return answer{}, fmt.Errorf("record group %d: %w", chat.ID, err)
			}
			admin, err := b.isAdmin(ctx, chat.ID, user.ID)
			if err != nil {
				b.log.Warn("could not check the rights of the person who opened the agreement", "chat", chat.ID, "user", user.ID, "error", err)
				return dialog(i18n.T(lang, "settings.invalid")), nil
			}
			opts.Accepted = g.AgreementAccepted()
			opts.CanAccept = admin && !opts.Accepted
		}
		b.editAgreement(ctx, cq, agreement.Build(lang, opts))
		return answer{}, nil

	case agreement.ActionAccept:
		if !inGroup {
			return answer{}, nil
		}
		admin, err := b.isAdmin(ctx, chat.ID, user.ID)
		if err != nil {
			b.log.Warn("could not check the rights of the person who accepted the agreement", "chat", chat.ID, "user", user.ID, "error", err)
			return dialog(i18n.T(lang, "settings.invalid")), nil
		}
		if !admin {
			return dialog(i18n.T(lang, "agreement.admin_only")), nil
		}
		if _, err := b.d.Groups.Ensure(ctx, personFrom(&user), chatInfo(chat)); err != nil {
			return answer{}, fmt.Errorf("record group %d: %w", chat.ID, err)
		}
		if _, err := b.d.Groups.AcceptAgreement(ctx, chat.ID, user.ID, lang); err != nil {
			return answer{}, fmt.Errorf("record the acceptance of group %d: %w", chat.ID, err)
		}
		// The confirmation is HTML even on a rich server: it is two short lines. With
		// no panel to go back to the buttons go away; an empty keyboard is how
		// Telegram is told to remove them.
		markup := &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{}}
		if len(nav) > 0 {
			markup.InlineKeyboard = [][]tg.InlineKeyboardButton{nav}
		}
		b.editPanel(ctx, cq, agreement.AcceptedText(lang), markup)
		b.catchUp(chat.ID)
		return toast(i18n.T(lang, "agreement.accepted_toast")), nil
	}
	return answer{}, nil
}

// catchUp lets the scheduler publish the day's quote now if the group accepted
// after its cutoff, so a late acceptance does not lose the quote of the day. It
// runs apart from the press: it can take as long as an AI call.
func (b *Bot) catchUp(chatID int64) {
	if b.d.CatchUp == nil {
		return
	}
	ctx := b.background()
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				b.fail(ctx, "catch_up", fmt.Errorf("panic: %v", r))
			}
		}()
		b.d.CatchUp.CatchUp(ctx, chatID)
	}()
}
