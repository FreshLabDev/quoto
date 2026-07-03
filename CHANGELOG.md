# Changelog

All notable changes to **Quoto** are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

> **Branching model**
> - `dev` — alpha pre-releases, tagged `vX.Y.Z-alpha.N` (GitHub *pre-release*).
> - `main` — stable releases, tagged `vX.Y.Z` (full GitHub Release).
>
> The `[Unreleased]` section accumulates on `dev` and is renamed to the version
> number when promoted to `main`. See [`RELEASING.md`](RELEASING.md) for the full process.

## [Unreleased]

## [0.9.0] - 2026-07-03

First release under Semantic Versioning and this changelog. Earlier builds used
CalVer date tags (`vYYYY.MM.DD`) with no changelog.

### Changed
- Quoto now runs on the shared **`core` PostgreSQL** database. Identity, chat and
  language state live in schema `core`; quoto's own tables live in schema `quoto`
  and reference `core.person` / `core.chat` by the Telegram natural keys (user id,
  chat id). Per-user and per-chat language is resolved through `core` and shared
  across all bots on the host.

### Removed
- Surrogate `users` / `groups` id primary keys. Everything keys on the Telegram
  ids now, matching vido and branchy. Group settings moved to a `GroupSettings`
  table keyed by `chat_id`.

[Unreleased]: https://github.com/FreshLabDev/quoto/compare/v0.9.0...dev
[0.9.0]: https://github.com/FreshLabDev/quoto/releases/tag/v0.9.0
