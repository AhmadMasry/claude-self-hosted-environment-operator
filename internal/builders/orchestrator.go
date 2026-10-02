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
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

const (
	HookBinaryPath            = "/spawn-runner"
	HookInstallDir            = "/hooks"
	OrchestratorContainerName = "orchestrator"
	HookInitContainerName     = "install-hook"
	hookVolumeName            = "hooks"
	secretVolumeName          = "environment-secret"
	homeVolumeName            = "home"
	tmpVolumeName             = "tmp"
	healthzPath               = "/healthz"
	appsAPIVersion            = "apps/v1"
	orchestratorGraceSeconds  = int64(30)
)

// OrchestratorName names every orchestrator-side object for an environment.
func OrchestratorName(env *selfhostedv1alpha1.ClaudeEnvironment) string {
	return env.Name + "-orchestrator"
}

// OrchestratorSelectorLabels are the immutable selector labels of the orchestrator Deployment.
func OrchestratorSelectorLabels(env *selfhostedv1alpha1.ClaudeEnvironment) map[string]string {
	return map[string]string{
		selfhostedv1alpha1.LabelEnvironment: env.Name,
		selfhostedv1alpha1.LabelRole:        selfhostedv1alpha1.RoleOrchestrator,
	}
}

func orchestratorMeta(env *selfhostedv1alpha1.ClaudeEnvironment) metav1.ObjectMeta {
	labels := OrchestratorSelectorLabels(env)
	labels[selfhostedv1alpha1.LabelPartOf] = selfhostedv1alpha1.PartOfValue
	return metav1.ObjectMeta{Name: OrchestratorName(env), Namespace: env.Namespace, Labels: labels}
}

// OrchestratorServiceAccount is the identity the spawn-runner hook uses.
func OrchestratorServiceAccount(env *selfhostedv1alpha1.ClaudeEnvironment) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"},
		ObjectMeta: orchestratorMeta(env),
	}
}

// OrchestratorRole grants exactly what the hook does: read the environment,
// create ClaudeRunners (and list them only when a concurrency cap applies),
// and create the work-order Secret then hand it to the ClaudeRunner.
func OrchestratorRole(env *selfhostedv1alpha1.ClaudeEnvironment) *rbacv1.Role {
	runnerVerbs := []string{"create", "get"}
	if env.Spec.OnDemand != nil && env.Spec.OnDemand.MaxConcurrentRunners > 0 {
		runnerVerbs = append(runnerVerbs, "list")
	}
	return &rbacv1.Role{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "Role"},
		ObjectMeta: orchestratorMeta(env),
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{selfhostedv1alpha1.GroupVersion.Group}, Resources: []string{"claudeenvironments"}, Verbs: []string{"get"},
				ResourceNames: []string{env.Name}},
			{APIGroups: []string{selfhostedv1alpha1.GroupVersion.Group}, Resources: []string{"clauderunners"}, Verbs: runnerVerbs},
			{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"create", "patch", "delete"}},
		},
	}
}

// OrchestratorRoleBinding binds the Role to the ServiceAccount.
func OrchestratorRoleBinding(env *selfhostedv1alpha1.ClaudeEnvironment) *rbacv1.RoleBinding {
	name := OrchestratorName(env)
	return &rbacv1.RoleBinding{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "RoleBinding"},
		ObjectMeta: orchestratorMeta(env),
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: name, Namespace: env.Namespace}},
	}
}

// OrchestratorArgs renders `claude self-hosted-runner orchestrator ...`.
func OrchestratorArgs(env *selfhostedv1alpha1.ClaudeEnvironment, secretFile string) []string {
	o := env.Spec.OnDemand.Orchestrator
	a := []string{
		"self-hosted-runner", "orchestrator",
		"--environment-secret-file", secretFile,
		"--hooks-dir", HooksMountPath,
		"--health-port", strconv.Itoa(int(valueOr(o.HealthPort, DefaultHealthPort))),
		"--expected-spawn-seconds", strconv.Itoa(int(o.ExpectedSpawnSeconds)),
		"--hook-timeout", strconv.Itoa(int(o.HookTimeoutSeconds)),
		"--hook-concurrency", strconv.Itoa(int(o.HookConcurrency)),
	}
	a = appendInt(a, "--min-idle", o.MinIdle)
	return appendStr(a, "--log-level", o.LogLevel)
}

// restrictedContainerSecurityContext is the Restricted-profile container context.
func restrictedContainerSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: new(false),
		ReadOnlyRootFilesystem:   new(true),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
}

func orchestratorPodSecurityContext(pt *selfhostedv1alpha1.PodTemplate) *corev1.PodSecurityContext {
	podSC := &corev1.PodSecurityContext{
		RunAsNonRoot:   new(true),
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	if pt.SecurityContext != nil {
		podSC.RunAsUser, podSC.RunAsGroup = pt.SecurityContext.RunAsUser, pt.SecurityContext.RunAsGroup
		podSC.FSGroup, podSC.SupplementalGroups = pt.SecurityContext.FSGroup, pt.SecurityContext.SupplementalGroups
	}
	return podSC
}

func orchestratorVolumes(env *selfhostedv1alpha1.ClaudeEnvironment) []corev1.Volume {
	pt := env.Spec.OnDemand.Orchestrator.PodTemplate
	volumes := make([]corev1.Volume, 0, 4+len(pt.Volumes))
	volumes = append(volumes,
		corev1.Volume{Name: secretVolumeName, VolumeSource: EnvironmentSecretVolume(env)},
		corev1.Volume{Name: hookVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		corev1.Volume{Name: homeVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		corev1.Volume{Name: tmpVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	)
	for _, v := range pt.Volumes {
		volumes = append(volumes, v.ToCoreVolume())
	}
	return volumes
}

func orchestratorEnv(env *selfhostedv1alpha1.ClaudeEnvironment) []corev1.EnvVar {
	o := env.Spec.OnDemand.Orchestrator
	envVars := make([]corev1.EnvVar, 0, 3+len(o.Env))
	envVars = append(envVars,
		corev1.EnvVar{Name: selfhostedv1alpha1.EnvHookEnvironment, Value: env.Name},
		corev1.EnvVar{Name: selfhostedv1alpha1.EnvHookNamespace, ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}}},
		corev1.EnvVar{Name: selfhostedv1alpha1.EnvHookMaxConcurrentRunners,
			Value: strconv.Itoa(int(env.Spec.OnDemand.MaxConcurrentRunners))},
	)
	return append(envVars, o.Env...)
}

func orchestratorContainers(env *selfhostedv1alpha1.ClaudeEnvironment) []corev1.Container {
	o := env.Spec.OnDemand.Orchestrator
	pt := o.PodTemplate
	image := o.Image
	if image == "" {
		image = env.Spec.Runner.Image
	}
	healthPort := valueOr(o.HealthPort, DefaultHealthPort)
	healthURL := "http://127.0.0.1:" + strconv.Itoa(int(healthPort)) + healthzPath

	mounts := make([]corev1.VolumeMount, 0, 4+len(pt.VolumeMounts))
	mounts = append(mounts,
		corev1.VolumeMount{Name: secretVolumeName, MountPath: SecretMountPath, ReadOnly: true},
		corev1.VolumeMount{Name: hookVolumeName, MountPath: HooksMountPath, ReadOnly: true},
		corev1.VolumeMount{Name: homeVolumeName, MountPath: HomeMountPath},
		corev1.VolumeMount{Name: tmpVolumeName, MountPath: TmpMountPath},
	)
	mounts = append(mounts, pt.VolumeMounts...)

	return []corev1.Container{{
		Name:  OrchestratorContainerName,
		Image: image,
		Args:  OrchestratorArgs(env, SecretMountPath+"/"+SecretFileName),
		Env:   orchestratorEnv(env),
		Ports: []corev1.ContainerPort{{Name: "health", ContainerPort: healthPort, Protocol: corev1.ProtocolTCP}},
		ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{
			Command: []string{HooksMountPath + HookBinaryPath, "--probe-connected", healthURL}}},
			InitialDelaySeconds: 5, PeriodSeconds: 10, TimeoutSeconds: 5},
		LivenessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
			Path: healthzPath, Port: intstr.FromString("health")}}, InitialDelaySeconds: 30, PeriodSeconds: 30},
		Resources:                pt.Resources,
		VolumeMounts:             mounts,
		TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
		SecurityContext:          restrictedContainerSecurityContext(),
	}}
}

func orchestratorInitContainers(hookImage string) []corev1.Container {
	return []corev1.Container{{
		Name:            HookInitContainerName,
		Image:           hookImage,
		Command:         []string{HookBinaryPath},
		Args:            []string{"--install", HookInstallDir},
		VolumeMounts:    []corev1.VolumeMount{{Name: hookVolumeName, MountPath: HookInstallDir}},
		SecurityContext: restrictedContainerSecurityContext(),
	}}
}

// OrchestratorDeployment runs the orchestrator from the user's image with the
// operator's spawn-runner hook injected by an init container.
func OrchestratorDeployment(env *selfhostedv1alpha1.ClaudeEnvironment, hash, hookImage string) *appsv1.Deployment {
	o := env.Spec.OnDemand.Orchestrator
	pt := o.PodTemplate

	labels := map[string]string{}
	maps.Copy(labels, pt.Labels)
	maps.Copy(labels, OrchestratorSelectorLabels(env))
	labels[selfhostedv1alpha1.LabelPartOf] = selfhostedv1alpha1.PartOfValue
	annotations := map[string]string{}
	maps.Copy(annotations, pt.Annotations)
	annotations[selfhostedv1alpha1.AnnotationConfigHash] = hash

	return &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: appsAPIVersion, Kind: "Deployment"},
		ObjectMeta: orchestratorMeta(env),
		Spec: appsv1.DeploymentSpec{
			Replicas: o.Replicas,
			Selector: &metav1.LabelSelector{MatchLabels: OrchestratorSelectorLabels(env)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: annotations},
				Spec: corev1.PodSpec{
					ServiceAccountName:            OrchestratorName(env),
					AutomountServiceAccountToken:  new(true),
					TerminationGracePeriodSeconds: new(orchestratorGraceSeconds),
					SecurityContext:               orchestratorPodSecurityContext(&pt),
					ImagePullSecrets:              pt.ImagePullSecrets,
					NodeSelector:                  pt.NodeSelector,
					Tolerations:                   pt.Tolerations,
					Affinity:                      pt.Affinity,
					TopologySpreadConstraints:     pt.TopologySpreadConstraints,
					PriorityClassName:             pt.PriorityClassName,
					RuntimeClassName:              pt.RuntimeClassName,
					SchedulerName:                 pt.SchedulerName,
					Volumes:                       orchestratorVolumes(env),
					InitContainers:                orchestratorInitContainers(hookImage),
					Containers:                    orchestratorContainers(env),
				},
			},
		},
	}
}
