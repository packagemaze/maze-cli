package endpoint

import "testing"

func TestValidateURLAdmitsHTTPSAndLocalhostHTTPOnlyWhenAllowed(t *testing.T) {
	if err := ValidateURL("base-url", "https://api.packagemaze.com", false); err != nil {
		t.Fatalf("https refused: %v", err)
	}
	if err := ValidateURL("base-url", "http://api.packagemaze.com", true); err == nil {
		t.Fatal("plain http outside localhost was admitted")
	}
	for _, host := range []string{"localhost", "127.0.0.1", "[::1]"} {
		if err := ValidateURL("base-url", "http://"+host+":8787", true); err != nil {
			t.Fatalf("%s refused with the flag: %v", host, err)
		}
		if err := ValidateURL("base-url", "http://"+host+":8787", false); err == nil {
			t.Fatalf("%s admitted without the flag", host)
		}
	}
	if err := ValidateURL("api-url", "not a url", true); err == nil || err.Error() != "--api-url must be an absolute URL" {
		t.Fatalf("relative URL error = %v", err)
	}
}

func TestSplitFeedSlugAcceptsExactlyOrganizationSlashFeed(t *testing.T) {
	organization, name, ok := SplitFeedSlug(" acme/npm-main ")
	if !ok || organization != "acme" || name != "npm-main" {
		t.Fatalf("split = %q %q %v", organization, name, ok)
	}
	for _, invalid := range []string{"acme", "acme/", "/npm", "acme/npm/extra", "-acme/npm", "acme/np m", ""} {
		if _, _, ok := SplitFeedSlug(invalid); ok {
			t.Fatalf("%q was accepted", invalid)
		}
	}
}

func TestResolvePackageClientURLPrefersFlagThenEnvironmentThenDefault(t *testing.T) {
	env := func(values map[string]string) func(string) (string, bool) {
		return func(key string) (string, bool) {
			value, ok := values[key]
			return value, ok
		}
	}
	if got := ResolvePackageClientURL("https://pkg.example.test/", env(map[string]string{PackageClientURLEnv: "https://env.example.test"})); got != "https://pkg.example.test" {
		t.Fatalf("flag precedence = %q", got)
	}
	if got := ResolvePackageClientURL("", env(map[string]string{PackageClientURLEnv: "https://env.example.test/"})); got != "https://env.example.test" {
		t.Fatalf("environment precedence = %q", got)
	}
	if got := ResolvePackageClientURL("  ", env(nil)); got != DefaultPackageClientURL {
		t.Fatalf("default = %q", got)
	}
}
