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

package builders

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

const (
	testNamespace       = "claude"
	testEnvName         = "platform"
	testWorkOrderSecret = "order-1-work-order"
	capacityFlag        = "--capacity"
	testOrderID         = "order-1"
)

func onDemandEnv() *selfhostedv1alpha1.ClaudeEnvironment {
	env := testEnv()
	env.Spec.Fixed = nil
	env.Spec.OnDemand = &selfhostedv1alpha1.OnDemandSpec{Orchestrator: selfhostedv1alpha1.OrchestratorSpec{
		Replicas: ptr.To[int32](1), ExpectedSpawnSeconds: 120, HookTimeoutSeconds: 60, HookConcurrency: 4,
		HealthPort: ptr.To[int32](8080)}}
	return env
}

func testRunner() *selfhostedv1alpha1.ClaudeRunner {
	return &selfhostedv1alpha1.ClaudeRunner{
		ObjectMeta: metav1.ObjectMeta{Name: testOrderID, Namespace: testNamespace, UID: "uid-1"},
		Spec: selfhostedv1alpha1.ClaudeRunnerSpec{
			EnvironmentRef: selfhostedv1alpha1.LocalObjectRef{Name: testEnvName}, OrderID: testOrderID,
			SessionID: "session_abc", WorkOrderSecretRef: selfhostedv1alpha1.LocalObjectRef{Name: testWorkOrderSecret},
		},
	}
}

func TestWorkOrderSecretName(t *testing.T) {
	if got := WorkOrderSecretName(testOrderID); got != testWorkOrderSecret {
		t.Fatalf("got %q", got)
	}
}

func TestOnDemandRunnerPod(t *testing.T) {
	pod := OnDemandRunnerPod(onDemandEnv(), testRunner(), "h1")
	if pod.Name != testOrderID || pod.Namespace != testNamespace {
		t.Fatalf("name/namespace %s/%s", pod.Namespace, pod.Name)
	}
	if pod.APIVersion != "v1" || pod.Kind != "Pod" {
		t.Fatal("TypeMeta must be set for server-side apply")
	}
	if pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Fatal("on-demand runners must never restart")
	}
	if pod.Labels[selfhostedv1alpha1.LabelOrderID] != testOrderID || pod.Labels[selfhostedv1alpha1.LabelSessionID] != "session_abc" ||
		pod.Labels[selfhostedv1alpha1.LabelEnvironment] != testEnvName || pod.Labels[selfhostedv1alpha1.LabelRole] != selfhostedv1alpha1.RoleRunner {
		t.Fatalf("labels wrong: %v", pod.Labels)
	}
	if pod.Annotations[selfhostedv1alpha1.AnnotationConfigHash] != "h1" {
		t.Fatal("config hash annotation missing")
	}
	var secret *corev1.Volume
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Name == "environment-secret" {
			secret = &pod.Spec.Volumes[i]
		}
	}
	if secret == nil || secret.Secret == nil || secret.Secret.SecretName != testWorkOrderSecret ||
		secret.Secret.Items[0].Key != selfhostedv1alpha1.WorkOrderSecretKey || secret.Secret.Items[0].Path != SecretFileName {
		t.Fatalf("work-order volume wrong: %+v", secret)
	}
	args := pod.Spec.Containers[0].Args
	if args[2] != SecretMountPath+"/"+SecretFileName {
		t.Fatalf("secret file arg wrong: %v", args[:3])
	}
	if !containsSeq(args, []string{capacityFlag, "1"}) {
		t.Fatalf("capacity must be 1: %v", args)
	}
	if pod.Spec.SecurityContext == nil || !*pod.Spec.SecurityContext.RunAsNonRoot {
		t.Fatal("restricted pod security context missing")
	}
}

func TestOnDemandRunnerPodForcesCapacityOne(t *testing.T) {
	env := onDemandEnv()
	env.Spec.Runner.Capacity = 4 // CEL forbids this, but the builder must still be safe
	pod := OnDemandRunnerPod(env, testRunner(), "h")
	if !containsSeq(pod.Spec.Containers[0].Args, []string{capacityFlag, "1"}) {
		t.Fatalf("capacity not forced to 1: %v", pod.Spec.Containers[0].Args)
	}
}

func TestOnDemandRunnerPodPreWarmHasNoSessionLabel(t *testing.T) {
	r := testRunner()
	r.Spec.SessionID = ""
	pod := OnDemandRunnerPod(onDemandEnv(), r, "h")
	if _, ok := pod.Labels[selfhostedv1alpha1.LabelSessionID]; ok {
		t.Fatal("pre-warming runners must not carry a session label")
	}
}
