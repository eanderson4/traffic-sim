package main

import "testing"

// The HTTP control plane is unauthenticated; the ADR-0002 carve-out is
// argued on loopback, so -http must refuse anything else.
func TestCheckLoopback(t *testing.T) {
	for _, ok := range []string{
		"127.0.0.1:8901",
		"127.0.0.2:1", // the whole 127.0.0.0/8 is loopback
		"localhost:8901",
		"[::1]:8901",
	} {
		if err := checkLoopback(ok); err != nil {
			t.Errorf("checkLoopback(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{
		"0.0.0.0:8901", // all interfaces
		":8901",        // empty host = all interfaces
		"[::]:8901",    // IPv6 any
		"192.168.1.10:8901",
		"example.com:8901",
	} {
		if err := checkLoopback(bad); err == nil {
			t.Errorf("checkLoopback(%q) = nil, want refusal", bad)
		}
	}
}
