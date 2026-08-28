package auth

import "testing"

func TestOAuthModelAliasChannelScopedProviders(t *testing.T) {
	tests := []struct {
		provider string
		authKind string
		want     string
	}{
		{provider: "claude", authKind: "oauth", want: "claude"},
		{provider: "codex", authKind: "oauth", want: "codex"},
		{provider: "claude", authKind: "apikey", want: ""},
		{provider: "plugin-provider", authKind: "oauth", want: "plugin-provider"},
	}
	for _, test := range tests {
		if got := OAuthModelAliasChannel(test.provider, test.authKind); got != test.want {
			t.Fatalf("OAuthModelAliasChannel(%q, %q) = %q, want %q", test.provider, test.authKind, got, test.want)
		}
	}
}
