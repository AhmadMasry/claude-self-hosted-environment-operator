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

package v1alpha1

import corev1 "k8s.io/api/core/v1"

// PodTemplate is the curated subset of a PodSpec users may set. Fields that
// could break the Restricted Pod Security Standard are deliberately absent.
type PodTemplate struct {
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`
	// +optional
	AutomountServiceAccountToken *bool `json:"automountServiceAccountToken,omitempty"`
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`
	// +optional
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`
	// +optional
	PriorityClassName string `json:"priorityClassName,omitempty"`
	// +optional
	RuntimeClassName *string `json:"runtimeClassName,omitempty"`
	// +optional
	SchedulerName string `json:"schedulerName,omitempty"`
	// +optional
	SecurityContext *PodSecurityContext `json:"securityContext,omitempty"`
	// +kubebuilder:validation:MaxItems=32
	// +optional
	Volumes []Volume `json:"volumes,omitempty"`
	// +kubebuilder:validation:MaxItems=64
	// +optional
	VolumeMounts []corev1.VolumeMount `json:"volumeMounts,omitempty"`
}

// PodSecurityContext exposes only the identity fields; everything else is fixed by the operator.
type PodSecurityContext struct {
	// +kubebuilder:validation:Minimum=1
	// +optional
	RunAsUser *int64 `json:"runAsUser,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +optional
	RunAsGroup *int64 `json:"runAsGroup,omitempty"`
	// +optional
	FSGroup *int64 `json:"fsGroup,omitempty"`
	// +optional
	SupplementalGroups []int64 `json:"supplementalGroups,omitempty"`
}

// Volume is a pod volume limited to the sources the Restricted standard allows.
// +kubebuilder:validation:XValidation:rule="[has(self.configMap),has(self.secret),has(self.emptyDir),has(self.projected),has(self.downwardAPI),has(self.persistentVolumeClaim),has(self.ephemeral),has(self.csi)].filter(x, x).size() == 1",message="exactly one volume source must be set"
type Volume struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +optional
	ConfigMap *corev1.ConfigMapVolumeSource `json:"configMap,omitempty"`
	// +optional
	Secret *corev1.SecretVolumeSource `json:"secret,omitempty"`
	// +optional
	EmptyDir *corev1.EmptyDirVolumeSource `json:"emptyDir,omitempty"`
	// +optional
	Projected *corev1.ProjectedVolumeSource `json:"projected,omitempty"`
	// +optional
	DownwardAPI *corev1.DownwardAPIVolumeSource `json:"downwardAPI,omitempty"`
	// +optional
	PersistentVolumeClaim *corev1.PersistentVolumeClaimVolumeSource `json:"persistentVolumeClaim,omitempty"`
	// +optional
	Ephemeral *corev1.EphemeralVolumeSource `json:"ephemeral,omitempty"`
	// +optional
	CSI *corev1.CSIVolumeSource `json:"csi,omitempty"`
}

// ToCoreVolume converts to the core type.
func (v Volume) ToCoreVolume() corev1.Volume {
	return corev1.Volume{Name: v.Name, VolumeSource: corev1.VolumeSource{
		ConfigMap: v.ConfigMap, Secret: v.Secret, EmptyDir: v.EmptyDir, Projected: v.Projected,
		DownwardAPI: v.DownwardAPI, PersistentVolumeClaim: v.PersistentVolumeClaim,
		Ephemeral: v.Ephemeral, CSI: v.CSI,
	}}
}
