package types

import "testing"

func TestAccountPasswordServerURLIsTrusted(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		trusted bool
	}{
		{name: "public https", url: "https://headscale.example.com", trusted: true},
		{name: "localhost", url: "http://localhost:8080", trusted: true},
		{name: "IPv4 loopback", url: "http://127.0.0.1:8080", trusted: true},
		{name: "IPv6 loopback", url: "http://[::1]:8080", trusted: true},
		{name: "public hostname", url: "http://headscale.example.com", trusted: false},
		{name: "private address", url: "http://10.0.0.10:8080", trusted: false},
		{name: "missing host", url: "http://", trusted: false},
		{name: "path", url: "https://headscale.example.com/control", trusted: false},
		{name: "query", url: "https://headscale.example.com/?next=x", trusted: false},
		{name: "userinfo", url: "https://user:pass@headscale.example.com", trusted: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := AccountPasswordServerURLIsTrusted(test.url); got != test.trusted {
				t.Fatalf("AccountPasswordServerURLIsTrusted(%q) = %t, want %t", test.url, got, test.trusted)
			}
		})
	}
}
