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

// Command stubrunner stands in for the real runner in e2e tests. It accepts the
// real runner's args, serves /healthz and /metrics on --health-port, and exits
// 0 on SIGTERM. It never contacts Anthropic.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func healthPort(args []string) string {
	for i, a := range args {
		if a == "--health-port" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return "8080"
}

func main() {
	port := healthPort(os.Args[1:])
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"ok","runner_id":"ccrunner_stub","active_sessions":0,`+
			`"last_poll_at":null,"last_poll_age_ms":null}`)
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintln(w, "claude_code_self_hosted_runner_capacity 1")
		_, _ = fmt.Fprintln(w, "claude_code_self_hosted_runner_active_sessions 0")
	})
	srv := &http.Server{Addr: ":" + port, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	}()
	fmt.Println("[self-hosted-runner] stub listening on", port, "args:", os.Args[1:])
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
