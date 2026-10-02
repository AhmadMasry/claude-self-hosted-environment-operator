package builders

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

const (
	testWorkloadName = "platform-runner"
	testEnvSecret    = "env-secret"
)

func TestFixedDeployment(t *testing.T) {
	env := testEnv()
	env.Spec.Fixed.Replicas = ptr.To[int32](3)
	d := FixedDeployment(env, "h1")
	if d.Name != testWorkloadName || d.Namespace != "claude" {
		t.Fatalf("name/namespace wrong: %s/%s", d.Namespace, d.Name)
	}
	if d.APIVersion != "apps/v1" || d.Kind != "Deployment" {
		t.Fatal("TypeMeta must be set for server-side apply")
	}
	if *d.Spec.Replicas != 3 {
		t.Fatal("replicas not propagated")
	}
	if d.Spec.Selector.MatchLabels[selfhostedv1alpha1.LabelEnvironment] != "platform" {
		t.Fatal("selector must use environment label")
	}
	if d.Spec.Template.Annotations[selfhostedv1alpha1.AnnotationConfigHash] != "h1" {
		t.Fatal("hash annotation missing")
	}
	if d.Spec.Template.Spec.RestartPolicy != corev1.RestartPolicyAlways {
		t.Fatal("fixed runners restart")
	}
	v := d.Spec.Template.Spec.Volumes[0]
	if v.Secret == nil || v.Secret.SecretName != testEnvSecret || v.Secret.Items[0].Key != SecretFileName || v.Secret.Items[0].Path != SecretFileName {
		t.Fatalf("secret volume wrong: %+v", v)
	}
	if d.Spec.Template.Spec.Containers[0].Args[2] != "/etc/claude/environment-secret" {
		t.Fatal("secret file path wrong")
	}
}

func TestFixedStatefulSet(t *testing.T) {
	env := testEnv()
	env.Spec.Runner.Settings.LockToAccount = "user_1"
	env.Spec.Fixed.PersistentWorkspace = &selfhostedv1alpha1.PersistentWorkspaceSpec{
		VolumeClaimTemplate: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("20Gi")}}}}
	s := FixedStatefulSet(env, "h2")
	if s.Kind != "StatefulSet" || s.Name != testWorkloadName {
		t.Fatal("statefulset identity wrong")
	}
	if s.Spec.ServiceName != testWorkloadName {
		t.Fatal("serviceName must be set")
	}
	if len(s.Spec.VolumeClaimTemplates) != 1 || s.Spec.VolumeClaimTemplates[0].Name != WorkspaceVolume {
		t.Fatal("volumeClaimTemplate must be named workspace")
	}
	for _, v := range s.Spec.Template.Spec.Volumes {
		if v.Name == WorkspaceVolume {
			t.Fatal("workspace must come from the PVC, not an emptyDir")
		}
	}
	if s.Spec.PodManagementPolicy != "Parallel" {
		t.Fatal("runners are independent; use Parallel")
	}
}
