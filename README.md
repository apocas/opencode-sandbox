# opencode-sandbox

Run [opencode](https://opencode.ai) in hardened Docker sandboxes where the agent
can use **exactly one SSH key** (or none), works on a **copy** of your files, and
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
  with the sandbox.
- **Your files are copied, not mounted.** The sandbox gets a snapshot of a
  folder. Nothing the agent writes reaches the original.
- **Only the providers you choose.** Each sandbox gets a filtered copy of your
  opencode config with just the selected provider(s) and their API keys.
  opencode's built-in providers (such as its free hosted models) are switched
  off.
- **Hardened container.** Non-root user, read-only root filesystem, all
  capabilities dropped, `no-new-privileges`, a PID limit, and a Docker network
  on which sandboxes can't reach each other.
- **As many sandboxes as you like.** Every run creates a new one. List them,
  resume one (continuing its last opencode session), or delete them.

## Requirements

- Linux with Docker Engine, rootful. Rootless Docker, userns-remap and Docker
  Desktop aren't supported: the container has to run as your uid for the
  host ssh-agent to accept it.
- bash 4.4+, the OpenSSH client tools (`ssh-agent`, `ssh-add`, `ssh-keygen`),
  `jq`, GNU coreutils.
- An opencode config at `~/.config/opencode/opencode.json` (plain JSON) with
  your provider(s) under `provider`. Providers you logged into with
  `opencode auth login` aren't passed to sandboxes.

Tested on Ubuntu (x86_64) with Docker 29, OpenSSH 10.2 and opencode 2.4.2. The
Dockerfile also handles arm64 Linux, but that hasn't been tested yet.

## Install

```bash
git clone https://github.com/apocas/opencode-sandbox.git
ln -s "$PWD/opencode-sandbox/opencode-sandbox" ~/.local/bin/opencode-sandbox
opencode-sandbox --build    # optional: the first run builds the image anyway
```

## Quick start

```bash
mkdir -p ~/agent-work && cd ~/agent-work
opencode-sandbox
```

You'll be asked which SSH key the sandbox may use and, if your config has more
than one, which provider. opencode then opens with a copy of the folder at
`/workspace`.

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
| `-k KEY` | a key name from `~/.ssh`, `new` (disposable) or `none`; asked if omitted |
| `-p PROVIDER` | provider id(s) from your opencode config, several as `a,b`; asked if omitted, automatic if you have only one |
| `-- ARGS` | passed to opencode |

| Command | |
|---|---|
| `--list` | list sandboxes with their key, provider and state |
| `--resume [NAME]` | reopen a sandbox and continue its last session (pick from a list if no NAME) |
| `--pubkey NAME` | print the public key a sandbox uses |
| `--shell NAME` | bash inside an open sandbox |
| `--rm NAME...` / `--rm --all` | delete sandboxes, their files and disposable keys |
| `--status` | show the ssh-agents of open sandboxes |
| `--stop-agent` | stop all sandbox ssh-agents |
| `--build [ARGS]` | rebuild the image (ARGS go to `docker build`) |
| `-h`, `--help` | full help |

Quitting opencode or closing the terminal stops the sandbox and unloads its key.
Press `ctrl-]` `ctrl-]` to detach and leave it running; `--resume` reattaches.

## SSH through a jump host

Tell the agent how to reach your servers, for example:

```bash
ssh -i ~/.ssh/id_ed25519 -A -p 2222 user@jump.example.com \
  ssh root@10.0.0.5 "'uptime; df -h'"
```

- The public key is mounted under its own name, so `-i ~/.ssh/<name>` works
  as-is inside the sandbox.
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

- `$XDG_RUNTIME_DIR/opencode-sandbox/agents/NAME/`: the sandbox's ssh-agent,
  started when the sandbox opens and stopped when it closes. Only its socket
  directory is mounted into the sandbox. The agent refuses to load PKCS#11/FIDO
  provider libraries.
- `$XDG_RUNTIME_DIR/opencode-sandbox/configs/NAME.json`: the sandbox's filtered
  opencode config, in RAM and readable only by you. It's regenerated from your
  config every time the sandbox starts, so a rotated API key is picked up.
- `keys/` and `known_hosts` next to the script: disposable keys and seeded host
  keys. Both are git-ignored.

In the sandbox:

- `/workspace` and `/home/agent` (opencode's sessions) are volumes of their own,
  kept until you `--rm` the sandbox.
- `~/.ssh` holds only the public key; `SSH_AUTH_SOCK` points at the mounted
  agent socket. A sandbox created with `-k none` gets neither.

## What it doesn't protect against

- **Outbound network is open.** The sandbox has to reach your LLM provider and
  your servers, so it can also reach the internet, your LAN, and services
  listening on the host.
- **Anything inside an open sandbox can use its key**, and so log into every
  server that trusts it. With `ssh -A`, so can root on the host you forward to.
- **The provider API key is readable inside the sandbox**, since opencode needs it.
- **A sandbox's home persists until you delete it.** Something planted there runs
  again when you resume that sandbox.
- **Prompt injection.** Text in files or command output can try to steer the
  agent. Limiting its key and providers limits the damage, but doesn't prevent it.
- **Containers share the host kernel.** For genuinely hostile code, use a VM.

## Configuration

| Variable | |
|---|---|
| `OPENCODE_SANDBOX_CONFIG` | opencode config to take providers from (default `~/.config/opencode/opencode.json`) |
| `OPENCODE_SANDBOX_SEED_HOSTS` | space-separated hosts whose keys are copied from `~/.ssh/known_hosts` into sandboxes; read when `known_hosts` is created, delete it to re-seed |

To use another opencode release, pass its version and archive checksums:

```bash
opencode-sandbox --build --build-arg OPENCODE_VERSION=X.Y.Z \
  --build-arg OPENCODE_SHA256_AMD64=... --build-arg OPENCODE_SHA256_ARM64=...
```

Existing sandboxes keep the image they were created with.

## License

[Apache-2.0](LICENSE)
