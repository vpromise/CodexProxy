package config

// ConsoleNodeConfig enables the private control Interface without exposing management keys.
// Identity, TLS and egress changes require a restart. Existing accounts are unaffected.
type ConsoleNodeConfig struct {
	Enabled                  bool                   `yaml:"enabled" json:"enabled"`
	NodeID                   string                 `yaml:"node-id" json:"node-id"`
	InstanceID               string                 `yaml:"instance-id" json:"instance-id"`
	Listen                   string                 `yaml:"listen" json:"listen"`
	Cert                     string                 `yaml:"cert" json:"-"`
	Key                      string                 `yaml:"key" json:"-"`
	ClientCA                 string                 `yaml:"client-ca" json:"-"`
	ConsoleCertificateSHA256 string                 `yaml:"console-certificate-sha256" json:"-"`
	PublicOrigin             string                 `yaml:"public-origin" json:"public-origin"`
	EgressProfiles           []ConsoleEgressProfile `yaml:"egress-profiles" json:"-"`
}

type ConsoleEgressProfile struct {
	ID      string `yaml:"id" json:"id"`
	Version string `yaml:"version" json:"version"`
	// ProxyURL must be an explicit HTTP(S)/SOCKS proxy or direct-ip://<local IPv4>.
	ProxyURL         string `yaml:"proxy-url" json:"-"`
	ExpectedPublicIP string `yaml:"expected-public-ip" json:"expected_public_ip"`
}
