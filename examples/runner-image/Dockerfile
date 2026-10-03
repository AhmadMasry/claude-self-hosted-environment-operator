# Runner and orchestrator image. One image serves both roles: the operator
# runs `claude self-hosted-runner` or `claude self-hosted-runner orchestrator`.
FROM debian:bookworm-slim
ARG CLAUDE_CODE_VERSION
# Release platform of the Claude binary; empty derives it from the build platform.
ARG CLAUDE_ARCH=
ARG TARGETARCH
ARG RUNNER_UID=10001
RUN apt-get update \
 && apt-get install -y --no-install-recommends git curl ca-certificates openssh-client \
 && rm -rf /var/lib/apt/lists/*
RUN arch="${CLAUDE_ARCH:-}"; \
    if [ -z "$arch" ]; then \
      case "${TARGETARCH:-amd64}" in \
        amd64) arch=linux-x64 ;; \
        arm64) arch=linux-arm64 ;; \
        *) echo "unsupported TARGETARCH ${TARGETARCH}; set --build-arg CLAUDE_ARCH" >&2; exit 1 ;; \
      esac; \
    fi; \
    curl -fsSL "https://downloads.claude.ai/claude-code-releases/${CLAUDE_CODE_VERSION:?set with --build-arg CLAUDE_CODE_VERSION}/${arch}/claude" \
      -o /usr/local/bin/claude \
 && chmod 0755 /usr/local/bin/claude
# Verify the binary against the release's signed manifest before promoting the
# image: https://code.claude.com/docs/en/setup#binary-integrity-and-code-signing
# /etc/claude is left out: the operator mounts the environment Secret, hooks
# and wrapper there.
RUN groupadd -g "${RUNNER_UID}" runner \
 && useradd -m -u "${RUNNER_UID}" -g "${RUNNER_UID}" -s /usr/sbin/nologin runner \
 && mkdir -p /workspace /home/runner/.claude \
 && chown -R runner:runner /workspace /home/runner
RUN git config --system user.name "Claude" \
 && git config --system user.email "noreply@anthropic.com" \
 && git config --system --add safe.directory '*'
USER ${RUNNER_UID}:${RUNNER_UID}
ENV HOME=/home/runner
ENTRYPOINT ["claude"]
