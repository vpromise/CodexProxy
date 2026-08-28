package cmd

import (
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
)

// newAuthManager creates a new authentication manager for Codex and Claude.
//
// Returns:
//   - *sdkAuth.Manager: A configured authentication manager instance
func newAuthManager() *sdkAuth.Manager {
	store := sdkAuth.GetTokenStore()
	manager := sdkAuth.NewManager(store,
		sdkAuth.NewCodexAuthenticator(),
		sdkAuth.NewClaudeAuthenticator(),
	)
	return manager
}
