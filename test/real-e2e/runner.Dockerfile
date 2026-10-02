# Real-environment test runner: the example runner image plus jq for the
# Stop hook. Built in CI and never published: it bundles Anthropic's binary.
ARG BASE=example.com/claude-runner:real-e2e
FROM ${BASE}
USER root
RUN apt-get update && apt-get install -y --no-install-recommends jq && rm -rf /var/lib/apt/lists/*
USER 10001:10001
