#!/bin/sh
# A sandbox is resumed with `docker start`, which reruns the command it was
# created with. For the plain opencode TUI, a resumed sandbox continues its
# last session instead of opening a blank one.
set -e
marker="$HOME/.local/state/opencode-sandbox/started"
if [ "$*" = opencode ] && [ -e "$marker" ]; then
  set -- opencode --continue
fi
mkdir -p "${marker%/*}" && : >"$marker"
exec "$@"
