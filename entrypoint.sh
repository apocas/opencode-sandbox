#!/bin/sh
set -e

# Where no ssh-agent on the host is possible (see the launcher), a sandbox gets
# its disposable key as a read-only file, or in a read-only folder where only
# folders can be mounted (Apple container). ssh wants a copy only this user
# can read, and an agent inside the sandbox keeps `ssh -A` (jump hosts)
# working.
key=/run/opencode-sandbox/key
[ -r "$key" ] || key=/run/opencode-sandbox/keydir/id_ed25519
if [ -r "$key" ]; then
  install -m 600 "$key" "$HOME/.ssh/sandbox_key"
  install -m 644 "$key.pub" "$HOME/.ssh/sandbox_key.pub"
  rm -f "$SSH_AUTH_SOCK"
  ssh-agent -a "$SSH_AUTH_SOCK" >/dev/null
  ssh-add -q "$HOME/.ssh/sandbox_key"
fi

# Where your providers' API keys stay on your machine, opencode reaches them
# through a gateway that the launcher starts in here right after the sandbox
# (see gateway.pl). Wait for it, so that opencode's first request finds it.
if [ -n "${OPENCODE_SANDBOX_GATEWAY:-}" ] &&
  ! opencode-sandbox-gateway wait "$OPENCODE_SANDBOX_GATEWAY" 10; then
  echo "opencode-sandbox: the gateway to your providers isn't up, so opencode can't reach them; close this sandbox and open it again" >&2
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
