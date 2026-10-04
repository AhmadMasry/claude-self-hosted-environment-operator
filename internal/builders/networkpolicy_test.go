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
	"net/netip"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/util/intstr"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

func TestEnvironmentNetworkPolicy(t *testing.T) {
	env := testEnv()
	env.Spec.Runner.NetworkPolicy = &selfhostedv1alpha1.NetworkPolicySpec{Enabled: true, EgressCIDRs: []string{"169.254.0.0/16", "10.0.0.0/8"}}
	np := EnvironmentNetworkPolicy(env)
	if np.Name != testEnvName+"-egress" || np.Namespace != testNamespace || np.Kind != "NetworkPolicy" {
		t.Fatalf("identity wrong: %+v", np.ObjectMeta)
	}
	if np.Spec.PodSelector.MatchLabels[selfhostedv1alpha1.LabelEnvironment] != testEnvName || len(np.Spec.PodSelector.MatchLabels) != 1 {
		t.Fatalf("must select every pod of the environment (runner and orchestrator): %v", np.Spec.PodSelector)
	}
	if len(np.Spec.PolicyTypes) != 1 || np.Spec.PolicyTypes[0] != networkingv1.PolicyTypeEgress {
		t.Fatal("egress-only policy expected")
	}
	if len(np.Spec.Egress) != 2 {
		t.Fatalf("expected a DNS rule and an HTTPS rule, got %d", len(np.Spec.Egress))
	}
	dns := np.Spec.Egress[0]
	if len(dns.Ports) != 2 || *dns.Ports[0].Port != intstr.FromInt32(53) || *dns.Ports[0].Protocol != "UDP" || *dns.Ports[1].Protocol != "TCP" {
		t.Fatalf("DNS rule wrong: %+v", dns.Ports)
	}
	if dns.To[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "kube-system" || dns.To[0].PodSelector.MatchLabels["k8s-app"] != "kube-dns" {
		t.Fatalf("DNS peer wrong: %+v", dns.To[0])
	}
	https := np.Spec.Egress[1]
	if len(https.To) != 2 || *https.Ports[0].Port != intstr.FromInt32(443) {
		t.Fatalf("HTTPS rule wrong: %+v", https)
	}
	if ex := https.To[0].IPBlock.Except; len(ex) != 1 || ex[0] != "169.254.169.254/32" {
		t.Fatalf("a CIDR containing the metadata endpoint must exclude it: %+v", https.To[0].IPBlock)
	}
	if ex := https.To[1].IPBlock.Except; len(ex) != 0 {
		t.Fatalf("a CIDR not containing the metadata endpoint must have no except (the API server rejects it): %+v", https.To[1].IPBlock)
	}
}

func TestEnvironmentNetworkPolicyAppendsAdditionalEgress(t *testing.T) {
	env := testEnv()
	tcp := corev1.ProtocolTCP
	sink := networkingv1.NetworkPolicyEgressRule{
		Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: new(intstr.FromInt32(8080))}},
		To:    []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "sink"}}}},
	}
	metadata := networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{
			{IPBlock: &networkingv1.IPBlock{CIDR: "169.254.0.0/16", Except: []string{"169.254.1.0/24"}}},
			{IPBlock: &networkingv1.IPBlock{CIDR: "0.0.0.0/0", Except: []string{metadataEndpointCIDR}}},
			{IPBlock: &networkingv1.IPBlock{CIDR: "10.0.0.0/8"}},
		},
	}
	env.Spec.Runner.NetworkPolicy = &selfhostedv1alpha1.NetworkPolicySpec{Enabled: true, AdditionalEgress: []networkingv1.NetworkPolicyEgressRule{sink, metadata}}
	np := EnvironmentNetworkPolicy(env)
	if len(np.Spec.Egress) != 3 {
		t.Fatalf("expected DNS plus the two additional rules, got %d", len(np.Spec.Egress))
	}
	if !reflect.DeepEqual(np.Spec.Egress[1], sink) {
		t.Fatalf("selector rule must be appended as given: %+v", np.Spec.Egress[1])
	}
	got := np.Spec.Egress[2].To
	if want := []string{"169.254.1.0/24", metadataEndpointCIDR}; !reflect.DeepEqual(got[0].IPBlock.Except, want) {
		t.Fatalf("metadata exclusion must be added to an ipBlock covering it: %v", got[0].IPBlock.Except)
	}
	if want := []string{metadataEndpointCIDR}; !reflect.DeepEqual(got[1].IPBlock.Except, want) {
		t.Fatalf("an existing exclusion must not be duplicated: %v", got[1].IPBlock.Except)
	}
	if len(got[2].IPBlock.Except) != 0 {
		t.Fatalf("an ipBlock not covering the endpoint must stay as given: %v", got[2].IPBlock.Except)
	}
	if len(env.Spec.Runner.NetworkPolicy.AdditionalEgress[1].To[0].IPBlock.Except) != 1 {
		t.Fatal("the environment's own rules must not be modified")
	}
}

func TestEnvironmentNetworkPolicyNoCIDRsIsDNSOnly(t *testing.T) {
	env := testEnv()
	env.Spec.Runner.NetworkPolicy = &selfhostedv1alpha1.NetworkPolicySpec{Enabled: true}
	np := EnvironmentNetworkPolicy(env)
	if len(np.Spec.Egress) != 1 || np.Spec.Egress[0].Ports[0].Port.IntVal != 53 {
		t.Fatalf("with no CIDRs only DNS may be allowed, got %+v", np.Spec.Egress)
	}
}

func TestAPIServerNetworkPolicy(t *testing.T) {
	env := testEnv()
	//nolint:staticcheck // see APIServerNetworkPolicy
	ep := &corev1.Endpoints{Subsets: []corev1.EndpointSubset{{
		Addresses: []corev1.EndpointAddress{{IP: "172.18.0.2"}, {IP: "172.18.0.3"}},
		Ports:     []corev1.EndpointPort{{Port: 6443}},
	}}}
	np := APIServerNetworkPolicy(env, ep)
	if np.Name != testEnvName+"-egress-apiserver" || np.Namespace != testNamespace || np.Kind != "NetworkPolicy" {
		t.Fatalf("identity wrong: %+v", np.ObjectMeta)
	}
	if len(np.Labels) != 2 || np.Labels[selfhostedv1alpha1.LabelEnvironment] != testEnvName || np.Labels[selfhostedv1alpha1.LabelPartOf] != selfhostedv1alpha1.PartOfValue {
		t.Fatalf("labels wrong: %v", np.Labels)
	}
	sel := np.Spec.PodSelector.MatchLabels
	if len(sel) != 2 || sel[selfhostedv1alpha1.LabelEnvironment] != testEnvName || sel[selfhostedv1alpha1.LabelRole] != selfhostedv1alpha1.RoleOrchestrator {
		t.Fatalf("must select only the orchestrator pod: %v", sel)
	}
	if len(np.Spec.PolicyTypes) != 1 || np.Spec.PolicyTypes[0] != networkingv1.PolicyTypeEgress || len(np.Spec.Egress) != 1 {
		t.Fatalf("one egress rule expected: %+v", np.Spec)
	}
	rule := np.Spec.Egress[0]
	if len(rule.To) != 2 || rule.To[0].IPBlock.CIDR != "172.18.0.2/32" || rule.To[1].IPBlock.CIDR != "172.18.0.3/32" {
		t.Fatalf("addresses wrong: %+v", rule.To)
	}
	if len(rule.Ports) != 1 || rule.Ports[0].Port.IntVal != 6443 || *rule.Ports[0].Protocol != corev1.ProtocolTCP {
		t.Fatalf("ports wrong: %+v", rule.Ports)
	}
}

func TestAPIServerNetworkPolicyUnionsPortsAcrossSubsets(t *testing.T) {
	//nolint:staticcheck // see APIServerNetworkPolicy
	ep := &corev1.Endpoints{Subsets: []corev1.EndpointSubset{
		{Addresses: []corev1.EndpointAddress{{IP: "10.0.0.1"}}, Ports: []corev1.EndpointPort{{Port: 6443}}},
		{Addresses: []corev1.EndpointAddress{{IP: "10.0.0.2"}}, Ports: []corev1.EndpointPort{{Port: 443}, {Port: 6443}}},
	}}
	rule := APIServerNetworkPolicy(testEnv(), ep).Spec.Egress[0]
	if len(rule.To) != 2 || len(rule.Ports) != 2 || rule.Ports[0].Port.IntVal != 6443 || rule.Ports[1].Port.IntVal != 443 {
		t.Fatalf("expected both addresses and the union of ports, got %+v", rule)
	}
}

func TestAPIServerNetworkPolicyFromAddrPorts(t *testing.T) {
	env := testEnv()
	np := APIServerNetworkPolicyFromAddrPorts(env, []netip.AddrPort{
		netip.MustParseAddrPort("10.0.0.1:6443"),
		netip.MustParseAddrPort("10.0.0.1:443"),
		netip.MustParseAddrPort("[fd00::1]:6443"),
	})
	//nolint:staticcheck // see APIServerNetworkPolicy
	fromEndpoints := APIServerNetworkPolicy(env, &corev1.Endpoints{Subsets: []corev1.EndpointSubset{{
		Addresses: []corev1.EndpointAddress{{IP: "10.0.0.1"}},
		Ports:     []corev1.EndpointPort{{Port: 6443}},
	}}})
	if np.Name != fromEndpoints.Name || !equality.Semantic.DeepEqual(np.Spec.PodSelector, fromEndpoints.Spec.PodSelector) || !equality.Semantic.DeepEqual(np.Labels, fromEndpoints.Labels) {
		t.Fatalf("both sources must build the same policy shape: %+v vs %+v", np.ObjectMeta, fromEndpoints.ObjectMeta)
	}
	rule := np.Spec.Egress[0]
	if len(rule.To) != 2 || rule.To[0].IPBlock.CIDR != "10.0.0.1/32" || rule.To[1].IPBlock.CIDR != "fd00::1/128" {
		t.Fatalf("each address once, as a host block: %+v", rule.To)
	}
	if len(rule.Ports) != 2 || rule.Ports[0].Port.IntVal != 6443 || rule.Ports[1].Port.IntVal != 443 || *rule.Ports[0].Protocol != corev1.ProtocolTCP {
		t.Fatalf("each port once, TCP: %+v", rule.Ports)
	}
}
