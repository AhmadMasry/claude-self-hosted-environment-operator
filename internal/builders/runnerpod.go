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
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

// WorkOrderSecretName is the Secret holding one order's single-use JWT. It is
// keyed by the order, not the runner name, so a prefix change never breaks
// the ClaudeRunner's reference or the admission policy's check.
func WorkOrderSecretName(orderID string) string {
	return orderID + selfhostedv1alpha1.WorkOrderSecretSuffix
}

// MaxRunnerNameLength bounds ClaudeRunner and pod names: the pod name is the
// hostname, which kubelet truncates past 63 characters.
const MaxRunnerNameLength = 63

// RunnerName names the ClaudeRunner and its pod for one order:
// <prefix>-<orderID>, where the prefix is onDemand.runnerNamePrefix or the
// ClaudeEnvironment name. The prefix is cut, and a trailing dash dropped, so
// the whole name fits MaxRunnerNameLength; an order ID that leaves no room
// stands alone. The name must be a pure function of the environment and the
// order, so a redelivered order finds its ClaudeRunner.
func RunnerName(env *selfhostedv1alpha1.ClaudeEnvironment, orderID string) string {
	prefix := env.Name
	if env.Spec.OnDemand != nil && env.Spec.OnDemand.RunnerNamePrefix != "" {
		prefix = env.Spec.OnDemand.RunnerNamePrefix
	}
	if room := MaxRunnerNameLength - len(orderID) - 1; len(prefix) > room {
		if room < 1 {
			return orderID
		}
		prefix = strings.TrimRight(prefix[:room], "-")
	}
	return prefix + "-" + orderID
}

// WorkOrderSecretVolume mounts the work-order JWT where the runner expects the
// environment secret file, so RunnerArgs and the nested hook mounts are the
// same as in fixed mode.
func WorkOrderSecretVolume(secretName string) corev1.VolumeSource {
	return corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
		SecretName: secretName,
		Items:      []corev1.KeyToPath{{Key: selfhostedv1alpha1.WorkOrderSecretKey, Path: SecretFileName}},
	}}
}

// OnDemandRunnerPod builds the single-session pod for a ClaudeRunner. The
// caller sets the owner reference. hostConfigKeys come from HostConfigKeys.
func OnDemandRunnerPod(env *selfhostedv1alpha1.ClaudeEnvironment, runner *selfhostedv1alpha1.ClaudeRunner, hash string, hostConfigKeys []string) *corev1.Pod {
	// A session-bound work order registers exactly one runner; extra slots never receive work.
	single := env.DeepCopy()
	single.Spec.Runner.Capacity = 1

	tmpl := RunnerPodTemplate(RunnerPodInput{
		Env:            single,
		ConfigHash:     hash,
		SecretVolume:   WorkOrderSecretVolume(runner.Spec.WorkOrderSecretRef.Name),
		SecretFile:     SecretMountPath + "/" + SecretFileName,
		RestartPolicy:  corev1.RestartPolicyNever,
		HostConfigKeys: hostConfigKeys,
	})
	tmpl.Labels[selfhostedv1alpha1.LabelOrderID] = selfhostedv1alpha1.LabelValue(runner.Spec.OrderID)
	if runner.Spec.SessionID != "" {
		tmpl.Labels[selfhostedv1alpha1.LabelSessionID] = selfhostedv1alpha1.LabelValue(runner.Spec.SessionID)
	}
	return &corev1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{
			Name: runner.Name, Namespace: runner.Namespace,
			Labels: tmpl.Labels, Annotations: tmpl.Annotations,
		},
		Spec: tmpl.Spec,
	}
}
