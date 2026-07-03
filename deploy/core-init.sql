-- SPDX-License-Identifier: Apache-2.0
-- LOCAL-DEV ONLY. Provisions the shared `core` schema (identity / presence /
-- language + the SECURITY DEFINER API functions) and the `quoto` schema inside
-- the bundled docker-compose Postgres, so quoto's Alembic migrations -- which FK
-- into core.person / core.chat and call core.touch / core.set_language /
-- core.effective_language -- can boot standalone.
--
-- In production these live in the real shared `core-postgres` (provisioned from
-- the `core` repo's migrations); this file is a verbatim copy mounted via
-- /docker-entrypoint-initdb.d/ only for `docker compose up` local development.
-- Keep in sync with core/migrations/001_core.sql. Idempotent.
CREATE SCHEMA IF NOT EXISTS quoto;

-- ============================================================================
-- core :: 001 — shared cross-bot identity / presence / language
-- Keys are GLOBAL Telegram ids (telegram_user_id / chat_id).
-- Bots write ONLY through the SECURITY DEFINER API functions below.
-- Applied by core-migrate (bin/apply.sh), never by a bot's own migrator.
-- Idempotent: safe to re-run.
-- ============================================================================

CREATE SCHEMA IF NOT EXISTS core;

-- ---- bot registry ----------------------------------------------------------
CREATE TABLE IF NOT EXISTS core.bot (
  bot      text PRIMARY KEY,
  added_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO core.bot(bot) VALUES ('vido'),('searchy'),('quoto'),('branchy')
ON CONFLICT (bot) DO NOTHING;

DO $$ BEGIN CREATE TYPE core.pref_scope  AS ENUM ('user','chat');
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN CREATE TYPE core.lang_source AS ENUM ('manual','auto','client','default');
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

-- ---- person (global telegram user id) --------------------------------------
CREATE TABLE IF NOT EXISTS core.person (
  telegram_user_id bigint PRIMARY KEY,
  username         text,
  first_name       text,
  last_name        text,
  is_bot           boolean     NOT NULL DEFAULT false,
  tg_language_code text,                        -- last Telegram hint (fallback only)
  first_seen_at    timestamptz NOT NULL DEFAULT now(),
  last_seen_at     timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_person_uname ON core.person (lower(username));

-- ---- username / name history (append-only) ---------------------------------
CREATE TABLE IF NOT EXISTS core.person_name_history (
  id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  telegram_user_id bigint NOT NULL REFERENCES core.person(telegram_user_id),
  username         text,
  first_name       text,
  last_name        text,
  seen_by_bot      text REFERENCES core.bot(bot),
  observed_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_name_hist
  ON core.person_name_history (telegram_user_id, observed_at DESC);

-- ---- chat directory --------------------------------------------------------
CREATE TABLE IF NOT EXISTS core.chat (
  chat_id       bigint PRIMARY KEY,            -- groups/supergroups negative; DM chat_id == user_id
  type          text,
  title         text,
  username      text,
  first_seen_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at  timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now()
);

-- group->supergroup promotion re-keys chat_id (quoto migrate_group_chat_id)
CREATE TABLE IF NOT EXISTS core.chat_alias (
  old_chat_id bigint PRIMARY KEY,
  chat_id     bigint NOT NULL REFERENCES core.chat(chat_id),
  noted_at    timestamptz NOT NULL DEFAULT now()
);

-- ---- presence: who-was-where (person x bot x chat). chat_id=0 => DM/no group -
CREATE TABLE IF NOT EXISTS core.presence (
  telegram_user_id bigint NOT NULL REFERENCES core.person(telegram_user_id),
  bot              text   NOT NULL REFERENCES core.bot(bot),
  chat_id          bigint NOT NULL DEFAULT 0,
  first_seen_at    timestamptz NOT NULL DEFAULT now(),
  last_seen_at     timestamptz NOT NULL DEFAULT now(),
  event_count      bigint NOT NULL DEFAULT 0,
  PRIMARY KEY (telegram_user_id, bot, chat_id)
);
CREATE INDEX IF NOT EXISTS ix_presence_bot  ON core.presence (bot);
CREATE INDEX IF NOT EXISTS ix_presence_chat ON core.presence (chat_id);

-- ---- language: raw per-bot observations (audit + resolver source) -----------
CREATE TABLE IF NOT EXISTS core.language_observation (
  source_bot  text NOT NULL REFERENCES core.bot(bot),
  scope       core.pref_scope NOT NULL,
  subject_id  bigint NOT NULL,                 -- user: telegram_user_id ; chat: chat_id
  language    text NOT NULL,                   -- normalized: en,ru,uk,de,be...
  source      core.lang_source NOT NULL,
  observed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (source_bot, scope, subject_id)
);
CREATE INDEX IF NOT EXISTS ix_lang_obs ON core.language_observation (scope, subject_id);

-- ---- language: resolved winner (user_pref & chat_pref in one table by scope) -
CREATE TABLE IF NOT EXISTS core.language_pref (
  scope        core.pref_scope NOT NULL,
  subject_id   bigint NOT NULL,
  language     text NOT NULL,
  source       core.lang_source NOT NULL,
  decided_from text NOT NULL,                  -- which observation won
  updated_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (scope, subject_id)
);

-- ---- other shared prefs (timezone/is_premium/agreement) --------------------
-- STUB per decision #3: not populated/used yet. Kept for future.
CREATE TABLE IF NOT EXISTS core.pref (
  scope          core.pref_scope NOT NULL,
  subject_id     bigint NOT NULL,
  key            text NOT NULL,
  value          jsonb NOT NULL,
  source         text NOT NULL DEFAULT 'manual',
  updated_by_bot text REFERENCES core.bot(bot),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (scope, subject_id, key)
);

-- ============================================================================
-- helpers: normalization + priority
-- ============================================================================
CREATE OR REPLACE FUNCTION core.norm_lang(raw text) RETURNS text
LANGUAGE sql IMMUTABLE AS $$
  SELECT CASE WHEN raw IS NULL OR btrim(raw) = '' THEN NULL
              ELSE split_part(lower(btrim(raw)), '-', 1) END;   -- 'en-US' -> 'en'
$$;

CREATE OR REPLACE FUNCTION core.lang_rank(s core.lang_source) RETURNS int
LANGUAGE sql IMMUTABLE AS $$
  SELECT CASE s WHEN 'manual' THEN 100 WHEN 'auto' THEN 50
                WHEN 'client' THEN 20 WHEN 'default' THEN 10 ELSE 0 END;
$$;

CREATE OR REPLACE FUNCTION core.bot_rank(b text) RETURNS int
LANGUAGE sql IMMUTABLE AS $$
  SELECT CASE b WHEN 'quoto' THEN 4 WHEN 'searchy' THEN 3
                WHEN 'vido' THEN 2 WHEN 'branchy' THEN 1 ELSE 0 END;
$$;

-- recompute the winner for ONE (scope, subject) from observations
CREATE OR REPLACE FUNCTION core.resolve_language(p_scope core.pref_scope, p_subject bigint)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE w record;
BEGIN
  SELECT source_bot, language, source INTO w
  FROM core.language_observation
  WHERE scope = p_scope AND subject_id = p_subject
  ORDER BY core.lang_rank(source) DESC, observed_at DESC, core.bot_rank(source_bot) DESC
  LIMIT 1;
  IF NOT FOUND THEN
    DELETE FROM core.language_pref WHERE scope = p_scope AND subject_id = p_subject;
    RETURN;
  END IF;
  INSERT INTO core.language_pref(scope, subject_id, language, source, decided_from, updated_at)
  VALUES (p_scope, p_subject, w.language, w.source, w.source_bot, now())
  ON CONFLICT (scope, subject_id) DO UPDATE
    SET language = EXCLUDED.language, source = EXCLUDED.source,
        decided_from = EXCLUDED.decided_from, updated_at = now();
END; $$;

-- ============================================================================
-- API functions called by bots (SECURITY DEFINER: run as owner, bots only EXECUTE)
-- ============================================================================

-- one call per interaction: person + name history + chat + presence + hint capture
-- p_chat_id: pass the GROUP chat_id when in a group; pass NULL for DM/private
--            (presence then rolls up under chat_id = 0).
CREATE OR REPLACE FUNCTION core.touch(
  p_bot text, p_user_id bigint,
  p_username text DEFAULT NULL, p_first_name text DEFAULT NULL, p_last_name text DEFAULT NULL,
  p_tg_lang text DEFAULT NULL, p_chat_id bigint DEFAULT NULL, p_chat_type text DEFAULT NULL,
  p_chat_title text DEFAULT NULL, p_chat_uname text DEFAULT NULL,
  p_is_bot boolean DEFAULT false, p_at timestamptz DEFAULT now()
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path = core AS $$
DECLARE v_old record; v_existed boolean; v_pres_chat bigint := COALESCE(p_chat_id, 0);
BEGIN
  SELECT username, first_name, last_name INTO v_old
  FROM core.person WHERE telegram_user_id = p_user_id;
  v_existed := FOUND;

  INSERT INTO core.person AS pe (telegram_user_id, username, first_name, last_name, is_bot,
                                 tg_language_code, first_seen_at, last_seen_at)
  VALUES (p_user_id, p_username, p_first_name, p_last_name, p_is_bot, p_tg_lang, p_at, p_at)
  ON CONFLICT (telegram_user_id) DO UPDATE SET
    username         = COALESCE(EXCLUDED.username, pe.username),
    first_name       = COALESCE(EXCLUDED.first_name, pe.first_name),
    last_name        = COALESCE(EXCLUDED.last_name, pe.last_name),   -- COALESCE, not blind overwrite
    is_bot           = EXCLUDED.is_bot,
    tg_language_code = COALESCE(EXCLUDED.tg_language_code, pe.tg_language_code),
    first_seen_at    = LEAST(pe.first_seen_at, EXCLUDED.first_seen_at),
    last_seen_at     = GREATEST(pe.last_seen_at, EXCLUDED.last_seen_at),
    updated_at       = now();

  IF (NOT v_existed OR v_old.username IS DISTINCT FROM p_username
      OR v_old.first_name IS DISTINCT FROM p_first_name
      OR v_old.last_name IS DISTINCT FROM p_last_name)
     AND (p_username IS NOT NULL OR p_first_name IS NOT NULL OR p_last_name IS NOT NULL) THEN
    INSERT INTO core.person_name_history(telegram_user_id, username, first_name, last_name, seen_by_bot, observed_at)
    VALUES (p_user_id, p_username, p_first_name, p_last_name, p_bot, p_at);
  END IF;

  IF p_chat_id IS NOT NULL AND p_chat_id <> 0 THEN
    INSERT INTO core.chat AS ch (chat_id, type, title, username, first_seen_at, last_seen_at)
    VALUES (p_chat_id, p_chat_type, p_chat_title, p_chat_uname, p_at, p_at)
    ON CONFLICT (chat_id) DO UPDATE SET
      type         = COALESCE(EXCLUDED.type, ch.type),
      title        = COALESCE(EXCLUDED.title, ch.title),
      username     = COALESCE(EXCLUDED.username, ch.username),
      last_seen_at = GREATEST(ch.last_seen_at, EXCLUDED.last_seen_at),
      updated_at   = now();
  END IF;

  INSERT INTO core.presence AS pr (telegram_user_id, bot, chat_id, first_seen_at, last_seen_at, event_count)
  VALUES (p_user_id, p_bot, v_pres_chat, p_at, p_at, 1)
  ON CONFLICT (telegram_user_id, bot, chat_id) DO UPDATE SET
    first_seen_at = LEAST(pr.first_seen_at, EXCLUDED.first_seen_at),
    last_seen_at  = GREATEST(pr.last_seen_at, EXCLUDED.last_seen_at),
    event_count   = pr.event_count + 1;

  -- Telegram hint as weakest source 'client'; NEVER overrides an existing stronger claim
  IF core.norm_lang(p_tg_lang) IS NOT NULL THEN
    INSERT INTO core.language_observation(source_bot, scope, subject_id, language, source, observed_at)
    VALUES (p_bot, 'user', p_user_id, core.norm_lang(p_tg_lang), 'client', p_at)
    ON CONFLICT (source_bot, scope, subject_id) DO NOTHING;
    PERFORM core.resolve_language('user', p_user_id);
  END IF;
END; $$;

-- explicit language set: write an observation and recompute the winner
CREATE OR REPLACE FUNCTION core.set_language(
  p_bot text, p_scope core.pref_scope, p_subject bigint,
  p_lang text, p_source core.lang_source
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path = core AS $$
DECLARE v text := core.norm_lang(p_lang);
BEGIN
  IF v IS NULL THEN RETURN; END IF;
  INSERT INTO core.language_observation(source_bot, scope, subject_id, language, source, observed_at)
  VALUES (p_bot, p_scope, p_subject, v, p_source, now())
  ON CONFLICT (source_bot, scope, subject_id) DO UPDATE
    SET language = EXCLUDED.language, source = EXCLUDED.source, observed_at = now();
  PERFORM core.resolve_language(p_scope, p_subject);
END; $$;

-- clear THIS bot's claim only (next-priority observation resurfaces)
CREATE OR REPLACE FUNCTION core.clear_language(
  p_bot text, p_scope core.pref_scope, p_subject bigint
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path = core AS $$
BEGIN
  DELETE FROM core.language_observation
   WHERE source_bot = p_bot AND scope = p_scope AND subject_id = p_subject;
  PERFORM core.resolve_language(p_scope, p_subject);
END; $$;

-- read: effective language for a user in (optional) chat.
-- p_prefer='chat' -> chat language dominates (group broadcast);
-- p_prefer='user' -> personal language dominates (settings/stats/DM), even inside a group.
CREATE OR REPLACE FUNCTION core.effective_language(
  p_user bigint, p_chat bigint DEFAULT NULL, p_prefer core.pref_scope DEFAULT 'user'
) RETURNS text LANGUAGE sql STABLE AS $$
  SELECT language FROM core.language_pref
   WHERE (scope = 'user' AND subject_id = p_user)
      OR (scope = 'chat' AND subject_id = p_chat AND p_chat IS NOT NULL)
   ORDER BY (scope = p_prefer) DESC, core.lang_rank(source) DESC, updated_at DESC
   LIMIT 1;
$$;

-- re-key a chat when a group is promoted to supergroup (handles merge collisions)
CREATE OR REPLACE FUNCTION core.rekey_chat(p_old bigint, p_new bigint)
RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path = core AS $$
BEGIN
  IF p_old = p_new THEN RETURN; END IF;
  INSERT INTO core.chat(chat_id, type) VALUES (p_new, 'supergroup') ON CONFLICT DO NOTHING;
  INSERT INTO core.presence AS pr (telegram_user_id, bot, chat_id, first_seen_at, last_seen_at, event_count)
    SELECT telegram_user_id, bot, p_new, first_seen_at, last_seen_at, event_count
    FROM core.presence WHERE chat_id = p_old
  ON CONFLICT (telegram_user_id, bot, chat_id) DO UPDATE SET
    first_seen_at = LEAST(pr.first_seen_at, EXCLUDED.first_seen_at),
    last_seen_at  = GREATEST(pr.last_seen_at, EXCLUDED.last_seen_at),
    event_count   = pr.event_count + EXCLUDED.event_count;   -- merge, not PK conflict
  DELETE FROM core.presence WHERE chat_id = p_old;
  UPDATE core.language_observation o SET subject_id = p_new
   WHERE scope = 'chat' AND subject_id = p_old
     AND NOT EXISTS (SELECT 1 FROM core.language_observation x
                     WHERE x.scope = 'chat' AND x.subject_id = p_new AND x.source_bot = o.source_bot);
  DELETE FROM core.language_observation WHERE scope = 'chat' AND subject_id = p_old;
  PERFORM core.resolve_language('chat', p_new);
  DELETE FROM core.language_pref WHERE scope = 'chat' AND subject_id = p_old;
  INSERT INTO core.chat_alias(old_chat_id, chat_id) VALUES (p_old, p_new)
  ON CONFLICT (old_chat_id) DO UPDATE SET chat_id = EXCLUDED.chat_id;
END; $$;

-- ============================================================================
-- who-was-where view (plain, not materialized — instant at this scale, always fresh)
-- ============================================================================
CREATE OR REPLACE VIEW core.who_was_where AS
SELECT pe.telegram_user_id, pe.username, pe.first_name, pe.last_name,
       array_agg(DISTINCT pr.bot ORDER BY pr.bot) AS bots,
       count(DISTINCT pr.bot) AS bot_count,
       array_remove(array_agg(DISTINCT pr.chat_id) FILTER (WHERE pr.chat_id <> 0), NULL) AS chats,
       min(pr.first_seen_at) AS first_seen_anywhere,
       max(pr.last_seen_at)  AS last_seen_anywhere,
       lp.language AS user_language, lp.source AS user_language_source
FROM core.presence pr
JOIN core.person pe USING (telegram_user_id)
LEFT JOIN core.language_pref lp ON lp.scope = 'user' AND lp.subject_id = pe.telegram_user_id
GROUP BY pe.telegram_user_id, pe.username, pe.first_name, pe.last_name, lp.language, lp.source;
