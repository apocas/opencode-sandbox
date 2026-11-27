# opencode-sandbox

Run [opencode](https://opencode.ai) in hardened sandboxes (Docker containers,
or on a Mac a VM for each sandbox with Apple's `container`) where the agent can
use **exactly one SSH key** (or none), works on a **copy** of your files, and
only talks to the **LLM provider you pick**.

Coding agents are most useful when they can run commands, including `ssh` into
your servers. That also means a confused or prompt-injected agent can use every
key in your ssh-agent, read anything in your home directory, and send your code
to whichever provider happens to be configured. opencode-sandbox narrows that
down:

- **One SSH key per sandbox, never copied in.** Pick a key pair from `~/.ssh`, a
  new disposable key, or none. The private key stays on the host, loaded into a
  dedicated `ssh-agent` that holds only that key; the sandbox gets that agent's
  socket and the public key. The agent can *use* the key while the sandbox is
  open, but can't read or copy it. Your normal ssh-agent and other keys are
  invisible.
- **Disposable keys.** Generated per sandbox and kept outside `~/.ssh`. The
  public part is shown so you can add it to `authorized_keys`; the key is deleted
  with the sandbox. On macOS and Docker Desktop, where a sandbox can't reach an
  ssh-agent on your machine, these are what you get (see [Platforms](#platforms)).
- **Your files are copied, not mounted.** The sandbox gets a snapshot of a
  folder. Nothing the agent writes reaches the original.
- **Only the providers you choose.** Each sandbox gets a filtered copy of your
  opencode config with just the selected provider(s) and their API keys.
  opencode's built-in providers (such as its free hosted models) are switched
  off.
- **Hardened container.** Non-root user, read-only root filesystem, all
  capabilities dropped, no setuid programs, `no-new-privileges`, a PID limit,
  and a network on which sandboxes can't reach each other.
- **Stronger boundaries where you have them.** On a Mac with Apple's
  [`container`](https://github.com/apple/container), every sandbox runs in a
  lightweight VM of its own. On Linux, sandboxes can run on
  [gVisor](https://gvisor.dev), a separate user-space kernel. `--health` shows
  what this machine has and what you could install
  ([Isolation](#isolation)).
- **As many sandboxes as you like.** Every run creates a new one. List them,
  resume one (continuing its last opencode session), or delete them.

## Platforms

| Setup | SSH keys a sandbox can get |
|---|---|
| Linux with Docker Engine (rootful), also with gVisor if its runtime has `--host-uds=open` | a key pair from `~/.ssh`, a disposable key, or none. Private keys never enter the sandbox. |
| macOS (Apple container or Docker Desktop); Docker Desktop on any OS (including Windows with WSL); rootless Docker; userns-remap; gVisor without `--host-uds=open` | a disposable key, or none. The disposable key's private part is stored **inside** the sandbox. |

In the second group, sandboxes run in a VM, under another uid or in a kernel
that can't reach sockets on your machine, so a sandbox can't use an ssh-agent
on your machine. (Docker Desktop's built-in SSH
passthrough would forward your *main* agent with all its keys, which is what
this tool avoids.) opencode-sandbox detects this and switches by itself; keys
from `~/.ssh` are then never offered, because they would have to be copied in.
Set `OPENCODE_SANDBOX_NO_AGENT=1` to use this mode anywhere.

Tested with opencode 2.4.2 on macOS 27 (arm64) with Apple container 1.7.1 and
Docker Desktop 29, and on Linux (arm64, Docker 27) with runc and with gVisor
(release-20261123), with and without `--host-uds=open`. Earlier versions were
also tested on Ubuntu (x86_64) with Docker 29. `test/integration.sh` runs every
command against a real Docker or Apple container; CI runs it on Linux with
runc and gVisor.

## Isolation

What separates a sandbox from your machine depends on what runs it.
`opencode-sandbox --health` shows it for this machine, which stronger options
it has, and what you could install for them.

| Runs in | Boundary around a sandbox |
|---|---|
| Docker Engine (Linux) | your kernel, shared with all containers: namespaces, seccomp, AppArmor or SELinux |
| Docker with gVisor (Linux) | a user-space kernel of its own |
| Docker Desktop (Mac, Windows, Linux) | one Linux VM, shared with all your containers |
| Apple container (Mac) | a lightweight VM of its own |

### Apple container (macOS)

On a Mac with Apple silicon and macOS 26 or later, install
[Apple's container](https://github.com/apple/container/releases) and run
`container system start`. New sandboxes then go there by themselves; ones made
with Docker stay in Docker, and `--list` shows where each runs (RUNS IN). Set
`OPENCODE_SANDBOX_ENGINE=docker` to keep using Docker for new ones.

What's different there:

- Each sandbox is a VM with 4 GB of memory and 4 CPUs, on a network of its own.
- No detaching: `ctrl-]` `ctrl-]` does nothing, and an open sandbox can't be
  opened from a second terminal (`--shell` works).
- There's no `no-new-privileges` or PID limit; the image has no setuid programs
  and the VM contains the rest.
- SSH keys are disposable or none, stored inside the sandbox, as with Docker
  Desktop.
- Copying into or out of a closed sandbox starts a short-lived helper VM.

### gVisor (Linux)

[Install gVisor](https://gvisor.dev/docs/user_guide/install/), register it
with Docker (`sudo runsc install`, then `sudo systemctl reload docker`) and set
`OPENCODE_SANDBOX_RUNTIME=runsc`. For keys from `~/.ssh` (a host ssh-agent),
gVisor must be allowed to connect to the agent socket mounted into the
sandbox. Register a second runtime for that, so other containers keep the
stricter default, and use it with `OPENCODE_SANDBOX_RUNTIME=runsc-ssh`:

```bash
sudo runsc install --runtime runsc-ssh -- --host-uds=open
sudo systemctl reload docker
```

## Requirements

- Docker, used as a regular user (not root); or on a Mac with Apple silicon and
  macOS 26 or later, Apple's `container` instead (see [Isolation](#isolation)).
- The OpenSSH client tools (`ssh-agent`, `ssh-add`, `ssh-keygen`), which Linux
  distributions and macOS include.
- Linux or macOS, on x86_64 or arm64. To install from a git clone or with
  `go install`, [Go](https://go.dev/dl/) 1.24 or newer.
- An opencode config at `~/.config/opencode/opencode.json` (plain JSON) with
  your provider(s) under `provider`. Providers you logged into with
  `opencode auth login` aren't passed to sandboxes.

## Install

opencode-sandbox is a single program with nothing to install next to it. Pick
one way:

**A release binary.** Download the one for your system from the
[latest release](https://github.com/apocas/opencode-sandbox/releases/latest),
check it against `SHA256SUMS`, and put it on your `PATH`:

```bash
v=0.5.0 os=$(uname -s | tr A-Z a-z) arch=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
f=opencode-sandbox_${v}_${os}_${arch}
curl -fLO https://github.com/apocas/opencode-sandbox/releases/download/v$v/$f
curl -fL https://github.com/apocas/opencode-sandbox/releases/download/v$v/SHA256SUMS | grep " $f\$" | shasum -a 256 -c
install -m 755 $f ~/.local/bin/opencode-sandbox && rm $f
```

The binaries are built by GitHub Actions from the tagged source, with a
provenance attestation: `gh attestation verify ~/.local/bin/opencode-sandbox -R apocas/opencode-sandbox`
shows which workflow and commit made yours.

**With Go:** `go install github.com/apocas/opencode-sandbox/cmd/opencode-sandbox@latest`
puts it in `~/go/bin`.

**From a git clone,** as before 0.5.0. The `opencode-sandbox` script in the
clone builds the program the first time you run it, and again after the sources
change (this needs Go):

```bash
git clone https://github.com/apocas/opencode-sandbox.git
cd opencode-sandbox && ln -s "$PWD/opencode-sandbox" ~/.local/bin/opencode-sandbox
opencode-sandbox --version
```

The first run builds the Docker image (or run `opencode-sandbox --build`).

## Updating

Releases follow [Semantic Versioning](https://semver.org) and are tagged
`vX.Y.Z`; see [CHANGELOG.md](CHANGELOG.md) for what changed in each.
`opencode-sandbox --version` shows your version and the opencode release it
bundles.

Replace the binary with the new release's, run `go install` again, or update
your clone:

```bash
cd /path/to/opencode-sandbox           # your clone
git checkout master && git pull        # the latest code
# or a specific release:
git fetch --tags && git checkout v0.5.0
opencode-sandbox --version
```

- The next run builds the image for the new version by itself (or run
  `opencode-sandbox --build`).
- **Coming from 0.4 or earlier:** new disposable keys and `known_hosts` are
  kept in `~/.local/share/opencode-sandbox/` instead of the clone. Sandboxes you
  already have still use the `keys/` and `known_hosts` in your clone, so keep
  the clone until you've deleted them (`--rm` deletes their keys there too). An
  installed binary manages them as well. Run from the clone, the first new
  sandbox also copies the clone's `known_hosts`.
- Sandboxes you already have keep the image and settings they were created
  with; `--resume` tells you when a sandbox is older than the launcher. To move
  to the new version, create a new sandbox and delete the old one with `--rm`.
- Check the changelog before updating for anything you need to do.
- Old images stay on disk: list them with `docker image ls opencode-sandbox`
  and remove one with `docker image rm opencode-sandbox:OLD_VERSION` once no
  sandbox uses it (Docker refuses while one does).

## Quick start

```bash
mkdir -p ~/agent-work && cd ~/agent-work
opencode-sandbox
```

You'll be asked which SSH key the sandbox may use and, if your config has more
than one, which provider. opencode then opens with a copy of the folder at
`/workspace`. (On macOS and Docker Desktop the key menu offers only a new
disposable key or none.)

```
Which SSH key may sandbox 'agent-work-3f9a' use?
  1) id_ed25519       (ED25519) SHA256:Xk3p... you@laptop
  2) new disposable   a fresh key for this sandbox only, deleted with it
  3) none             no SSH access
Choice [1-3]:
```

## Usage

```
opencode-sandbox [-w DIR] [-n NAME] [-k KEY] [-p PROVIDER] [-- ARGS...]
```

| Option | |
|---|---|
| `-w DIR` | folder to copy in (default: the current one). Your home folder, `~/.ssh` and similar are refused |
| `-n NAME` | sandbox name (default: folder name plus a random suffix) |
| `-k KEY` | a key name from `~/.ssh`, `new` (disposable) or `none`; asked if omitted. Only `new` or `none` on [some platforms](#platforms) |
| `-p PROVIDER` | provider id(s) from your opencode config, several as `a,b`; asked if omitted, automatic if you have only one |
| `-- ARGS` | passed to opencode |

| Command | |
|---|---|
| `--list` | list sandboxes with their key, provider and state |
| `--resume [NAME]` | reopen a sandbox and continue its last session (pick from a list if no NAME) |
| `--pubkey NAME` | print the public key a sandbox uses |
| `--shell NAME` | bash inside an open sandbox |
| `--cp NAME PATH...` | copy files or folders into a sandbox's `/workspace`, open or closed |
| `--get NAME PATH...` | copy files or folders out of a sandbox (paths relative to `/workspace`) into the current folder |
| `--rm NAME...` / `--rm --all` | delete sandboxes, their files and disposable keys |
| `--status` | show the ssh-agents of open sandboxes |
| `--stop-agent` | stop all sandbox ssh-agents |
| `--health` | check that sandboxes can run here, show how they're isolated and what you could install to isolate them better |
| `--build [ARGS]` | rebuild the image (ARGS go to `docker build`) |
| `-h`, `--help` | full help |

Quitting opencode or closing the terminal stops the sandbox and unloads its key.
Press `ctrl-]` `ctrl-]` to detach and leave it running; `--resume` reattaches.

### Adding files later

The folder is copied in once, when the sandbox is created: files you add to it
afterwards don't reach the sandbox. Copy them in with `--cp`, even while
opencode is running; it can use them straight away, no restart needed:

```bash
opencode-sandbox --cp agent-work-3f9a ./logs.txt ./configs   # into /workspace
opencode-sandbox --get agent-work-3f9a report.md             # out, into the current folder
```

`--cp` refuses the same paths as `-w` (your home folder, `~/.ssh`, `~/.config`
and similar, and wherever disposable keys are kept). `--get` never overwrites a file, and
won't write into those folders either. Whatever comes out was written by the
agent, so treat it as untrusted.

## SSH through a jump host

Tell the agent how to reach your servers, for example:

```bash
ssh -i ~/.ssh/id_ed25519 -A -p 2222 user@jump.example.com \
  ssh root@10.0.0.5 "'uptime; df -h'"
```

- The public key is mounted under its own name, so `-i ~/.ssh/<name>` works
  as-is inside the sandbox. A disposable key is `~/.ssh/sandbox_key`, which ssh
  uses even without `-i`.
- Without a host agent ([Platforms](#platforms)), the disposable key is loaded
  into an ssh-agent inside the sandbox, so `-A` works the same way.
- Quote the remote command twice, as above. With one layer of quotes, `$(...)`
  is expanded on the jump host instead of the target.
- `-A` forwards the agent, so while connected, root on the jump host can use the
  sandbox's key (only that key). If the jump host allows TCP forwarding, `-J`
  avoids forwarding the agent at all:
  `ssh -i ~/.ssh/id_ed25519 -J user@jump.example.com:2222 root@10.0.0.5 'uptime'`
- Set `OPENCODE_SANDBOX_SEED_HOSTS="[jump.example.com]:2222"` before the first
  run to copy the jump host's key from your `~/.ssh/known_hosts` into sandboxes,
  so the first hop is verified instead of trusted on first use.

## How it works

On the host:

Runtime files live in `$XDG_RUNTIME_DIR/opencode-sandbox/`, or
`$TMPDIR/opencode-sandbox-UID/` where that isn't set (as on macOS):

- `agents/NAME/`: the sandbox's ssh-agent,
  started when the sandbox opens and stopped when it closes. Only its socket
  directory is mounted into the sandbox. The agent refuses to load PKCS#11/FIDO
  provider libraries.
- `configs/NAME.json`: the sandbox's filtered opencode config, in a directory
  only you can open. It's regenerated from your config every time the sandbox
  starts, so a rotated API key is picked up.
- `sandboxes/NAME/`: the same for a sandbox in Apple container, which can only
  mount folders: its config and a copy of `known_hosts`.
Persistent files live in `$XDG_DATA_HOME/opencode-sandbox/`, by default
`~/.local/share/opencode-sandbox/`:

- `keys/NAME/`: a sandbox's disposable key pair, in a folder only you can open,
  deleted with the sandbox.
- `known_hosts`: the seeded host keys every sandbox trusts.

Set `OPENCODE_SANDBOX_DEBUG=1` to see every command opencode-sandbox runs.

In the sandbox:

- `/workspace` and `/home/agent` (opencode's sessions) are volumes of their own,
  kept until you `--rm` the sandbox.
- `~/.ssh` holds only the public key; `SSH_AUTH_SOCK` points at the mounted
  agent socket. A sandbox created with `-k none` gets neither.
- Without a host agent, the disposable key is mounted read-only instead, copied
  to `~/.ssh/sandbox_key` at each start, and loaded into an ssh-agent running
  inside the sandbox.

## What it doesn't protect against

- **Outbound network is open.** The sandbox has to reach your LLM provider and
  your servers, so it can also reach the internet, your LAN, and services
  listening on the host.
- **Anything inside an open sandbox can use its key**, and so log into every
  server that trusts it. With `ssh -A`, so can root on the host you forward to.
- **Without a host agent, the disposable key can be copied out.** Whatever runs
  in the sandbox can read it, and a copy keeps working wherever you authorized
  it until you remove it from `authorized_keys`, even after `--rm`.
- **The provider API key is readable inside the sandbox**, since opencode needs it.
- **A sandbox's home persists until you delete it.** Something planted there runs
  again when you resume that sandbox.
- **Prompt injection.** Text in files or command output can try to steer the
  agent. Limiting its key and providers limits the damage, but doesn't prevent it.
- **With Docker, sandboxes share a kernel**: your machine's on Linux, Docker
  Desktop's VM elsewhere. For genuinely hostile code, use Apple container on a
  Mac (a VM for each sandbox) or gVisor on Linux ([Isolation](#isolation)).

## Configuration

| Variable | |
|---|---|
| `OPENCODE_SANDBOX_CONFIG` | opencode config to take providers from (default `~/.config/opencode/opencode.json`) |
| `OPENCODE_SANDBOX_NO_AGENT` | `1` to use the no-agent mode ([Platforms](#platforms)) even where a host ssh-agent would work |
| `OPENCODE_SANDBOX_SEED_HOSTS` | space-separated hosts whose keys are copied from `~/.ssh/known_hosts` into sandboxes; read when `known_hosts` is created, delete it to re-seed |
| `OPENCODE_SANDBOX_ENGINE` | `docker` or `apple`: where new sandboxes go (default: Apple container where it runs, else Docker) |
| `OPENCODE_SANDBOX_RUNTIME` | a Docker runtime for new sandboxes, e.g. `runsc` for gVisor ([Isolation](#isolation)) |
| `OPENCODE_SANDBOX_DEBUG` | `1` to print every command opencode-sandbox runs |

To use another opencode release, pass its version and archive checksums:

```bash
opencode-sandbox --build --build-arg OPENCODE_VERSION=X.Y.Z \
  --build-arg OPENCODE_SHA256_AMD64=... --build-arg OPENCODE_SHA256_ARM64=...
```

Existing sandboxes keep the image they were created with.

## Development

```bash
go vet ./... && go test ./...                       # unit tests (no Docker)
go build -o .bin/opencode-sandbox ./cmd/opencode-sandbox
test/integration.sh .bin/opencode-sandbox          # against a real Docker
```

To also check compatibility with the bash launcher, pass a checkout of v0.4.0:
`git worktree add /tmp/v040 v0.4.0 && test/integration.sh .bin/opencode-sandbox /tmp/v040/opencode-sandbox`.
CI runs all of this on every push.

## Releasing

1. Bump `Version` in `internal/app/app.go` and add a section to
   `CHANGELOG.md`.
2. To bundle a newer opencode, update `OPENCODE_VERSION` and both checksums in
   the `Dockerfile`.
3. Commit, tag and push:
   `git tag -a vX.Y.Z -m "vX.Y.Z" && git push origin master vX.Y.Z`. The
   release workflow builds the binaries, attests them and creates the GitHub
   release with that version's changelog section.

## License

[Apache-2.0](LICENSE)
