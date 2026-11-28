# Changelog

All notable changes to opencode-sandbox. Versions follow
[Semantic Versioning](https://semver.org); each release is tagged `vX.Y.Z`.
To update, see [Updating](README.md#updating).

## [0.15.0] - 2026-11-28

### Added

- **`update` checks who built what it installs.** Until now it checked a
  download against the release's `SHA256SUMS`, which sit next to the builds:
  whoever could swap a build could swap its checksum. It now also checks the
  release's build attestation: a statement signed by GitHub for that run of
  this project's release workflow, and recorded in a public log, that the
  workflow built this very file for that release's tag. A build made any
  other way is refused, and nothing is changed. The keys it trusts are built
  in, so the check needs no network beyond the download. See
  [Updating](README.md#updating).
- Each release now carries its attestation as a file, `attestation.jsonl`,
  next to the builds.
- The install script checks the same where GitHub's `gh` is installed and
  signed in (`gh attestation verify`), and says when it could only check the
  checksum.

### Notes

- The check comes with the Sigstore library, which makes the program larger:
  about 22 MB, from about 10 MB.
- Releases before this one have no attestation file; `update` only ever
  installs the latest, which has.

## [0.14.0] - 2026-11-28

### Added

- **A filtered network: a sandbox that reaches only the hosts you allow.**
  `opencode-sandbox new --network filtered --allow github,npm` makes a sandbox
  that reaches its providers and the hosts on its list, and nothing else: not
  the rest of the internet, not your network, not your machine. So an agent
  can `git pull` and install packages without being able to send your code
  wherever it likes. The list takes hosts (`example.com`, `*.example.com`,
  `192.0.2.7`, `10.0.0.0/8`, each for ports 443, 80 and 22, or with `:PORT`)
  and the built-in lists `github`, `gitlab`, `npm`, `pypi` and `go`. See
  [A filtered network](README.md#a-filtered-network).
- `opencode-sandbox allow NAME HOST...` and `deny NAME HOST...` change a
  sandbox's list, at once and also while it's open; `net NAME` shows the
  list and the hosts the sandbox was refused, with how often and why. In the
  dashboard, `a` shows the same and allows or denies a refused host with one
  key, and the form (`ctrl-o`) has the network and its allowed hosts.
- On a Mac, a dialog asks when a sandbox wants a host that's on no list,
  while the connection waits: Deny (the default), allow until the sandbox
  closes, or always allow. Not with the screen locked, and not with
  `OPENCODE_SANDBOX_ASK=off`; then, and on Linux, the host is refused and
  written down.
- Such a sandbox has no network of its own, like one with `--network none`.
  Its programs use the gateway as their proxy, and `ssh` leaves through it
  too (so SSH keys work, and `git` over SSH). Nothing listens on a network,
  and it works the same on Docker, gVisor and Apple container.
- Addresses that aren't on the internet (this machine, your network, a
  cloud's metadata address) are reached only by a rule that names the
  address; a name that leads there isn't enough. For HTTPS, the name in the
  TLS handshake has to be the host that was allowed.
- `OPENCODE_SANDBOX_NETWORK=filtered` makes it the default for new sandboxes.

### Fixed

- One connection through the gateway that stopped reading could hold up
  every other one of its sandbox, a model's answer among them, for as long
  as it stayed open. Such a connection is now given up after 30 seconds.

### Notes

- What a filtered network doesn't do is in the README: an allowed host is a
  way out for whoever has an account on it, an allowed SSH host is a tunnel,
  and programs that ignore proxy settings have no route at all.
- The image changes (the in-sandbox half of the gateway, and ssh's config),
  so the first new sandbox after updating rebuilds it.

## [0.13.0] - 2026-11-28

### Added

- **MCP servers' secrets stay on your machine.** A remote MCP server in your
  opencode config often has a token, in a header or in its address. It used
  to be copied into every sandbox's config, where the agent could read it.
  Now such a server is reached through the gateway, like a provider: the
  sandbox's config has the gateway's address and a token that means nothing
  outside, and opencode-sandbox puts the real header in on your machine. See
  [MCP servers](README.md#mcp-servers).
- `DELETE`, which ends an MCP session, is passed on to MCP servers. Providers
  stay on `GET`, `HEAD` and `POST`.
- In a sandbox without a network, every remote MCP server goes through the
  gateway, so it can be used there.
- What can't be kept out is said when a sandbox starts, marked with `*` in
  `ls` and named by `health`: the `environment` of a local MCP server, which
  runs inside the sandbox, and an OAuth `clientSecret`.

### Changed

- A provider's address inside a sandbox is now
  `http://127.0.0.1:17872/provider/NAME` (it was `/NAME`), so that a provider
  and an MCP server can share a name. A sandbox's config is written again each
  time it opens, so nothing needs doing; the old form is still served.

### Notes

- Sandboxes you already have get this the next time they're opened, without
  being recreated. One that is open now keeps the config it started with.

## [0.12.0] - 2026-11-28

### Added

- **A sandbox has a cap on memory and CPUs.** New sandboxes may use up to
  4 GB of memory and 4 CPUs, so a build that eats memory or spins every core
  is held inside the sandbox instead of starving your machine. Docker
  sandboxes had no such cap before; in Apple container it's the size of the
  sandbox's VM, which was fixed at the same. `opencode-sandbox new --memory 8g
  --cpus 6` sets them for one sandbox, `OPENCODE_SANDBOX_MEMORY` and
  `OPENCODE_SANDBOX_CPUS` for new ones, and the dashboard's form asks them
  with its other options (`ctrl-o`). See
  [Memory and CPUs](README.md#memory-and-cpus).
- A sandbox never gets more than its engine has: where Docker Desktop's VM
  has 2 CPUs, say, it gets 2, and is told so if it asked for more. In Docker
  the memory cap covers swap too, where Docker can do that.
- The dashboard's details show each sandbox's limits, and `health` the ones
  new sandboxes get. Where Docker can't hold a container to a limit (no
  cgroup controller for it), `health` says so.

### Notes

- Existing sandboxes keep what they have: Docker ones no cap, Apple container
  ones 4 GB and 4 CPUs.
- Disk isn't capped; the README's limits say so.

## [0.11.1] - 2026-11-28

### Fixed

- The dashboard came back garbled, with parts missing or in the wrong place,
  after a sandbox in Apple container closed: on quitting opencode with
  `ctrl-c`, say. Apple's `container` leaves the terminal in non-blocking mode
  when it exits, so every redraw of the dashboard was cut short after its
  first 1024 bytes. The terminal is now put back in blocking mode whenever a
  sandbox's program returns. The shell you come back to after `new`, `open`
  or `shell` no longer inherits it non-blocking either.

## [0.11.0] - 2026-11-28

### Added

- A sandbox can start empty: `opencode-sandbox new --empty` copies no folder
  into it, and in the dashboard's form leaving the folder field empty does
  the same. Its `/workspace` is empty; `cp` still copies files in later. `ls`
  and the dashboard show such a sandbox as copied from "nothing", and its
  default name is `sandbox-` plus a suffix.

## [0.10.0] - 2026-11-27

### Added

- **A sandbox can have no network.** `opencode-sandbox new --network none`
  makes one with only its own loopback interface: it reaches nothing but its
  providers, through the gateway, which doesn't travel over a network. Not
  the internet, your network, your machine's services or a cloud's metadata
  endpoint; so no SSH key, no `git pull` and no package installs either. Every
  provider of such a sandbox goes through the gateway, those without an API
  key too. `OPENCODE_SANDBOX_NETWORK=none` makes it the default. See
  [Network](README.md#network).
- `new` takes `--engine docker|apple`, `--runtime NAME` and `--gateway on|off`
  for one sandbox, where before only the environment could say
  (`OPENCODE_SANDBOX_ENGINE`, `_RUNTIME`, `_GATEWAY`, which stay as defaults).
- The dashboard's new-sandbox form asks these too: `ctrl-o` adds a page with
  the network, where the sandbox runs (each engine running here and, in
  Docker, each kind of runtime) and where its API keys are.
- `ls` has a NETWORK column, the dashboard shows a sandbox's network, and
  `health` says what a new sandbox reaches.
- The dashboard opens with a short intro: the name is written while a wall
  goes up around it and closes. It lasts a little over a second, any key ends
  it, and `OPENCODE_SANDBOX_INTRO=off` turns it off. It isn't shown on a
  terminal too small for it, nor when there's no engine to run sandboxes in.

### Changed

- `ls`: the NETWORK column comes after RUNS IN; a script that reads columns
  by position needs a look.
- Compatibility with the bash launcher of 0.4 and earlier is no longer
  tested: sandboxes made by it aren't promised to keep working.
## [0.9.2] - 2026-11-27

### Fixed

- The dashboard crashed (`slice bounds out of range`) when `n` was pressed
  after starting it in your home folder: the form opens on `~`, and completing
  that as a folder name failed. Tab in the copy prompt crashed the same way on
  `~` and `~/`, and turned `~/src/` into `~/ssrc/`; the form offered no folders
  after a `~/…/`. Now `~` completes to `~/` and what's under it as typed.

## [0.9.1] - 2026-11-27

### Security

- The gateway passes on only `GET`, `HEAD` and `POST` requests. 0.9.0 passed
  on any kind, including `TRACE`, which makes a server that answers it send
  the request back: a program in a sandbox could have read the provider's API
  key that way. Most APIs refuse `TRACE`. If yours answers it and something
  you don't trust ran in a sandbox made with 0.9.0, replace that key.
- A provider's secrets other than `options.apiKey` and `options.headers` (a
  cloud's `secretAccessKey`, a header of a single model) aren't kept by the
  gateway and go into the sandbox's config. 0.9.0 said nothing about them:
  now opencode-sandbox warns when the sandbox starts, `ls` marks it with `*`
  and `health` names the option.

### Added

- `new` and `cp` name the dot files that may hold secrets in what they copy
  (`.env`, `.npmrc`, a `.git/config` with a token, any other but the likes of
  `.gitignore`), since they go into the sandbox with the rest. On a terminal,
  `new` waits for enter before it copies; the dashboard's form shows them
  under the folder.

## [0.9.0] - 2026-11-27

### Added

- **Your providers' API keys no longer go into sandboxes.** A new sandbox's
  opencode config has a token in their place and a gateway inside the sandbox
  (`127.0.0.1:17872`) as each provider's address. What opencode sends there
  is relayed to opencode-sandbox on your machine, which puts the real key into
  the request and passes it on to the provider. The agent can use a provider
  while its sandbox is open, and can't read or copy the key. See
  [API keys](README.md#api-keys).
  - Nothing listens on a network: the two ends talk over the engine's `exec`,
    so it works the same with Docker, Docker Desktop, gVisor and Apple
    container.
  - The key is put only into a header keys travel in (`Authorization`,
    `x-api-key`, `api-key`, `x-goog-api-key`) of a request to that provider,
    and the headers of `options.headers` are kept back the same way.
  - A key written as `{env:NAME}` or `{file:PATH}` is resolved on your machine,
    and a key you rotate is used from the next request on.
  - It needs the provider's address: `options.baseURL`, or the well-known one
    of `anthropic`, `openai`, `google`, `groq`, `mistral`, `xai` and
    `openrouter`. A provider without either still gets its key in the
    sandbox's config, and opencode-sandbox says so.
- `ls` marks a sandbox that has an API key in its config with `*` after its
  provider, the dashboard shows where a sandbox's API keys are, and `health`
  says where a new sandbox's will be.
- `OPENCODE_SANDBOX_GATEWAY=off` creates sandboxes as before, with the keys
  inside.

### Changed

- Sandboxes you already have keep the API key in their config (they have no
  gateway): create new ones to get it out.
- The image has `opencode-sandbox-gateway`, the gateway's end inside a
  sandbox, and opencode starts once it's listening.

## [0.8.0] - 2026-11-27

### Added

- `opencode-sandbox setup` installs the strongest isolation the machine can
  have, after showing every step and command and asking: on a Mac (Apple
  silicon, macOS 26 or later) Apple container, whose installer it downloads,
  checks for Apple's signature, runs and starts; on Linux with Docker Engine
  gVisor, which it downloads, checks against its published SHA-512, installs in
  `/usr/local/bin` and registers with Docker as runtimes `runsc` and
  `runsc-ssh`. Steps that need root run with `sudo`. Where nothing stronger can
  be installed, it says why.
- `health` ends with a "Next step" when `setup` has something to install, and
  its options name `setup` instead of commands to type.
- The dashboard's header shows when new sandboxes run on gVisor.

### Changed

- New sandboxes run on gVisor by themselves where Docker has it, as they
  already go to Apple container where that runs: no more
  `OPENCODE_SANDBOX_RUNTIME=runsc`. A gVisor runtime that can't reach the
  host's ssh-agent is only picked where no agent could serve sandboxes anyway,
  so nobody loses keys from `~/.ssh` by it. `OPENCODE_SANDBOX_RUNTIME=runc`
  keeps new sandboxes on plain Docker. Sandboxes you already have don't change.

### Fixed

- Apple container: sandboxes showed as "new" instead of "closed" once its
  service had been restarted or the Mac rebooted. They opened and continued
  their session as usual; only the state shown was wrong.

## [0.7.0] - 2026-11-27

### Added

- `opencode-sandbox update` updates the program itself: it downloads the latest
  release's binary for this system, checks it against the release's
  `SHA256SUMS` and that it runs, and only then replaces the one in use
  (`--check` only says whether there's something newer). Where opencode-sandbox
  runs from a git clone, it runs `git pull --ff-only` there and rebuilds.
- `install.sh` installs the latest release in one line:
  `curl -fsSL https://raw.githubusercontent.com/apocas/opencode-sandbox/master/install.sh | sh`.
  It checks the download against `SHA256SUMS` and puts it in `~/.local/bin`
  (or `OPENCODE_SANDBOX_INSTALL_DIR`; `OPENCODE_SANDBOX_VERSION` picks a
  version). Running it again updates, also from versions without `update`.

## [0.6.1] - 2026-11-27

### Fixed

- Dashboard: `esc` (or `ctrl-c`) leaves the new-sandbox form without creating
  anything. Before, the only ways out were to create a sandbox or to quit.
- Dashboard: while a sandbox is being created or the image built, `esc` goes
  back to the list; the work carries on, and you're told when it's done.
- Dashboard: `ctrl-c` in the list quits on the second press, so one press too
  many when closing opencode doesn't close the dashboard as well. `q` quits at
  once.
- Dashboard: the new-sandbox form hid its provider choices. It now also shows
  the keys of the field in use, and `tab` completes the folder.
- Dashboard: messages were hidden while a filter was applied, and "wait until
  it's done" while something ran in the background.
- `test/integration.sh` leaves sandboxes other than its own alone: with any
  around, it no longer runs `rm --all`, `agents stop` or the sandbox picker.

## [0.6.0] - 2026-11-27

### Changed (breaking)

- Commands are subcommands, and `opencode-sandbox` on its own opens the
  dashboard (on a terminal) instead of creating a sandbox. An old option says
  what replaced it.

  | 0.5 | 0.6 |
  |---|---|
  | `opencode-sandbox [-w DIR] …` | `opencode-sandbox new [DIR] …` |
  | `--list` | `ls` |
  | `--resume [NAME]` | `open [NAME]` |
  | `--pubkey NAME` | `key NAME` |
  | `--shell`, `--cp`, `--get`, `--rm`, `--build`, `--health` | `shell`, `cp`, `get`, `rm`, `build`, `health` |
  | `--status`, `--stop-agent` | `agents`, `agents stop` |
  | `--help`, `--version` | `help`, `version` (the old spellings still work) |

- Without a terminal, commands never ask: `new` needs `-k` (and `-p` with
  several providers), `open` a name, and `rm --all` needs `-y`.
- Requires Go 1.26 to build from source.

### Added

- A dashboard: your sandboxes, kept up to date, with the details of the
  selected one (state, what isolates it, key and fingerprint, providers,
  folder, age). Keys open (and come back when opencode quits or detaches),
  create, shell, copy in and out, show the key, mark and delete, filter, show
  the health report, and rebuild the image.
- On a terminal, commands show arrow-key menus for the key, providers and
  sandbox, spinners for long steps such as the first image build (with its
  output if it fails), and colored, aligned tables. Colors follow a light or
  dark terminal and `NO_COLOR`; piped output stays plain.
- `help COMMAND` shows all options of a command.

## [0.5.0] - 2026-11-27

### Changed

- The image has no setuid or setgid programs, so nothing in a sandbox can gain
  privileges even where `no-new-privileges` doesn't exist (Apple container).
- opencode-sandbox is now a single Go program instead of a bash script. Its
  commands, options, messages and sandbox labels are unchanged, and sandboxes
  created by 0.1–0.4 keep working: list, resume, copy into and delete them as
  before.
- Install a release binary (built and attested by GitHub Actions), use
  `go install`, or keep your git clone: its `opencode-sandbox` script now
  builds the program when needed, which needs Go. bash and `jq` are no longer
  needed.
- New disposable keys and `known_hosts` are kept in
  `~/.local/share/opencode-sandbox/` (or under `$XDG_DATA_HOME`) instead of the
  clone. Sandboxes you already have keep using the clone's, so keep the clone
  until you've deleted them; run from the clone, the first new sandbox copies
  its `known_hosts`.

### Added

- Apple container: on a Mac with Apple silicon and macOS 26 or later where
  Apple's `container` runs, new sandboxes go there, each in a lightweight VM
  of its own on a network of its own (4 GB, 4 CPUs). Sandboxes made with
  Docker stay there; `--list` shows where each runs. Set
  `OPENCODE_SANDBOX_ENGINE=docker` to keep using Docker. Apple container can't
  detach (`ctrl-]` `ctrl-]`) or open a sandbox in a second terminal.
- gVisor: set `OPENCODE_SANDBOX_RUNTIME=runsc` (or any Docker runtime) to run
  new sandboxes on gVisor, a user-space kernel for each sandbox. Keys from
  `~/.ssh` need the runtime to have `--host-uds=open`; without it, sandboxes
  get disposable keys stored inside them.
- `--health` checks that sandboxes can run on this machine and shows how a new
  one is isolated there (VM or kernel boundary, SSH keys, network, hardening),
  which stronger isolation is available, and what to install for it.
- `--list` has a RUNS IN column: docker, gvisor or apple.
- `OPENCODE_SANDBOX_DEBUG=1` prints every command opencode-sandbox runs.
- `--resume` explains when a file a sandbox mounts is gone (for example the
  `known_hosts` of a deleted clone) instead of failing with Docker's error.
- An integration test runs every command against a real Docker in CI, including
  on sandboxes made by 0.4.0.

### Fixed

- `--cp` and `--get` failed on a closed sandbox that uses a host ssh-agent
  (Linux), and on any closed sandbox after a reboot: Docker needs the files a
  sandbox mounts, which only exist while it's open. Broken since 0.3.0.
- `-w`, `--cp` and `--get` also refuse the folders that hold existing
  sandboxes' disposable keys, wherever they are.
- `--rm` deletes a disposable key only if it is plainly that sandbox's: in its
  folder, under its name, and made by opencode-sandbox for it.

## [0.4.0] - 2026-11-27

### Changed

- Works with the bash that comes with macOS (3.2); bash 4.4 is no longer
  required, so a Mac doesn't need Homebrew's bash.

### Fixed

- `--list` on macOS showed Docker's raw states ("Up 5 minutes",
  "Exited (0) …") instead of open, closed and new: macOS's `sed` doesn't
  understand `\t`.

First tested on a Mac (arm64, Docker Desktop, the built-in bash 3.2).

## [0.3.0] - 2026-11-26

### Added

- `--cp NAME PATH...` copies files or folders into a sandbox's `/workspace`,
  whether it's open or closed; opencode can use them straight away. It refuses
  the same paths as `-w`.
- `--get NAME PATH...` copies files or folders out of a sandbox into the
  current folder. It never overwrites, and won't write into your home folder,
  `~/.ssh` and similar.

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

[0.15.0]: https://github.com/apocas/opencode-sandbox/releases/tag/v0.15.0
[0.14.0]: https://github.com/apocas/opencode-sandbox/releases/tag/v0.14.0
[0.13.0]: https://github.com/apocas/opencode-sandbox/releases/tag/v0.13.0
[0.12.0]: https://github.com/apocas/opencode-sandbox/releases/tag/v0.12.0
[0.11.1]: https://github.com/apocas/opencode-sandbox/releases/tag/v0.11.1
[0.11.0]: https://github.com/apocas/opencode-sandbox/releases/tag/v0.11.0
[0.10.0]: https://github.com/apocas/opencode-sandbox/releases/tag/v0.10.0
[0.9.2]: https://github.com/apocas/opencode-sandbox/releases/tag/v0.9.2
[0.9.1]: https://github.com/apocas/opencode-sandbox/releases/tag/v0.9.1
[0.9.0]: https://github.com/apocas/opencode-sandbox/releases/tag/v0.9.0
[0.8.0]: https://github.com/apocas/opencode-sandbox/releases/tag/v0.8.0
[0.7.0]: https://github.com/apocas/opencode-sandbox/releases/tag/v0.7.0
[0.6.1]: https://github.com/apocas/opencode-sandbox/releases/tag/v0.6.1
[0.6.0]: https://github.com/apocas/opencode-sandbox/releases/tag/v0.6.0
[0.5.0]: https://github.com/apocas/opencode-sandbox/releases/tag/v0.5.0
[0.4.0]: https://github.com/apocas/opencode-sandbox/releases/tag/v0.4.0
[0.3.0]: https://github.com/apocas/opencode-sandbox/releases/tag/v0.3.0
[0.2.0]: https://github.com/apocas/opencode-sandbox/releases/tag/v0.2.0
[0.1.1]: https://github.com/apocas/opencode-sandbox/releases/tag/v0.1.1
[0.1.0]: https://github.com/apocas/opencode-sandbox/releases/tag/v0.1.0
