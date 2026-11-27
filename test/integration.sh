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
# The refusal of ~/.ssh is tested on it (CI runners have none).
[[ -d "$HOME/.ssh" ]] || mkdir -m 700 "$HOME/.ssh"

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
# pty KEYS CMD...: run CMD on a 120x30 terminal, pressing KEYS a second
# apart after 3 seconds: enter, space, down, up, esc, wait (2 more seconds),
# or text to type.
pty() {
  local keys="$1"; shift
  local cmd; cmd="stty cols 120 rows 30 2>/dev/null; exec $(printf '%q ' "$@")"
  {
    sleep 3
    for k in $keys; do
      case "$k" in
        enter) printf '\r' ;; space) printf ' ' ;; down) printf '\033[B' ;; up) printf '\033[A' ;;
        esc) printf '\033' ;; wait) sleep 2 ;; *) printf '%s' "$k" ;;
      esac
      sleep 1
    done
    sleep 20
  } | if [[ "$(uname)" == Darwin ]]; then script -q /dev/null bash -c "$cmd"; else script -qec "bash -c $(printf '%q' "$cmd")" /dev/null; fi \
    | tr -d '\r' | sed 's/\x1b\[[0-9;?]*[a-zA-Z]//g; s/\x1b[]P_][^\x07\x1b]*\(\x07\|\x1b\\\\\)//g'
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
"$BIN" health 2>/dev/null | grep -qE '^  used +Apple container' && ENGINE=apple
state() {
  if [[ "$ENGINE" == apple ]]; then
    container inspect "$1" 2>/dev/null | grep -o '"state" *: *"[a-z]*"' | head -1 | sed 's/.*"\([a-z]*\)"$/\1/; s/^stopped$/exited/'
  else
    docker inspect -f '{{.State.Status}}' "$1" 2>/dev/null
  fi
}
echo "== engine: $ENGINE"

echo "== basics"
check  "version" '^opencode-sandbox [0-9]+\.[0-9]+\.[0-9]+ \(opencode ' "$BIN" version
check  "help" 'Usage: opencode-sandbox' "$BIN" help
check  "help new" '-k KEY' "$BIN" help new
refuse "unknown command" 'unknown command: nope' "$BIN" nope
refuse "an old option names its command" '--list is now: opencode-sandbox ls' "$BIN" --list
refuse "no dashboard without a terminal" 'Usage: opencode-sandbox' "$BIN"
refuse "rm without names" 'needs sandbox names' "$BIN" rm
refuse "-n needs a value" '-n needs a name' "$BIN" new -n
refuse "unknown provider" "no provider 'bogus'" "$BIN" new "$T/ws" -n it-x -k none -p bogus
refuse "invalid name" 'invalid sandbox name' "$BIN" new "$T/ws" -n '-bad' -k none
refuse "home folder refused" 'contains your home directory' "$BIN" new "$HOME" -k none
refuse "~/.ssh refused" 'inside ~/.ssh' "$BIN" new "$HOME/.ssh" -k none
refuse "no key without a terminal" 'pass -k NAME' "$BIN" new "$T/ws" -n it-x
check  "ls empty" 'no sandboxes' "$BIN" ls
check  "health" 'used +(Docker|gVisor|Apple container)' "$BIN" health
if [[ "$ENGINE" == apple ]]; then
  DOCKER_HOST=unix:///nonexistent.sock check "health without Docker" "only sandboxes made with it need it" "$BIN" health
else
  DOCKER_HOST=unix:///nonexistent.sock refuse "health without Docker" "can't reach Docker" "$BIN" health
fi

echo "== create"
cd "$T/ws"
check "create with a disposable key" 'opencode v?[0-9]' "$BIN" new -n it-a -k new -- --version
[[ -f "$DATA/keys/it-a/id_ed25519" ]] && ok "disposable key in the data folder" || bad "disposable key in the data folder"
check "create with no key" 'opencode v?[0-9]' "$BIN" new . -n it-b -k none -- --version
check "ls" 'it-a +closed.*disposable' "$BIN" ls
runs_in=docker; [[ "${OPENCODE_SANDBOX_RUNTIME:-}" == *runsc* ]] && runs_in=gvisor; [[ "$ENGINE" == apple ]] && runs_in=apple
check "ls shows what it runs in" "it-a .* $runs_in " "$BIN" ls
if [[ -n "${OPENCODE_SANDBOX_RUNTIME:-}" ]]; then
  [[ "$(docker inspect -f '{{.HostConfig.Runtime}}' opencode-sandbox-it-a)" == "$OPENCODE_SANDBOX_RUNTIME" ]] \
    && ok "created with runtime $OPENCODE_SANDBOX_RUNTIME" || bad "created with runtime $OPENCODE_SANDBOX_RUNTIME"
fi
check "key" "$(cut -d' ' -f2 "$DATA/keys/it-a/id_ed25519.pub" | sed 's/[+/]/./g')" "$BIN" key it-a
refuse "key of a keyless sandbox" 'has no SSH key' "$BIN" key it-b
refuse "create an existing name" 'already exists' "$BIN" new -n it-a -k none

echo "== copy"
check  "cp" 'copied .*extra.txt into it-a' "$BIN" cp it-a "$T/extra.txt"
refuse "cp refuses ~/.ssh" 'inside ~/.ssh' "$BIN" cp it-a "$HOME/.ssh"
refuse "cp refuses the key folder" 'disposable keys' "$BIN" cp it-a "$DATA/keys"
cd "$T/out"
check  "get" 'copied it-a:/workspace/sub' "$BIN" get it-a a.txt extra.txt sub
[[ "$(cat "$T/out/a.txt" "$T/out/extra.txt")" == $'hello\nextra' && -d "$T/out/sub" ]] && ok "get contents" || bad "get contents"
refuse "get never overwrites" 'already exists' "$BIN" get it-a a.txt
refuse "get needs a name" 'name a file or folder' "$BIN" get it-a /
cd "$T/ws"

echo "== open and agents"
check  "open NAME" 'opencode v?[0-9]' "$BIN" open it-b
refuse "open without a name or terminal" 'open needs a sandbox name' "$BIN" open
check  "agents" 'no sandbox agents running|\(' "$BIN" agents
check  "agents stop" 'all sandbox ssh-agents stopped' "$BIN" agents stop

echo "== on a terminal"
rm -f "$DATA/known_hosts"
out="$(OPENCODE_SANDBOX_SEED_HOSTS="github.com" pty "enter" "$BIN" new -n it-c -- --version 2>&1)"
grep -q 'Which SSH key' <<<"$out" && grep -q 'opening sandbox' <<<"$out" && ok "key picker" || bad "key picker" "$out"
if ssh-keygen -F github.com -f "$HOME/.ssh/known_hosts" >/dev/null 2>&1; then
  [[ -s "$DATA/known_hosts" ]] && ok "seed hosts" || bad "seed hosts"
fi
out="$(OPENCODE_SANDBOX_CONFIG="$T/two.json" pty "space down space enter" "$BIN" new -n it-d -k none -- --version 2>&1)"
grep -q 'Which opencode providers' <<<"$out" && grep -q 'opening sandbox' <<<"$out" && ok "provider picker" || bad "provider picker" "$out"
check "both providers chosen" 'it-d .*(p1,p2|p2,p1)' "$BIN" ls
out="$(pty "enter" "$BIN" open 2>&1)"
grep -q 'Open which sandbox' <<<"$out" && grep -q 'opening sandbox' <<<"$out" && ok "sandbox picker" || bad "sandbox picker" "$out"
out="$(pty "h wait esc q" "$BIN" 2>&1)"
grep -q 'Sandboxes (4)' <<<"$out" && grep -q 'it-d' <<<"$out" && grep -q 'How a new sandbox is isolated' <<<"$out" \
  && ok "dashboard" || bad "dashboard" "$(tail -c 3000 <<<"$out")"

echo "== closing the terminal stops the sandbox"
pty "" "$BIN" new -n it-h -k none >/dev/null 2>&1 &
for _ in $(seq 60); do [[ "$(state opencode-sandbox-it-h)" == running ]] && break; sleep 1; done
pid="$(pgrep -f -- "$BIN new -n it-h" | head -1)"
if [[ -n "$pid" ]]; then
  kill -HUP "$pid"
  for _ in $(seq 30); do [[ "$(state opencode-sandbox-it-h)" == exited ]] && break; sleep 1; done
  [[ "$(state opencode-sandbox-it-h)" == exited ]] && ok "hangup stops the sandbox" || bad "hangup stops the sandbox"
else
  bad "hangup test: launcher not found"
fi
wait 2>/dev/null

echo "== delete"
out="$("$BIN" rm it-a it-b it-nope 2>&1)"
grep -q 'removed it-a and its disposable key SHA256' <<<"$out" && grep -q "no sandbox named 'it-nope'" <<<"$out" \
  && ok "rm" || bad "rm" "$out"
[[ ! -e "$DATA/keys/it-a" ]] && ok "disposable key deleted" || bad "disposable key deleted"
refuse "rm --all needs -y without a terminal" 'add -y' "$BIN" rm --all
check "rm --all -y" 'removed it-c' "$BIN" rm --all -y
check "ls empty again" 'no sandboxes' "$BIN" ls

if [[ -n "$OLD" && "$ENGINE" == docker ]]; then
  echo "== compatibility with $("$OLD" --version)"
  OLDDIR="$(cd "$(dirname "$OLD")" && pwd)"
  check "bash creates (disposable key)" 'opencode v?[0-9]' "$OLD" -n it-old-a -k new -- --version
  check "bash creates (no key)" 'opencode v?[0-9]' "$OLD" -n it-old-b -k none -- --version
  check "Go lists them" 'it-old-a +closed.*disposable' "$BIN" ls
  check "Go prints the bash key" "$(cut -d' ' -f2 "$OLDDIR/keys/it-old-a.pub" | sed 's/[+/]/./g')" "$BIN" key it-old-a
  check "Go opens a bash sandbox" 'opencode v?[0-9]' "$BIN" open it-old-a
  mv "$OLDDIR/known_hosts" "$OLDDIR/known_hosts.moved"
  refuse "Go explains a deleted clone" 'needs .*known_hosts, which no longer exists' "$BIN" open it-old-a
  mv "$OLDDIR/known_hosts.moved" "$OLDDIR/known_hosts"
  refuse "Go refuses the bash clone's keys" 'disposable keys' "$BIN" cp it-old-b "$OLDDIR/keys"
  check "Go copies into a bash sandbox" 'copied' "$BIN" cp it-old-b "$T/extra.txt"
  check "Go creates" 'opencode v?[0-9]' "$BIN" new -n it-new -k new -- --version
  check "bash lists a Go sandbox" 'it-new +disposable' "$OLD" --list
  check "bash resumes a Go sandbox" 'opencode v?[0-9]' "$OLD" --resume it-new
  check "Go deletes the bash sandboxes" 'removed it-old-a and its disposable key' "$BIN" rm it-old-a it-old-b it-new
  [[ ! -e "$OLDDIR/keys/it-old-a" ]] && ok "bash's disposable key deleted" || bad "bash's disposable key deleted"
fi

echo "== $pass passed, $fail failed"
(( fail == 0 ))
