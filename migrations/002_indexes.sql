-- SPDX-License-Identifier: Apache-2.0
--
-- Two indexes the Python version never had. Additive only, so the Python image
-- still runs against this schema. IF NOT EXISTS: a database where someone
-- already made the index by hand must still start.
--
-- Every window query (count, read, delete) filters messages on chat_id and
-- created_at. The hourly retention job filters on created_at alone, which this
-- index does not serve (chat_id leads); on the sizes this bot has, a scan of the
-- table once an hour costs nothing, and a bigger table would want an index on
-- created_at of its own.
CREATE INDEX IF NOT EXISTS ix_messages_chat_created ON messages (chat_id, created_at);

-- Deleting a message checks message_ai_scores for ON DELETE SET NULL, and the
-- table is never pruned.
CREATE INDEX IF NOT EXISTS ix_message_ai_scores_message_db_id ON message_ai_scores (message_db_id);
