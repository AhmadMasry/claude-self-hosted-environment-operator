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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
	"github.com/AhmadMasry/claude-self-hosted-environment-operator/internal/builders"
)

// Exit codes from the product's spawn-runner hook contract.
const (
	ExitSubmitted    = 0
	ExitRetryable    = 1
	ExitNonRetryable = 2

	// Timeout bounds one hook run well inside the orchestrator's default
	// --hook-timeout of 60 seconds so the hook is never killed mid-create.
	Timeout = 45 * time.Second
)

// Result is the hook's outcome.
type Result struct {
	ExitCode   int
	Outcome    string // submitted | redelivered | at_capacity | error
	RunnerName string
	Warning    string // non-fatal follow-up problems, e.g. the owner hand-off patch failed
	Err        error
}

// NewScheme has the core and operator types the hook touches.
func NewScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = selfhostedv1alpha1.AddToScheme(s)
	return s
}

// Run performs the single idempotent spawn: create the work-order Secret, then
// the ClaudeRunner named after the order ID, then hand the Secret to the
// ClaudeRunner for garbage collection.
func Run(ctx context.Context, c client.Client, in Input, jwt []byte, traceparent string) Result {
	env := &selfhostedv1alpha1.ClaudeEnvironment{}
	if err := c.Get(ctx, types.NamespacedName{Name: in.Environment, Namespace: in.Namespace}, env); err != nil {
		return failure(fmt.Errorf("get ClaudeEnvironment %s/%s: %w", in.Namespace, in.Environment, err))
	}

	existing := &selfhostedv1alpha1.ClaudeRunner{}
	switch err := c.Get(ctx, types.NamespacedName{Name: in.OrderID, Namespace: in.Namespace}, existing); {
	case err == nil:
		res := Result{ExitCode: ExitSubmitted, Outcome: "redelivered", RunnerName: in.OrderID}
		res.Warning = handOffSecret(ctx, c, existing, builders.WorkOrderSecretName(in.OrderID))
		return res
	case !apierrors.IsNotFound(err):
		return failure(fmt.Errorf("get ClaudeRunner %s/%s: %w", in.Namespace, in.OrderID, err))
	}

	if in.MaxConcurrentRunners > 0 {
		active, err := countActiveRunners(ctx, c, in.Namespace, in.Environment)
		if err != nil {
			return failure(fmt.Errorf("list ClaudeRunners: %w", err))
		}
		if active >= in.MaxConcurrentRunners {
			return Result{ExitCode: ExitRetryable, Outcome: "at_capacity",
				Err: fmt.Errorf("%d runners active, cap is %d", active, in.MaxConcurrentRunners)}
		}
	}

	envRef := controllerRef(env, "ClaudeEnvironment")
	labels := map[string]string{
		selfhostedv1alpha1.LabelEnvironment: in.Environment,
		selfhostedv1alpha1.LabelOrderID:     selfhostedv1alpha1.LabelValue(in.OrderID),
	}
	if in.SessionID != "" {
		labels[selfhostedv1alpha1.LabelSessionID] = selfhostedv1alpha1.LabelValue(in.SessionID)
	}

	secretName := builders.WorkOrderSecretName(in.OrderID)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: in.Namespace, Labels: maps.Clone(labels), OwnerReferences: []metav1.OwnerReference{*envRef}},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{selfhostedv1alpha1.WorkOrderSecretKey: jwt},
	}
	if err := c.Create(ctx, secret); err != nil && !apierrors.IsAlreadyExists(err) {
		return failure(fmt.Errorf("create work-order Secret: %w", err))
	}

	runner := &selfhostedv1alpha1.ClaudeRunner{
		ObjectMeta: metav1.ObjectMeta{Name: in.OrderID, Namespace: in.Namespace, Labels: maps.Clone(labels), OwnerReferences: []metav1.OwnerReference{*envRef}},
		Spec: selfhostedv1alpha1.ClaudeRunnerSpec{
			EnvironmentRef:     selfhostedv1alpha1.LocalObjectRef{Name: in.Environment},
			OrderID:            in.OrderID,
			SessionID:          in.SessionID,
			SessionUUID:        in.SessionUUID,
			Attempt:            in.Attempt,
			ClientPlatform:     in.ClientPlatform,
			PrimaryRepoURL:     in.PrimaryRepoURL,
			AccountID:          in.AccountID,
			WorkOrderSecretRef: selfhostedv1alpha1.LocalObjectRef{Name: secretName},
		},
	}
	if traceparent != "" {
		runner.Annotations = map[string]string{selfhostedv1alpha1.AnnotationTraceparent: traceparent}
	}
	if err := c.Create(ctx, runner); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return Result{ExitCode: ExitSubmitted, Outcome: "redelivered", RunnerName: in.OrderID}
		}
		res := failure(fmt.Errorf("create ClaudeRunner: %w", err))
		if res.ExitCode == ExitNonRetryable {
			// Do not leave a live work order behind; a retryable failure keeps it for the retry.
			if delErr := c.Delete(ctx, secret); delErr != nil && !apierrors.IsNotFound(delErr) {
				res.Err = fmt.Errorf("%w; delete work-order Secret: %w", res.Err, delErr)
			}
		}
		return res
	}

	// Hand the Secret to the ClaudeRunner so it is collected with it. Best
	// effort: the environment owner reference already guarantees collection.
	return Result{ExitCode: ExitSubmitted, Outcome: "submitted", RunnerName: in.OrderID,
		Warning: handOffSecret(ctx, c, runner, secretName)}
}

// handOffSecret makes the runner the Secret's controller owner with a raw
// merge patch, so the hook never needs read access to Secrets. It is
// idempotent. It returns a warning, never an error: a Secret that is already
// gone or a failed patch is not fatal.
func handOffSecret(ctx context.Context, c client.Client, runner *selfhostedv1alpha1.ClaudeRunner, secretName string) string {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: runner.Namespace}}
	ref := controllerRef(runner, "ClaudeRunner")
	body, err := json.Marshal(map[string]any{"metadata": map[string]any{"ownerReferences": []metav1.OwnerReference{*ref}}})
	if err != nil {
		return "could not build the owner hand-off patch: " + err.Error()
	}
	if err := c.Patch(ctx, secret, client.RawPatch(types.MergePatchType, body)); err != nil && !apierrors.IsNotFound(err) {
		return "could not hand the work-order Secret to the ClaudeRunner: " + err.Error()
	}
	return ""
}

// controllerRef is a controller owner reference without blockOwnerDeletion.
// Setting blockOwnerDeletion requires update on the owner's finalizers
// subresource under OwnerReferencesPermissionEnforcement (on by default on
// OpenShift), which the orchestrator Role does not grant; background garbage
// collection does not need it.
func controllerRef(owner metav1.Object, kind string) *metav1.OwnerReference {
	ref := metav1.NewControllerRef(owner, selfhostedv1alpha1.GroupVersion.WithKind(kind))
	ref.BlockOwnerDeletion = nil
	return ref
}

func countActiveRunners(ctx context.Context, c client.Client, namespace, environment string) (int, error) {
	list := &selfhostedv1alpha1.ClaudeRunnerList{}
	if err := c.List(ctx, list, client.InNamespace(namespace), client.MatchingLabels{selfhostedv1alpha1.LabelEnvironment: environment}); err != nil {
		return 0, err
	}
	n := 0
	for i := range list.Items {
		if !list.Items[i].Status.Phase.IsTerminal() {
			n++
		}
	}
	return n, nil
}

func failure(err error) Result {
	return Result{ExitCode: Classify(err), Outcome: "error", Err: err}
}

// Classify maps an error onto the hook exit-code contract. Transport and
// server-side failures are retryable; everything the API rejected outright
// is not.
func Classify(err error) int {
	if err == nil {
		return ExitSubmitted
	}
	switch {
	case apierrors.IsTimeout(err), apierrors.IsServerTimeout(err), apierrors.IsTooManyRequests(err),
		apierrors.IsInternalError(err), apierrors.IsServiceUnavailable(err), apierrors.IsUnexpectedServerError(err):
		return ExitRetryable
	}
	if _, ok := errors.AsType[*apierrors.StatusError](err); !ok {
		return ExitRetryable // transport, DNS, context deadline: not an API verdict
	}
	return ExitNonRetryable
}
