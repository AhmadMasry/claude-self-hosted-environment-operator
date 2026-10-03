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
	"slices"

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

	volEnvironmentSecret = "environment-secret"
	volHome              = "home"
	volTmp               = "tmp"
	volHooks             = "hooks"
	volWrapper           = "wrapper"
	volHostConfig        = "host-config"
)

// ReservedMountPaths are the paths the operator mounts onto; users may not
// mount over them or set baseDir to them.
var ReservedMountPaths = []string{SecretMountPath, HomeMountPath, TmpMountPath}

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
	// HostConfigKeys are the keys of the hostConfig ConfigMap, each mounted
	// as a plain file; see HostConfigKeys.
	HostConfigKeys []string
}

// HostConfigKeys returns the sorted keys of the ConfigMap the runner's
// hostConfig names, found by name in configMaps, or nil when the environment
// has no hostConfig or the ConfigMap is not in the list.
func HostConfigKeys(env *selfhostedv1alpha1.ClaudeEnvironment, configMaps []*corev1.ConfigMap) []string {
	hc := env.Spec.Runner.HostConfig
	if hc == nil {
		return nil
	}
	for _, cm := range configMaps {
		if cm == nil || cm.Name != hc.Name {
			continue
		}
		keys := slices.Collect(maps.Keys(cm.Data))
		keys = append(keys, slices.Collect(maps.Keys(cm.BinaryData))...)
		slices.Sort(keys)
		return slices.Compact(keys)
	}
	return nil
}

// hostConfigMounts mounts every key of the hostConfig ConfigMap as a plain
// file under HostConfigMountPath. A ConfigMap volume mounted as a directory
// exposes its keys as symlinks into a hidden timestamped directory, and the
// runner's host-config snapshot captured nothing from such a mount, so no
// settings or hooks reached the sessions (seen with Claude Code 2.1.288).
// Without keys the directory is mounted as is, so the path the runner is
// pointed at exists.
func hostConfigMounts(keys []string) []corev1.VolumeMount {
	if len(keys) == 0 {
		return []corev1.VolumeMount{{Name: volHostConfig, MountPath: HostConfigMountPath, ReadOnly: true}}
	}
	keys = slices.Clone(keys)
	slices.Sort(keys)
	mounts := make([]corev1.VolumeMount, 0, len(keys))
	for _, k := range keys {
		mounts = append(mounts, corev1.VolumeMount{Name: volHostConfig, MountPath: HostConfigMountPath + "/" + k, SubPath: k, ReadOnly: true})
	}
	return mounts
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
		{Name: volEnvironmentSecret, VolumeSource: in.SecretVolume},
		{Name: volHome, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: volTmp, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
	if !in.WorkspaceFromPVC {
		volumes = append(volumes, corev1.Volume{Name: WorkspaceVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}})
	}
	mounts := []corev1.VolumeMount{
		{Name: volEnvironmentSecret, MountPath: SecretMountPath, ReadOnly: true},
		{Name: WorkspaceVolume, MountPath: r.BaseDir},
		{Name: volHome, MountPath: HomeMountPath},
		{Name: volTmp, MountPath: TmpMountPath},
	}
	envVars := append([]corev1.EnvVar{}, r.Env...)
	if r.Settings.ClientLabel == "" {
		envVars = append(envVars, corev1.EnvVar{Name: "SELF_HOSTED_RUNNER_CLIENT_LABEL",
			ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}})
	}
	if r.LifecycleHooks != nil {
		volumes = append(volumes, configMapVolume(volHooks, r.LifecycleHooks.Name, 0o555))
		mounts = append(mounts, corev1.VolumeMount{Name: volHooks, MountPath: HooksMountPath, ReadOnly: true})
	}
	if r.WrapperScript != nil {
		volumes = append(volumes, configMapVolume(volWrapper, r.WrapperScript.Name, 0o555))
		mounts = append(mounts, corev1.VolumeMount{Name: volWrapper, MountPath: WrapperMountPath, ReadOnly: true})
	}
	if r.HostConfig != nil {
		volumes = append(volumes, configMapVolume(volHostConfig, r.HostConfig.Name, 0o444))
		mounts = append(mounts, hostConfigMounts(in.HostConfigKeys)...)
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
