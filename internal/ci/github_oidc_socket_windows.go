package ci

import (
	"errors"
	"syscall"
)

func gitHubOIDCPlatformConnectionError(err error) bool {
	return errors.Is(err, syscall.WSAECONNRESET) || errors.Is(err, syscall.WSAECONNABORTED) || errors.Is(err, syscall.ERROR_NETNAME_DELETED)
}
