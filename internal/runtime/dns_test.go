package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"openpass/internal/model"
)

func TestRouterDNSRedirectsBeforePlaintextLeakGuard(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	rt := &Runtime{NFTPath: filepath.Join(t.TempDir(), "dns.nft")}
	st := model.State{Settings: model.DefaultSettings()}
	st.Settings.Enabled = true
	if err := rt.writeFirewall(st); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(rt.NFTPath)
	if err != nil {
		t.Fatal(err)
	}
	policy := string(b)
	for _, rule := range []string{
		"openpass_prerouting iifname \"br-lan\" meta nfproto ipv6 udp dport 53 drop",
		"openpass_prerouting iifname \"br-lan\" meta nfproto ipv6 tcp dport 53 drop",
		"flush chain inet fw4 openpass_dns_output",
		"openpass_dns_output meta nfproto ipv4 udp dport 53 redirect to :1053",
		"openpass_dns_output meta nfproto ipv4 tcp dport 53 redirect to :1053",
		"openpass_dns_output meta nfproto ipv6 udp dport 53 redirect to :1054",
		"openpass_dns_output meta nfproto ipv6 tcp dport 53 redirect to :1054",
		"openpass_output udp dport 53 drop",
		"openpass_output tcp dport 53 drop",
	} {
		if !strings.Contains(policy, rule) {
			t.Errorf("missing DNS enforcement: %s", rule)
		}
	}
	if strings.Index(policy, "meta nfproto ipv6 udp dport 53 drop") > strings.Index(policy, "fib daddr type local accept") {
		t.Fatal("IPv6 DNS drop must precede local-destination exception")
	}
	if strings.Contains(policy, "443 redirect") {
		t.Fatal("DoH upstream must not recurse into DNS redirect")
	}
	st.Settings.Enabled = false
	if err := rt.writeFirewall(st); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(rt.NFTPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "redirect to") {
		t.Fatal("disabled OpenPass must release router DNS")
	}
}
