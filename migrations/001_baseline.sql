-- SPDX-License-Identifier: Apache-2.0
--
-- Quoto baseline: the schema Alembic revision 20261018_01 produced, written out.
-- Names (constraints, indexes, sequences) are the ones on the production
-- database. Runs with search_path = quoto; core.person and core.chat belong to
-- the shared core database.
--
-- A database that already has quoto.alembic_version at 20261018_01 does not run
-- this file: the migration runner checks the tables against this definition and
-- records the version.

CREATE TABLE group_settings (
    chat_id bigint NOT NULL,
    is_active boolean DEFAULT true NOT NULL,
    quote_hour integer,
    quote_minute integer,
    min_messages integer,
    boring_notice_enabled boolean,
    pin_enabled boolean,
    quote_context_enabled boolean,
    is_premium boolean,
    timezone character varying,
    media_analysis_enabled boolean,
    agreement_accepted_at timestamp with time zone,
    agreement_accepted_by bigint,
    agreement_language character varying,
    agreement_version character varying,
    CONSTRAINT group_settings_pkey PRIMARY KEY (chat_id),
    CONSTRAINT group_settings_chat_id_fkey FOREIGN KEY (chat_id) REFERENCES core.chat (chat_id)
);

CREATE TABLE messages (
    id bigserial NOT NULL,
    message_id bigint NOT NULL,
    chat_id bigint NOT NULL,
    user_id bigint NOT NULL,
    text text NOT NULL,
    content_type character varying NOT NULL,
    caption text,
    media_status character varying,
    reply_to_message_id bigint,
    created_at timestamp with time zone NOT NULL,
    CONSTRAINT messages_pkey PRIMARY KEY (id),
    CONSTRAINT uq_message_chat UNIQUE (message_id, chat_id),
    CONSTRAINT messages_user_id_fkey FOREIGN KEY (user_id) REFERENCES core.person (telegram_user_id)
);

CREATE TABLE reactions (
    id bigserial NOT NULL,
    message_db_id bigint NOT NULL,
    emoji character varying NOT NULL,
    count integer NOT NULL,
    CONSTRAINT reactions_pkey PRIMARY KEY (id),
    CONSTRAINT uq_reaction_emoji UNIQUE (message_db_id, emoji),
    CONSTRAINT reactions_message_db_id_fkey FOREIGN KEY (message_db_id) REFERENCES messages (id) ON DELETE CASCADE
);

CREATE TABLE media_cache (
    id bigserial NOT NULL,
    media_kind character varying NOT NULL,
    telegram_file_unique_id character varying,
    telegram_file_id character varying,
    sha256 character varying NOT NULL,
    phash character varying,
    phash_algo character varying,
    description text NOT NULL,
    model character varying NOT NULL,
    prompt_version character varying NOT NULL,
    usage_prompt_tokens integer,
    usage_completion_tokens integer,
    usage_total_tokens integer,
    created_at timestamp with time zone NOT NULL,
    CONSTRAINT media_cache_pkey PRIMARY KEY (id),
    CONSTRAINT uq_media_cache_prompt_kind_sha256 UNIQUE (prompt_version, media_kind, sha256)
);

CREATE INDEX ix_media_cache_file_unique_id ON media_cache (telegram_file_unique_id);
CREATE INDEX ix_media_cache_file_id ON media_cache (telegram_file_id);
CREATE INDEX ix_media_cache_sha256 ON media_cache (sha256);
CREATE INDEX ix_media_cache_phash ON media_cache (phash);

CREATE TABLE message_media (
    id bigserial NOT NULL,
    message_db_id bigint NOT NULL,
    media_cache_id bigint,
    media_kind character varying NOT NULL,
    telegram_file_id character varying,
    telegram_file_unique_id character varying,
    mime_type character varying,
    file_name character varying,
    file_size bigint,
    width integer,
    height integer,
    duration double precision,
    sha256 character varying,
    phash character varying,
    analysis_status character varying NOT NULL,
    analysis_error text,
    retry_count integer DEFAULT 0 NOT NULL,
    next_retry_at timestamp with time zone,
    description_snapshot text,
    created_at timestamp with time zone NOT NULL,
    CONSTRAINT message_media_pkey PRIMARY KEY (id),
    CONSTRAINT message_media_message_db_id_fkey FOREIGN KEY (message_db_id) REFERENCES messages (id) ON DELETE CASCADE,
    CONSTRAINT message_media_media_cache_id_fkey FOREIGN KEY (media_cache_id) REFERENCES media_cache (id) ON DELETE SET NULL
);

CREATE INDEX ix_message_media_message_db_id ON message_media (message_db_id);
CREATE INDEX ix_message_media_file_unique_id ON message_media (telegram_file_unique_id);
CREATE INDEX ix_message_media_sha256 ON message_media (sha256);
CREATE INDEX ix_message_media_pending_retry ON message_media (analysis_status, next_retry_at);

CREATE TABLE quotes (
    id bigserial NOT NULL,
    group_id bigint NOT NULL,
    author_id bigint NOT NULL,
    text character varying NOT NULL,
    score double precision NOT NULL,
    reaction_score double precision,
    ai_score double precision,
    length_score double precision,
    reaction_count integer,
    message_id bigint,
    content_type character varying NOT NULL,
    bot_message_id bigint,
    notice_message_id bigint,
    ai_model character varying,
    ai_best_text character varying,
    context_message_ids character varying,
    context_snapshot character varying,
    quote_day date NOT NULL,
    window_start_at timestamp with time zone NOT NULL,
    window_end_at timestamp with time zone NOT NULL,
    decision_status character varying NOT NULL,
    status_changed_at timestamp with time zone NOT NULL,
    decision_reason character varying,
    operation_error character varying,
    forced_by_admin boolean NOT NULL,
    created_at timestamp with time zone NOT NULL,
    CONSTRAINT quotes_pkey PRIMARY KEY (id),
    CONSTRAINT uq_quote_group_day UNIQUE (group_id, quote_day),
    CONSTRAINT quotes_group_id_fkey FOREIGN KEY (group_id) REFERENCES core.chat (chat_id),
    CONSTRAINT quotes_author_id_fkey FOREIGN KEY (author_id) REFERENCES core.person (telegram_user_id)
);

CREATE INDEX ix_quoto_quotes_quote_day ON quotes (quote_day);

CREATE TABLE ai_evaluation_runs (
    id bigserial NOT NULL,
    group_id bigint NOT NULL,
    chat_id bigint NOT NULL,
    quote_day date NOT NULL,
    window_start_at timestamp with time zone NOT NULL,
    window_end_at timestamp with time zone NOT NULL,
    requested_model character varying NOT NULL,
    actual_model character varying NOT NULL,
    status character varying NOT NULL,
    message_count integer NOT NULL,
    source_message_count integer NOT NULL,
    selected_message_db_id bigint,
    selected_telegram_message_id bigint,
    context_message_ids text,
    context_needed boolean NOT NULL,
    should_publish boolean,
    day_reason_code character varying,
    day_reason_text text,
    request_id character varying,
    prompt_tokens integer,
    completion_tokens integer,
    reasoning_tokens integer,
    total_tokens integer,
    cost_usd numeric(12, 8),
    created_at timestamp with time zone NOT NULL,
    CONSTRAINT ai_evaluation_runs_pkey PRIMARY KEY (id),
    CONSTRAINT uq_ai_evaluation_run_group_day UNIQUE (group_id, quote_day),
    CONSTRAINT ai_evaluation_runs_group_id_fkey FOREIGN KEY (group_id) REFERENCES core.chat (chat_id)
);

CREATE INDEX ix_ai_evaluation_runs_chat_day ON ai_evaluation_runs (chat_id, quote_day);
CREATE INDEX ix_ai_evaluation_runs_created_at ON ai_evaluation_runs (created_at);

CREATE TABLE message_ai_scores (
    id bigserial NOT NULL,
    run_id bigint NOT NULL,
    group_id bigint NOT NULL,
    chat_id bigint NOT NULL,
    quote_day date NOT NULL,
    message_db_id bigint,
    telegram_message_id bigint NOT NULL,
    reply_to_message_id bigint,
    user_id bigint,
    author_name_snapshot character varying NOT NULL,
    text_snapshot text NOT NULL,
    content_type character varying NOT NULL,
    caption_snapshot text,
    reactions_snapshot text,
    reaction_count integer NOT NULL,
    media_status character varying,
    media_description_snapshot text,
    media_kind character varying,
    telegram_file_id character varying,
    telegram_file_unique_id character varying,
    mime_type character varying,
    file_name character varying,
    file_size bigint,
    width integer,
    height integer,
    duration double precision,
    sha256 character varying,
    phash character varying,
    media_cache_id bigint,
    ai_score double precision NOT NULL,
    ai_score_raw double precision NOT NULL,
    rank integer NOT NULL,
    is_selected_primary boolean NOT NULL,
    is_selected_context boolean NOT NULL,
    created_at timestamp with time zone NOT NULL,
    CONSTRAINT message_ai_scores_pkey PRIMARY KEY (id),
    CONSTRAINT uq_message_ai_score_run_message UNIQUE (run_id, telegram_message_id),
    CONSTRAINT message_ai_scores_run_id_fkey FOREIGN KEY (run_id) REFERENCES ai_evaluation_runs (id) ON DELETE CASCADE,
    CONSTRAINT message_ai_scores_group_id_fkey FOREIGN KEY (group_id) REFERENCES core.chat (chat_id),
    CONSTRAINT message_ai_scores_message_db_id_fkey FOREIGN KEY (message_db_id) REFERENCES messages (id) ON DELETE SET NULL,
    CONSTRAINT message_ai_scores_user_id_fkey FOREIGN KEY (user_id) REFERENCES core.person (telegram_user_id)
);

CREATE INDEX ix_message_ai_scores_chat_day_rank ON message_ai_scores (chat_id, quote_day, rank);
CREATE INDEX ix_message_ai_scores_user_score ON message_ai_scores (user_id, ai_score);
CREATE INDEX ix_message_ai_scores_primary_day ON message_ai_scores (is_selected_primary, quote_day);
