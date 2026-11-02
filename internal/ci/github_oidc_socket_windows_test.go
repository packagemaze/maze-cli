package ci

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"syscall"
	"testing"
)

func TestRequestGitHubOIDCTokenRetriesWindowsConnectionErrors(t *testing.T) {
	for _, socketError := range []error{syscall.WSAECONNRESET, syscall.WSAECONNABORTED, syscall.ERROR_NETNAME_DELETED} {
		t.Run(socketError.Error(), func(t *testing.T) {
			t.Parallel()
			attempts := 0
			client := &http.Client{Transport: oidcRoundTripper(func(_ *http.Request) (*http.Response, error) {
				attempts++
				if attempts == 1 {
					return nil, fmt.Errorf("socket: %w", socketError)
				}
				return oidcResponse(http.StatusOK, &trackedOIDCBody{Reader: strings.NewReader(`{"value":"github-oidc-token"}`)}), nil
			})}
			token, err := RequestGitHubOIDCToken(context.Background(), "audience", oidcTestEnv(), client)
			if err != nil || token != "github-oidc-token" || attempts != 2 {
				t.Fatalf("attempts = %d, error = %v", attempts, err)
			}
		})
	}
}
