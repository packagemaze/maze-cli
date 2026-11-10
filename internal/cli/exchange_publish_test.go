package cli

import (
	"net/http"
	"testing"
)

const publishResponse = `{"token":"maze_ci_real","expires_at":"2026-06-08T12:30:00Z"}`

func TestExchangeOIDCPublishWithPackageRequestBodyIsUnchanged(t *testing.T) {
	_, body, err := runExchange(t, http.StatusOK, publishResponse, "--feed", "your-org/npm", "--purpose", "publish", "--package", "your-package")
	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}
	want := `{"provider":"manual","feed":"your-org/npm","purpose":"publish","package":"your-package","audience":"https://api.packagemaze.com","oidc_token":"manual-oidc"}`
	if body != want {
		t.Fatalf("request body = %s\nwant           %s", body, want)
	}
}
