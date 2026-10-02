# Runner image

Anthropic publishes no runner image; build this one and push it to your registry.

    docker build \
      --build-arg CLAUDE_CODE_VERSION="$(curl -fsSL https://downloads.claude.ai/claude-code-releases/stable)" \
      -t registry.example.com/claude-runner:2.1.280 examples/runner-image

Pin `CLAUDE_CODE_VERSION` to a specific release for reproducible builds. Use
`--build-arg CLAUDE_ARCH=linux-arm64` on ARM nodes. Layer your toolchains on
top; keep the non-root user and the pre-created `/workspace` and `/home/runner`
directories, because the operator runs the container with a read-only root
filesystem and mounts emptyDirs over `/workspace`, `/home/runner` and `/tmp`.
Do not add anything under `/etc/claude`: the operator mounts the environment
Secret there, with hooks, wrapper and host config nested inside it.
