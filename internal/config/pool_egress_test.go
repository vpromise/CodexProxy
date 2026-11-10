package config

import "testing"

func TestPoolEgressValidation(t *testing.T) {
	for _, test := range []struct {
		mode, ip string
		valid    bool
	}{
		{"inherit", "", true}, {"direct-ip", "203.0.113.10", true}, {"direct-ip", "10.0.0.2", true},
		{"direct-ip", "127.0.0.1", true}, {"inherit", "203.0.113.10", false}, {"proxy", "203.0.113.10", false},
		{"direct-ip", "", false}, {"direct-ip", "::1", false}, {"direct-ip", "0.0.0.0", false},
		{"direct-ip", "224.0.0.1", false}, {"direct-ip", "255.255.255.255", false}, {"direct-ip", "203.0.113.999", false},
	} {
		t.Run(test.mode+"/"+test.ip, func(t *testing.T) {
			cfg := &Config{AccessIsolation: AccessIsolation{Pools: []AccountPool{{ID: "one", Name: "One", Provider: "codex", Egress: &PoolEgress{Mode: test.mode, SourceIP: test.ip}}}}}
			if err := cfg.ValidateAccessIsolation(); (err == nil) != test.valid {
				t.Fatalf("validation = %v, valid=%v", err, test.valid)
			}
		})
	}
}
