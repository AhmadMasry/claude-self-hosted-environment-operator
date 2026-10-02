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
	"maps"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

const (
	RunnerContainerName = "runner"
	HomeMountPath       = "/home/runner"
	TmpMountPath        = "/tmp"
	WorkspaceVolume     = "workspace"
)

// RunnerPodInput is everything RunnerPodTemplate needs beyond the environment.
type RunnerPodInput struct {
	Env           *selfhostedv1alpha1.ClaudeEnvironment
	ConfigHash    string
	SecretVolume  corev1.VolumeSource // environment secret or per-order work-order secret
	SecretFile    string              // path passed to --environment-secret-file
	RestartPolicy corev1.RestartPolicy
	// WorkspaceFromPVC, when true, omits the workspace emptyDir because a
	// StatefulSet volumeClaimTemplate named WorkspaceVolume provides it.
	WorkspaceFromPVC bool
}

// RunnerSelectorLabels are the immutable labels used as the workload selector.
func RunnerSelectorLabels(env *selfhostedv1alpha1.ClaudeEnvironment) map[string]string {
	return map[string]string{
		selfhostedv1alpha1.LabelEnvironment: env.Name,
		selfhostedv1alpha1.LabelRole:        selfhostedv1alpha1.RoleRunner,
	}
}

// EffectiveGracePeriod returns the grace period to put on the pod and whether
// a user-supplied value is below the computed drain budget.
func EffectiveGracePeriod(env *selfhostedv1alpha1.ClaudeEnvironment) (int64, bool) {
	budget := DrainBudgetSeconds(env.Spec.Runner.Settings)
	if u := env.Spec.Runner.TerminationGracePeriodSeconds; u != nil {
		return *u, *u < budget
	}
	return budget, false
}

// RunnerPodTemplate builds the hardened pod template for a runner.
func RunnerPodTemplate(in RunnerPodInput) corev1.PodTemplateSpec {
	env := in.Env
	r := env.Spec.Runner
	pt := r.PodTemplate
	healthPort := valueOr(r.Settings.HealthPort, DefaultHealthPort)
	grace, _ := EffectiveGracePeriod(env)

	labels := map[string]string{}
	maps.Copy(labels, pt.Labels)
	maps.Copy(labels, RunnerSelectorLabels(env))
	labels[selfhostedv1alpha1.LabelPartOf] = selfhostedv1alpha1.PartOfValue

	annotations := map[string]string{}
	maps.Copy(annotations, pt.Annotations)
	annotations[selfhostedv1alpha1.AnnotationConfigHash] = in.ConfigHash

	volumes := []corev1.Volume{
		{Name: "environment-secret", VolumeSource: in.SecretVolume},
		{Name: "home", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
	if !in.WorkspaceFromPVC {
		volumes = append(volumes, corev1.Volume{Name: WorkspaceVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}})
	}
	mounts := []corev1.VolumeMount{
		{Name: "environment-secret", MountPath: SecretMountPath, ReadOnly: true},
		{Name: WorkspaceVolume, MountPath: r.BaseDir},
		{Name: "home", MountPath: HomeMountPath},
		{Name: "tmp", MountPath: TmpMountPath},
	}
	envVars := append([]corev1.EnvVar{}, r.Env...)
	if r.Settings.ClientLabel == "" {
		envVars = append(envVars, corev1.EnvVar{Name: "SELF_HOSTED_RUNNER_CLIENT_LABEL",
			ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}})
	}
	if r.LifecycleHooks != nil {
		volumes = append(volumes, configMapVolume("hooks", r.LifecycleHooks.Name, 0o555))
		mounts = append(mounts, corev1.VolumeMount{Name: "hooks", MountPath: HooksMountPath, ReadOnly: true})
	}
	if r.WrapperScript != nil {
		volumes = append(volumes, configMapVolume("wrapper", r.WrapperScript.Name, 0o555))
		mounts = append(mounts, corev1.VolumeMount{Name: "wrapper", MountPath: WrapperMountPath, ReadOnly: true})
	}
	if r.HostConfig != nil {
		volumes = append(volumes, configMapVolume("host-config", r.HostConfig.Name, 0o444))
		mounts = append(mounts, corev1.VolumeMount{Name: "host-config", MountPath: HostConfigMountPath, ReadOnly: true})
		envVars = append(envVars, corev1.EnvVar{Name: "SELF_HOSTED_RUNNER_HOST_CONFIG_DIR", Value: HostConfigMountPath})
	}
	for _, v := range pt.Volumes {
		volumes = append(volumes, v.ToCoreVolume())
	}
	mounts = append(mounts, pt.VolumeMounts...)

	podSC := &corev1.PodSecurityContext{
		RunAsNonRoot:   new(true),
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	if pt.SecurityContext != nil {
		podSC.RunAsUser = pt.SecurityContext.RunAsUser
		podSC.RunAsGroup = pt.SecurityContext.RunAsGroup
		podSC.FSGroup = pt.SecurityContext.FSGroup
		podSC.SupplementalGroups = pt.SecurityContext.SupplementalGroups
	}

	container := corev1.Container{
		Name:  RunnerContainerName,
		Image: r.Image,
		Args:  RunnerArgs(r, in.SecretFile),
		Env:   envVars,
		Ports: []corev1.ContainerPort{{Name: "health", ContainerPort: healthPort, Protocol: corev1.ProtocolTCP}},
		ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
			Path: "/healthz", Port: intstr.FromString("health")}}, InitialDelaySeconds: 5, PeriodSeconds: 10},
		LivenessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
			Path: "/healthz", Port: intstr.FromString("health")}}, InitialDelaySeconds: 30, PeriodSeconds: 30},
		Resources:                pt.Resources,
		VolumeMounts:             mounts,
		TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: new(false),
			ReadOnlyRootFilesystem:   new(true),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		},
	}

	automount := false
	if pt.AutomountServiceAccountToken != nil {
		automount = *pt.AutomountServiceAccountToken
	}

	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: annotations},
		Spec: corev1.PodSpec{
			Containers:                    []corev1.Container{container},
			Volumes:                       volumes,
			RestartPolicy:                 in.RestartPolicy,
			TerminationGracePeriodSeconds: new(grace),
			ServiceAccountName:            pt.ServiceAccountName,
			AutomountServiceAccountToken:  new(automount),
			ImagePullSecrets:              pt.ImagePullSecrets,
			NodeSelector:                  pt.NodeSelector,
			Tolerations:                   pt.Tolerations,
			Affinity:                      pt.Affinity,
			TopologySpreadConstraints:     pt.TopologySpreadConstraints,
			PriorityClassName:             pt.PriorityClassName,
			RuntimeClassName:              pt.RuntimeClassName,
			SchedulerName:                 pt.SchedulerName,
			SecurityContext:               podSC,
		},
	}
}

func configMapVolume(name, cmName string, mode int32) corev1.Volume {
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
		LocalObjectReference: corev1.LocalObjectReference{Name: cmName}, DefaultMode: new(mode)}}}
}
