# syntax=docker/dockerfile:1
FROM debian:trixie-slim

# opencode release and the sha256 of its linux archives (see the release's assets).
ARG OPENCODE_VERSION=2.4.2
ARG OPENCODE_SHA256_AMD64=452912288f934d1771bd5bd0d8611ba3a6adc3db3ac3333c37facf373770ba66
ARG OPENCODE_SHA256_ARM64=bf8d18dde87224629341ff398e76c7c17216b9b406affc728b6c1a5c136abf93
ARG TARGETARCH
# Must match your host uid/gid: the host ssh-agent only serves its own uid.
ARG UID=1000
ARG GID=1000

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates curl git openssh-client ripgrep jq less procps \
 && rm -rf /var/lib/apt/lists/*

RUN case "${TARGETARCH:-amd64}" in \
      amd64) arch=x64;   sum="${OPENCODE_SHA256_AMD64}" ;; \
      arm64) arch=arm64; sum="${OPENCODE_SHA256_ARM64}" ;; \
      *) echo "unsupported architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
 && curl -fsSLo /tmp/opencode.tar.gz \
      "https://github.com/anomalyco/opencode/releases/download/v${OPENCODE_VERSION}/opencode-linux-${arch}.tar.gz" \
 && echo "${sum}  /tmp/opencode.tar.gz" | sha256sum -c - \
 && tar -xzf /tmp/opencode.tar.gz -C /usr/local/bin opencode \
 && chown root:root /usr/local/bin/opencode && chmod 755 /usr/local/bin/opencode \
 && rm /tmp/opencode.tar.gz

# Your gid may already exist in Debian (e.g. 20 "dialout", which is macOS's
# "staff", or 100 "users"): that group is then renamed to agent. A uid that
# belongs to a system user is refused rather than shared.
RUN if g="$(getent group "${GID}")"; then groupmod -n agent "${g%%:*}"; \
    else groupadd -g "${GID}" agent; fi \
 && if u="$(getent passwd "${UID}")"; then \
      echo "uid ${UID} is the image's '${u%%:*}' user; run opencode-sandbox as a regular user" >&2; \
      exit 1; \
    fi \
 && useradd -m -u "${UID}" -g "${GID}" -s /bin/bash agent \
 && install -d -o agent -g agent -m 700 /home/agent/.ssh \
 && install -d -o agent -g agent \
      /home/agent/.config/opencode /home/agent/.local/share/opencode \
      /home/agent/.local/state /home/agent/.cache /workspace

COPY --chmod=644 ssh_config /etc/ssh/ssh_config.d/10-opencode-sandbox.conf
COPY --chmod=755 entrypoint.sh /usr/local/bin/agent-entrypoint

# SSH_AUTH_SOCK is set per sandbox by the launcher, only when it has a key.
ENV OPENCODE_DISABLE_AUTOUPDATE=1

# Last, so a new launcher version doesn't invalidate the layers above.
ARG OPENCODE_SANDBOX_VERSION=dev
LABEL org.opencontainers.image.title="opencode-sandbox" \
      org.opencontainers.image.version="${OPENCODE_SANDBOX_VERSION}" \
      org.opencontainers.image.source="https://github.com/apocas/opencode-sandbox" \
      org.opencontainers.image.licenses="Apache-2.0"

USER agent
WORKDIR /workspace
ENTRYPOINT ["/usr/local/bin/agent-entrypoint"]
CMD ["opencode"]
