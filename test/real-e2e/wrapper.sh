#!/usr/bin/env bash
# Wrapper for the real-environment test: marks the session so the Stop hook
# can report that the wrapper ran, then execs the runner's own binary with
# the arguments as given, which keeps stdin and file descriptor 3 attached as
# the product requires.
export E2E_WRAPPER=ran
exec "$CLAUDE_RUNNER_CLAUDE_BIN" "$@"
