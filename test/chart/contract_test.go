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
	"slices"
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

func managerPod(t *testing.T, objs []unstructured.Unstructured) (corev1.PodSpec, corev1.Container) {
	t.Helper()
	d := find(objs, "Deployment", "controller-manager")
	if d == nil {
		t.Fatal("manager Deployment not rendered")
	}
	var dep appsv1.Deployment
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(d.Object, &dep); err != nil {
		t.Fatal(err)
	}
	for _, c := range dep.Spec.Template.Spec.Containers {
		if c.Name == "manager" {
			return dep.Spec.Template.Spec, c
		}
	}
	t.Fatal("manager container not rendered")
	return corev1.PodSpec{}, corev1.Container{}
}

// argValue returns the value of --flag=value in args.
func argValue(args []string, flag string) (string, bool) {
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, flag+"="); ok {
			return v, true
		}
	}
	return "", false
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
	ps, mgr := managerPod(t, render(t))
	psc := ps.SecurityContext
	if psc == nil || psc.RunAsNonRoot == nil || !*psc.RunAsNonRoot || psc.SeccompProfile == nil ||
		(psc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault &&
			psc.SeccompProfile.Type != corev1.SeccompProfileTypeLocalhost) {
		t.Fatalf("pod security context not restricted: %+v", psc)
	}
	for _, c := range ps.Containers {
		sc := c.SecurityContext
		if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation ||
			sc.Capabilities == nil || !slices.Contains(sc.Capabilities.Drop, corev1.Capability("ALL")) ||
			sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
			t.Fatalf("container %s not restricted: %+v", c.Name, sc)
		}
	}
	if hook, ok := argValue(mgr.Args, "--hook-image"); !ok || hook != mgr.Image {
		t.Fatalf("--hook-image must equal the manager image %q: %v", mgr.Image, mgr.Args)
	}
	if _, ok := argValue(mgr.Args, "--watch-namespaces"); !ok {
		t.Fatalf("manager args must carry --watch-namespaces: %v", mgr.Args)
	}
	if _, ok := argValue(mgr.Args, "--tracing-endpoint"); ok {
		t.Fatalf("--tracing-endpoint must be omitted when empty so OTEL_EXPORTER_OTLP_ENDPOINT applies: %v", mgr.Args)
	}
}

func TestChartManagerFlagsFollowValues(t *testing.T) {
	const endpoint = "otel-collector.observability:4317"
	_, mgr := managerPod(t, render(t, "manager.image.tag=9.9.9", "tracing.endpoint="+endpoint))
	if !strings.HasSuffix(mgr.Image, ":9.9.9") {
		t.Fatalf("manager image did not follow manager.image.tag: %s", mgr.Image)
	}
	if hook, _ := argValue(mgr.Args, "--hook-image"); hook != mgr.Image {
		t.Fatalf("--hook-image %q must follow the manager image %q", hook, mgr.Image)
	}
	if got, _ := argValue(mgr.Args, "--tracing-endpoint"); got != endpoint {
		t.Fatalf("--tracing-endpoint = %q, want %q", got, endpoint)
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
