package builders

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	testSecretName = "env-secret"
	testConfigMap  = "hooks"
)

func TestConfigHashChangesWithInputs(t *testing.T) {
	env := testEnv()
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: testSecretName}, Data: map[string][]byte{SecretFileName: []byte("k1")}}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: testConfigMap}, Data: map[string]string{"checkout": "#!/bin/sh"}}

	base := ConfigHash(env, sec, []*corev1.ConfigMap{cm})
	if len(base) != 16 {
		t.Fatalf("hash should be 16 hex chars, got %q", base)
	}
	if ConfigHash(env, sec, []*corev1.ConfigMap{cm}) != base {
		t.Fatal("hash must be deterministic")
	}
	sec2 := sec.DeepCopy()
	sec2.Data["environment-secret"] = []byte("k2")
	if ConfigHash(env, sec2, []*corev1.ConfigMap{cm}) == base {
		t.Fatal("secret change must change hash")
	}
	cm2 := cm.DeepCopy()
	cm2.Data["checkout"] = "#!/bin/bash"
	if ConfigHash(env, sec, []*corev1.ConfigMap{cm2}) == base {
		t.Fatal("configmap change must change hash")
	}
	env2 := env.DeepCopy()
	env2.Spec.Runner.Settings.DrainWaitSeconds = 10
	if ConfigHash(env2, sec, []*corev1.ConfigMap{cm}) == base {
		t.Fatal("runner spec change must change hash")
	}
	env3 := env.DeepCopy()
	env3.Spec.Fixed.Replicas = new(int32)
	if ConfigHash(env3, sec, []*corev1.ConfigMap{cm}) != base {
		t.Fatal("replica change must not change hash")
	}
	if ConfigHash(env, nil, nil) == "" {
		t.Fatal("nil inputs must still hash")
	}
}
