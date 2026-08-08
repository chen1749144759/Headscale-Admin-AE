package hscontrol

import (
	"strings"
	"testing"

	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

func TestRegistrationHostinfoIsBounded(t *testing.T) {
	t.Parallel()

	large := strings.Repeat("x", int(noiseBodyLimit/2))
	tags := make([]string, cachedRequestTagLimit+10)
	for idx := range tags {
		tags[idx] = large
	}
	source := &tailcfg.Hostinfo{
		Hostname:    large,
		OSVersion:   large,
		RequestTags: tags,
		Services:    []tailcfg.Service{{Proto: tailcfg.PeerAPI4}},
		NetInfo:     &tailcfg.NetInfo{},
	}

	got := registrationHostinfo(source)
	if len(got.Hostname) > cachedHostinfoStringLimit || len(got.OSVersion) > cachedHostinfoStringLimit {
		t.Fatalf("cached strings exceed %d bytes", cachedHostinfoStringLimit)
	}
	if len(got.RequestTags) != cachedRequestTagLimit {
		t.Fatalf("cached tag count = %d, want %d", len(got.RequestTags), cachedRequestTagLimit)
	}
	if got.Services != nil || got.NetInfo != nil {
		t.Fatal("live-only Hostinfo fields were retained in the registration cache")
	}
	if len(source.Hostname) != len(large) || len(source.RequestTags) != len(tags) {
		t.Fatal("registrationHostinfo mutated the request")
	}
}

func TestRegistrationDataNormalizesTopLevelHostname(t *testing.T) {
	t.Parallel()

	req := tailcfg.RegisterRequest{Hostinfo: &tailcfg.Hostinfo{
		Hostname: "  host\n<script>alert(1)</script>  ",
	}}
	got := registrationDataFromRequest(req, key.NewMachine().Public())
	if got.Hostname != "host-script-alert-1-script" {
		t.Fatalf("normalized hostname = %q", got.Hostname)
	}
	if got.Hostinfo == nil || got.Hostinfo.Hostname != got.Hostname {
		t.Fatalf("top-level and Hostinfo hostnames differ: %+v", got)
	}
}
