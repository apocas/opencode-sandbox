# opencode-sandbox

Run [opencode](https://opencode.ai) in hardened sandboxes where the agent works
on a **copy** of your files, can use **exactly one SSH key** (or none), talks
only to the **LLM providers you pick**, and never holds their **API keys**.
Sandboxes are Docker containers, or on a Mac a VM each with Apple's
`container`.

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/overview-dark.svg">
    <img src="docs/overview-light.svg" width="880" alt="Of your folders, SSH keys, LLM providers and API keys, a sandbox gets a copy of one folder, a new disposable key of its own instead of any of yours, and the providers you pick. It gets none of your API keys: it has a token, and every request it makes goes out through a gateway on your machine that swaps the token for the real key. The wall around it is a hardened Docker container, or after `opencode-sandbox setup` a gVisor kernel of its own on Linux or an Apple container VM of its own on a Mac.">
  </picture>
</p>

## Try it

<p align="center">
  <img src="docs/demo.gif" width="880" alt="A terminal session. The installer runs. `opencode-sandbox new` asks which SSH key the sandbox may use and shows its new disposable key. opencode opens inside the sandbox and answers a prompt. Then `opencode-sandbox` shows the dashboard, with the sandbox running on gVisor and its API keys kept on the machine.">
</p>
<p align="center"><sub>Recorded on Linux with gVisor, with a stand-in for the LLM provider.</sub></p>

**1. Install it.** Paste this into a terminal (macOS or Linux):

```bash
curl -fsSL https://raw.githubusercontent.com/apocas/opencode-sandbox/master/install.sh | sh
```

**2. Open a project in a sandbox.** Go to the project's folder and run `new`:

```bash
cd ~/my-project
opencode-sandbox new
```

It asks which SSH key the sandbox may use (and which provider, if you have
several), then opens opencode on a copy of the folder. Quit opencode to close
the sandbox. Your own folder is never touched.

**3. Come back to it.** Run it on its own:

```bash
opencode-sandbox
```

It lists your sandboxes: `enter` carries on where you left off, `g` copies a
file the agent made back to your machine, `?` shows the rest.

It needs two things first:

- **Something to run sandboxes in:**
  [Docker](https://docs.docker.com/get-started/get-docker/), running; or, on a
  Mac with Apple silicon and macOS 26 or later, Apple's `container`, which
  `opencode-sandbox setup` installs for you.
- **An opencode config with your provider** in
  `~/.config/opencode/opencode.json`.

`opencode-sandbox health` says what's missing. If the shell can't find
`opencode-sandbox` after installing, add the line the installer printed to your
shell's startup file and open a new terminal.

## What it protects

Coding agents are most useful when they can run commands, `ssh` included. So a
confused or prompt-injected agent can also use every key in your ssh-agent,
read your home directory and send your code anywhere. A sandbox narrows that:

- **Your files are copied, never mounted.** Nothing the agent writes reaches
  your folder. Dot files that may hold secrets (`.env`, `.npmrc`…) are named
  before they're copied; `new --empty` copies nothing.
- **One SSH key, never copied in.** A key from `~/.ssh`, a new disposable one
  (deleted with the sandbox), or none. Where it can, the private key stays on
  your machine, in an ssh-agent that holds only that key
  ([Platforms](#platforms)).
- **Only the providers you pick, without their API keys.** The sandbox's
  opencode config has just those providers, and a gateway on your machine puts
  the real keys into their requests ([API keys](#api-keys)).
- **Only the hosts you allow, if you want.** `--network filtered --allow
  github,npm` reaches the providers and those hosts; `--network none`, only the
  providers ([Network](#network)).
- **Hardened and capped.** Non-root, read-only root filesystem, no
  capabilities, no setuid programs, `no-new-privileges`, no way to reach other
  sandboxes, and a cap on memory, CPUs and processes
  ([Memory and CPUs](#memory-and-cpus)).
- **The strongest wall this machine has.** A VM of its own with Apple
  container on a Mac, a kernel of its own with gVisor on Linux; `setup`
  installs them ([Isolation](#isolation)).
- **As many sandboxes as you like.** List them, resume one where it left off,
  delete them.

## Platforms

Whether a key from `~/.ssh` can be used depends on whether a sandbox can reach
an ssh-agent on your machine:

| Setup | SSH keys a sandbox can get |
|---|---|
| Linux with Docker Engine (rootful), also with gVisor if its runtime has `--host-uds=open` | a key pair from `~/.ssh`, a disposable key, or none. Private keys never enter the sandbox. |
| macOS (Apple container or Docker Desktop); Docker Desktop on any OS (including Windows with WSL); rootless Docker; userns-remap; gVisor without `--host-uds=open` | a disposable key, or none. The disposable key's private part is stored **inside** the sandbox. |

In the second group, a sandbox runs in a VM, under another uid or on a kernel
that can't reach your machine's sockets. opencode-sandbox detects this and
offers only a disposable key: a key from `~/.ssh` would have to be copied in.
(Docker Desktop's SSH passthrough would forward your *main* agent, with all its
keys.) `OPENCODE_SANDBOX_NO_AGENT=1` uses this mode everywhere.

Tested with opencode 2.4.2 on macOS 27 (Apple container 1.7.1, Docker Desktop
29) and on Linux (Docker 27, with runc and with gVisor). `test/integration.sh`
runs every command against a real engine; CI runs it on Linux with runc and
gVisor.

## API keys

**A sandbox never holds your providers' API keys.** Its opencode config has a
token in their place, and each provider's address is the gateway inside the
sandbox, `127.0.0.1:17872`. On your machine, opencode-sandbox swaps the token
for the real key and passes the request on. The agent can use your providers
while the sandbox is open, but can't read or copy their keys.

- **Nothing listens on a network.** The gateway's two ends talk over the
  engine's `exec`, so it works the same with Docker, gVisor and Apple
  container, and nothing else can reach it.
- **The key goes only into a key header** (`Authorization`, `x-api-key`,
  `api-key`, `x-goog-api-key`) of a request to that provider, and only where
  that header is the token; `options.headers` are put back the same way. Never
  into a body or an address, so a model can't be made to say a key back. Only
  `GET`, `HEAD` and `POST` are passed on.
- **`{env:NAME}` and `{file:PATH}` are resolved on your machine**, and a
  changed config applies at once: rotate a key and the next request uses it.

These still go into the sandbox's config, and are named when it starts:

- the key of a provider whose API address the gateway doesn't know: one without
  `options.baseURL`, other than `anthropic`, `openai`, `google`, `groq`,
  `mistral`, `xai` and `openrouter` (by id or `npm` package);
- a secret under another name, such as a cloud's `secretAccessKey` or a header
  of a single model.

`ls` marks sandboxes with a secret in their config, and those made before
0.9.0, with `*` after the provider; `health` says which and why.
`OPENCODE_SANDBOX_GATEWAY=off` puts the keys in the config instead.

### MCP servers

**The tokens of remote MCP servers stay on your machine too.** That's a header
such as `Authorization: Bearer …`, or a secret in the server's address (a query
like `?key=…`, a password). In the sandbox's config, the server's address is
the gateway (`/mcp/NAME`) and its headers hold the token; the gateway puts the
real ones back. `DELETE`, which ends an MCP session, is passed on to MCP
servers only.

What can't be kept out:

- the `environment` of a local MCP server (`"type": "local"`), which runs
  inside the sandbox;
- for a server you sign in to with OAuth, its `clientSecret`, and the token,
  which opencode keeps inside the sandbox;
- a token in the path of an address (`https://mcp.example.com/TOKEN/mcp`): only
  a query or a password is recognised.

A remote server with nothing to keep is reached directly, except from a sandbox
with a filtered network or none, where every remote server goes through the
gateway.

## Network

**A sandbox's network is chosen when it's created, and can't be changed
afterwards:**

| `--network` | What the sandbox reaches |
|---|---|
| `open` (the default) | the internet, your network and services on your machine; not other sandboxes |
| `filtered` | its providers, and the hosts on its list ([below](#a-filtered-network)) |
| `none` | only its providers, through the [gateway](#api-keys) |

`none` leaves only the sandbox's own loopback. opencode still works, because
the gateway doesn't travel over a network; but there's no SSH (so no key), no
`git pull`, no installs. It suits work on code that's all in the folder.

With `filtered` or `none`, every provider goes through the gateway, those
without an API key too (a model server on your machine, say), so each needs a
known address, and the gateway can't be off. `OPENCODE_SANDBOX_NETWORK` sets
the default; `ls` shows each sandbox's network.

### A filtered network

**The sandbox reaches its providers and the hosts on its list, and nothing
else:** not the rest of the internet, your network or your machine. Unlike with
`none`, `git`, package installs and SSH work, to those hosts.

```bash
opencode-sandbox new --allow github,npm        # a list makes the network filtered
opencode-sandbox allow NAME example.com        # more later: at once, also while it's open
opencode-sandbox net NAME                      # its list, and what it was refused
opencode-sandbox deny NAME evil.example.com    # refused, whatever else allows it
```

| On the list | Allows |
|---|---|
| `example.com` | that host |
| `*.example.com` | every host under that domain (not `example.com` itself) |
| `192.0.2.7`, `10.0.0.0/8` | an address, a range of addresses |
| `example.com:8443` | that port only; without a port, 443, 80 and 22 (HTTPS, HTTP, SSH) |
| `github`, `gitlab`, `npm`, `pypi`, `go` | a built-in list: the hosts that tool needs (`net` shows them) |

The sandbox has no network of its own, as with `none`. Its programs use the
gateway as their proxy (`HTTPS_PROXY` and the like), and `ssh` goes through it
too. On your machine, each host is checked against the list before it's even
looked up. Nothing listens on a network, on any engine.

- **Refused hosts are written down**, with how often and why: `net NAME`, or
  `a` in the dashboard, where one key allows one.
- **On a Mac, you're asked** about a host on no list, while the connection
  waits up to 30 seconds: Deny (the default, and remembered), allow until the
  sandbox closes, or always allow. Nothing is asked with the screen locked, for
  a while after a question went unanswered, or with `OPENCODE_SANDBOX_ASK=off`:
  the host is refused and written down, as on Linux.
- **Your machine and network need a rule with their address**
  (`192.168.1.20`, `127.0.0.1:5432`). Private, link-local and cloud metadata
  addresses and this machine's own are never reached through an allowed name.
- **The host allowed is the host reached:** for HTTPS, the name in the TLS
  handshake must match it. (A request inside the encrypted connection that
  names another site on the same server can't be seen.)
- **SSH keys work**, to hosts on the list (`github` and `gitlab` include SSH).

Before relying on it:

- **An allowed host is a way out** for whoever has an account there: with
  `github.com` allowed, an agent holding a token can push to a repository that
  isn't yours. Allow what the work needs.
- **An allowed SSH host is a tunnel** to whatever it can reach.
- **Programs that ignore proxy settings get no route** at all (raw sockets,
  some JVM tools, old Node).
- **A machine that itself needs a proxy to reach the internet isn't
  supported:** the filter connects to hosts directly.
- opencode fetches `models.opencode.ai` at every start; it's refused without a
  dialog, and opencode works without it.

## Memory and CPUs

**A sandbox may use up to 4 GB of memory, 4 CPUs and 1024 processes,** so a
runaway build or a fork bomb is stopped inside it instead of starving your
machine. For more, or less:

```bash
opencode-sandbox new --memory 8g --cpus 6
```

`OPENCODE_SANDBOX_MEMORY` and `OPENCODE_SANDBOX_CPUS` set them for every new
sandbox. A sandbox never gets more than its engine has (Docker Desktop's VM,
say), and is told so. In Docker there's no swap to spill into; in Apple
container the limits are the VM's size. They can't be changed afterwards, and
Docker sandboxes made before 0.12.0 have none.

Disk isn't capped: a sandbox can fill the disk its volumes are on. Its `/tmp`
is in memory, 1 GB at most.

## Isolation

**New sandboxes get the strongest boundary this machine has:** Apple container,
else Docker with gVisor, else Docker. `opencode-sandbox health` (or `h` in the
dashboard) shows what that is here; `opencode-sandbox setup` installs something
stronger.

| Runs in | Boundary around a sandbox |
|---|---|
| Docker Engine (Linux) | your kernel, shared with all containers: namespaces, seccomp, AppArmor or SELinux |
| Docker with gVisor (Linux) | a user-space kernel of its own |
| Docker Desktop (Mac, Windows, Linux) | one Linux VM, shared with all your containers |
| Apple container (Mac) | a lightweight VM of its own |

### Apple container (macOS)

On a Mac with Apple silicon and macOS 26 or later, `setup` downloads
[Apple's container](https://github.com/apple/container/releases), checks
Apple's signature, installs it (`sudo installer`) and starts it. By hand:
install it from that page and run `container system start`. New sandboxes then
go there; ones in Docker stay in Docker (`ls` shows where each runs).
`OPENCODE_SANDBOX_ENGINE=docker` keeps new ones in Docker.

What's different there:

- Each sandbox is a VM the size of its [limits](#memory-and-cpus), on a network
  of its own.
- No detaching (`ctrl-]` `ctrl-]`), and an open sandbox can't be opened from a
  second terminal; `shell` works.
- No `no-new-privileges` or process limit: the VM contains the sandbox, and the
  image has no setuid programs.
- SSH keys are disposable or none, stored inside the sandbox.
- Copying into or out of a closed sandbox starts a short-lived helper VM.

### gVisor (Linux)

With Docker Engine, `setup` downloads [gVisor](https://gvisor.dev), checks its
published SHA-512, installs it in `/usr/local/bin` and registers it with
Docker, running these with `sudo`:

```bash
sudo runsc install                                          # runtime runsc
sudo runsc install --runtime runsc-ssh -- --host-uds=open   # runtime runsc-ssh
sudo systemctl reload docker                                # running containers carry on
```

`runsc-ssh` may connect to a sandbox's ssh-agent socket, which keys from
`~/.ssh` need; anything else you run on `runsc` keeps the stricter setting. By
hand: [install gVisor](https://gvisor.dev/docs/user_guide/install/) and run the
same commands.

New sandboxes then use `runsc-ssh` where an ssh-agent on this machine can serve
them, and a runtime without host sockets only where there's no such agent
anyway (rootless Docker, userns-remap, Docker in a VM). Existing sandboxes and
your other containers don't change. `OPENCODE_SANDBOX_RUNTIME=runc` keeps plain
Docker. With rootless Docker, `setup` leaves gVisor to you.

## Requirements

- Docker, used as a regular user (not root); or on a Mac with Apple silicon and
  macOS 26 or later, Apple's `container`, which `setup` installs.
- The OpenSSH client tools (`ssh-agent`, `ssh-add`, `ssh-keygen`), which Linux
  distributions and macOS include.
- Linux or macOS, on x86_64 or arm64. To build it yourself,
  [Go](https://go.dev/dl/) 1.26 or newer.
- An opencode config at `~/.config/opencode/opencode.json` (plain JSON) with
  your providers under `provider`. Providers you logged into with
  `opencode auth login` aren't passed to sandboxes.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/apocas/opencode-sandbox/master/install.sh | sh
```

[`install.sh`](install.sh) puts the latest release in `~/.local/bin`, after
checking it against the release's `SHA256SUMS` and, where GitHub's `gh` is
signed in, that this project's release workflow built it. It's a single
program, with nothing to install next to it. Then:

```bash
opencode-sandbox health   # can sandboxes run here, and what isolates them?
opencode-sandbox setup    # install the strongest isolation this machine can have
```

`setup` shows each step and command first, and asks before doing anything.

| To install | Pipe it into this instead of `sh` |
|---|---|
| somewhere else | `OPENCODE_SANDBOX_INSTALL_DIR=~/bin sh` |
| for everyone, in `/usr/local/bin` | `sudo sh` |
| a given version | `OPENCODE_SANDBOX_VERSION=0.6.1 sh` |

Releases are built by GitHub Actions from the tagged source and attested:
`gh attestation verify ~/.local/bin/opencode-sandbox -R apocas/opencode-sandbox`
shows which workflow and commit made yours.

Other ways:

- **By hand:** download your system's binary from the
  [latest release](https://github.com/apocas/opencode-sandbox/releases/latest),
  check it, and put it on your `PATH`:

  ```bash
  v=0.15.0 os=$(uname -s | tr A-Z a-z) arch=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
  f=opencode-sandbox_${v}_${os}_${arch}
  curl -fLO https://github.com/apocas/opencode-sandbox/releases/download/v$v/$f
  curl -fL https://github.com/apocas/opencode-sandbox/releases/download/v$v/SHA256SUMS | grep " $f\$" | shasum -a 256 -c
  install -m 755 $f ~/.local/bin/opencode-sandbox && rm $f
  ```

- **With Go:** `go install github.com/apocas/opencode-sandbox/cmd/opencode-sandbox@latest`
  (into `~/go/bin`).
- **From a git clone:** the `opencode-sandbox` script in the clone builds the
  program on first run and again when the sources change (this needs Go):

  ```bash
  git clone https://github.com/apocas/opencode-sandbox.git
  cd opencode-sandbox && ln -s "$PWD/opencode-sandbox" ~/.local/bin/opencode-sandbox
  ```

The first sandbox builds the image (or run `opencode-sandbox build`).

## Updating

```bash
opencode-sandbox update            # to the latest release
opencode-sandbox update --check    # only say whether there's something newer
```

**`update` installs a release only if this project's release workflow built
it.** Before replacing the program, it checks that the download matches the
release's `SHA256SUMS`, that the release's build attestation names this
project's release workflow for that tag, and that it runs as the version
announced. If any check fails, nothing changes.

The checksums sit next to the builds, so they only show the download is whole;
the attestation is signed by GitHub for the workflow's run. The keys it's
checked with (Sigstore's) are built into the program; if they're ever replaced,
reinstall with the [install script](#install). From a git clone, `update` runs
`git pull --ff-only` and rebuilds instead; your own changes are never
discarded.

Releases follow [Semantic Versioning](https://semver.org); read the
[changelog](CHANGELOG.md) before updating. Sandboxes you have keep the image
and settings they were made with (`open` says when one is older); new ones
build the new image on first use. Remove an old image with
`docker image rm opencode-sandbox:VERSION` (or `container image delete`) once
no sandbox uses it.

Coming from an older version:

- **0.9.0:** update. Its gateway passed on any kind of request, `TRACE` too,
  which a provider can answer with the key in it.
- **0.8 or earlier:** sandboxes made then keep their API keys in their config
  (`ls` marks them `*`); create new ones to get the keys out.
- **0.7 or earlier:** new sandboxes run on gVisor by themselves where Docker
  has a runtime that keeps the ssh-agent working;
  `OPENCODE_SANDBOX_RUNTIME=runc` keeps plain Docker.
- **0.6.1 or earlier** has no `update`: run the install script again.
- **0.5 or earlier:** the commands are subcommands now, and `opencode-sandbox`
  alone opens the dashboard; an old option names its new command.

## Usage

### The dashboard

`opencode-sandbox` on its own opens the dashboard: your sandboxes, kept up to
date, with the selected one's details. A second of intro plays first; any key
skips it, `OPENCODE_SANDBOX_INTRO=off` turns it off.

| Key | |
|---|---|
| `↑` `↓` (`k` `j`) | move |
| `enter` | open the sandbox, continuing its last session (or attach to it if it's open) |
| `n` | create a sandbox. In the form, `ctrl-o` asks the rest: network and allowed hosts, where it runs, API keys, memory and CPUs. An empty folder field copies nothing |
| `esc` | back, without doing anything (as does `ctrl-c`); while a sandbox is being created, back to the list as the work carries on |
| `s` | bash inside the sandbox (it must be open) |
| `c` / `g` | copy a file or folder into its `/workspace` / out of it, into the current folder |
| `p` | show its public key (`y` copies it) |
| `a` | for a [filtered network](#a-filtered-network): the hosts it may reach and the ones it was refused (`a` allows the one under the cursor, `d` denies it) |
| `space`, `d` | mark sandboxes; delete the marked ones (or the selected one), after asking |
| `/` | filter by name, folder or provider |
| `h` | the health report ([Isolation](#isolation)) |
| `b` | rebuild the image for new sandboxes |
| `?` | all keys |
| `q` | quit, as does `ctrl-c` twice |

### Commands

```bash
opencode-sandbox new ~/src/api                 # asks for the key and provider
opencode-sandbox new . -n api -k new -p my-provider
opencode-sandbox new --empty                   # nothing copied in
opencode-sandbox open api                      # continue where you left off
```

```
opencode-sandbox new [DIR] [-n NAME] [-k KEY] [-p PROVIDER] [OPTIONS] [-- ARGS...]
```

| Option of `new` | |
|---|---|
| `DIR` | folder to copy in (default: the current one). Your home folder, `~/.ssh` and similar are refused |
| `--empty` | copy nothing: `/workspace` starts empty |
| `-n NAME` | sandbox name (default: the folder's name plus a random suffix) |
| `-k KEY` | a key name from `~/.ssh`, `new` (disposable) or `none`; asked if omitted. Only `new` or `none` on [some platforms](#platforms) |
| `-p PROVIDER` | provider ids from your opencode config, several as `a,b`; asked if omitted, automatic if you have only one |
| `--network open\|filtered\|none` | what it reaches ([Network](#network)) |
| `--allow HOSTS` | the hosts and built-in lists a [filtered network](#a-filtered-network) reaches, as `a,b`; implies `--network filtered` |
| `--engine docker\|apple` | where it runs (default: Apple container where that runs, else Docker) |
| `--runtime NAME` | the Docker runtime, e.g. `runc` (default: gVisor where Docker has it) |
| `--gateway on\|off` | `off` puts its providers' API keys in its config ([API keys](#api-keys)) |
| `--memory AMOUNT`, `--cpus N` | the most memory (`8g`, `512m`) and CPUs it may use (default `4g` and 4; [Memory and CPUs](#memory-and-cpus)) |
| `-- ARGS` | passed to opencode |

`--network`, `--engine`, `--runtime`, `--gateway`, `--memory` and `--cpus`
have defaults you can set in the [environment](#configuration); in the
dashboard's form, `ctrl-o` asks them.

| Command | |
|---|---|
| `ls` | list sandboxes: state, what they run in, network, key, providers, age, folder |
| `open [NAME]` | reopen a sandbox and continue its last session (asks which if no NAME) |
| `shell NAME` | bash inside an open sandbox |
| `key NAME` | print the public key a sandbox uses |
| `net NAME` | the hosts a sandbox with a [filtered network](#a-filtered-network) may reach, and what it was refused |
| `allow NAME HOST...` / `deny NAME HOST...` | let it reach hosts, or refuse them whatever else allows them; at once, also while it's open |
| `cp NAME PATH...` | copy files or folders into a sandbox's `/workspace`, open or closed |
| `get NAME PATH...` | copy files or folders out of a sandbox (paths relative to `/workspace`) into the current folder |
| `rm NAME...` / `rm --all [-y]` | delete sandboxes, their files and disposable keys |
| `health` | whether sandboxes can run here, how they're isolated, and what would isolate them better |
| `setup [-y]` | install the strongest isolation this machine can have; asks first (`-y`: doesn't) |
| `build [ARGS]` | rebuild the image (ARGS go to `docker build` or `container build`) |
| `agents [stop]` | show (or stop) the ssh-agents of open sandboxes |
| `update [--check]` | update opencode-sandbox ([Updating](#updating)) |
| `help [COMMAND]` | help, or all options of a command |
| `version` | the opencode-sandbox and opencode versions |

On a terminal, commands ask with menus and show progress; in scripts and pipes
they never ask: pass `-k`, `-p`, a NAME or `-y`. `NO_COLOR=1` turns colors off.

Quitting opencode or closing the terminal stops the sandbox and unloads its
key. `ctrl-]` `ctrl-]` detaches and leaves it running (not in Apple
container); `open` reattaches.

### Adding files later

**The folder is copied once, when the sandbox is created.** Copy in what you
add later with `cp` (or `c` in the dashboard), even while opencode runs; it can
use it straight away:

```bash
opencode-sandbox cp api ./logs.txt ./configs   # into /workspace
opencode-sandbox get api report.md             # out, into the current folder
```

`cp` refuses the same paths as `new`. `get` never overwrites a file, nor writes
into those paths. What comes out was written by the agent: treat it as
untrusted.

## SSH through a jump host

Tell the agent how to reach your servers, for example:

```bash
ssh -i ~/.ssh/id_ed25519 -A -p 2222 user@jump.example.com \
  ssh root@10.0.0.5 "'uptime; df -h'"
```

- The public key is mounted under its own name, so `-i ~/.ssh/<name>` works as
  is. A disposable key is `~/.ssh/sandbox_key`, which ssh uses without `-i`.
- Quote the remote command twice, as above; with one layer, `$(...)` runs on
  the jump host instead of the target.
- `-A` lets root on the jump host use the sandbox's key (only that key) while
  you're connected. Where the jump host allows TCP forwarding, `-J` avoids it:
  `ssh -i ~/.ssh/id_ed25519 -J user@jump.example.com:2222 root@10.0.0.5 'uptime'`
- `OPENCODE_SANDBOX_SEED_HOSTS="[jump.example.com]:2222"`, set before the first
  run, copies the jump host's key from your `~/.ssh/known_hosts` into
  sandboxes, so the first hop is verified instead of trusted on first use.

## How it works

On your machine, in `$XDG_RUNTIME_DIR/opencode-sandbox/` (or
`$TMPDIR/opencode-sandbox-UID/`, as on macOS):

- `agents/NAME/`: the sandbox's own ssh-agent, running while it's open. Only
  its socket's folder is mounted in. It refuses PKCS#11/FIDO libraries.
- `configs/NAME.json`: the sandbox's filtered opencode config, written again at
  every start.
- `gateways/NAME/`: its gateway's token and log (provider, path, status; no
  keys, no contents). The gateway is a second opencode-sandbox process that
  runs while the sandbox is open. Nothing here is mounted in.
- `sandboxes/NAME/`: for Apple container, which mounts only folders: the
  config and a copy of `known_hosts`.

In `$XDG_DATA_HOME/opencode-sandbox/` (by default
`~/.local/share/opencode-sandbox/`):

- `keys/NAME/`: a sandbox's disposable key pair, deleted with the sandbox.
- `net/NAME/`: the list and refused hosts of a sandbox with a filtered network.
- `known_hosts`: the seeded host keys every sandbox trusts.

In the sandbox:

- `/workspace` and `/home/agent` (opencode's sessions) are volumes, kept until
  you `rm` the sandbox.
- `~/.ssh` holds only the public key, and `SSH_AUTH_SOCK` is the mounted
  agent's socket. Without a host agent, the disposable key is copied to
  `~/.ssh/sandbox_key` at each start and loaded into an ssh-agent inside.
- `127.0.0.1:17872` is the gateway: `opencode-sandbox-gateway`, a small program
  in the image that relays what it's sent to your machine. opencode starts once
  it's listening.

`OPENCODE_SANDBOX_DEBUG=1` prints every command opencode-sandbox runs.

## What it doesn't protect against

- **An open network is open.** By default a sandbox reaches the internet, your
  LAN, services on your machine and a cloud's metadata endpoint.
  `--network filtered` or `none` closes that ([Network](#network)).
- **A filtered network is as tight as its list.** An allowed host is a way out
  for whoever has an account there, and an allowed SSH host is a tunnel.
- **Anything inside an open sandbox can use its SSH key and its providers**:
  log into every server that trusts the key, spend your credit. With `ssh -A`,
  so can root on the host you forward to.
- **Without a host agent, the disposable key can be copied out**, and works
  wherever you authorized it until you remove it from `authorized_keys`, even
  after `rm`.
- **Secrets in the folder go in with it**: a `.env`, an `.npmrc`, a token in
  `.git/config`. `new` and `cp` name the dot files that may hold some, but
  can't tell what's in other files.
- **Some secrets in your opencode config go in as they are:** a local MCP
  server's `environment`, a secret under a name the gateway doesn't know
  ([API keys](#api-keys)).
- **A sandbox can fill your disk:** only memory, CPUs and processes are capped.
- **A sandbox's home persists until you delete it**, so something planted there
  runs again when you resume it.
- **Prompt injection.** Limiting the key, providers and network limits the
  damage; it doesn't prevent it.
- **With Docker, sandboxes share a kernel**: your machine's on Linux, Docker
  Desktop's VM elsewhere. For hostile code, use Apple container or gVisor
  ([Isolation](#isolation)).

## Configuration

| Variable | |
|---|---|
| `OPENCODE_SANDBOX_CONFIG` | opencode config to take providers from (default `~/.config/opencode/opencode.json`) |
| `OPENCODE_SANDBOX_NO_AGENT` | `1`: keep disposable keys inside sandboxes even where a host ssh-agent would work ([Platforms](#platforms)) |
| `OPENCODE_SANDBOX_GATEWAY` | `off`: give new sandboxes their providers' API keys instead of a gateway ([API keys](#api-keys)) |
| `OPENCODE_SANDBOX_NETWORK` | `filtered` or `none`: the network of new sandboxes ([Network](#network)) |
| `OPENCODE_SANDBOX_ASK` | `off`: never ask on screen (macOS) about a host on no list ([A filtered network](#a-filtered-network)) |
| `OPENCODE_SANDBOX_MEMORY`, `OPENCODE_SANDBOX_CPUS` | the most memory (`8g`) and CPUs a new sandbox may use ([Memory and CPUs](#memory-and-cpus)) |
| `OPENCODE_SANDBOX_ENGINE` | `docker` or `apple`: where new sandboxes go (default: Apple container where it runs) |
| `OPENCODE_SANDBOX_RUNTIME` | a Docker runtime for new sandboxes, e.g. `runc` (default: gVisor where Docker has it) |
| `OPENCODE_SANDBOX_SEED_HOSTS` | hosts whose keys are copied from `~/.ssh/known_hosts` into sandboxes; read when `known_hosts` is created, delete it to re-seed |
| `OPENCODE_SANDBOX_INTRO` | `off`: open the dashboard without its intro |
| `OPENCODE_SANDBOX_DEBUG` | `1`: print every command opencode-sandbox runs |

To use another opencode release, pass its version and archive checksums (new
sandboxes only):

```bash
opencode-sandbox build --build-arg OPENCODE_VERSION=X.Y.Z \
  --build-arg OPENCODE_SHA256_AMD64=... --build-arg OPENCODE_SHA256_ARM64=...
```

## Development

```bash
go vet ./... && go test ./...                       # unit tests (no Docker)
go build -o .bin/opencode-sandbox ./cmd/opencode-sandbox
test/integration.sh .bin/opencode-sandbox          # against a real Docker
```

CI runs all of this on every push.

## Releasing

1. Bump `Version` in `internal/core/core.go` and add a section to
   `CHANGELOG.md`. To bundle a newer opencode, update `OPENCODE_VERSION` and
   both checksums in the `Dockerfile`.
2. Now and then, refresh the keys `update` trusts:
   `go run ./tools/trustedroot /tmp/tuf > internal/selfupdate/trusted_root.json`;
   `go test ./internal/selfupdate/` verifies a real release with them.
3. Commit, tag and push:
   `git tag -a vX.Y.Z -m "vX.Y.Z" && git push origin master vX.Y.Z`. The
   release workflow builds and attests the binaries and publishes them with
   their `SHA256SUMS`, the attestation and that version's changelog section.
   Check the CI run the tag starts, too.

## License

[Apache-2.0](LICENSE)
