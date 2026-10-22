-- SPDX-License-Identifier: Apache-2.0
--
-- Two indexes the Python version never had. Additive only, so the Python image
-- still runs against this schema.
--
-- Every window query (count, read, delete) filters messages on chat_id and
-- created_at, and the retention job on created_at alone.
CREATE INDEX ix_messages_chat_created ON messages (chat_id, created_at);

-- Deleting a message checks message_ai_scores for ON DELETE SET NULL, and the
-- table is never pruned.
CREATE INDEX ix_message_ai_scores_message_db_id ON message_ai_scores (message_db_id);
