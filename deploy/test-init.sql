-- SPDX-License-Identifier: Apache-2.0
-- Runs after core_stub.sql in the test stack's PostgreSQL (compose.test.yaml).
-- In production the role and the schema are provisioned by the core repository
-- and the host; here the bot role simply owns its schema.
ALTER ROLE quoto_core PASSWORD 'quoto';
CREATE SCHEMA IF NOT EXISTS quoto AUTHORIZATION quoto_core;
