//go:build !windows

package ci

func gitHubOIDCPlatformConnectionError(error) bool { return false }
