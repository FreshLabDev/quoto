-- SPDX-License-Identifier: Apache-2.0
--
-- Only for the CI step "up, down, up". Never run against a real database.
DROP INDEX IF EXISTS ix_message_ai_scores_message_db_id;
DROP INDEX IF EXISTS ix_messages_chat_created;
