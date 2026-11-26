# Changelog

All notable changes to opencode-sandbox. Versions follow
[Semantic Versioning](https://semver.org); each release is tagged `vX.Y.Z`.
To update, see [Updating](README.md#updating).

## [0.1.0] - 2026-11-26

First release.

### Added

- A new sandbox (Docker container) on every run, with a copy of a folder at
  `/workspace`. The folder is copied, never mounted.
- `--list`, `--resume` (continues the sandbox's last opencode session),
  `--shell`, `--rm` and `--rm --all` to manage sandboxes.
- One SSH key per sandbox: an existing key pair from `~/.ssh`, a new disposable
  key, or none. A dedicated host ssh-agent holds only that key and only its
  socket is mounted in, so the private key never enters the sandbox. The agent
  stops when the sandbox closes.
- Disposable keys, kept outside `~/.ssh`, shown with `--pubkey` and deleted
  with their sandbox.
- Provider selection: each sandbox gets a filtered opencode config with only
  the chosen provider(s), and opencode's built-in providers are switched off.
- Hardened container: non-root user, read-only root filesystem, no
  capabilities, `no-new-privileges`, PID limit, and a network on which
  sandboxes can't reach each other.
- Closing the terminal stops the sandbox; `ctrl-]` `ctrl-]` detaches.
- `--version`; images are tagged with the opencode-sandbox version, and
  `--resume` warns about sandboxes created by an older version.
- Bundles opencode 2.4.2 (x86_64 and arm64 builds, checksums verified).

[0.1.0]: https://github.com/apocas/opencode-sandbox/releases/tag/v0.1.0
