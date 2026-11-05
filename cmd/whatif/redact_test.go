package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// redactedURL keeps the scheme, host and path, and nothing that can carry a
// secret.
func TestRedactedURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://prom.example:9091":                   "https://prom.example:9091",
		"https://prom.example:9091/prom/":             "https://prom.example:9091/prom/",
		"https://u:p4ss@prom.example/?token=s3cr3t#f": "https://prom.example/",
		"https://prom.example/?":                      "https://prom.example/",
		"http://prom.example/a?token=s%2Fcr%3Dt&b=1":  "http://prom.example/a",
		"http://u@prom.example":                       "http://prom.example",
		"::not a url":                                 "(unparsable URL)",
	} {
		assert.Equal(t, want, redactedURL(in), in)
	}
}

// redactedError replaces each secret of the URL wherever an error quotes
// it: the raw query, or a query value or password alone, decoded or
// escaped, as a client that reorders the query, or a proxy that quotes one
// value, prints it. What it wraps is still found.
func TestRedactedError(t *testing.T) {
	const prom = "https://u:pw%21x@prom.example/?b=t%2Fk&a=s3cr3t#frag"
	inner := usageError{errors.New("inner")}
	for _, msg := range []string{
		"Post https://prom.example/api?b=t%2Fk&a=s3cr3t: refused",
		"Post https://prom.example/api?a=s3cr3t&b=t%2Fk: refused",
		"value s3cr3t rejected",
		"value t/k rejected",
		"auth pw!x refused",
		"auth u:pw%21x refused",
		"fragment frag",
	} {
		err := redactedError(fmt.Errorf("%s: %w", msg, inner), prom)
		for _, leak := range []string{"s3cr3t", "t/k", "t%2Fk", "pw!x", "pw%21x", "frag"} {
			assert.NotContains(t, err.Error(), leak, msg)
		}
		assert.Contains(t, err.Error(), "REDACTED", msg)
		assert.Equal(t, 2, exitCode(err), "%s: the usage error is still found", msg)
	}
	plain := errors.New("nothing to hide")
	assert.Equal(t, plain, redactedError(plain, prom), "an error with no secret is returned as it is")
	assert.NoError(t, redactedError(nil, prom))
}
