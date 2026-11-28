#!/bin/sh
# Installs opencode-sandbox: downloads the latest release's program for this
# system from GitHub, checks it against the release's SHA256SUMS, and puts it
# in ~/.local/bin (/usr/local/bin when run as root). Where GitHub's gh is
# installed and signed in, it also checks that this project's release
# workflow built the download (its build attestation).
#
#   curl -fsSL https://raw.githubusercontent.com/apocas/opencode-sandbox/master/install.sh | sh
#
# Run it again to update, or use: opencode-sandbox update
#
#   OPENCODE_SANDBOX_VERSION=0.6.1          install this version, not the latest
#   OPENCODE_SANDBOX_INSTALL_DIR=/some/bin  put it in this folder
set -eu

repo=https://github.com/apocas/opencode-sandbox

say() { printf '%s\n' "opencode-sandbox: $*"; }
die() {
  say "$*" >&2
  exit 1
}
has() { command -v "$1" >/dev/null 2>&1; }

# fetch URL FILE
fetch() {
  if has curl; then
    curl -fsSL --proto '=https' --proto-redir '=https' -o "$2" "$1"
  else
    wget -q -O "$2" "$1"
  fi
}

# sha256 FILE
sha256() {
  if has sha256sum; then
    sha256sum "$1" | cut -d ' ' -f 1
  else
    shasum -a 256 "$1" | cut -d ' ' -f 1
  fi
}

main() {
  has curl || has wget || die "curl or wget is needed to download it"
  has sha256sum || has shasum || die "sha256sum or shasum is needed to check the download"

  case $(uname -s) in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    *) die "there are builds for Linux and macOS only (this is $(uname -s))" ;;
  esac
  case $(uname -m) in
    x86_64 | amd64) arch=amd64 ;;
    arm64 | aarch64) arch=arm64 ;;
    *) die "there are builds for x86_64 and arm64 only (this is $(uname -m))" ;;
  esac
  # A shell running under Rosetta says x86_64 on Apple silicon.
  if [ "$os" = darwin ] && [ "$arch" = amd64 ] && [ "$(sysctl -n hw.optional.arm64 2>/dev/null || true)" = 1 ]; then
    arch=arm64
  fi

  if [ -n "${OPENCODE_SANDBOX_INSTALL_DIR:-}" ]; then
    dir=$OPENCODE_SANDBOX_INSTALL_DIR
  elif [ "$(id -u)" = 0 ]; then
    dir=/usr/local/bin
  else
    [ -n "${HOME:-}" ] || die "HOME isn't set; say where to install with OPENCODE_SANDBOX_INSTALL_DIR"
    dir=$HOME/.local/bin
  fi
  dest=$dir/opencode-sandbox
  # A folder there, or a link to one, would get the program put inside it.
  [ ! -d "$dest" ] || die "$dest is a folder; remove it, or choose another place with OPENCODE_SANDBOX_INSTALL_DIR"

  tmp=$(mktemp -d)
  new=$tmp/none
  trap 'rm -rf "$tmp" "$new"' EXIT
  trap 'exit 1' HUP INT TERM

  # The release's SHA256SUMS names its version and every build.
  v=${OPENCODE_SANDBOX_VERSION:-}
  v=${v#v}
  if [ -n "$v" ]; then
    sums=$repo/releases/download/v$v/SHA256SUMS
  else
    sums=$repo/releases/latest/download/SHA256SUMS
  fi
  fetch "$sums" "$tmp/SHA256SUMS" || die "couldn't download $sums"
  line=$(grep -E "^[0-9a-f]{64} [ *]opencode-sandbox_[0-9]+\.[0-9]+\.[0-9]+_${os}_${arch}\$" "$tmp/SHA256SUMS" | head -n 1 || true)
  [ -n "$line" ] || die "that release has no build for $os/$arch (see $repo/releases)"
  want=${line%% *}
  file=${line##*[ *]}
  v=${file#opencode-sandbox_}
  v=${v%%_*}

  say "downloading opencode-sandbox $v for $os/$arch"
  fetch "$repo/releases/download/v$v/$file" "$tmp/$file" || die "couldn't download $repo/releases/download/v$v/$file"
  got=$(sha256 "$tmp/$file")
  [ "$got" = "$want" ] || die "the download doesn't match the release's SHA256SUMS (got $got, want $want); nothing was installed"

  # SHA256SUMS comes from where the download does. Who built it is said by
  # its attestation, which gh can check (this script, in plain sh, can't;
  # `opencode-sandbox update` does it by itself from then on).
  if has gh && gh attestation verify --help >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
    if gh attestation verify "$tmp/$file" --repo apocas/opencode-sandbox \
      --signer-workflow apocas/opencode-sandbox/.github/workflows/release.yml >"$tmp/attestation.out" 2>&1; then
      say "checked that this project's release workflow built it (gh attestation verify)"
    else
      sed 's/^/  /' "$tmp/attestation.out" >&2
      die "gh attestation verify doesn't confirm that this project's release workflow built the download; nothing was installed"
    fi
  else
    say "checked against the release's checksums only: who built it is checked too where GitHub's gh is installed and signed in"
  fi

  # Next to where it goes, so that putting it in place is one rename: an
  # opencode-sandbox that's there keeps working until then.
  mkdir -p "$dir" 2>/dev/null || die "can't create $dir; choose another place with OPENCODE_SANDBOX_INSTALL_DIR"
  new=$dir/.opencode-sandbox.$$
  if ! { cp "$tmp/$file" "$new" && chmod 755 "$new"; } 2>/dev/null; then
    die "can't write to $dir; choose another place with OPENCODE_SANDBOX_INSTALL_DIR, or run this as someone who can"
  fi
  "$new" version >/dev/null 2>&1 || die "the downloaded program doesn't run on this system; nothing was installed"
  if [ -L "$dest" ]; then
    say "replacing the link $dest -> $(readlink "$dest")"
  fi
  mv -f "$new" "$dest"

  say "installed $("$dest" version) as $dest"
  case ":$PATH:" in
    *":$dir:"*)
      first=$(command -v opencode-sandbox 2>/dev/null || true)
      if [ -n "$first" ] && [ "$first" != "$dest" ]; then
        say "another one, $first, comes first on your PATH and is what 'opencode-sandbox' runs; remove it, or put $dir before it"
      fi
      ;;
    *)
      say "$dir isn't on your PATH. Add this line to your shell's startup file (~/.zshrc, ~/.bashrc, ...):"
      say "  export PATH=\"$dir:\$PATH\""
      ;;
  esac
  say "next: 'opencode-sandbox health' shows whether sandboxes can run here and how they're isolated,"
  say "      'opencode-sandbox setup' installs what isolates them best, and 'opencode-sandbox' opens the dashboard"
}

# All of the script is read before any of it runs: half a download does nothing.
main "$@"
