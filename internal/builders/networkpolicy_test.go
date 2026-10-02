package builders

import (
	"testing"

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
