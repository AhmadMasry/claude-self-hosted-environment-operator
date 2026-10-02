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

package hook

import (
	"fmt"
	"strconv"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

// Input is everything the hook reads from its environment. The account email
// is deliberately not read: it is personally identifiable and never stored.
type Input struct {
	WorkOrderFile        string
	OrderID              string
	SessionID            string
	SessionUUID          string
	Attempt              int32
	ClientPlatform       string
	PrimaryRepoURL       string
	AccountID            string
	Environment          string
	Namespace            string
	MaxConcurrentRunners int
}

// ParseEnv reads the product's hook variables plus the operator's own.
func ParseEnv(getenv func(string) string) (Input, error) {
	in := Input{
		WorkOrderFile:  getenv("CLAUDE_RUNNER_WORK_ORDER_FILE"),
		OrderID:        getenv("CLAUDE_RUNNER_ORDER_ID"),
		SessionID:      getenv("CLAUDE_RUNNER_SESSION_ID"),
		SessionUUID:    getenv("CLAUDE_RUNNER_SESSION_UUID"),
		ClientPlatform: getenv("CLAUDE_RUNNER_CLIENT_PLATFORM"),
		PrimaryRepoURL: getenv("CLAUDE_RUNNER_PRIMARY_REPO_URL"),
		AccountID:      getenv("CLAUDE_RUNNER_ACCOUNT_ID"),
		Environment:    getenv(selfhostedv1alpha1.EnvHookEnvironment),
		Namespace:      getenv(selfhostedv1alpha1.EnvHookNamespace),
	}
	for name, v := range map[string]string{
		"CLAUDE_RUNNER_WORK_ORDER_FILE":       in.WorkOrderFile,
		"CLAUDE_RUNNER_ORDER_ID":              in.OrderID,
		selfhostedv1alpha1.EnvHookEnvironment: in.Environment,
		selfhostedv1alpha1.EnvHookNamespace:   in.Namespace,
	} {
		if v == "" {
			return Input{}, fmt.Errorf("required environment variable %s is not set", name)
		}
	}
	if s := getenv("CLAUDE_RUNNER_ATTEMPT"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return Input{}, fmt.Errorf("CLAUDE_RUNNER_ATTEMPT is not an integer: %w", err)
		}
		in.Attempt = int32(n)
	}
	if s := getenv(selfhostedv1alpha1.EnvHookMaxConcurrentRunners); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return Input{}, fmt.Errorf("%s is not an integer: %w", selfhostedv1alpha1.EnvHookMaxConcurrentRunners, err)
		}
		in.MaxConcurrentRunners = n
	}
	return in, nil
}
