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

package controller

import (
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// byType looks a cache entry up by object type: map keys are fresh pointers,
// so indexing with a new literal never matches.
func byType(byObject map[client.Object]cache.ByObject, obj client.Object) (cache.ByObject, bool) {
	for k, v := range byObject {
		if fmt.Sprintf("%T", k) == fmt.Sprintf("%T", obj) {
			return v, true
		}
	}
	return cache.ByObject{}, false
}

func TestCacheByObjectFiltersOperatorObjects(t *testing.T) {
	byObject, err := CacheByObject()
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range []client.Object{&corev1.Pod{}, &corev1.ServiceAccount{}, &rbacv1.Role{}, &rbacv1.RoleBinding{}} {
		cfg, ok := byType(byObject, obj)
		if !ok || cfg.Label == nil {
			t.Fatalf("%T must have a label selector", obj)
		}
	}
	for _, obj := range []client.Object{&corev1.Secret{}, &corev1.ConfigMap{}} {
		if _, ok := byType(byObject, obj); ok {
			t.Fatalf("%T must stay unfiltered (user-named)", obj)
		}
	}
	role, _ := byType(byObject, &rbacv1.Role{})
	if !role.Label.Matches(labels.Set{"app.kubernetes.io/part-of": "claude-code-self-hosted-runner"}) {
		t.Fatal("operator-created RBAC objects must match")
	}
	if role.Label.Matches(labels.Set{"app": "other"}) {
		t.Fatal("unrelated objects must not match")
	}
}
