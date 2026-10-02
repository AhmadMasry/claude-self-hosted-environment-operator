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

// Command spawn-runner is the orchestrator's spawn-runner hook. It also serves
// as its own installer (--install) and as the orchestrator readiness probe
// (--probe-connected) so the user's image needs no extra tooling.
package main

import (
	"context"
	"fmt"
	"os"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/hook"
)

var version = "dev"

func main() {
	args := os.Args[1:]
	switch {
	case len(args) == 2 && args[0] == "--install":
		if err := hook.Install(args[1]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	case len(args) == 2 && args[0] == "--probe-connected":
		ok, err := hook.ProbeConnected(context.Background(), args[1])
		if err != nil || !ok {
			os.Exit(1)
		}
		return
	case len(args) == 1 && args[0] == "--version":
		fmt.Println(version)
		return
	}
	os.Exit(runHook())
}

func runHook() int {
	in, err := hook.ParseEnv(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return hook.ExitNonRetryable
	}
	jwt, err := os.ReadFile(in.WorkOrderFile)
	if err != nil || len(jwt) == 0 {
		fmt.Fprintln(os.Stderr, "error: work-order file is missing or empty")
		return hook.ExitNonRetryable
	}
	ctx, cancel := context.WithTimeout(context.Background(), hook.Timeout)
	defer cancel()

	cfg, err := ctrl.GetConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: no in-cluster configuration:", err)
		return hook.ExitNonRetryable
	}
	c, err := client.New(cfg, client.Options{Scheme: hook.NewScheme()})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return hook.ExitRetryable
	}
	res := hook.Run(ctx, c, in, jwt, "")
	hook.WriteLog(os.Stdout, in, res)
	if res.Err != nil {
		fmt.Fprintln(os.Stderr, "error:", hook.Redact(res.Err.Error()))
	}
	return res.ExitCode
}
