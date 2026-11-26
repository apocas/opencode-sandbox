#!/bin/sh
set -e

# Where no ssh-agent on the host is possible (see the launcher), a sandbox gets
# its disposable key as a read-only file. ssh wants a copy only this user can
# read, and an agent inside the sandbox keeps `ssh -A` (jump hosts) working.
if [ -r /run/opencode-sandbox/key ]; then
  install -m 600 /run/opencode-sandbox/key "$HOME/.ssh/sandbox_key"
  install -m 644 /run/opencode-sandbox/key.pub "$HOME/.ssh/sandbox_key.pub"
  rm -f "$SSH_AUTH_SOCK"
  ssh-agent -a "$SSH_AUTH_SOCK" >/dev/null
  ssh-add -q "$HOME/.ssh/sandbox_key"
fi

# A sandbox is resumed with `docker start`, which reruns the command it was
# created with. For the plain opencode TUI, a resumed sandbox continues its
# last session instead of opening a blank one.
marker="$HOME/.local/state/opencode-sandbox/started"
if [ "$*" = opencode ] && [ -e "$marker" ]; then
  set -- opencode --continue
fi
mkdir -p "${marker%/*}" && : >"$marker"
exec "$@"
