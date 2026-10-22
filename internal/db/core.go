// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/FreshLabDev/quoto/internal/domain"
)

// touch calls core.touch on tx. Strings are trimmed and an empty result goes in
// as NULL, as core_client.py did: core.touch treats NULL as "no news" and never
// overwrites a stored name with it. chat may be the zero value for a person
// seen outside a group.
func touch(ctx context.Context, tx dbtx, p domain.Person, chat domain.ChatInfo) error {
	var chatID, chatType, chatTitle, chatUsername any
	if chat.ID != 0 {
		chatID, chatType, chatTitle, chatUsername = chat.ID, trimmed(chat.Type), trimmed(chat.Title), trimmed(chat.Username)
	}
	_, err := tx.Exec(ctx, `SELECT core.touch($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		domain.App, p.ID, trimmed(p.Username), trimmed(p.FirstName), trimmed(p.LastName), trimmed(p.LanguageCode),
		chatID, chatType, chatTitle, chatUsername, p.IsBot)
	return err
}

// TouchUser implements domain.Core.
func (s *Store) TouchUser(ctx context.Context, p domain.Person) error {
	return touch(ctx, s.pool, p, domain.ChatInfo{})
}

// SetLanguage implements domain.Core.
func (s *Store) SetLanguage(ctx context.Context, scope domain.LangScope, subject int64, code string, source domain.LangSource) error {
	_, err := s.pool.Exec(ctx, `SELECT core.set_language($1, $2::core.pref_scope, $3, $4, $5::core.lang_source)`,
		domain.App, string(scope), subject, code, string(source))
	return err
}

// ClearLanguage implements domain.Core.
func (s *Store) ClearLanguage(ctx context.Context, scope domain.LangScope, subject int64) error {
	_, err := s.pool.Exec(ctx, `SELECT core.clear_language($1, $2::core.pref_scope, $3)`,
		domain.App, string(scope), subject)
	return err
}

// LanguageClaim implements domain.Core. It reads core.language_pref directly,
// because no core function returns the source of the winning claim.
func (s *Store) LanguageClaim(ctx context.Context, scope domain.LangScope, subject int64) (domain.LangClaim, error) {
	var claim domain.LangClaim
	var source string
	err := s.pool.QueryRow(ctx,
		`SELECT language, source::text FROM core.language_pref WHERE scope = $1::core.pref_scope AND subject_id = $2`,
		string(scope), subject).Scan(&claim.Language, &source)
	if err == pgx.ErrNoRows {
		return domain.LangClaim{}, nil
	}
	claim.Source = domain.LangSource(source)
	return claim, err
}

// ClaimChatLanguageAuto implements domain.Core.
func (s *Store) ClaimChatLanguageAuto(ctx context.Context, chatID int64, code string) (bool, error) {
	var existing *string
	if err := s.pool.QueryRow(ctx,
		`SELECT core.effective_language(NULL::bigint, $1::bigint, 'chat'::core.pref_scope)`, chatID).Scan(&existing); err != nil {
		return false, err
	}
	if existing != nil {
		return false, nil
	}
	if err := s.SetLanguage(ctx, domain.ScopeChat, chatID, code, domain.SourceAuto); err != nil {
		return false, err
	}
	return true, nil
}

// DisplayNames implements domain.Core.
func (s *Store) DisplayNames(ctx context.Context, ids []int64) (map[int64]string, error) {
	out := make(map[int64]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT telegram_user_id, first_name, last_name, username FROM core.person WHERE telegram_user_id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var first, last, username *string
		if err := rows.Scan(&id, &first, &last, &username); err != nil {
			return nil, err
		}
		if name := displayName(str(first), str(last), str(username)); name != "" {
			out[id] = name
		}
	}
	return out, rows.Err()
}

// displayName is "first last", else the username, else nothing.
func displayName(first, last, username string) string {
	name := domain.PyStrip(domain.PyStrip(first) + " " + domain.PyStrip(last))
	if name != "" {
		return name
	}
	return domain.PyStrip(strings.TrimPrefix(domain.PyStrip(username), "@"))
}
