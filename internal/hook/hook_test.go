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
	"bytes"
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

const (
	testNS      = "claude"
	testEnvName = "platform"
	testOrder   = "order-abc"
	testSession = "session_1"
)

func envObj() *selfhostedv1alpha1.ClaudeEnvironment {
	return &selfhostedv1alpha1.ClaudeEnvironment{ObjectMeta: metav1.ObjectMeta{Name: testEnvName, Namespace: testNS, UID: "env-uid"}}
}

func input() Input {
	return Input{WorkOrderFile: "/dev/null", OrderID: testOrder, SessionID: testSession, SessionUUID: "uuid-1",
		Attempt: 1, ClientPlatform: "web_claude_ai", PrimaryRepoURL: "https://github.com/x/y", AccountID: "user_1",
		Environment: testEnvName, Namespace: testNS}
}

func newClient(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(NewScheme()).WithObjects(objs...).Build()
}

func TestParseEnv(t *testing.T) {
	env := map[string]string{
		"CLAUDE_RUNNER_WORK_ORDER_FILE": "/tmp/wo", "CLAUDE_RUNNER_ORDER_ID": "o1", "CLAUDE_RUNNER_SESSION_ID": "s1",
		"CLAUDE_RUNNER_SESSION_UUID": "u1", "CLAUDE_RUNNER_ATTEMPT": "2", "CLAUDE_RUNNER_CLIENT_PLATFORM": "ios",
		"CLAUDE_RUNNER_PRIMARY_REPO_URL": "https://r", "CLAUDE_RUNNER_ACCOUNT_ID": "user_9",
		"CLAUDE_RUNNER_ACCOUNT_EMAIL":         "someone@example.com",
		selfhostedv1alpha1.EnvHookEnvironment: "platform", selfhostedv1alpha1.EnvHookNamespace: "claude",
		selfhostedv1alpha1.EnvHookMaxConcurrentRunners: "3",
	}
	in, err := ParseEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if in.OrderID != "o1" || in.Attempt != 2 || in.MaxConcurrentRunners != 3 || in.Namespace != "claude" || in.AccountID != "user_9" {
		t.Fatalf("parsed wrong: %+v", in)
	}
	for _, missing := range []string{"CLAUDE_RUNNER_WORK_ORDER_FILE", "CLAUDE_RUNNER_ORDER_ID", selfhostedv1alpha1.EnvHookEnvironment, selfhostedv1alpha1.EnvHookNamespace} {
		e2 := map[string]string{}
		maps.Copy(e2, env)
		delete(e2, missing)
		if _, err := ParseEnv(func(k string) string { return e2[k] }); err == nil || !strings.Contains(err.Error(), missing) {
			t.Fatalf("missing %s must error naming it, got %v", missing, err)
		}
	}
	env["CLAUDE_RUNNER_ATTEMPT"] = ""
	if in, err := ParseEnv(func(k string) string { return env[k] }); err != nil || in.Attempt != 0 {
		t.Fatalf("empty attempt must default to 0: %+v %v", in, err)
	}
}

func TestRunSubmitsSecretAndRunner(t *testing.T) {
	ctx := context.Background()
	c := newClient(envObj())
	res := Run(ctx, c, input(), []byte("eyJ.fake.jwt"), "00-abc-def-01")
	if res.ExitCode != ExitSubmitted || res.Err != nil || res.Outcome != "submitted" {
		t.Fatalf("unexpected result %+v", res)
	}
	runner := &selfhostedv1alpha1.ClaudeRunner{}
	if err := c.Get(ctx, types.NamespacedName{Name: testOrder, Namespace: testNS}, runner); err != nil {
		t.Fatal(err)
	}
	if runner.Spec.OrderID != testOrder || runner.Spec.SessionID != testSession || runner.Spec.AccountID != "user_1" ||
		runner.Spec.WorkOrderSecretRef.Name != testOrder+"-work-order" || runner.Spec.EnvironmentRef.Name != testEnvName {
		t.Fatalf("runner spec wrong: %+v", runner.Spec)
	}
	if runner.Labels[selfhostedv1alpha1.LabelEnvironment] != testEnvName || runner.Labels[selfhostedv1alpha1.LabelOrderID] != testOrder ||
		runner.Labels[selfhostedv1alpha1.LabelSessionID] != testSession {
		t.Fatalf("runner labels wrong: %v", runner.Labels)
	}
	if runner.Annotations[selfhostedv1alpha1.AnnotationTraceparent] != "00-abc-def-01" {
		t.Fatal("traceparent annotation missing")
	}
	if len(runner.OwnerReferences) != 1 || runner.OwnerReferences[0].Kind != "ClaudeEnvironment" || runner.OwnerReferences[0].Controller == nil || !*runner.OwnerReferences[0].Controller {
		t.Fatalf("runner must be controller-owned by the environment: %+v", runner.OwnerReferences)
	}
	if runner.OwnerReferences[0].BlockOwnerDeletion != nil {
		t.Fatalf("runner owner reference must not set blockOwnerDeletion: %+v", runner.OwnerReferences)
	}
	secret := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Name: testOrder + "-work-order", Namespace: testNS}, secret); err != nil {
		t.Fatal(err)
	}
	if string(secret.Data[selfhostedv1alpha1.WorkOrderSecretKey]) != "eyJ.fake.jwt" {
		t.Fatal("jwt not stored under the jwt key")
	}
	if len(secret.OwnerReferences) != 1 || secret.OwnerReferences[0].Kind != "ClaudeRunner" || secret.OwnerReferences[0].Name != testOrder {
		t.Fatalf("secret must be handed to the runner: %+v", secret.OwnerReferences)
	}
	if secret.OwnerReferences[0].Controller == nil || !*secret.OwnerReferences[0].Controller || secret.OwnerReferences[0].BlockOwnerDeletion != nil {
		t.Fatalf("secret owner reference must be a controller reference without blockOwnerDeletion: %+v", secret.OwnerReferences)
	}
	if secret.Labels[selfhostedv1alpha1.LabelOrderID] != testOrder {
		t.Fatal("secret labels missing")
	}
}

func TestRunRedeliveryIsIdempotent(t *testing.T) {
	ctx := context.Background()
	c := newClient(envObj())
	first := Run(ctx, c, input(), []byte("jwt-1"), "")
	if first.ExitCode != ExitSubmitted {
		t.Fatalf("first: %+v", first)
	}
	second := Run(ctx, c, input(), []byte("jwt-2"), "")
	if second.ExitCode != ExitSubmitted || second.Outcome != "redelivered" {
		t.Fatalf("redelivery must exit 0: %+v", second)
	}
	runners := &selfhostedv1alpha1.ClaudeRunnerList{}
	if err := c.List(ctx, runners, client.InNamespace(testNS)); err != nil || len(runners.Items) != 1 {
		t.Fatalf("exactly one runner expected, got %d (%v)", len(runners.Items), err)
	}
	secret := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Name: testOrder + "-work-order", Namespace: testNS}, secret); err != nil {
		t.Fatal(err)
	}
	if string(secret.Data[selfhostedv1alpha1.WorkOrderSecretKey]) != "jwt-1" {
		t.Fatal("redelivery must not overwrite the first work order")
	}
}

func TestRunEnvironmentMissingIsNonRetryable(t *testing.T) {
	res := Run(context.Background(), newClient(), input(), []byte("j"), "")
	if res.ExitCode != ExitNonRetryable || res.Err == nil {
		t.Fatalf("got %+v", res)
	}
}

func TestRunConcurrencyCap(t *testing.T) {
	ctx := context.Background()
	active := &selfhostedv1alpha1.ClaudeRunner{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: testNS,
		Labels: map[string]string{selfhostedv1alpha1.LabelEnvironment: testEnvName}},
		Spec:   selfhostedv1alpha1.ClaudeRunnerSpec{EnvironmentRef: selfhostedv1alpha1.LocalObjectRef{Name: testEnvName}, OrderID: "other", WorkOrderSecretRef: selfhostedv1alpha1.LocalObjectRef{Name: "x"}},
		Status: selfhostedv1alpha1.ClaudeRunnerStatus{Phase: selfhostedv1alpha1.RunnerRunning}}
	done := active.DeepCopy()
	done.Name, done.Spec.OrderID, done.Status.Phase = "done", "done", selfhostedv1alpha1.RunnerSucceeded

	in := input()
	in.MaxConcurrentRunners = 1
	res := Run(ctx, newClient(envObj(), active, done), in, []byte("j"), "")
	if res.ExitCode != ExitRetryable || res.Outcome != "at_capacity" {
		t.Fatalf("at cap must be retryable: %+v", res)
	}
	in.MaxConcurrentRunners = 2
	if res := Run(ctx, newClient(envObj(), active, done), in, []byte("j"), ""); res.ExitCode != ExitSubmitted {
		t.Fatalf("terminal runners must not count: %+v", res)
	}
}

func TestRunClassifiesAPIErrors(t *testing.T) {
	ctx := context.Background()
	gr := schema.GroupResource{Group: "", Resource: "secrets"}
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"timeout", apierrors.NewTimeoutError("slow", 1), ExitRetryable},
		{"too many requests", apierrors.NewTooManyRequests("busy", 1), ExitRetryable},
		{"internal", apierrors.NewInternalError(errors.New("boom")), ExitRetryable},
		{"service unavailable", apierrors.NewServiceUnavailable("down"), ExitRetryable},
		{"transport", errors.New("dial tcp: connection refused"), ExitRetryable},
		{"forbidden", apierrors.NewForbidden(gr, "x", errors.New("no")), ExitNonRetryable},
		{"invalid", apierrors.NewBadRequest("bad"), ExitNonRetryable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(NewScheme()).WithObjects(envObj()).
				WithInterceptorFuncs(interceptor.Funcs{Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
					return tc.err
				}}).Build()
			res := Run(ctx, c, input(), []byte("j"), "")
			if res.ExitCode != tc.want {
				t.Fatalf("got %d want %d (%+v)", res.ExitCode, tc.want, res)
			}
			if Classify(tc.err) != tc.want {
				t.Fatalf("Classify mismatch")
			}
		})
	}
}

func TestWriteLogNeverPrintsSecrets(t *testing.T) {
	var buf bytes.Buffer
	in := input()
	WriteLog(&buf, in, Result{ExitCode: 2, Outcome: "error", Err: errors.New("forbidden: token eyJhbGci.x.y for someone@example.com")})
	out := buf.String()
	if !strings.HasPrefix(out, "{") || !strings.HasSuffix(strings.TrimSpace(out), "}") {
		t.Fatalf("expected one JSON line, got %q", out)
	}
	for _, leak := range []string{"eyJhbGci", "someone@example.com"} {
		if strings.Contains(out, leak) {
			t.Fatalf("log leaked %q: %s", leak, out)
		}
	}
	if !strings.Contains(out, `"orderID":"order-abc"`) || !strings.Contains(out, `"exitCode":2`) {
		t.Fatalf("log missing fields: %s", out)
	}
}

func TestRunRedeliveryAtCapacityStillExitsZero(t *testing.T) {
	ctx := context.Background()
	own := &selfhostedv1alpha1.ClaudeRunner{ObjectMeta: metav1.ObjectMeta{Name: testOrder, Namespace: testNS,
		Labels: map[string]string{selfhostedv1alpha1.LabelEnvironment: testEnvName}},
		Spec:   selfhostedv1alpha1.ClaudeRunnerSpec{EnvironmentRef: selfhostedv1alpha1.LocalObjectRef{Name: testEnvName}, OrderID: testOrder, WorkOrderSecretRef: selfhostedv1alpha1.LocalObjectRef{Name: testOrder + "-work-order"}},
		Status: selfhostedv1alpha1.ClaudeRunnerStatus{Phase: selfhostedv1alpha1.RunnerRunning}}
	c := newClient(envObj(), own)
	in := input()
	in.MaxConcurrentRunners = 1
	res := Run(ctx, c, in, []byte("j"), "")
	if res.ExitCode != ExitSubmitted || res.Outcome != "redelivered" {
		t.Fatalf("redelivery at cap must exit 0: %+v", res)
	}
	secrets := &corev1.SecretList{}
	if err := c.List(ctx, secrets, client.InNamespace(testNS)); err != nil || len(secrets.Items) != 0 {
		t.Fatalf("redelivery must create nothing, got %d secrets (%v)", len(secrets.Items), err)
	}
}

func TestRunNonRetryableRunnerCreateDeletesSecret(t *testing.T) {
	ctx := context.Background()
	gr := schema.GroupResource{Group: "x", Resource: "clauderunners"}
	c := fake.NewClientBuilder().WithScheme(NewScheme()).WithObjects(envObj()).
		WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*selfhostedv1alpha1.ClaudeRunner); ok {
				return apierrors.NewForbidden(gr, testOrder, errors.New("no"))
			}
			return cl.Create(ctx, obj, opts...)
		}}).Build()
	res := Run(ctx, c, input(), []byte("j"), "")
	if res.ExitCode != ExitNonRetryable {
		t.Fatalf("got %+v", res)
	}
	err := c.Get(ctx, types.NamespacedName{Name: testOrder + "-work-order", Namespace: testNS}, &corev1.Secret{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("work-order Secret must be deleted, got %v", err)
	}
}

func TestRunPatchFailureWarnsAndRedactsInLog(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(NewScheme()).WithObjects(envObj()).
		WithInterceptorFuncs(interceptor.Funcs{Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
			return errors.New("denied for someone@example.com")
		}}).Build()
	res := Run(ctx, c, input(), []byte("j"), "")
	if res.ExitCode != ExitSubmitted || res.Warning == "" {
		t.Fatalf("patch failure must warn but exit 0: %+v", res)
	}
	var buf bytes.Buffer
	WriteLog(&buf, input(), res)
	out := buf.String()
	if !strings.Contains(out, `"warning"`) || !strings.Contains(out, "[redacted]") || strings.Contains(out, "someone@example.com") {
		t.Fatalf("warning must be logged redacted: %s", out)
	}
}
