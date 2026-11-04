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
	"maps"
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
	if _, ok := argValue(mgr.Args, "--apiserver-endpoints"); ok {
		t.Fatalf("--apiserver-endpoints must be omitted when empty so the manager reads default/kubernetes: %v", mgr.Args)
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

func TestChartAPIServerEndpointsFlag(t *testing.T) {
	_, mgr := managerPod(t, render(t, "networkPolicy.apiServerEndpoints={10.0.0.1:6443}"))
	if got, ok := argValue(mgr.Args, "--apiserver-endpoints"); !ok || got != "10.0.0.1:6443" {
		t.Fatalf("--apiserver-endpoints = %q (present %v), want 10.0.0.1:6443: %v", got, ok, mgr.Args)
	}
	_, mgr = managerPod(t, render(t, "networkPolicy.apiServerEndpoints={10.0.0.1:6443,[fd00::1]:443}"))
	if got, _ := argValue(mgr.Args, "--apiserver-endpoints"); got != "10.0.0.1:6443,[fd00::1]:443" {
		t.Fatalf("--apiserver-endpoints must join the list with commas, got %q", got)
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

// managerRoles returns the rendered manager Roles keyed by namespace.
func managerRoles(t *testing.T, objs []unstructured.Unstructured) map[string]rbacv1.Role {
	t.Helper()
	out := map[string]rbacv1.Role{}
	for _, o := range objs {
		if o.GetKind() != "Role" || !strings.HasSuffix(o.GetName(), "manager-role") {
			continue
		}
		var r rbacv1.Role
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, &r); err != nil {
			t.Fatal(err)
		}
		if _, dup := out[r.Namespace]; dup {
			t.Fatalf("manager Role rendered twice in namespace %s", r.Namespace)
		}
		out[r.Namespace] = r
	}
	return out
}

func TestChartNamespacedWatchesReleaseNamespaceByDefault(t *testing.T) {
	objs := render(t, "rbac.namespaced=true")
	_, mgr := managerPod(t, objs)
	if got, _ := argValue(mgr.Args, "--watch-namespaces"); got != "ns" {
		t.Fatalf("--watch-namespaces = %q with rbac.namespaced and no list, want the release namespace", got)
	}
	if roles := managerRoles(t, objs); len(roles) != 1 || roles["ns"].Name == "" {
		t.Fatalf("want the manager Role in the release namespace only, got %v", slices.Collect(maps.Keys(roles)))
	}
	_, mgr = managerPod(t, render(t))
	if got, _ := argValue(mgr.Args, "--watch-namespaces"); got != "" {
		t.Fatalf("--watch-namespaces = %q with cluster-scoped RBAC, want empty (all namespaces)", got)
	}
}

func TestChartNamespacedWatchListGetsRBAC(t *testing.T) {
	const list = `ns\,team-a\, team-b`
	objs := render(t, "rbac.namespaced=true", "watchNamespaces="+list)
	_, mgr := managerPod(t, objs)
	if got, _ := argValue(mgr.Args, "--watch-namespaces"); got != "ns,team-a, team-b" {
		t.Fatalf("--watch-namespaces = %q, want the value as given", got)
	}
	roles := managerRoles(t, objs)
	want := []string{"ns", "team-a", "team-b"}
	if got := slices.Sorted(maps.Keys(roles)); !slices.Equal(got, want) {
		t.Fatalf("manager Roles in namespaces %v, want %v", got, want)
	}
	for _, ns := range want[1:] {
		if !equality.Semantic.DeepEqual(roles[ns].Rules, roles["ns"].Rules) {
			t.Fatalf("manager Role in %s differs from the release-namespace Role", ns)
		}
		var rb *rbacv1.RoleBinding
		for _, o := range objs {
			if o.GetKind() == "RoleBinding" && o.GetNamespace() == ns && strings.HasSuffix(o.GetName(), "manager-rolebinding") {
				rb = &rbacv1.RoleBinding{}
				if err := runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, rb); err != nil {
					t.Fatal(err)
				}
			}
		}
		if rb == nil {
			t.Fatalf("no manager RoleBinding in %s", ns)
		}
		if rb.RoleRef.Kind != "Role" || rb.RoleRef.Name != roles[ns].Name {
			t.Fatalf("RoleBinding in %s refers to %s %s, want Role %s", ns, rb.RoleRef.Kind, rb.RoleRef.Name, roles[ns].Name)
		}
		if len(rb.Subjects) != 1 || rb.Subjects[0].Kind != "ServiceAccount" || rb.Subjects[0].Namespace != "ns" ||
			!strings.HasSuffix(rb.Subjects[0].Name, "controller-manager") {
			t.Fatalf("RoleBinding in %s must bind the manager ServiceAccount in the release namespace: %+v", ns, rb.Subjects)
		}
	}
	if roles := managerRoles(t, render(t, "watchNamespaces="+list)); len(roles) != 0 {
		t.Fatalf("cluster-scoped RBAC must render no manager Roles, got %v", slices.Collect(maps.Keys(roles)))
	}
}
