package dependency

import (
	"encoding/json"
	"net/netip"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

func TestNetworkPoliciesDenyUnownedTrafficAndPreserveIngress(t *testing.T) {
	policies := networkPolicies(t, NetworkObjects("test-run", "run-a", nil, false))
	deny := policies[0]
	if deny.Name != "cf-egress-default-deny" || len(deny.Spec.Egress) != 0 || !selectorMatches(t, deny.Spec.PodSelector, nil) {
		t.Fatal("namespace-wide default egress deny missing")
	}
	for _, policy := range policies {
		if policy.Namespace != "test-run" || policy.Labels["cloudforge.dev/run-id"] != "run-a" || policy.Labels["app.kubernetes.io/managed-by"] != "cloudforge" {
			t.Fatal("policy ownership missing")
		}
		if policy.Kind != "NetworkPolicy" || policy.APIVersion != "networking.k8s.io/v1" || !reflect.DeepEqual(policy.Spec.PolicyTypes, []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}) || len(policy.Spec.Ingress) != 0 {
			t.Fatal("unexpected resource kind or ingress mutation")
		}
		if policy.Name != deny.Name && selectorMatches(t, policy.Spec.PodSelector, nil) {
			t.Fatal("unowned pod selected by an allow policy")
		}
	}
	for _, destination := range []networkDestination{
		{namespace: "kube-system", pod: map[string]string{"k8s-app": "kube-dns"}, port: 53, protocol: corev1.ProtocolUDP},
		{ip: "1.1.1.1", port: 443, protocol: corev1.ProtocolTCP},
	} {
		if permitsNetworkTraffic(t, policies, nil, destination) {
			t.Fatal("unowned source has allowed egress")
		}
	}
}

func TestNetworkDNSRequiresSystemResolverAndDNSPorts(t *testing.T) {
	policies := networkPolicies(t, NetworkObjects("test-run", "run-a", nil, true))
	source := networkOwner("run-a")
	source["cloudforge.dev/role"] = "application"
	for _, test := range []struct {
		name, namespace, app string
		port                 int32
		protocol             corev1.Protocol
		want                 bool
	}{
		{"udp resolver", "kube-system", "kube-dns", 53, corev1.ProtocolUDP, true},
		{"tcp resolver", "kube-system", "kube-dns", 53, corev1.ProtocolTCP, true},
		{"wrong namespace", "other", "kube-dns", 53, corev1.ProtocolUDP, false},
		{"wrong pod", "kube-system", "other", 53, corev1.ProtocolUDP, false},
		{"resolver http", "kube-system", "kube-dns", 80, corev1.ProtocolTCP, false},
		{"resolver metrics", "kube-system", "kube-dns", 9153, corev1.ProtocolTCP, false},
		{"wrong protocol", "kube-system", "kube-dns", 53, corev1.ProtocolSCTP, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			destination := networkDestination{namespace: test.namespace, pod: map[string]string{"k8s-app": test.app}, port: test.port, protocol: test.protocol}
			if got := permitsNetworkTraffic(t, policies, source, destination); got != test.want {
				t.Fatalf("DNS permission = %v, want %v", got, test.want)
			}
		})
	}
	if permitsNetworkTraffic(t, policies, source, networkDestination{ip: "8.8.8.8", port: 53, protocol: corev1.ProtocolUDP}) {
		t.Fatal("external DNS is allowed")
	}
}

func TestNetworkProviderTrafficRequiresMatchingRoleRunNamespaceAndPort(t *testing.T) {
	policies := networkPolicies(t, NetworkObjects("test-run", "run-a", []string{"redis", "postgresql", "clamav"}, false))
	for _, provider := range []struct {
		name string
		port int32
	}{{"redis", 6379}, {"postgresql", 5432}, {"clamav", 3310}} {
		for _, role := range []string{"application", "preparation"} {
			t.Run(provider.name+"/"+role, func(t *testing.T) {
				source := networkOwner("run-a")
				source["cloudforge.dev/role"] = role
				destination := networkDestination{namespace: "test-run", pod: networkOwner("run-a"), port: provider.port, protocol: corev1.ProtocolTCP}
				destination.pod["cloudforge.dev/dependency"] = provider.name
				if !permitsNetworkTraffic(t, policies, source, destination) {
					t.Fatal("enabled provider is not reachable")
				}
				for _, change := range []struct {
					name string
					edit func(map[string]string, *networkDestination)
				}{
					{"source run", func(s map[string]string, _ *networkDestination) { s["cloudforge.dev/run-id"] = "run-b" }},
					{"source owner", func(s map[string]string, _ *networkDestination) { s["app.kubernetes.io/managed-by"] = "other" }},
					{"source role", func(s map[string]string, _ *networkDestination) { s["cloudforge.dev/role"] = "other" }},
					{"destination run", func(_ map[string]string, d *networkDestination) { d.pod["cloudforge.dev/run-id"] = "run-b" }},
					{"destination owner", func(_ map[string]string, d *networkDestination) { d.pod["app.kubernetes.io/managed-by"] = "other" }},
					{"destination namespace", func(_ map[string]string, d *networkDestination) { d.namespace = "other" }},
					{"destination provider", func(_ map[string]string, d *networkDestination) { d.pod["cloudforge.dev/dependency"] = "other" }},
					{"port", func(_ map[string]string, d *networkDestination) { d.port = 8080 }},
					{"protocol", func(_ map[string]string, d *networkDestination) { d.protocol = corev1.ProtocolUDP }},
				} {
					t.Run(change.name, func(t *testing.T) {
						s := networkOwner("run-a")
						s["cloudforge.dev/role"] = role
						d := destination
						d.pod = networkOwner("run-a")
						d.pod["cloudforge.dev/dependency"] = provider.name
						change.edit(s, &d)
						if permitsNetworkTraffic(t, policies, s, d) {
							t.Fatal("provider permission escaped intended scope")
						}
					})
				}
			})
		}
	}
	policies = networkPolicies(t, NetworkObjects("test-run", "run-a", []string{"redis", "unknown"}, false))
	source, destination := networkOwner("run-a"), networkOwner("run-a")
	source["cloudforge.dev/role"] = "application"
	destination["cloudforge.dev/dependency"] = "postgresql"
	if permitsNetworkTraffic(t, policies, source, networkDestination{namespace: "test-run", pod: destination, port: 5432, protocol: corev1.ProtocolTCP}) {
		t.Fatal("disabled provider is reachable")
	}
}

func TestNetworkSignatureDownloadsArePublicAndClamAVOnly(t *testing.T) {
	policies := networkPolicies(t, NetworkObjects("test-run", "run-a", []string{"redis", "postgresql", "clamav"}, true))
	clamav := networkOwner("run-a")
	clamav["cloudforge.dev/dependency"] = "clamav"
	for _, address := range []string{"1.1.1.1", "93.184.216.34", "2606:4700:4700::1111", "2001:4860:4860::8888"} {
		for _, port := range []int32{80, 443} {
			if !permitsNetworkTraffic(t, policies, clamav, networkDestination{ip: address, port: port, protocol: corev1.ProtocolTCP}) {
				t.Fatalf("public signature HTTP(S) denied: %s:%d", address, port)
			}
		}
	}
	for _, address := range []string{
		"0.0.0.0", "10.0.0.1", "100.100.100.200", "127.0.0.1", "169.254.169.254", "168.63.129.16",
		"172.16.0.1", "192.0.0.1", "192.0.2.1", "192.88.99.1", "192.168.1.1", "198.18.0.1", "198.51.100.1",
		"203.0.113.1", "224.0.0.1", "239.255.255.255", "240.0.0.1", "255.255.255.255",
		"::", "::1", "::ffff:127.0.0.1", "64:ff9b::a00:1", "64:ff9b:1::1", "100::1", "2001::1", "2001:db8::1",
		"2002:a00:1::1", "3fff::1", "fc00::1", "fd00:ec2::254", "fe80::1", "ff02::1",
	} {
		if permitsNetworkTraffic(t, policies, clamav, networkDestination{ip: address, port: 443, protocol: corev1.ProtocolTCP}) {
			t.Errorf("private/special destination allowed: %s", address)
		}
	}
	for _, provider := range []string{"redis", "postgresql", "unknown"} {
		source := networkOwner("run-a")
		source["cloudforge.dev/dependency"] = provider
		if permitsNetworkTraffic(t, policies, source, networkDestination{ip: "1.1.1.1", port: 443, protocol: corev1.ProtocolTCP}) {
			t.Fatalf("non-ClamAV provider has internet access: %s", provider)
		}
	}
	for _, role := range []string{"application", "preparation"} {
		source := networkOwner("run-a")
		source["cloudforge.dev/role"] = role
		if permitsNetworkTraffic(t, policies, source, networkDestination{ip: "1.1.1.1", port: 443, protocol: corev1.ProtocolTCP}) {
			t.Fatalf("%s has internet access", role)
		}
	}
	for _, destination := range []networkDestination{{ip: "1.1.1.1", port: 22, protocol: corev1.ProtocolTCP}, {ip: "1.1.1.1", port: 443, protocol: corev1.ProtocolUDP}} {
		if permitsNetworkTraffic(t, policies, clamav, destination) {
			t.Fatal("signature allowance extends beyond TCP HTTP(S)")
		}
	}
}

func TestNetworkRevocationRetainsStableDenyPolicy(t *testing.T) {
	before := networkPolicies(t, NetworkObjects("test-run", "run-a", []string{"clamav"}, true))
	for _, providers := range [][]string{{"clamav"}, nil} {
		after := networkPolicies(t, NetworkObjects("test-run", "run-a", providers, false))
		if before[3].Name != after[3].Name || before[3].Namespace != after[3].Namespace || !reflect.DeepEqual(before[3].Spec.PodSelector, after[3].Spec.PodSelector) || len(after[3].Spec.Egress) != 0 {
			t.Fatal("reapply would not replace the previous signature allowance")
		}
		clamav := networkOwner("run-a")
		clamav["cloudforge.dev/dependency"] = "clamav"
		if permitsNetworkTraffic(t, after, clamav, networkDestination{ip: "1.1.1.1", port: 443, protocol: corev1.ProtocolTCP}) {
			t.Fatal("public egress survives revocation")
		}
	}
	absent := networkPolicies(t, NetworkObjects("test-run", "run-a", nil, true))
	if len(absent[3].Spec.Egress) != 0 {
		t.Fatal("signature flag opened egress without the ClamAV provider")
	}
	a, err := json.Marshal(NetworkObjects("test-run", "run-a", []string{"postgresql", "redis", "clamav"}, true))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(NetworkObjects("test-run", "run-a", []string{"clamav", "redis", "postgresql", "redis", "unknown"}, true))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("equivalent provider sets do not serialize deterministically")
	}
}

func networkPolicies(t *testing.T, objects []any) []*networkingv1.NetworkPolicy {
	t.Helper()
	if len(objects) != 4 {
		t.Fatalf("got %d network policies, want four", len(objects))
	}
	policies := make([]*networkingv1.NetworkPolicy, 0, len(objects))
	for _, object := range objects {
		policy, ok := object.(*networkingv1.NetworkPolicy)
		if !ok {
			t.Fatal("resource is not an official NetworkPolicy type")
		}
		policies = append(policies, policy)
	}
	return policies
}

func networkOwner(runID string) map[string]string {
	return map[string]string{"app.kubernetes.io/managed-by": "cloudforge", "cloudforge.dev/run-id": runID}
}

type networkDestination struct {
	namespace string
	pod       map[string]string
	ip        string
	port      int32
	protocol  corev1.Protocol
}

// Evaluate the generated selectors, peers and ports as an allow-list. This does
// not replace the runtime CNI-enforcement fixture; it catches additive policy
// rules accidentally granting another role, run, namespace, protocol or address.
func permitsNetworkTraffic(t *testing.T, policies []*networkingv1.NetworkPolicy, source map[string]string, destination networkDestination) bool {
	t.Helper()
	for _, policy := range policies {
		if !selectorMatches(t, policy.Spec.PodSelector, source) {
			continue
		}
		for _, rule := range policy.Spec.Egress {
			portAllowed := len(rule.Ports) == 0
			for _, port := range rule.Ports {
				if port.Protocol != nil && *port.Protocol == destination.protocol && port.Port != nil && port.Port.IntVal == destination.port {
					portAllowed = true
				}
			}
			if !portAllowed {
				continue
			}
			if len(rule.To) == 0 {
				return true
			}
			for _, peer := range rule.To {
				if peer.IPBlock != nil {
					address, err := netip.ParseAddr(destination.ip)
					if err != nil || !netip.MustParsePrefix(peer.IPBlock.CIDR).Contains(address) {
						continue
					}
					excluded := false
					for _, prefix := range peer.IPBlock.Except {
						if netip.MustParsePrefix(prefix).Contains(address) {
							excluded = true
						}
					}
					if !excluded {
						return true
					}
					continue
				}
				if destination.namespace == "" {
					continue
				}
				if peer.NamespaceSelector == nil {
					if destination.namespace != policy.Namespace {
						continue
					}
				} else if !selectorMatches(t, *peer.NamespaceSelector, map[string]string{"kubernetes.io/metadata.name": destination.namespace}) {
					continue
				}
				if peer.PodSelector == nil || selectorMatches(t, *peer.PodSelector, destination.pod) {
					return true
				}
			}
		}
	}
	return false
}

func selectorMatches(t *testing.T, selector metav1.LabelSelector, values map[string]string) bool {
	t.Helper()
	parsed, err := metav1.LabelSelectorAsSelector(&selector)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Matches(labels.Set(values))
}
