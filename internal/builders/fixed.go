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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

// FixedWorkloadName is the name of the Deployment or StatefulSet for a fixed fleet.
func FixedWorkloadName(env *selfhostedv1alpha1.ClaudeEnvironment) string {
	return env.Name + "-runner"
}

// EnvironmentSecretVolume mounts the environment key at /etc/claude/environment-secret.
func EnvironmentSecretVolume(env *selfhostedv1alpha1.ClaudeEnvironment) corev1.VolumeSource {
	return corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
		SecretName: env.Spec.EnvironmentSecretRef.Name,
		Items:      []corev1.KeyToPath{{Key: env.Spec.EnvironmentSecretRef.Key, Path: SecretFileName}},
	}}
}

func fixedInput(env *selfhostedv1alpha1.ClaudeEnvironment, hash string, pvc bool, hostConfigKeys []string) RunnerPodInput {
	return RunnerPodInput{
		Env: env, ConfigHash: hash,
		SecretVolume:     EnvironmentSecretVolume(env),
		SecretFile:       SecretMountPath + "/" + SecretFileName,
		RestartPolicy:    corev1.RestartPolicyAlways,
		WorkspaceFromPVC: pvc,
		HostConfigKeys:   hostConfigKeys,
	}
}

func workloadMeta(env *selfhostedv1alpha1.ClaudeEnvironment) metav1.ObjectMeta {
	labels := RunnerSelectorLabels(env)
	labels[selfhostedv1alpha1.LabelPartOf] = selfhostedv1alpha1.PartOfValue
	return metav1.ObjectMeta{Name: FixedWorkloadName(env), Namespace: env.Namespace, Labels: labels}
}

// FixedDeployment builds the Deployment for a fixed fleet without persistent
// workspace. hostConfigKeys come from HostConfigKeys.
func FixedDeployment(env *selfhostedv1alpha1.ClaudeEnvironment, hash string, hostConfigKeys []string) *appsv1.Deployment {
	return &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: workloadMeta(env),
		Spec: appsv1.DeploymentSpec{
			Replicas: env.Spec.Fixed.Replicas,
			Selector: &metav1.LabelSelector{MatchLabels: RunnerSelectorLabels(env)},
			Template: RunnerPodTemplate(fixedInput(env, hash, false, hostConfigKeys)),
		},
	}
}

// FixedStatefulSet builds the StatefulSet for a fixed fleet with a persistent
// workspace. hostConfigKeys come from HostConfigKeys.
func FixedStatefulSet(env *selfhostedv1alpha1.ClaudeEnvironment, hash string, hostConfigKeys []string) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "StatefulSet"},
		ObjectMeta: workloadMeta(env),
		Spec: appsv1.StatefulSetSpec{
			Replicas:            env.Spec.Fixed.Replicas,
			ServiceName:         FixedWorkloadName(env),
			PodManagementPolicy: appsv1.ParallelPodManagement,
			Selector:            &metav1.LabelSelector{MatchLabels: RunnerSelectorLabels(env)},
			Template:            RunnerPodTemplate(fixedInput(env, hash, true, hostConfigKeys)),
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{Name: WorkspaceVolume},
				Spec:       env.Spec.Fixed.PersistentWorkspace.VolumeClaimTemplate,
			}},
		},
	}
}
