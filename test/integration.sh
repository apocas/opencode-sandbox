#!/usr/bin/env bash
# End-to-end test of opencode-sandbox against a real Docker or Apple
# container (whichever new sandboxes go to; OPENCODE_SANDBOX_ENGINE picks).
# Creates, opens, copies into/out of and deletes sandboxes named it-*, with a
# throwaway opencode config (no real provider is called) and its own data
# folder.
#
# Usage: test/integration.sh BINARY [BASH_LAUNCHER]
#   BINARY         the opencode-sandbox binary to test
#   BASH_LAUNCHER  optional: the bash launcher of v0.4.0 (opencode-sandbox
#                  script in a checkout of that tag), to test that sandboxes
#                  made by either can be managed by the other
set -uo pipefail

BIN="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
OLD="${2:-}"
T="$(mktemp -d)"
T="$(cd "$T" && pwd -P)"
export XDG_DATA_HOME="$T/data" OPENCODE_SANDBOX_CONFIG="$T/one.json"
unset OPENCODE_SANDBOX_SEED_HOSTS OPENCODE_SANDBOX_LEGACY_DIR
DATA="$XDG_DATA_HOME/opencode-sandbox"
pass=0 fail=0

cat >"$T/one.json" <<'EOF'
{"provider":{"p1":{"name":"Fake","npm":"@ai-sdk/openai-compatible","models":{"m1":{}},"options":{"apiKey":"not-a-key","baseURL":"http://127.0.0.1:9/v1"}}}}
EOF
cat >"$T/two.json" <<'EOF'
{"provider":{"p1":{"name":"One","models":{"m1":{}}},"p2":{"name":"Two","models":{"m2":{},"m3":{}}}}}
EOF
mkdir -p "$T/ws/sub" "$T/out" && echo hello >"$T/ws/a.txt" && echo extra >"$T/extra.txt"

ok()   { pass=$((pass + 1)); echo "  ok    $1"; }
bad()  { fail=$((fail + 1)); echo "  FAIL  $1"; [[ -z "${2:-}" ]] || sed 's/^/        | /' <<<"$2" | tail -15; }
# check DESC PATTERN CMD...: CMD succeeds and its output matches PATTERN (ERE)
check() {
  local desc="$1" pat="$2" out; shift 2
  if out="$("$@" 2>&1 </dev/null)" && grep -qE -- "$pat" <<<"$out"; then ok "$desc"; else bad "$desc" "$out"; fi
}
# refuse DESC PATTERN CMD...: CMD fails with output matching PATTERN
refuse() {
  local desc="$1" pat="$2" out; shift 2
  if ! out="$("$@" 2>&1 </dev/null)" && grep -qE -- "$pat" <<<"$out"; then ok "$desc"; else bad "$desc" "$out"; fi
}
# pty INPUT CMD...: run CMD on a terminal, typing INPUT lines a second apart
pty() {
  local input="$1"; shift
  { sleep 3; while IFS= read -r l; do printf '%s\n' "$l"; sleep 1; done <<<"$input"; sleep 20; } \
    | if [[ "$(uname)" == Darwin ]]; then script -q /dev/null "$@"; else script -qec "$(printf '%q ' "$@")" /dev/null; fi \
    | tr -d '\r' | sed 's/\x1b\[[0-9;?]*[a-zA-Z]//g'
}
cleanup() {
  if command -v docker >/dev/null; then
    docker ps -a --format '{{.Names}}' | grep '^opencode-sandbox-it-' | xargs -r docker rm -f -v >/dev/null 2>&1
  fi
  if [[ "$ENGINE" == apple ]]; then
    for c in $(container list --all --quiet | grep '^opencode-sandbox-it-'); do
      container delete --force "$c" >/dev/null 2>&1
      container volume list --quiet | grep "^$c-" | xargs container volume delete >/dev/null 2>&1
      container network delete "$c" >/dev/null 2>&1
    done
  fi
  rm -rf "$T"
}
trap cleanup EXIT

# The engine new sandboxes go to, and a container's state in it ("running",
# "exited"…).
ENGINE=docker
"$BIN" --health 2>/dev/null | grep -qE '^  used +Apple container' && ENGINE=apple
state() {
  if [[ "$ENGINE" == apple ]]; then
    container inspect "$1" 2>/dev/null | grep -o '"state" *: *"[a-z]*"' | head -1 | sed 's/.*"\([a-z]*\)"$/\1/; s/^stopped$/exited/'
  else
    docker inspect -f '{{.State.Status}}' "$1" 2>/dev/null
  fi
}
echo "== engine: $ENGINE"

echo "== basics"
check  "--version" '^opencode-sandbox [0-9]+\.[0-9]+\.[0-9]+ \(opencode ' "$BIN" --version
check  "--help" 'Usage: opencode-sandbox' "$BIN" --help
refuse "unknown option" 'unknown option: --nope' "$BIN" --nope
refuse "--rm without names" 'needs sandbox names' "$BIN" --rm
refuse "-w needs a value" '-w needs a directory' "$BIN" -w
refuse "unknown provider" "no provider 'bogus'" "$BIN" -w "$T/ws" -n it-x -k none -p bogus
refuse "invalid name" 'invalid sandbox name' "$BIN" -w "$T/ws" -n '-bad' -k none
refuse "home folder refused" 'contains your home directory' "$BIN" -w "$HOME" -k none
refuse "~/.ssh refused" 'inside ~/.ssh' "$BIN" -w "$HOME/.ssh" -k none
check  "--list empty" 'no sandboxes' "$BIN" --list
check  "--health" 'used +(Docker|gVisor|Apple container)' "$BIN" --health
if [[ "$ENGINE" == apple ]]; then
  DOCKER_HOST=unix:///nonexistent.sock check "--health without Docker" "only sandboxes made with it need it" "$BIN" --health
else
  DOCKER_HOST=unix:///nonexistent.sock refuse "--health without Docker" "can't reach Docker" "$BIN" --health
fi

echo "== create"
cd "$T/ws"
check "create with a disposable key" 'opencode v?[0-9]' "$BIN" -n it-a -k new -- --version
[[ -f "$DATA/keys/it-a/id_ed25519" ]] && ok "disposable key in the data folder" || bad "disposable key in the data folder"
check "create with no key" 'opencode v?[0-9]' "$BIN" -n it-b -k none -- --version
check "--list" 'it-a +disposable.*closed' "$BIN" --list
runs_in=docker; [[ "${OPENCODE_SANDBOX_RUNTIME:-}" == *runsc* ]] && runs_in=gvisor; [[ "$ENGINE" == apple ]] && runs_in=apple
check "--list shows what it runs in" "it-a .* $runs_in " "$BIN" --list
if [[ -n "${OPENCODE_SANDBOX_RUNTIME:-}" ]]; then
  [[ "$(docker inspect -f '{{.HostConfig.Runtime}}' opencode-sandbox-it-a)" == "$OPENCODE_SANDBOX_RUNTIME" ]] \
    && ok "created with runtime $OPENCODE_SANDBOX_RUNTIME" || bad "created with runtime $OPENCODE_SANDBOX_RUNTIME"
fi
check "--pubkey" "$(cut -d' ' -f2 "$DATA/keys/it-a/id_ed25519.pub" | sed 's/[+/]/./g')" "$BIN" --pubkey it-a
refuse "--pubkey of a keyless sandbox" 'has no SSH key' "$BIN" --pubkey it-b
refuse "create an existing name" 'already exists' "$BIN" -n it-a -k none

echo "== copy"
check  "--cp" 'copied .*extra.txt into it-a' "$BIN" --cp it-a "$T/extra.txt"
refuse "--cp refuses ~/.ssh" 'inside ~/.ssh' "$BIN" --cp it-a "$HOME/.ssh"
refuse "--cp refuses the key folder" 'disposable keys' "$BIN" --cp it-a "$DATA/keys"
cd "$T/out"
check  "--get" 'copied it-a:/workspace/sub' "$BIN" --get it-a a.txt extra.txt sub
[[ "$(cat "$T/out/a.txt" "$T/out/extra.txt")" == $'hello\nextra' && -d "$T/out/sub" ]] && ok "--get contents" || bad "--get contents"
refuse "--get never overwrites" 'already exists' "$BIN" --get it-a a.txt
refuse "--get needs a name" 'name a file or folder' "$BIN" --get it-a /
cd "$T/ws"

echo "== resume and agents"
check "--resume NAME" 'opencode v?[0-9]' "$BIN" --resume it-b
out="$(printf '1\n' | "$BIN" --resume 2>&1)" && grep -qE 'opening sandbox' <<<"$out" && ok "--resume from the list" || bad "--resume from the list" "$out"
check "--status" 'no sandbox agents running|sandbox ' "$BIN" --status
check "--stop-agent" 'all sandbox ssh-agents stopped' "$BIN" --stop-agent

echo "== menus on a terminal"
rm -f "$DATA/known_hosts"
out="$(OPENCODE_SANDBOX_SEED_HOSTS="github.com" pty "2" "$BIN" -n it-c -- --version 2>&1)"
grep -q 'Which SSH key' <<<"$out" && grep -q 'opening sandbox' <<<"$out" && ok "key menu" || bad "key menu" "$out"
if ssh-keygen -F github.com -f "$HOME/.ssh/known_hosts" >/dev/null 2>&1; then
  [[ -s "$DATA/known_hosts" ]] && ok "seed hosts" || bad "seed hosts"
fi
out="$(OPENCODE_SANDBOX_CONFIG="$T/two.json" pty $'\n9\n2,1' "$BIN" -n it-d -k none -- --version 2>&1)"
grep -q 'Which opencode provider' <<<"$out" && grep -q 'opening sandbox' <<<"$out" && ok "provider menu" || bad "provider menu" "$out"
check "both providers chosen" 'it-d .*p2,p1' "$BIN" --list

echo "== closing the terminal stops the sandbox"
pty "" "$BIN" -n it-h -k none >/dev/null 2>&1 &
for _ in $(seq 60); do [[ "$(state opencode-sandbox-it-h)" == running ]] && break; sleep 1; done
pid="$(pgrep -f -- "$BIN -n it-h" | head -1)"
if [[ -n "$pid" ]]; then
  kill -HUP "$pid"
  for _ in $(seq 30); do [[ "$(state opencode-sandbox-it-h)" == exited ]] && break; sleep 1; done
  [[ "$(state opencode-sandbox-it-h)" == exited ]] && ok "hangup stops the sandbox" || bad "hangup stops the sandbox"
else
  bad "hangup test: launcher not found"
fi
wait 2>/dev/null

echo "== delete"
check "--rm" 'removed it-a and its disposable key SHA256' "$BIN" --rm it-a it-b it-nope
[[ ! -e "$DATA/keys/it-a" ]] && ok "disposable key deleted" || bad "disposable key deleted"
out="$(printf 'y\n' | "$BIN" --rm --all 2>&1)" && grep -q 'removed it-c' <<<"$out" && ok "--rm --all" || bad "--rm --all" "$out"
check "--list empty again" 'no sandboxes' "$BIN" --list

if [[ -n "$OLD" && "$ENGINE" == docker ]]; then
  echo "== compatibility with $("$OLD" --version)"
  OLDDIR="$(cd "$(dirname "$OLD")" && pwd)"
  check "bash creates (disposable key)" 'opencode v?[0-9]' "$OLD" -n it-old-a -k new -- --version
  check "bash creates (no key)" 'opencode v?[0-9]' "$OLD" -n it-old-b -k none -- --version
  check "Go lists them" 'it-old-a +disposable' "$BIN" --list
  check "Go prints the bash key" "$(cut -d' ' -f2 "$OLDDIR/keys/it-old-a.pub" | sed 's/[+/]/./g')" "$BIN" --pubkey it-old-a
  check "Go resumes a bash sandbox" 'opencode v?[0-9]' "$BIN" --resume it-old-a
  mv "$OLDDIR/known_hosts" "$OLDDIR/known_hosts.moved"
  refuse "Go explains a deleted clone" 'needs .*known_hosts, which no longer exists' "$BIN" --resume it-old-a
  mv "$OLDDIR/known_hosts.moved" "$OLDDIR/known_hosts"
  refuse "Go refuses the bash clone's keys" 'disposable keys' "$BIN" --cp it-old-b "$OLDDIR/keys"
  check "Go copies into a bash sandbox" 'copied' "$BIN" --cp it-old-b "$T/extra.txt"
  check "Go creates" 'opencode v?[0-9]' "$BIN" -n it-new -k new -- --version
  check "bash lists a Go sandbox" 'it-new +disposable' "$OLD" --list
  check "bash resumes a Go sandbox" 'opencode v?[0-9]' "$OLD" --resume it-new
  check "Go deletes the bash sandboxes" 'removed it-old-a and its disposable key' "$BIN" --rm it-old-a it-old-b it-new
  [[ ! -e "$OLDDIR/keys/it-old-a" ]] && ok "bash's disposable key deleted" || bad "bash's disposable key deleted"
fi

echo "== $pass passed, $fail failed"
(( fail == 0 ))
