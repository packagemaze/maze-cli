package identity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	notSet         = "(not set)"
	unverifiedNote = "Decoded locally and not verified; nothing was sent to PackageMaze."
)

var summaryClaims = map[string]bool{"iss": true, "aud": true, "exp": true}

// WriteText prints the token for a person reading a job log.
func WriteText(w io.Writer, token Token, now time.Time) error {
	var out strings.Builder
	out.WriteString(unverifiedNote + "\n")
	writeField(&out, "Issuer", valueOrNotSet(token.Claims["iss"]))
	writeField(&out, "Audience", valueOrNotSet(token.Claims["aud"]))
	writeField(&out, "Expires", expiry(token.Claims["exp"], now))
	writeField(&out, "Algorithm", valueOrNotSet(token.Algorithm))
	writeField(&out, "Key id", valueOrNotSet(token.KeyID))
	names := otherClaimNames(token.Claims)
	if len(names) > 0 {
		out.WriteString("Other claims:\n")
	}
	for _, name := range names {
		fmt.Fprintf(&out, "  %s  %s\n", escapeControl(name), DisplayValue(token.Claims[name]))
	}
	_, err := io.WriteString(w, out.String())
	return err
}

// WriteJSON prints the token as data. Control characters stay JSON-escaped.
func WriteJSON(w io.Writer, token Token) error {
	payload := map[string]any{
		"verified": false,
		"header":   map[string]any{"alg": token.Algorithm, "kid": token.KeyID},
		"claims":   token.Claims,
	}
	encoded, err := compactJSON(payload)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, encoded+"\n")
	return err
}

// DisplayValue renders strings and numbers as they are and every other JSON
// value as compact JSON, with control characters escaped.
func DisplayValue(value any) string {
	switch typed := value.(type) {
	case string:
		return escapeControl(typed)
	case json.Number:
		return typed.String()
	}
	encoded, err := compactJSON(value)
	if err != nil {
		return "(unreadable)"
	}
	return encoded
}

func writeField(out *strings.Builder, label string, value string) {
	fmt.Fprintf(out, "%-10s %s\n", label+":", value)
}

func valueOrNotSet(value any) string {
	if value == nil {
		return notSet
	}
	return DisplayValue(value)
}

func expiry(value any, now time.Time) string {
	number, ok := value.(json.Number)
	if !ok {
		return valueOrNotSet(value)
	}
	seconds, err := number.Int64()
	if err != nil {
		return DisplayValue(value)
	}
	expiresAt := time.Unix(seconds, 0).UTC()
	remaining := expiresAt.Sub(now).Round(time.Second)
	relative := "in " + remaining.String()
	if remaining < 0 {
		relative = "expired " + (-remaining).String() + " ago"
	}
	return fmt.Sprintf("%s (%s)", expiresAt.Format("2006-01-02 15:04:05 UTC"), relative)
}

func otherClaimNames(claims map[string]any) []string {
	names := make([]string, 0, len(claims))
	for name := range claims {
		if !summaryClaims[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func compactJSON(value any) (string, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	return escapeControl(strings.TrimSuffix(buffer.String(), "\n")), nil
}

// escapeControl makes a token-supplied string safe to print on one line: it
// cannot start a new log line or emit a terminal sequence.
func escapeControl(value string) string {
	var out strings.Builder
	for index, r := range value {
		switch {
		case r == utf8.RuneError && isInvalidByte(value, index):
			fmt.Fprintf(&out, `\x%02x`, value[index])
		case r == '\n':
			out.WriteString(`\n`)
		case r == '\r':
			out.WriteString(`\r`)
		case r == '\t':
			out.WriteString(`\t`)
		case unicode.IsControl(r):
			fmt.Fprintf(&out, `\u%04x`, r)
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

func isInvalidByte(value string, index int) bool {
	_, size := utf8.DecodeRuneInString(value[index:])
	return size == 1
}
