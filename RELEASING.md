# Releasing

Releases are created via the **Release** workflow (`Actions → Release → Run workflow`).

## Steps

1. Ensure `main` is in the state you want to release.
2. Go to **Actions → Release → Run workflow** on `main`.
3. Enter the version number **without** the leading `v` (e.g. `0.2.0`).
4. Click **Run workflow**.

## What happens

| Stage | What it does |
|-------|-------------|
| `build-candidate` | Builds a multi-arch image (`linux/amd64`, `linux/arm64`) and pushes it to GHCR as `:rc-<version>` |
| `e2e` | Runs the full 20-scenario harness against the candidate image |
| `release` | Retags the candidate as `:v<version>` and `:latest`, creates the git tag, and publishes the GitHub release with auto-generated notes |

The git tag and published release are only created if e2e passes. The candidate image (`:rc-<version>`) is left in GHCR and can be deleted manually afterwards.
