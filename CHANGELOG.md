# Changelog

All notable changes to opencode-sandbox. Versions follow
[Semantic Versioning](https://semver.org); each release is tagged `vX.Y.Z`.
To update, see [Updating](README.md#updating).

## [0.2.0] - 2026-11-26

### Added

- macOS, Docker Desktop, rootless Docker and userns-remap are now supported
  in a no-agent mode, instead of being refused. A sandbox there can't reach an
  ssh-agent on your machine, so it gets only a new disposable key or none; keys
  from `~/.ssh` are never offered. The disposable key's private part is stored
  inside the sandbox (you're warned when it's created), and an ssh-agent inside
  the sandbox keeps `ssh -A` working. `--list` marks these keys with `*`.
- `OPENCODE_SANDBOX_NO_AGENT=1` forces the no-agent mode anywhere.
- Clear errors for bash older than 4.4 (macOS ships 3.2: `brew install bash`)
  and a missing `jq`.

### Changed

- Runtime files go to `$TMPDIR/opencode-sandbox-UID` where `$XDG_RUNTIME_DIR`
  isn't set (as on macOS), and the launcher refuses a runtime directory that
  isn't yours.
- Generated opencode configs are readable inside sandboxes even where Docker
  maps your files to another uid; their directory stays private to you.
- ssh in a sandbox uses `~/.ssh/sandbox_key` (a disposable key) without `-i`.
- Closing the terminal still stops the sandbox where `setsid` isn't available
  (macOS).

Not yet tested on a Mac; the no-agent mode was tested on Linux.

## [0.1.1] - 2026-11-26

### Fixed

- The image failed to build when your gid already exists in Debian, for
  example macOS's gid 20 or gid 100 (`users`) on some Linux systems. That group
  is now reused. A uid that belongs to a system user is refused with a clear
  message.

### Added

- The launcher now stops with an explanation on setups it can't support:
  macOS and other non-Linux systems, Docker Desktop, rootless Docker,
  userns-remap, running as root, and Docker not being reachable. `--help` and
  `--version` still work everywhere.

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

[0.2.0]: https://github.com/apocas/opencode-sandbox/releases/tag/v0.2.0
[0.1.1]: https://github.com/apocas/opencode-sandbox/releases/tag/v0.1.1
[0.1.0]: https://github.com/apocas/opencode-sandbox/releases/tag/v0.1.0
