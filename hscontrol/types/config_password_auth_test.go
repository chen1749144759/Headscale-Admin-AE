package types

import "testing"

func TestAccountPasswordServerURLIsValid(t *testing.T) {
	tests := []struct {
		name  string
		url   string
		valid bool
	}{
		{name: "public HTTPS domain", url: "https://headscale.example.com", valid: true},
		{name: "public HTTP domain", url: "http://headscale.example.com:8080", valid: true},
		{name: "public HTTP IPv4", url: "http://192.0.2.10:8080", valid: true},
		{name: "private HTTP IPv4", url: "http://10.0.0.10:8080", valid: true},
		{name: "HTTP IPv6", url: "http://[2001:db8::10]:8080", valid: true},
		{name: "localhost", url: "http://localhost:8080", valid: true},
		{name: "missing host", url: "http://", valid: false},
		{name: "empty hostname", url: "http://:8080", valid: false},
		{name: "empty canonical hostname", url: "http://./", valid: false},
		{name: "root path", url: "https://headscale.example.com/", valid: true},
		{name: "path", url: "http://headscale.example.com/control", valid: false},
		{name: "query", url: "https://headscale.example.com?next=x", valid: false},
		{name: "empty query", url: "https://headscale.example.com?", valid: false},
		{name: "fragment", url: "https://headscale.example.com#control", valid: false},
		{name: "userinfo", url: "https://user:pass@headscale.example.com", valid: false},
		{name: "unsupported scheme", url: "ftp://headscale.example.com", valid: false},
		{name: "missing scheme", url: "headscale.example.com", valid: false},
		{name: "empty port", url: "http://headscale.example.com:", valid: false},
		{name: "zero port", url: "http://headscale.example.com:0", valid: false},
		{name: "port too large", url: "http://headscale.example.com:65536", valid: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := AccountPasswordServerURLIsValid(test.url); got != test.valid {
				t.Fatalf("AccountPasswordServerURLIsValid(%q) = %t, want %t", test.url, got, test.valid)
			}
		})
	}
}
