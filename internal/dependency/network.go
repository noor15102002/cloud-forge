package dependency

import (
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// NetworkObjects isolates the disposable namespace before any application or
// preparation container starts. Only explicitly enabled providers are reachable.
// The fixed signature-policy name is also emitted when downloads are disabled so
// applying the returned objects revokes the previous public HTTP(S) allowance.
// Enforcement requires a NetworkPolicy-capable CNI; these objects alone do not
// establish that the runtime has enforced isolation.
func NetworkObjects(namespace, runID string, providers []string, allowSignatureDownloads bool) []any {
	owned := func() map[string]string {
		return map[string]string{
			"app.kubernetes.io/managed-by": "cloudforge",
			"cloudforge.dev/run-id":        runID,
		}
	}
	policy := func(name string, selector metav1.LabelSelector, egress []networkingv1.NetworkPolicyEgressRule) *networkingv1.NetworkPolicy {
		return &networkingv1.NetworkPolicy{
			TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"},
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: owned()},
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: selector,
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
				Egress:      egress,
			},
		}
	}

	defaultDeny := policy("cf-egress-default-deny", metav1.LabelSelector{}, nil)
	dns := policy("cf-egress-dns", metav1.LabelSelector{MatchLabels: owned()}, []networkingv1.NetworkPolicyEgressRule{{
		To: []networkingv1.NetworkPolicyPeer{{
			NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"}},
			PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}},
		}},
		Ports: []networkingv1.NetworkPolicyPort{networkPort(53, corev1.ProtocolUDP), networkPort(53, corev1.ProtocolTCP)},
	}})

	enabled := make(map[string]bool, len(providers))
	for _, provider := range providers {
		enabled[provider] = true
	}
	var providerRules []networkingv1.NetworkPolicyEgressRule
	// Fixed order gives equivalent configuration deterministic serialization.
	for _, provider := range []struct {
		name string
		port int32
	}{{"redis", 6379}, {"postgresql", 5432}, {"clamav", 3310}} {
		if !enabled[provider.name] {
			continue
		}
		labels := owned()
		labels["cloudforge.dev/dependency"] = provider.name
		providerRules = append(providerRules, networkingv1.NetworkPolicyEgressRule{
			// A pod selector without a namespace selector is confined to this
			// policy's namespace, even if another namespace copies the labels.
			To:    []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{MatchLabels: labels}}},
			Ports: []networkingv1.NetworkPolicyPort{networkPort(provider.port, corev1.ProtocolTCP)},
		})
	}
	application := policy("cf-egress-providers", metav1.LabelSelector{
		MatchLabels: owned(),
		MatchExpressions: []metav1.LabelSelectorRequirement{{
			Key: "cloudforge.dev/role", Operator: metav1.LabelSelectorOpIn, Values: []string{"application", "preparation"},
		}},
	}, providerRules)

	clamavLabels := owned()
	clamavLabels["cloudforge.dev/dependency"] = "clamav"
	var downloadRules []networkingv1.NetworkPolicyEgressRule
	if allowSignatureDownloads && enabled["clamav"] {
		downloadRules = []networkingv1.NetworkPolicyEgressRule{{
			To: []networkingv1.NetworkPolicyPeer{
				{IPBlock: &networkingv1.IPBlock{CIDR: "0.0.0.0/0", Except: []string{
					"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
					"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24",
					"192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24",
					"203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
					// Azure's platform virtual IP is public-addressed metadata;
					// link-local and shared-address exclusions cover other common
					// metadata endpoints without giving providers LAN access.
					"168.63.129.16/32",
				}}},
				{IPBlock: &networkingv1.IPBlock{CIDR: "2000::/3", Except: []string{
					// Only global unicast: excludes loopback, link-local, ULA,
					// multicast, mapped IPv4 and NAT64 translation prefixes.
					// Exclude special allocations, documentation and 6to4 too.
					"2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20",
				}}},
			},
			Ports: []networkingv1.NetworkPolicyPort{networkPort(80, corev1.ProtocolTCP), networkPort(443, corev1.ProtocolTCP)},
		}}
	}
	signatures := policy("cf-egress-clamav-signatures", metav1.LabelSelector{MatchLabels: clamavLabels}, downloadRules)
	return []any{defaultDeny, dns, application, signatures}
}

func networkPort(port int32, protocol corev1.Protocol) networkingv1.NetworkPolicyPort {
	value := intstr.FromInt32(port)
	return networkingv1.NetworkPolicyPort{Protocol: &protocol, Port: &value}
}
