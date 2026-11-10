package cli

import (
	"net/http"
	"strings"
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

func TestExchangeOIDCPublishWithoutPackageSendsANullPackageAsInstallDoes(t *testing.T) {
	stdout, body, err := runExchange(t, http.StatusOK, publishResponse, "--feed", "your-org/npm", "--purpose", "publish")
	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}
	want := `{"provider":"manual","feed":"your-org/npm","purpose":"publish","package":null,"audience":"https://api.packagemaze.com","oidc_token":"manual-oidc"}`
	if body != want {
		t.Fatalf("request body = %s\nwant           %s", body, want)
	}
	if stdout != "maze_ci_real\n" {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestExchangeOIDCPublishWithoutPackageSurfacesAnOlderServersRefusal(t *testing.T) {
	_, _, err := runExchange(t, http.StatusUnprocessableEntity,
		`{"detail":[{"type":"value_error","loc":["body"],"msg":"Value error, package is required when purpose is publish"}]}`,
		"--feed", "your-org/npm", "--purpose", "publish")
	want := "HTTP 422: Value error, package is required when purpose is publish"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want it to contain %q", err, want)
	}
}
