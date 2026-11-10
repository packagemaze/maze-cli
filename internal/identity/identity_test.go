package identity

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

func segment(content string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(content))
}

func jwt(header string, claims string) string {
	return segment(header) + "." + segment(claims) + ".c2lnbmF0dXJlLWZpeHR1cmU"
}

func TestDecodeRejectsMalformedTokensWithoutEchoingThem(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{name: "not a JWT", raw: "opaque-fixture-value", wantErr: "is not three dot-separated parts"},
		{name: "too many parts", raw: "a.b.c.d", wantErr: "is not three dot-separated parts"},
		{name: "header not base64url", raw: "%%%." + segment(`{}`) + ".sig", wantErr: "header could not be read: it is not base64url"},
		{name: "header not JSON", raw: segment("plain") + "." + segment(`{}`) + ".sig", wantErr: "header could not be read: it is not a JSON object"},
		{name: "header is an array", raw: segment(`[1]`) + "." + segment(`{}`) + ".sig", wantErr: "header could not be read: it is not a JSON object"},
		{name: "claims not base64url", raw: segment(`{}`) + ".!!!.sig", wantErr: "claims could not be read: it is not base64url"},
		{name: "claims are null", raw: segment(`{}`) + "." + segment(`null`) + ".sig", wantErr: "claims could not be read: it is not a JSON object"},
		{name: "claims have trailing data", raw: segment(`{}`) + "." + segment(`{} {}`) + ".sig", wantErr: "claims could not be read: it is not a JSON object"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Decode(test.raw)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("err = %v, want %q", err, test.wantErr)
			}
			if strings.Contains(err.Error(), test.raw) || strings.Contains(err.Error(), "\n") {
				t.Fatalf("error echoed the token or spans lines: %q", err)
			}
		})
	}
}

func TestDecodeAcceptsUnsignedAndPaddedTokens(t *testing.T) {
	padded := base64.URLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." + base64.URLEncoding.EncodeToString([]byte(`{"iss":"https://ci.example.com"}`)) + "."
	token, err := Decode(padded)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if token.Algorithm != "none" || token.Claims["iss"] != "https://ci.example.com" {
		t.Fatalf("token = %#v", token)
	}
}

func TestDisplayValue(t *testing.T) {
	tests := []struct {
		name   string
		claims string
		want   string
	}{
		{name: "string as is", claims: `{"v":"octo-org/app"}`, want: "octo-org/app"},
		{name: "integer as is", claims: `{"v":1731240000}`, want: "1731240000"},
		{name: "large number keeps its digits", claims: `{"v":12345678901234567890}`, want: "12345678901234567890"},
		{name: "decimal as is", claims: `{"v":1.50}`, want: "1.50"},
		{name: "boolean as JSON", claims: `{"v":true}`, want: "true"},
		{name: "null as JSON", claims: `{"v":null}`, want: "null"},
		{name: "array as compact JSON", claims: `{"v":[ "a", 1 ]}`, want: `["a",1]`},
		{name: "nested object as compact JSON with sorted keys", claims: `{"v":{"z":1,"a":{"b":"<x&y>"}}}`, want: `{"a":{"b":"<x&y>"},"z":1}`},
		{name: "newline escaped", claims: `{"v":"main\nFAKE LOG LINE"}`, want: `main\nFAKE LOG LINE`},
		{name: "terminal escape escaped", claims: `{"v":"\u001b[31mred"}`, want: `\u001b[31mred`},
		{name: "C1 control escaped", claims: `{"v":"\u009b31m"}`, want: `\u009b31m`},
		{name: "C1 control escaped inside JSON", claims: `{"v":["\u009b"]}`, want: `["\u009b"]`},
		{name: "carriage return and tab escaped", claims: `{"v":"a\rb\tc"}`, want: `a\rb\tc`},
		{name: "bidirectional override escaped", claims: `{"v":"main\u202egnp.exe"}`, want: `main\u202egnp.exe`},
		{name: "invisible formatting outside the BMP escaped", claims: `{"v":"a\udb40\udc01b"}`, want: `a\udb40\udc01b`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			token, err := Decode(jwt(`{"alg":"RS256"}`, test.claims))
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if got := DisplayValue(token.Claims["v"]); got != test.want {
				t.Fatalf("DisplayValue = %q, want %q", got, test.want)
			}
		})
	}
}

func TestWriteText(t *testing.T) {
	tests := []struct {
		name   string
		header string
		claims string
		want   string
	}{
		{
			name:   "summary then other claims sorted by name",
			header: `{"alg":"RS256","kid":"key-1","typ":"JWT"}`,
			claims: `{"sub":"job:42","iss":"https://ci.example.com","aud":"https://api.packagemaze.com","exp":1794312300,"https://ci.example.com/claims/repository":"acme/app","attempt":2,"protected":false,"ctx":{"ref":"main"}}`,
			want: unverifiedNote + "\n" +
				"Issuer:    https://ci.example.com\n" +
				"Audience:  https://api.packagemaze.com\n" +
				"Expires:   2026-11-10 12:05:00 UTC (in 5m0s)\n" +
				"Algorithm: RS256\n" +
				"Key id:    key-1\n" +
				"Other claims:\n" +
				"  attempt  2\n" +
				"  ctx  {\"ref\":\"main\"}\n" +
				"  https://ci.example.com/claims/repository  acme/app\n" +
				"  protected  false\n" +
				"  sub  job:42\n",
		},
		{
			name:   "missing summary fields and audience list",
			header: `{}`,
			claims: `{"aud":["https://api.packagemaze.com","other"],"exp":1794311700}`,
			want: unverifiedNote + "\n" +
				"Issuer:    (not set)\n" +
				"Audience:  [\"https://api.packagemaze.com\",\"other\"]\n" +
				"Expires:   2026-11-10 11:55:00 UTC (expired 5m0s ago)\n" +
				"Algorithm: (not set)\n" +
				"Key id:    (not set)\n",
		},
		{
			name:   "control characters in names and values cannot forge lines",
			header: `{"alg":"RS256\u001b[2J","kid":"k"}`,
			claims: `{"iss":"https://ci.example.com\nIssuer: forged","ref\nsub":"x\r\u001b]0;title\u0007"}`,
			want: unverifiedNote + "\n" +
				"Issuer:    https://ci.example.com\\nIssuer: forged\n" +
				"Audience:  (not set)\n" +
				"Expires:   (not set)\n" +
				"Algorithm: RS256\\u001b[2J\n" +
				"Key id:    k\n" +
				"Other claims:\n" +
				"  ref\\nsub  x\\r\\u001b]0;title\\u0007\n",
		},
		{
			name:   "non-integer expiry printed as is",
			header: `{"alg":"ES256","kid":"k"}`,
			claims: `{"exp":"tomorrow"}`,
			want: unverifiedNote + "\n" +
				"Issuer:    (not set)\n" +
				"Audience:  (not set)\n" +
				"Expires:   tomorrow\n" +
				"Algorithm: ES256\n" +
				"Key id:    k\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := jwt(test.header, test.claims)
			token, err := Decode(raw)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			var out bytes.Buffer
			if err := WriteText(&out, token, fixedNow); err != nil {
				t.Fatalf("WriteText: %v", err)
			}
			if out.String() != test.want {
				t.Fatalf("output =\n%s\nwant\n%s", out.String(), test.want)
			}
			assertNoTokenSegments(t, out.String(), raw)
		})
	}
}

func TestWriteJSON(t *testing.T) {
	raw := jwt(`{"alg":"RS256","kid":"key-1","typ":"JWT"}`, `{"iss":"https://ci.example.com","n":7,"note":"a\u009bb","nested":{"k":[1]}}`)
	token, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	var out bytes.Buffer
	if err := WriteJSON(&out, token); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	want := `{"claims":{"iss":"https://ci.example.com","n":7,"nested":{"k":[1]},"note":"a\u009bb"},"header":{"alg":"RS256","kid":"key-1"},"verified":false}` + "\n"
	if out.String() != want {
		t.Fatalf("output = %s\nwant %s", out.String(), want)
	}
	var payload map[string]any
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if payload["claims"].(map[string]any)["note"] != "a\u009bb" {
		t.Fatalf("escaped value did not round-trip: %#v", payload["claims"])
	}
	assertNoTokenSegments(t, out.String(), raw)
}

func assertNoTokenSegments(t *testing.T, output string, raw string) {
	t.Helper()
	for _, part := range strings.Split(raw, ".") {
		if part != "" && strings.Contains(output, part) {
			t.Fatalf("output contains a token segment %q", part)
		}
	}
}
