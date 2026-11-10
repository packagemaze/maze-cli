// Package identity reads what a job's identity token carries without
// verifying it, for people setting up a platform in PackageMaze.
package identity

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

// Token is what a decoded identity token carries. It never holds the raw
// token, any of its segments, or the signature.
type Token struct {
	Algorithm any
	KeyID     any
	Claims    map[string]any
}

// Decode reads the header and claims of a compact JWT. Errors name the part
// that could not be read and never echo the input.
func Decode(raw string) (Token, error) {
	parts := strings.Split(strings.TrimSpace(raw), ".")
	if len(parts) != 3 {
		return Token{}, errors.New("the identity token could not be read: it is not three dot-separated parts")
	}
	header, err := decodeSegment(parts[0])
	if err != nil {
		return Token{}, errors.New("the identity token header could not be read: " + err.Error())
	}
	claims, err := decodeSegment(parts[1])
	if err != nil {
		return Token{}, errors.New("the identity token claims could not be read: " + err.Error())
	}
	return Token{Algorithm: header["alg"], KeyID: header["kid"], Claims: claims}, nil
}

func decodeSegment(segment string) (map[string]any, error) {
	content, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(segment, "="))
	if err != nil {
		return nil, errors.New("it is not base64url")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil || decoder.More() {
		return nil, errors.New("it is not a JSON object")
	}
	return object, nil
}
