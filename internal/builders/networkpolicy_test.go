package builders

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
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
