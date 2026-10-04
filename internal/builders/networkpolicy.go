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
	"net"
	"net/netip"
	"slices"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

const metadataEndpointCIDR = "169.254.169.254/32"

// containsMetadataEndpoint reports whether cidr covers the cloud metadata
// address. The API server rejects an except block outside its cidr, so the
// exclusion is added only where it is valid. CIDR syntax is validated by CEL.
func containsMetadataEndpoint(cidr string) bool {
	_, block, err := net.ParseCIDR(cidr)
	return err == nil && block.Contains(net.ParseIP("169.254.169.254"))
}

// NetworkPolicyName names the per-environment egress policy.
func NetworkPolicyName(env *selfhostedv1alpha1.ClaudeEnvironment) string {
	return env.Name + "-egress"
}

// EnvironmentNetworkPolicy is a default-deny egress policy for every pod of
// the environment: DNS to kube-dns, TCP 443 to the user's CIDRs with the
// cloud metadata endpoint always excluded, then the user's additionalEgress
// rules as given, with the same exclusion applied to their ipBlocks. The
// user supplies the CIDRs for api.anthropic.com and the git host; hostnames
// cannot be expressed here.
func EnvironmentNetworkPolicy(env *selfhostedv1alpha1.ClaudeEnvironment) *networkingv1.NetworkPolicy {
	udp, tcp := corev1.ProtocolUDP, corev1.ProtocolTCP
	dns := networkingv1.NetworkPolicyEgressRule{
		Ports: []networkingv1.NetworkPolicyPort{
			{Protocol: &udp, Port: new(intstr.FromInt32(53))},
			{Protocol: &tcp, Port: new(intstr.FromInt32(53))},
		},
		To: []networkingv1.NetworkPolicyPeer{{
			NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"}},
			PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}},
		}},
	}
	egress := []networkingv1.NetworkPolicyEgressRule{dns}
	if np := env.Spec.Runner.NetworkPolicy; np != nil && len(np.EgressCIDRs) > 0 {
		https := networkingv1.NetworkPolicyEgressRule{Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: new(intstr.FromInt32(443))}}}
		for _, cidr := range np.EgressCIDRs {
			block := &networkingv1.IPBlock{CIDR: cidr}
			if containsMetadataEndpoint(cidr) {
				block.Except = []string{metadataEndpointCIDR}
			}
			https.To = append(https.To, networkingv1.NetworkPolicyPeer{IPBlock: block})
		}
		egress = append(egress, https)
	}
	if np := env.Spec.Runner.NetworkPolicy; np != nil {
		for _, rule := range np.AdditionalEgress {
			rule := *rule.DeepCopy()
			for i := range rule.To {
				if block := rule.To[i].IPBlock; block != nil && containsMetadataEndpoint(block.CIDR) && !slices.Contains(block.Except, metadataEndpointCIDR) {
					block.Except = append(block.Except, metadataEndpointCIDR)
				}
			}
			egress = append(egress, rule)
		}
	}
	labels := map[string]string{selfhostedv1alpha1.LabelEnvironment: env.Name, selfhostedv1alpha1.LabelPartOf: selfhostedv1alpha1.PartOfValue}
	return &networkingv1.NetworkPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"},
		ObjectMeta: metav1.ObjectMeta{Name: NetworkPolicyName(env), Namespace: env.Namespace, Labels: labels},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{selfhostedv1alpha1.LabelEnvironment: env.Name}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress:      egress,
		},
	}
}

// APIServerNetworkPolicyName names the orchestrator's API server egress policy.
func APIServerNetworkPolicyName(env *selfhostedv1alpha1.ClaudeEnvironment) string {
	return env.Name + "-egress-apiserver"
}

// APIServerNetworkPolicy lets the orchestrator pod reach the Kubernetes API
// server, whose addresses and ports come from the default/kubernetes
// Endpoints, so its spawn hook can create work-order Secrets and ClaudeRunners
// under the default-deny policy. NetworkPolicy cannot name a Service, so the
// endpoint addresses are used as host blocks.
func APIServerNetworkPolicy(env *selfhostedv1alpha1.ClaudeEnvironment, endpoints *corev1.Endpoints) *networkingv1.NetworkPolicy { //nolint:staticcheck // Endpoints is the only API read with get on a single named object; default/kubernetes is still maintained
	var addrs []netip.Addr
	var ports []uint16
	for _, subset := range endpoints.Subsets {
		for _, a := range subset.Addresses {
			if ip, err := netip.ParseAddr(a.IP); err == nil {
				addrs = append(addrs, ip)
			}
		}
		for _, p := range subset.Ports {
			ports = append(ports, uint16(p.Port))
		}
	}
	return apiServerNetworkPolicy(env, addrs, ports)
}

// APIServerNetworkPolicyFromAddrPorts builds the same policy from a configured
// address list (--apiserver-endpoints) instead of the Endpoints object, for
// installs whose RBAC cannot read the default namespace. The rule allows every
// listed port to every listed address.
func APIServerNetworkPolicyFromAddrPorts(env *selfhostedv1alpha1.ClaudeEnvironment, endpoints []netip.AddrPort) *networkingv1.NetworkPolicy {
	addrs := make([]netip.Addr, 0, len(endpoints))
	ports := make([]uint16, 0, len(endpoints))
	for _, ep := range endpoints {
		addrs = append(addrs, ep.Addr())
		ports = append(ports, ep.Port())
	}
	return apiServerNetworkPolicy(env, addrs, ports)
}

func apiServerNetworkPolicy(env *selfhostedv1alpha1.ClaudeEnvironment, addrs []netip.Addr, ports []uint16) *networkingv1.NetworkPolicy {
	tcp := corev1.ProtocolTCP
	rule := networkingv1.NetworkPolicyEgressRule{}
	seenAddr := map[netip.Addr]bool{}
	for _, a := range addrs {
		a = a.Unmap()
		if !seenAddr[a] {
			seenAddr[a] = true
			rule.To = append(rule.To, networkingv1.NetworkPolicyPeer{IPBlock: &networkingv1.IPBlock{CIDR: netip.PrefixFrom(a, a.BitLen()).String()}})
		}
	}
	seenPort := map[uint16]bool{}
	for _, p := range ports {
		if !seenPort[p] {
			seenPort[p] = true
			rule.Ports = append(rule.Ports, networkingv1.NetworkPolicyPort{Protocol: &tcp, Port: new(intstr.FromInt32(int32(p)))})
		}
	}
	labels := map[string]string{selfhostedv1alpha1.LabelEnvironment: env.Name, selfhostedv1alpha1.LabelPartOf: selfhostedv1alpha1.PartOfValue}
	return &networkingv1.NetworkPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"},
		ObjectMeta: metav1.ObjectMeta{Name: APIServerNetworkPolicyName(env), Namespace: env.Namespace, Labels: labels},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: OrchestratorSelectorLabels(env)},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress:      []networkingv1.NetworkPolicyEgressRule{rule},
		},
	}
}
