// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"math/rand/v2"

	"github.com/jackc/pgx/v5"

	"github.com/FreshLabDev/quoto/internal/domain"
)

const groupColumns = `g.chat_id, g.is_active, g.quote_hour, g.quote_minute, g.min_messages,
	g.boring_notice_enabled, g.pin_enabled, g.quote_context_enabled, g.is_premium, g.timezone,
	g.media_analysis_enabled, g.agreement_accepted_at, g.agreement_accepted_by, g.agreement_language,
	g.agreement_version`

// groupHydratedFrom adds the chat title and the language claim of any bot, from
// core, to a group row in one round trip.
const groupHydratedFrom = `
	FROM group_settings g
	LEFT JOIN core.chat c ON c.chat_id = g.chat_id
	LEFT JOIN core.language_pref lp ON lp.scope = 'chat'::core.pref_scope AND lp.subject_id = g.chat_id`

func scanGroup(row pgx.Row, hydrated bool) (domain.GroupSettings, error) {
	var g domain.GroupSettings
	var title, language, source *string
	dest := []any{&g.ChatID, &g.IsActive, &g.QuoteHour, &g.QuoteMinute, &g.MinMessages,
		&g.BoringNotice, &g.Pin, &g.QuoteContext, &g.IsPremium, &g.Timezone,
		&g.MediaAnalysis, &g.AgreementAcceptedAt, &g.AgreementAcceptedBy, &g.AgreementLanguage,
		&g.AgreementVersion}
	if hydrated {
		dest = append(dest, &title, &language, &source)
	}
	if err := row.Scan(dest...); err != nil {
		return domain.GroupSettings{}, err
	}
	g.Name, g.Language, g.LanguageSource = str(title), str(language), domain.LangSource(str(source))
	return g, nil
}

// Ensure implements domain.Groups.
func (s *Store) Ensure(ctx context.Context, actor domain.Person, chat domain.ChatInfo) (domain.GroupSettings, error) {
	var g domain.GroupSettings
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if err := touch(ctx, tx, actor, chat); err != nil {
			return err
		}
		row, err := scanGroup(tx.QueryRow(ctx, `SELECT `+groupColumns+` FROM group_settings g WHERE g.chat_id = $1`, chat.ID), false)
		switch {
		case err == pgx.ErrNoRows:
			// A new group gets its jittered minute and nothing else: every other
			// column stays NULL, meaning "the default of the day it is read".
			if _, err := tx.Exec(ctx,
				`INSERT INTO group_settings (chat_id, is_active, quote_minute) VALUES ($1, true, $2)
				 ON CONFLICT (chat_id) DO NOTHING`, chat.ID, newGroupMinute()); err != nil {
				return err
			}
			row, err = scanGroup(tx.QueryRow(ctx, `SELECT `+groupColumns+` FROM group_settings g WHERE g.chat_id = $1`, chat.ID), false)
			if err != nil {
				return err
			}
		case err != nil:
			return err
		}
		if !row.IsActive {
			// Any message or /start reactivates a group that was suspended after
			// the bot left it. Written only when it changes, not on every message.
			if _, err := tx.Exec(ctx, `UPDATE group_settings SET is_active = true WHERE chat_id = $1`, chat.ID); err != nil {
				return err
			}
			row.IsActive = true
		}
		g = row
		return nil
	})
	return g, err
}

// newGroupMinute spreads new groups over the minutes after the cutoff. The hour
// stays NULL, so a jitter of 10 never changes the hour.
func newGroupMinute() any {
	if domain.QuoteMinuteJitter <= 0 {
		return nil
	}
	return (domain.DefaultQuoteMinute + rand.IntN(domain.QuoteMinuteJitter+1)) % 60
}

// Get implements domain.Groups.
func (s *Store) Get(ctx context.Context, chatID int64) (domain.GroupSettings, bool, error) {
	g, err := scanGroup(s.pool.QueryRow(ctx,
		`SELECT `+groupColumns+`, c.title, lp.language, lp.source::text`+groupHydratedFrom+` WHERE g.chat_id = $1`, chatID), true)
	if err == pgx.ErrNoRows {
		return domain.GroupSettings{}, false, nil
	}
	return g, err == nil, err
}

// ListActive implements domain.Groups.
func (s *Store) ListActive(ctx context.Context) ([]domain.GroupSettings, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+groupColumns+`, c.title, lp.language, lp.source::text`+groupHydratedFrom+` WHERE g.is_active ORDER BY g.chat_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.GroupSettings
	for rows.Next() {
		g, err := scanGroup(rows, true)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// exec1 runs a statement and reports whether it touched a row.
func (s *Store) exec1(ctx context.Context, sql string, args ...any) (bool, error) {
	tag, err := s.pool.Exec(ctx, sql, args...)
	return tag.RowsAffected() > 0, err
}

// SetActive implements domain.Groups.
func (s *Store) SetActive(ctx context.Context, chatID int64, active bool) (bool, error) {
	return s.exec1(ctx, `UPDATE group_settings SET is_active = $2 WHERE chat_id = $1`, chatID, active)
}

// SetTimezone implements domain.Groups.
func (s *Store) SetTimezone(ctx context.Context, chatID int64, tz string) (bool, error) {
	return s.exec1(ctx, `UPDATE group_settings SET timezone = $2 WHERE chat_id = $1`, chatID, tz)
}

// SetTimezoneAuto implements domain.Groups. It reports whether it wrote.
func (s *Store) SetTimezoneAuto(ctx context.Context, chatID int64, tz string) (bool, error) {
	return s.exec1(ctx,
		`UPDATE group_settings SET timezone = $2 WHERE chat_id = $1 AND (timezone IS NULL OR timezone = '')`, chatID, tz)
}

// updateLocked reads the group row under a row lock, lets change compute the new
// values and writes them; change returns an empty statement to write nothing. It
// reports false when the group does not exist.
func (s *Store) updateLocked(ctx context.Context, chatID int64, change func(g domain.GroupSettings) (sql string, args []any)) (bool, error) {
	found := false
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		g, err := scanGroup(tx.QueryRow(ctx, `SELECT `+groupColumns+` FROM group_settings g WHERE g.chat_id = $1 FOR UPDATE`, chatID), false)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		sql, args := change(g)
		if sql == "" {
			return nil
		}
		_, err = tx.Exec(ctx, sql, append([]any{chatID}, args...)...)
		return err
	})
	return found, err
}

// AdjustQuoteTime implements domain.Groups.
func (s *Store) AdjustQuoteTime(ctx context.Context, chatID int64, deltaMinutes int) (bool, error) {
	return s.updateLocked(ctx, chatID, func(g domain.GroupSettings) (string, []any) {
		h, m := g.QuoteTime()
		h, m = domain.NormalizeQuoteTime(h, m+deltaMinutes)
		return `UPDATE group_settings SET quote_hour = $2, quote_minute = $3 WHERE chat_id = $1`, []any{h, m}
	})
}

// AdjustMinMessages implements domain.Groups.
func (s *Store) AdjustMinMessages(ctx context.Context, chatID int64, delta int) (bool, error) {
	return s.updateLocked(ctx, chatID, func(g domain.GroupSettings) (string, []any) {
		return `UPDATE group_settings SET min_messages = $2 WHERE chat_id = $1`,
			[]any{domain.ClampMinMessages(g.MinMessagesEffective() + delta)}
	})
}

// toggleColumns maps a panel key to its column. The names are constants, never
// user input, so they may be spliced into SQL.
var toggleColumns = map[domain.ToggleKey]string{
	domain.ToggleContext: "quote_context_enabled",
	domain.ToggleBoring:  "boring_notice_enabled",
	domain.TogglePin:     "pin_enabled",
	domain.ToggleMedia:   "media_analysis_enabled",
}

// Toggle implements domain.Groups.
func (s *Store) Toggle(ctx context.Context, chatID int64, key domain.ToggleKey) (bool, error) {
	column, ok := toggleColumns[key]
	if !ok {
		return false, nil
	}
	return s.updateLocked(ctx, chatID, func(g domain.GroupSettings) (string, []any) {
		return `UPDATE group_settings SET ` + column + ` = $2 WHERE chat_id = $1`, []any{!g.Enabled(key)}
	})
}

// AcceptAgreement implements domain.Groups.
func (s *Store) AcceptAgreement(ctx context.Context, chatID, userID int64, language string) (bool, error) {
	return s.updateLocked(ctx, chatID, func(g domain.GroupSettings) (string, []any) {
		if g.AgreementAccepted() {
			// A second admin pressing the button changes nothing on record.
			return "", nil
		}
		return `UPDATE group_settings SET agreement_accepted_at = $2, agreement_accepted_by = $3,
			agreement_language = $4, agreement_version = $5 WHERE chat_id = $1`,
			[]any{s.utcNow(), userID, language, domain.DocumentVersion}
	})
}

// MigrateChatID implements domain.Groups.
func (s *Store) MigrateChatID(ctx context.Context, oldID, newID int64, actor domain.Person, newChat domain.ChatInfo) (bool, error) {
	if oldID == newID {
		return false, nil
	}
	moved := false
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if actor.ID != 0 && newChat.ID != 0 {
			if err := touch(ctx, tx, actor, newChat); err != nil {
				return err
			}
		}
		var known, taken, exists bool
		if err := tx.QueryRow(ctx, `SELECT
			EXISTS (SELECT 1 FROM core.chat WHERE chat_id = $1),
			EXISTS (SELECT 1 FROM group_settings WHERE chat_id = $1),
			EXISTS (SELECT 1 FROM group_settings WHERE chat_id = $2)`, newID, oldID).Scan(&known, &taken, &exists); err != nil {
			return err
		}
		if !known || taken || !exists {
			return nil
		}
		// core.rekey_chat is revoked for bots, so core's own rows (language claim,
		// presence, alias) are not moved: the new chat starts fresh there.
		for _, stmt := range []string{
			`UPDATE group_settings SET chat_id = $2 WHERE chat_id = $1`,
			`UPDATE messages SET chat_id = $2 WHERE chat_id = $1`,
			`UPDATE quotes SET group_id = $2 WHERE group_id = $1`,
			`UPDATE ai_evaluation_runs SET group_id = $2, chat_id = $2 WHERE group_id = $1`,
			`UPDATE message_ai_scores SET group_id = $2, chat_id = $2 WHERE group_id = $1`,
		} {
			if _, err := tx.Exec(ctx, stmt, oldID, newID); err != nil {
				return err
			}
		}
		moved = true
		return nil
	})
	return moved, err
}
