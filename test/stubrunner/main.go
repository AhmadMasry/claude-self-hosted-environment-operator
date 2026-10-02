/*
Copyright 2026 Ahmad Masry.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Command stubrunner stands in for the real runner and orchestrator in e2e
// tests. It accepts the real runner's args, checks that the files the operator
// mounts are readable, serves /healthz and /metrics on --health-port, and exits
// 0 on SIGTERM. In orchestrator mode it also runs the injected spawn-runner hook
// twice with the same order, as a redelivery would. It never contacts Anthropic.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// argValue returns the value following flag name, or def when it is absent.
func argValue(args []string, name, def string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return def
}

// checkMounts fails when the environment secret is unreadable or empty, or
// when a hooks directory or wrapper script passed in args cannot be found.
// It never prints file contents.
func checkMounts(args []string) error {
	secretFile := argValue(args, "--environment-secret-file", "")
	if secretFile == "" {
		return errors.New("--environment-secret-file is required")
	}
	b, err := os.ReadFile(secretFile)
	if err != nil {
		return fmt.Errorf("cannot read environment secret file: %w", err)
	}
	if len(b) == 0 {
		return fmt.Errorf("environment secret file %s is empty", secretFile)
	}
	for _, flag := range []string{"--hooks-dir", "--exec-path"} {
		if p := argValue(args, flag, ""); p != "" {
			if _, err := os.Stat(p); err != nil {
				return fmt.Errorf("%s: %w", flag, err)
			}
		}
	}
	return nil
}

const (
	workOrderFile    = "/tmp/work-order"
	hookRunDelay     = 5 * time.Second
	orchestratorBody = `{"status":"ok","connected":true,` +
		`"queue_counts":{"pending":0,"backing_off":0,"circuit_broken":0}}`
)

// hookEnv is the work-order environment the real orchestrator passes to the hook.
var hookEnv = []string{
	"CLAUDE_RUNNER_WORK_ORDER_FILE=" + workOrderFile,
	"CLAUDE_RUNNER_ORDER_ID=e2e-order-1",
	"CLAUDE_RUNNER_SESSION_ID=session_e2e1",
	"CLAUDE_RUNNER_SESSION_UUID=11111111-1111-4111-8111-111111111111",
	"CLAUDE_RUNNER_ATTEMPT=1",
	"CLAUDE_RUNNER_POOL_ID=ccpool_e2e",
	"CLAUDE_RUNNER_CLIENT_PLATFORM=web_claude_ai",
	"CLAUDE_RUNNER_ACCOUNT_ID=user_e2e",
}

// checkOrchestratorMounts checks the environment secret and that
// <--hooks-dir>/spawn-runner exists and is executable, and returns the hook path.
func checkOrchestratorMounts(args []string) (string, error) {
	const secretFlag = "--environment-secret-file"
	if err := checkMounts([]string{secretFlag, argValue(args, secretFlag, "")}); err != nil {
		return "", err
	}
	dir := argValue(args, "--hooks-dir", "")
	if dir == "" {
		return "", errors.New("--hooks-dir is required")
	}
	hook := filepath.Join(dir, "spawn-runner")
	fi, err := os.Stat(hook)
	if err != nil {
		return "", fmt.Errorf("hook: %w", err)
	}
	if fi.IsDir() || fi.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("hook %s is not executable", hook)
	}
	return hook, nil
}

// runHooks writes the work order and runs the hook twice in sequence, piping
// its output to stdout. The work order itself is never printed.
func runHooks(ctx context.Context, hook string) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(hookRunDelay):
	}
	if err := os.WriteFile(workOrderFile, []byte("stub-work-order-jwt"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "error: write work order:", err)
		return
	}
	for n := 1; n <= 2; n++ {
		//nolint:gosec // hook path comes from the orchestrator's own --hooks-dir argument
		cmd := exec.CommandContext(ctx, hook)
		cmd.Env = append(os.Environ(), hookEnv...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stdout
		code := 0
		if err := cmd.Run(); err != nil {
			if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
				code = exitErr.ExitCode()
			} else {
				fmt.Println("stub-orchestrator: hook run", n, "error:", err)
				code = -1
			}
		}
		fmt.Printf("stub-orchestrator: hook run %d exit %d\n", n, code)
	}
}

// runnerHealthz serves the runner's /healthz body.
func runnerHealthz() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"ok","runner_id":"ccrunner_stub","active_sessions":0,`+
			`"last_poll_at":null,"last_poll_age_ms":null}`)
	}
}

// orchestratorHealthz serves the orchestrator's /healthz body.
func orchestratorHealthz() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, orchestratorBody)
	}
}

// startServer serves healthz and metrics on port in the background and
// returns the server so the caller can shut it down.
func startServer(port string, healthz, metrics http.HandlerFunc) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthz)
	mux.HandleFunc("/metrics", metrics)
	srv := &http.Server{Addr: ":" + port, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	}()
	return srv
}

func stopServer(srv *http.Server) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

// runRunner stands in for the runner and returns the process exit code.
func runRunner(args []string) int {
	if err := checkMounts(args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	port := argValue(args, "--health-port", "8080")
	srv := startServer(port, runnerHealthz(), func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintln(w, "claude_code_self_hosted_runner_capacity 1")
		_, _ = fmt.Fprintln(w, "claude_code_self_hosted_runner_active_sessions 0")
	})
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	fmt.Println("[self-hosted-runner] stub listening on", port, "args:", args)
	if n, err := strconv.Atoi(os.Getenv("STUB_EXIT_AFTER_SECONDS")); err == nil && n > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(time.Duration(n) * time.Second):
			fmt.Println("stub-runner: session finished, exiting 0")
		}
		stop()
	}
	<-ctx.Done()
	stopServer(srv)
	return 0
}

// runOrchestrator stands in for the orchestrator and returns the process exit code.
func runOrchestrator(args []string) int {
	hook, err := checkOrchestratorMounts(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	port := argValue(args, "--health-port", "8080")
	srv := startServer(port, orchestratorHealthz(), func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintln(w, "claude_code_self_hosted_orchestrator_connected 1")
	})
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	fmt.Println("stub-orchestrator: listening on", port, "args:", args)
	go runHooks(ctx, hook)
	<-ctx.Done()
	stopServer(srv)
	return 0
}

func main() {
	args := os.Args[1:]
	if len(os.Args) > 2 && os.Args[1] == "self-hosted-runner" && os.Args[2] == "orchestrator" {
		os.Exit(runOrchestrator(args))
	}
	os.Exit(runRunner(args))
}
