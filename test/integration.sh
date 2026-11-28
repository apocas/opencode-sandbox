#!/usr/bin/env bash
# End-to-end test of opencode-sandbox against a real Docker or Apple
# container (whichever new sandboxes go to; OPENCODE_SANDBOX_ENGINE picks).
# Creates, opens, copies into/out of and deletes sandboxes named it-*, with a
# throwaway opencode config (its provider is test/provider.py, a stand-in on
# this machine; no real one is called) and its own data folder. It touches no other sandbox: where you have some, the steps that
# act on "all" or "the first" sandbox are skipped.
#
# Usage: [EXPECT_RUNTIME=NAME] [IT_OWN_ONLY=1] test/integration.sh BINARY
#   IT_OWN_ONLY    never act on "all" sandboxes, even where there are none
#                  but the test's: for a machine where you may make one
#                  while this runs
#   EXPECT_RUNTIME the Docker runtime new sandboxes must get without being
#                  asked for one (runsc-ssh after `opencode-sandbox setup`)
#   BINARY         the opencode-sandbox binary to test
set -uo pipefail

BIN="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
T="$(mktemp -d)"
T="$(cd "$T" && pwd -P)"
export XDG_DATA_HOME="$T/data" OPENCODE_SANDBOX_CONFIG="$T/one.json"
unset OPENCODE_SANDBOX_SEED_HOSTS OPENCODE_SANDBOX_LEGACY_DIR
DATA="$XDG_DATA_HOME/opencode-sandbox"
pass=0 fail=0

# The provider: a stand-in that records what it's sent.
REAL_KEY=it-real-key-4c1f MCP_KEY=it-mcp-secret-9d2e
python3 "$(dirname "$0")/provider.py" "$T/provider.log" "$T/provider.port" &
PROVIDER=$!
disown "$PROVIDER"
for _ in $(seq 50); do [[ -s "$T/provider.port" ]] && break; sleep 0.1; done
cat >"$T/one.json" <<EOF
{"model":"p1/m1","provider":{"p1":{"name":"Fake","npm":"@ai-sdk/openai-compatible","models":{"m1":{}},"options":{"apiKey":"$REAL_KEY","baseURL":"http://127.0.0.1:$(cat "$T/provider.port")/v1"}}},
 "mcp":{"tools":{"type":"remote","url":"http://127.0.0.1:$(cat "$T/provider.port")/mcp","headers":{"X-Mcp-Key":"$MCP_KEY"}}}}
EOF
# An MCP server that runs inside a sandbox, with a secret of its own.
cat >"$T/local.json" <<'EOF'
{"provider":{"p1":{"name":"One","models":{"m1":{}}}},"mcp":{"servers":{"files":{"type":"local","command":["true"],"environment":{"FILES_TOKEN":"t"}}}}}
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
# flat CMD...: CMD's output as one line (health wraps its lines)
flat() { "$@" 2>&1 | tr -s ' \n' ' '; }
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
  kill "$PROVIDER" 2>/dev/null
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
# xc NAME CMD...: run CMD inside an open sandbox
xc() {
  local c="opencode-sandbox-$1"; shift
  if [[ "$ENGINE" == apple ]]; then container exec "$c" "$@"; else docker exec "$c" "$@"; fi
}
echo "== engine: $ENGINE"

# Sandboxes that aren't this test's. With any around, `rm --all` and picking
# the first sandbox of the list are not run: they would hit yours.
names() { "$BIN" ls 2>/dev/null | awk 'NR > 1 && $1 != "*" && $2 != "" { print $2 }'; }
FOREIGN="$(names | grep -vc '^it-' || true)"
# ALL: it may act on every sandbox there is. Not with sandboxes of yours
# around, nor with IT_OWN_ONLY set: for a machine where you may make one
# while this runs.
ALL=1; why="you have $FOREIGN sandboxes of your own here"
(( FOREIGN == 0 )) || ALL=0
[[ -z "${IT_OWN_ONLY:-}" ]] || { ALL=0; why="IT_OWN_ONLY is set"; }
skip() { echo "  skip  $1 ($why)"; }
# none DESC: no it-* sandbox is listed
none() {
  if names | grep -q '^it-'; then bad "$1" "$("$BIN" ls 2>&1)"; else ok "$1"; fi
}

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
if (( ALL )); then check "ls empty" 'no sandboxes' "$BIN" ls; else none "ls has none of the test's"; fi
check  "health" 'used +(Docker|gVisor|Apple container)' "$BIN" health
check  "health: API keys stay on this machine" 'api keys +stay on this machine' "$BIN" health
OPENCODE_SANDBOX_GATEWAY=off check "health: unless the gateway is off" '\(OPENCODE_SANDBOX_GATEWAY is off\)' "$BIN" health
if [[ "$ENGINE" == apple ]]; then
  DOCKER_HOST=unix:///nonexistent.sock check "health without Docker" "only sandboxes made with it need it" "$BIN" health
else
  DOCKER_HOST=unix:///nonexistent.sock refuse "health without Docker" "can't reach Docker" "$BIN" health
fi
# setup only shows what it would do, and asks first: nothing changes here.
if "$BIN" health 2>/dev/null | grep -q '^Next step'; then
  refuse "setup shows its plan and asks first" 'add -y to go ahead' "$BIN" setup
else
  check "setup has nothing to install" 'nothing to set up|unset it|the most|needs|rootless|no build' "$BIN" setup
fi

echo "== create"
cd "$T/ws"
check "create with a disposable key" 'opencode v?[0-9]' "$BIN" new -n it-a -k new -- --version
[[ -f "$DATA/keys/it-a/id_ed25519" ]] && ok "disposable key in the data folder" || bad "disposable key in the data folder"
check "create with no key" 'opencode v?[0-9]' "$BIN" new . -n it-b -k none -- --version
check "ls" 'it-a +closed.*disposable' "$BIN" ls
# health says what new sandboxes run in: gVisor is used where Docker has it.
runs_in=docker; "$BIN" health 2>/dev/null | grep -q '^  boundary   gVisor' && runs_in=gvisor; [[ "$ENGINE" == apple ]] && runs_in=apple
check "ls shows what it runs in" "it-a .* $runs_in " "$BIN" ls
want_rt="${EXPECT_RUNTIME:-${OPENCODE_SANDBOX_RUNTIME:-}}"
if [[ -n "$want_rt" ]]; then
  [[ "$(docker inspect -f '{{.HostConfig.Runtime}}' opencode-sandbox-it-a)" == "$want_rt" ]] \
    && ok "created with runtime $want_rt" || bad "created with runtime $want_rt"
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
# (stopping every agent would cut SSH in sandboxes of yours that are open)
if (( ALL )); then check "agents stop" 'all sandbox ssh-agents stopped' "$BIN" agents stop; else skip "agents stop"; fi

echo "== on a terminal"
rm -f "$DATA/known_hosts"
# (Afterwards, on the same terminal: is it still in blocking mode? An
# engine's client may leave it otherwise, and what's drawn next is cut short.)
cat >"$T/blocking.py" <<'EOF'
import fcntl, os
print("terminal:", "NON-BLOCKING" if fcntl.fcntl(0, fcntl.F_GETFL) & os.O_NONBLOCK else "blocking")
EOF
out="$(OPENCODE_SANDBOX_SEED_HOSTS="github.com" pty "enter" bash -c "\"$BIN\" new -n it-c -- --version; python3 \"$T/blocking.py\"" 2>&1)"
grep -q 'Which SSH key' <<<"$out" && grep -q 'opening sandbox' <<<"$out" && ok "key picker" || bad "key picker" "$out"
grep -q 'terminal: blocking' <<<"$out" && ok "the terminal is left in blocking mode" || bad "the terminal is left in blocking mode" "$(tail -c 600 <<<"$out")"
if ssh-keygen -F github.com -f "$HOME/.ssh/known_hosts" >/dev/null 2>&1; then
  [[ -s "$DATA/known_hosts" ]] && ok "seed hosts" || bad "seed hosts"
fi
out="$(OPENCODE_SANDBOX_CONFIG="$T/two.json" pty "space down space enter" "$BIN" new -n it-d -k none -- --version 2>&1)"
grep -q 'Which opencode providers' <<<"$out" && grep -q 'opening sandbox' <<<"$out" && ok "provider picker" || bad "provider picker" "$out"
check "both providers chosen" 'it-d .*(p1,p2|p2,p1)' "$BIN" ls
if (( ALL )); then
  out="$(pty "enter" "$BIN" open 2>&1)"
  grep -q 'Open which sandbox' <<<"$out" && grep -q 'opening sandbox' <<<"$out" && ok "sandbox picker" || bad "sandbox picker" "$out"
else
  skip "sandbox picker"
fi
# The dashboard: the list, the health report and back, the new-sandbox form
# and back without creating one, then quit.
listed="$(names | grep -c . || true)"
out="$(pty "h wait esc n wait esc q" "$BIN" 2>&1)"
grep -q "Sandboxes ($listed)" <<<"$out" && grep -q 'it-d' <<<"$out" && grep -q 'How a new sandbox is isolated' <<<"$out" \
  && grep -q 'a copy of your folder · a key of its own · no API keys' <<<"$out" \
  && ok "dashboard" || bad "dashboard" "$(tail -c 3000 <<<"$out")"
grep -q 'Folder to copy' <<<"$out" && grep -q 'no sandbox created' <<<"$out" \
  && ok "dashboard: esc leaves the new-sandbox form" || bad "dashboard: esc leaves the new-sandbox form" "$(tail -c 3000 <<<"$out")"
[[ "$(names | grep -c '^it-')" == 4 ]] && ok "the form created nothing" || bad "the form created nothing" "$("$BIN" ls)"

echo "== the gateway, and closing the terminal"
pty "" "$BIN" new -n it-h -k none --memory 2g --cpus 2 >/dev/null 2>&1 &
job=$!
for _ in $(seq 60); do [[ "$(state opencode-sandbox-it-h)" == running ]] && break; sleep 1; done
# The sandbox's opencode config and what it was started with: the real key is
# in neither, and the provider's address is the gateway inside the sandbox.
CFG='"${OPENCODE_CONFIG:-$HOME/.config/opencode/opencode.json}"'
out="$(xc it-h sh -c "cat $CFG; echo; env" 2>&1)"
grep -q '"baseURL": "http://127.0.0.1:17872/provider/p1"' <<<"$out" && ! grep -q "$REAL_KEY" <<<"$out" \
  && ok "the API key isn't in the sandbox" || bad "the API key isn't in the sandbox" "$(sed "s/$REAL_KEY/<THE REAL KEY>/g" <<<"$out" | head -30)"
# Nor is the header your config sets for an MCP server, or the server's address.
grep -q '"url": "http://127.0.0.1:17872/mcp/tools"' <<<"$out" && ! grep -q "$MCP_KEY" <<<"$out" \
  && ok "an MCP server's secret isn't in the sandbox" || bad "an MCP server's secret isn't in the sandbox" "$(sed "s/$MCP_KEY/<THE REAL SECRET>/g" <<<"$out" | head -30)"
# A request as opencode makes it, with what its config has for a key.
auth="-H \"Authorization: Bearer \$(jq -r .provider.p1.options.apiKey $CFG)\""
: >"$T/provider.log"
out="$(xc it-h sh -c "curl -sS -m 20 $auth http://127.0.0.1:17872/provider/p1/models" 2>&1)"
grep -q '"id": "m1"' <<<"$out" && grep -q "\"authorization\": \"Bearer $REAL_KEY\"" "$T/provider.log" \
  && ok "the gateway puts the key into a request" || bad "the gateway puts the key into a request" "$out"$'\n'"$(cat "$T/provider.log")"
# An address as a sandbox's config had it before 0.13.0 still works.
check "an older config's address is served too" '"id": "m1"' xc it-h sh -c "curl -sS -m 20 $auth http://127.0.0.1:17872/p1/models"
# The MCP server, as opencode asks it: with what its config has for the header.
: >"$T/provider.log"
mcp="-H \"X-Mcp-Key: \$(jq -r '.mcp.tools.headers[\"X-Mcp-Key\"]' $CFG)\" -H 'Content-Type: application/json'"
out="$(xc it-h sh -c "curl -sS -m 20 $mcp -d '{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}' http://127.0.0.1:17872/mcp/tools" 2>&1)"
grep -q '"name": "echo"' <<<"$out" && grep -q "\"path\": \"/mcp\", \"headers\": {.*\"x-mcp-key\": \"$MCP_KEY\"" "$T/provider.log" \
  && ok "the gateway puts an MCP server's header into a request" || bad "the gateway puts an MCP server's header into a request" "$out"$'\n'"$(cat "$T/provider.log")"
# Ending a session is a DELETE: passed on to an MCP server, never to a provider.
out="$(xc it-h sh -c "curl -sS -m 20 -o /dev/null -w '%{http_code} ' -X DELETE $mcp http://127.0.0.1:17872/mcp/tools
  curl -sS -m 20 -o /dev/null -w '%{http_code} ' -X DELETE $auth http://127.0.0.1:17872/provider/p1/models
  curl -sS -m 20 -o /dev/null -w '%{http_code}' $mcp http://127.0.0.1:17872/mcp/nope" 2>&1)"
[[ "$out" == "200 405 404" ]] && ok "DELETE reaches an MCP server only" || bad "DELETE reaches an MCP server only" "$out"
out="$(xc it-h sh -c "curl -sS -m 20 -N $auth -H 'Content-Type: application/json' -d '{\"model\":\"m1\",\"stream\":true}' http://127.0.0.1:17872/provider/p1/chat/completions" 2>&1)"
grep -q 'data: \[DONE\]' <<<"$out" && ok "an answer is streamed back" || bad "an answer is streamed back" "$out"
# The key goes into the header keys travel in and nowhere else, and only to
# this sandbox's provider.
: >"$T/provider.log"
out="$(xc it-h sh -c "curl -sS -m 20 -o /dev/null -H 'Authorization: Bearer nope' -H \"X-Debug: \$(jq -r .provider.p1.options.apiKey $CFG)\" http://127.0.0.1:17872/provider/p1/models
  curl -sS -m 20 -o /dev/null -w '%{http_code} ' --path-as-is http://127.0.0.1:17872/provider/p1/../x
  curl -sS -m 20 -o /dev/null -w '%{http_code} ' http://127.0.0.1:17872/provider/p2/models
  curl -sS -m 20 -o /dev/null -w '%{http_code}' -X TRACE $auth http://127.0.0.1:17872/provider/p1/models" 2>&1)"
[[ "$out" == "400 404 405" ]] && grep -q '"Bearer nope"' "$T/provider.log" && ! grep -q "$REAL_KEY" "$T/provider.log" \
  && ok "the key goes nowhere else" || bad "the key goes nowhere else" "$out"$'\n'"$(cat "$T/provider.log")"
pgrep -f -- "$BIN --internal-gateway it-h" >/dev/null && ok "its gateway runs while it's open" || bad "its gateway runs while it's open"
# Its limits (2 GB, 2 CPUs), seen from inside: what its cgroup may use
# (Docker), or all there is where it has a kernel of its own (gVisor, a VM).
out="$(xc it-h sh -c 'cat /sys/fs/cgroup/memory.max /sys/fs/cgroup/memory/memory.limit_in_bytes 2>/dev/null
  awk "/MemTotal/ { print \$2 }" /proc/meminfo; echo; cat /sys/fs/cgroup/cpu.max 2>/dev/null; nproc' 2>&1)"
mem="${out%%$'\n\n'*}" cpu="${out#*$'\n\n'}" held=0
for v in $mem; do [[ "$v" =~ ^[0-9]+$ ]] && (( v == 2147483648 || (v > 1048576 && v <= 2097152) )) && held=1; done
(( held )) && ok "inside, it has no more memory than its limit" || bad "inside, it has no more memory than its limit" "$out"
grep -qxE '200000 100000|2' <<<"$cpu" && ok "nor more CPUs" || bad "nor more CPUs" "$out"
pid="$(pgrep -f -- "$BIN new -n it-h" | head -1)"
if [[ -n "$pid" ]]; then
  kill -HUP "$pid"
  for _ in $(seq 30); do [[ "$(state opencode-sandbox-it-h)" == exited ]] && break; sleep 1; done
  [[ "$(state opencode-sandbox-it-h)" == exited ]] && ok "hangup stops the sandbox" || bad "hangup stops the sandbox"
else
  bad "hangup test: launcher not found"
fi
wait "$job" 2>/dev/null
for _ in $(seq 20); do pgrep -f -- "$BIN --internal-gateway it-h" >/dev/null || break; sleep 0.5; done
pgrep -f -- "$BIN --internal-gateway it-h" >/dev/null && bad "its gateway ends with it" || ok "its gateway ends with it"
# opencode itself, asking the provider one question through the gateway.
: >"$T/provider.log"
check "opencode reaches its provider" 'the gateway works' "$BIN" new -n it-q -k none -- run hello
grep -q "\"path\": \"/v1/chat/completions\", \"headers\": {\"authorization\": \"Bearer $REAL_KEY\"" "$T/provider.log" \
  && ok "with the key put in on this machine" || bad "with the key put in on this machine" "$(cat "$T/provider.log")"
grep -q "\"method\": \"POST\", \"path\": \"/mcp\", \"headers\": {.*\"x-mcp-key\": \"$MCP_KEY\"" "$T/provider.log" \
  && ok "opencode reaches its MCP server the same way" || bad "opencode reaches its MCP server the same way" "$(cat "$T/provider.log")"
# Without a gateway, as before 0.9.0, the key is in the sandbox: ls says so.
OPENCODE_SANDBOX_GATEWAY=off check "create without a gateway" 'opencode v?[0-9]' "$BIN" new -n it-k -k none -- --version
check "ls marks an API key stored inside" 'it-k .* p1\* ' "$BIN" ls
check "ls: and only there" 'it-q .* p1 ' "$BIN" ls
check "ls explains the mark" "\* an API key or an MCP server's secret is stored inside the sandbox" "$BIN" ls
# An MCP server that runs inside a sandbox takes its secrets with it: health
# says which; and how many servers' secrets stay behind the gateway.
OPENCODE_SANDBOX_CONFIG="$T/local.json" check "health names an MCP server's secret that goes in" \
  'Except a secret of MCP server files, which goes into the sandbox: it runs inside the sandbox, with FILES_TOKEN in its environment' flat "$BIN" health
check "health counts the MCP servers behind the gateway" 'So do the headers and address of 1 MCP server in your opencode config' flat "$BIN" health

echo "== dot files"
# What often holds secrets is named before a folder is copied; what never
# does (.gitignore) isn't. On a terminal, it waits for enter first.
mkdir -p "$T/dots/app" && echo 'KEY=1' >"$T/dots/.env" && echo 'x' >"$T/dots/.gitignore" && echo 'KEY=2' >"$T/dots/app/.env.local"
out="$("$BIN" new "$T/dots" -n it-e -k none -- --version 2>&1 </dev/null)"
grep -q 'has dot files, which often hold secrets.*: \.env, app/\.env\.local\. Anything' <<<"$out" && ! grep -q 'gitignore' <<<"$out" && grep -qE 'opencode v?[0-9]' <<<"$out" \
  && ok "new names the dot files it copies" || bad "new names the dot files it copies" "$out"
out="$("$BIN" new "$T/ws" -n it-f -k none -- --version 2>&1 </dev/null)"
grep -q 'dot files' <<<"$out" && bad "no warning without dot files" "$out" || ok "no warning without dot files"
out="$(pty "wait enter" "$BIN" new "$T/dots" -n it-g -k none -- --version 2>&1)"
grep -q 'Press enter to copy it as it is' <<<"$out" && grep -q 'opening sandbox' <<<"$out" && ok "on a terminal it asks first" || bad "on a terminal it asks first" "$out"
check "cp names them too" 'is a dot file, which often holds secrets' "$BIN" cp it-f "$T/dots/.env"

echo "== nothing copied"
# --empty: an empty /workspace, though this runs in a folder with a.txt in
# it; files can still be copied in later.
mkdir -p "$T/out2"
out="$("$BIN" new --empty -n it-z -k none -- --version 2>&1 </dev/null)"
grep -q "created sandbox 'it-z', with nothing copied into it" <<<"$out" && grep -qE 'opencode v?[0-9]' <<<"$out" \
  && ok "a sandbox with no folder" || bad "a sandbox with no folder" "$out"
check "ls says where it's from" 'it-z .* nothing *$' "$BIN" ls
cd "$T/out2"
refuse "nothing was copied" 'could not copy' "$BIN" get it-z a.txt
check "files can be copied in later" 'copied .*extra.txt into it-z' "$BIN" cp it-z "$T/extra.txt"
check "and out again" 'copied it-z:/workspace/extra.txt' "$BIN" get it-z extra.txt
cd "$T/ws"
refuse "a folder and --empty at once" 'or --empty, not both' "$BIN" new "$T/ws" --empty -n it-x -k none

echo "== without a network, and where it runs"
# No network: opencode still reaches its provider, through the gateway, and
# needs no SSH key (none is asked for, even without a terminal).
: >"$T/provider.log"
check "a sandbox without a network reaches its provider" 'the gateway works' "$BIN" new "$T/ws" -n it-m --network none -- run hello
grep -q "\"authorization\": \"Bearer $REAL_KEY\"" "$T/provider.log" && ok "through the gateway" || bad "through the gateway" "$(cat "$T/provider.log")"
if [[ "$ENGINE" == apple ]]; then
  container inspect opencode-sandbox-it-m | tr -d ' \n' | grep -q '"networks":\[\]' && ok "it is on no network" || bad "it is on no network"
else
  [[ "$(docker inspect -f '{{.HostConfig.NetworkMode}}' opencode-sandbox-it-m)" == none ]] && ok "it is on no network" || bad "it is on no network"
fi
check "ls shows that" 'it-m .* none +none ' "$BIN" ls
check "ls: the others are open" 'it-q .* open ' "$BIN" ls
OPENCODE_SANDBOX_NETWORK=none check "none can be the default" 'opencode v?[0-9]' "$BIN" new "$T/ws" -n it-o -- --version
check "ls shows the default's too" 'it-o .* none +none ' "$BIN" ls
OPENCODE_SANDBOX_NETWORK=none check "health names the default" 'network +none \(OPENCODE_SANDBOX_NETWORK\)' "$BIN" health
refuse "no SSH key without a network" 'gets no SSH key' "$BIN" new "$T/ws" -n it-x --network none -k new
refuse "no network without the gateway" 'with the gateway off' "$BIN" new "$T/ws" -n it-x --network none --gateway off
refuse "an unknown network" 'must be open or none' "$BIN" new "$T/ws" -n it-x --network lan -k none
refuse "an unknown engine" '--engine must be docker or apple' "$BIN" new "$T/ws" -n it-x --engine podman -k none
refuse "an unknown gateway choice" '--gateway must be on or off' "$BIN" new "$T/ws" -n it-x --gateway maybe -k none
# Where it runs, asked for: the engine in use, by name; and in Docker a runtime.
check "the engine asked for" 'opencode v?[0-9]' "$BIN" new "$T/ws" -n it-r --engine "$ENGINE" --gateway off -k none -- --version
check "ls: and without a gateway, as asked" 'it-r .* p1\* ' "$BIN" ls
if [[ "$ENGINE" == apple ]]; then
  refuse "a Docker runtime where it goes to Apple container" '--runtime runc chooses a Docker runtime' "$BIN" new "$T/ws" -n it-x --runtime runc -k none
else
  check "the runtime asked for" 'opencode v?[0-9]' "$BIN" new "$T/ws" -n it-s --runtime runc -k none -- --version
  [[ "$(docker inspect -f '{{.HostConfig.Runtime}}' opencode-sandbox-it-s)" == runc ]] && ok "created with it" || bad "created with it"
  refuse "a runtime Docker hasn't" '--runtime nope, but Docker has no runtime by that name' "$BIN" new "$T/ws" -n it-x --runtime nope -k none
fi

echo "== limits"
# What the engine holds a sandbox to: memory in MiB, then CPUs.
limits() {
  if [[ "$ENGINE" == apple ]]; then
    container inspect "opencode-sandbox-$1" | python3 -c 'import json, sys
r = json.load(sys.stdin)[0]["configuration"]["resources"]
print(r["memoryInBytes"] >> 20, r["cpus"])'
  else
    docker inspect -f '{{.HostConfig.Memory}} {{.HostConfig.NanoCpus}}' "opencode-sandbox-$1" | awk '{ print $1 / 1048576, $2 / 1e9 }'
  fi
}
# What the engine has, and so what a sandbox gets by itself: 4 GB and 4
# CPUs, or all there is where that's less.
if [[ "$ENGINE" == apple ]]; then
  HAS_CPUS="$(sysctl -n hw.ncpu)" HAS_MB="$(( $(sysctl -n hw.memsize) / 1048576 ))"
else
  read -r HAS_CPUS HAS_MB < <(docker info --format '{{.NCPU}} {{.MemTotal}}' | awk '{ print $1, int($2 / 1048576) }')
fi
USUAL="$(( HAS_MB < 4096 ? HAS_MB : 4096 )) $(( HAS_CPUS < 4 ? HAS_CPUS : 4 ))"
[[ "$(limits it-r)" == "$USUAL" ]] && ok "a sandbox may use 4 GB and 4 CPUs, or all its engine has ($USUAL)" || bad "the usual limits ($USUAL)" "$(limits it-r)"
check "the limits asked for" 'opencode v?[0-9]' "$BIN" new "$T/ws" -n it-l -k none --memory 1g --cpus 1 -- --version
[[ "$(limits it-l)" == "1024 1" ]] && ok "the engine holds it to them" || bad "the engine holds it to them" "$(limits it-l)"
if [[ "$ENGINE" != apple ]]; then
  # No swap to spill into: memory and swap together are capped at the same.
  [[ "$(docker inspect -f '{{.HostConfig.MemorySwap}}' opencode-sandbox-it-l)" == 1073741824 ]] && ok "and it has no swap" || bad "and it has no swap"
fi
# More than there is: all there is, and it says so.
out="$("$BIN" new "$T/ws" -n it-u -k none --memory 4096g --cpus 4096 -- --version 2>&1 </dev/null)"
grep -q "has $HAS_CPUS CPUs, so that is this sandbox's limit, not 4096" <<<"$out" && grep -q "of memory, so that is this sandbox's limit, not 4096 GB" <<<"$out" \
  && ok "more than the engine has is said to be less" || bad "more than the engine has is said to be less" "$out"
[[ "$(limits it-u)" == "$HAS_MB $HAS_CPUS" ]] && ok "and is all the engine has" || bad "and is all the engine has" "$(limits it-u)"
OPENCODE_SANDBOX_MEMORY=2g OPENCODE_SANDBOX_CPUS=1 check "health names the limits of new sandboxes" '2 GB of memory, 1 CPU \(' flat "$BIN" health
refuse "memory without a unit" "isn't an amount of memory; say it like 8g or 512m" "$BIN" new "$T/ws" -n it-x -k none --memory 8
refuse "too little memory" 'opencode needs at least 512m' "$BIN" new "$T/ws" -n it-x -k none --memory 100m
refuse "half a CPU" '--cpus must be a whole number of CPUs' "$BIN" new "$T/ws" -n it-x -k none --cpus 0.5
OPENCODE_SANDBOX_CPUS=many refuse "limits in the environment that can't be read" 'OPENCODE_SANDBOX_CPUS must be a whole number' "$BIN" new "$T/ws" -n it-x -k none

echo "== delete"
out="$("$BIN" rm it-a it-b it-nope 2>&1)"
grep -q 'removed it-a and its disposable key SHA256' <<<"$out" && grep -q "no sandbox named 'it-nope'" <<<"$out" \
  && ok "rm" || bad "rm" "$out"
[[ ! -e "$DATA/keys/it-a" ]] && ok "disposable key deleted" || bad "disposable key deleted"
refuse "rm --all needs -y without a terminal" 'add -y' "$BIN" rm --all
if (( ALL )); then
  check "rm --all -y" 'removed it-c' "$BIN" rm --all -y
  check "ls empty again" 'no sandboxes' "$BIN" ls
else
  skip "rm --all -y"
  check "rm the rest by name" 'removed it-c' "$BIN" rm it-c it-d it-h it-q it-k it-e it-f it-g it-m it-o it-r it-s it-z it-l it-u
  none "ls has none of the test's again"
fi

echo "== $pass passed, $fail failed"
(( fail == 0 ))
