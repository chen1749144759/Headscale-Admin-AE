package hscontrol

import (
	"testing"

	"github.com/juanfont/headscale/hscontrol/types"
)

func TestNormalizeRuntimeDNSConfig(t *testing.T) {
	t.Parallel()

	config, err := normalizeRuntimeDNSConfig(types.RuntimeDNSConfig{
		MagicDNS:          true,
		OverrideLocalDNS:  true,
		GlobalNameservers: []string{" 1.1.1.1 ", "https://dns.example.com/dns-query", "1.1.1.1"},
		SearchDomains:     []string{"Corp.Example.com", "corp.example.com."},
	})
	if err != nil {
		t.Fatalf("normalizeRuntimeDNSConfig() error = %v", err)
	}
	if got, want := len(config.GlobalNameservers), 2; got != want {
		t.Fatalf("global nameserver count = %d, want %d", got, want)
	}
	if got, want := len(config.SearchDomains), 1; got != want {
		t.Fatalf("search domain count = %d, want %d", got, want)
	}
	if got, want := config.SearchDomains[0], "corp.example.com"; got != want {
		t.Fatalf("search domain = %q, want %q", got, want)
	}
}

func TestNormalizeRuntimeDNSConfigRejectsInsecureDoH(t *testing.T) {
	t.Parallel()

	_, err := normalizeRuntimeDNSConfig(types.RuntimeDNSConfig{
		OverrideLocalDNS:  true,
		GlobalNameservers: []string{"http://dns.example.com/dns-query"},
	})
	if err == nil {
		t.Fatal("normalizeRuntimeDNSConfig() accepted an insecure remote resolver")
	}
}
