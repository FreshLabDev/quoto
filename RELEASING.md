# Releasing Quoto

Quoto uses [Semantic Versioning](https://semver.org) and a two-branch model.
All notable changes live in [`CHANGELOG.md`](CHANGELOG.md).

## Branch model

| Branch | Role | Tag format | GitHub Release |
|--------|------|-----------|----------------|
| `dev`  | Alpha / work in progress | `vX.Y.Z-alpha.N` | Pre-release |
| `main` | Stable release | `vX.Y.Z` | Full release |

- Day-to-day work lands on `dev` and is logged under `## [Unreleased]` in the
  changelog (subsections `Added` / `Changed` / `Fixed` / `Removed`).
- The current target version is **0.9.0**.

## Cutting an alpha (from `dev`)

1. Make sure `## [Unreleased]` in `CHANGELOG.md` reflects what's shipping.
2. Tag the dev commit (bump `alpha.N` for each subsequent alpha):
   ```bash
   git tag -a v0.9.0-alpha.1 -m "v0.9.0-alpha.1"
   git push origin v0.9.0-alpha.1
   ```
3. Publish a GitHub **pre-release** from that tag, pasting the `[Unreleased]`
   notes as the body:
   ```bash
   gh release create v0.9.0-alpha.1 --prerelease --target dev \
     --title "v0.9.0-alpha.1" --notes "<paste the [Unreleased] section>"
   ```

## Promoting to a stable release (`dev → main`)

1. In `CHANGELOG.md`, rename `## [Unreleased]` to `## [X.Y.Z] - YYYY-MM-DD`,
   add a fresh empty `## [Unreleased]` above it, and update the compare links at
   the bottom.
2. Merge `dev` into `main`:
   ```bash
   git checkout main
   git merge --no-ff dev
   git push origin main
   ```
3. Tag the release on `main` and publish a full **GitHub Release** with the
   version's changelog section as the body:
   ```bash
   git tag -a v0.9.0 -m "v0.9.0"
   git push origin v0.9.0
   gh release create v0.9.0 --target main \
     --title "v0.9.0" --notes "<paste the [0.9.0] section>"
   ```
4. Continue the next cycle on `dev` under the new `[Unreleased]`.

## Release notes style

Keep GitHub release bodies to the point — the changelog section, nothing extra.
Bullet what changed and why it matters to a user or operator; skip internal
churn, review back-and-forth, and marketing.

## Version bump rules (SemVer)

- **MAJOR** (`X`) — incompatible / breaking changes.
- **MINOR** (`Y`) — new features, backward compatible.
- **PATCH** (`Z`) — backward-compatible bug fixes only.
