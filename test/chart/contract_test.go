//go:build chart

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

// Package chart holds the contract test that keeps the Helm chart in
// dist/chart aligned with the controller-gen output it was generated from.
package chart

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	sigsyaml "sigs.k8s.io/yaml"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func render(t *testing.T, sets ...string) []unstructured.Unstructured {
	t.Helper()
	args := make([]string, 0, 5+2*len(sets))
	args = append(args, "template", "rel", filepath.Join(repoRoot(t), "dist", "chart"), "--namespace", "ns")
	for _, s := range sets {
		args = append(args, "--set", s)
	}
	out, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, out)
	}
	var objs []unstructured.Unstructured
	dec := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(out), 4096)
	for {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("decode helm template output: %v", err)
		}
		if len(m) > 0 {
			objs = append(objs, unstructured.Unstructured{Object: m})
		}
	}
	return objs
}

func find(objs []unstructured.Unstructured, kind, nameSuffix string) *unstructured.Unstructured {
	for i := range objs {
		if objs[i].GetKind() == kind && strings.HasSuffix(objs[i].GetName(), nameSuffix) {
			return &objs[i]
		}
	}
	return nil
}

func envHas(c corev1.Container, name string) bool {
	for _, e := range c.Env {
		if e.Name == name && e.Value != "" {
			return true
		}
	}
	return false
}

func TestChartRBACMatchesControllerGen(t *testing.T) {
	objs := render(t)
	rendered := find(objs, "ClusterRole", "manager-role")
	if rendered == nil {
		t.Fatal("manager ClusterRole not rendered")
	}
	var got rbacv1.ClusterRole
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(rendered.Object, &got); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "config", "rbac", "role.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var want rbacv1.ClusterRole
	if err := sigsyaml.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if !equality.Semantic.DeepEqual(got.Rules, want.Rules) {
		t.Fatalf("chart ClusterRole rules drifted from controller-gen output:\nchart=%v\nrole.yaml=%v", got.Rules, want.Rules)
	}
}

func TestChartManagerIsRestricted(t *testing.T) {
	objs := render(t)
	d := find(objs, "Deployment", "controller-manager")
	if d == nil {
		t.Fatal("manager Deployment not rendered")
	}
	var dep appsv1.Deployment
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(d.Object, &dep); err != nil {
		t.Fatal(err)
	}
	ps := dep.Spec.Template.Spec
	psc := ps.SecurityContext
	if psc == nil || psc.RunAsNonRoot == nil || !*psc.RunAsNonRoot || psc.SeccompProfile == nil {
		t.Fatalf("pod security context not restricted: %+v", ps.SecurityContext)
	}
	sawManager := false
	for _, c := range ps.Containers {
		sc := c.SecurityContext
		if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation ||
			sc.Capabilities == nil || len(sc.Capabilities.Drop) == 0 {
			t.Fatalf("container %s not restricted: %+v", c.Name, sc)
		}
		if c.Name != "manager" {
			continue
		}
		sawManager = true
		joined := strings.Join(c.Args, " ")
		if !strings.Contains(joined, "--hook-image") && !envHas(c, "OPERATOR_IMAGE") {
			t.Fatalf("manager must carry --hook-image or OPERATOR_IMAGE: args=%v env=%v", c.Args, c.Env)
		}
		for _, flag := range []string{"--watch-namespaces", "--tracing-endpoint"} {
			if !strings.Contains(joined, flag) {
				t.Fatalf("manager args must carry %s: %v", flag, c.Args)
			}
		}
	}
	if !sawManager {
		t.Fatal("manager container not rendered")
	}
}

func TestChartCRDsMatchGenerated(t *testing.T) {
	objs := render(t)
	dir := filepath.Join(repoRoot(t), "config", "crd", "bases")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatalf("no CRDs in %s", dir)
	}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var want apiextensionsv1.CustomResourceDefinition
		if err := sigsyaml.Unmarshal(raw, &want); err != nil {
			t.Fatal(err)
		}
		rendered := find(objs, "CustomResourceDefinition", want.Name)
		if rendered == nil {
			t.Fatalf("CRD %s missing from chart", want.Name)
		}
		var got apiextensionsv1.CustomResourceDefinition
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(rendered.Object, &got); err != nil {
			t.Fatal(err)
		}
		if !equality.Semantic.DeepEqual(got.Spec, want.Spec) {
			t.Fatalf("CRD %s spec drifted between chart and config/crd/bases", want.Name)
		}
	}
}

func TestChartAdmissionPolicyToggle(t *testing.T) {
	on, off := render(t), render(t, "admissionPolicy.enabled=false")
	for _, name := range []string{"orchestrator-secrets", "orchestrator-runners"} {
		for _, kind := range []string{"ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding"} {
			if find(on, kind, name) == nil {
				t.Fatalf("%s %s must render by default", kind, name)
			}
			if find(off, kind, name) != nil {
				t.Fatalf("%s %s must not render when disabled", kind, name)
			}
		}
	}
	if find(render(t), "PodMonitor", "runner-metrics") != nil {
		t.Fatal("PodMonitor must not render when prometheus is disabled")
	}
	if find(render(t, "prometheus.enabled=true"), "PodMonitor", "runner-metrics") == nil {
		t.Fatal("PodMonitor must render when prometheus is enabled")
	}
}
