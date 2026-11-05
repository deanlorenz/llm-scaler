package main

import (
	"net/url"
	"slices"
	"strings"
)

// redactedURL is the Prometheus URL as whatif prints it: the scheme, host and
// path alone. A URL can carry a secret in its userinfo (a password) or in its
// query (?token=...), and a fragment is no part of the address, so all three
// are dropped, not redacted: a page cannot tell which query values are
// secrets. A string that does not parse prints as "(unparsable URL)".
func redactedURL(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return "(unparsable URL)"
	}
	u.User, u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = nil, "", false, "", ""
	return u.String()
}

// urlSecrets is every part of the Prometheus URL that must not be printed:
// its query and its userinfo, raw and decoded, and each query value and
// password on its own, longest first, so that a longer secret is replaced
// before a shorter one inside it.
func urlSecrets(s string) []string {
	u, err := url.Parse(s)
	if err != nil {
		return []string{s}
	}
	var out []string
	add := func(v string) {
		if v != "" {
			out = append(out, v)
		}
	}
	if u.RawQuery != "" {
		add("?" + u.RawQuery)
		add(u.RawQuery)
		q, _ := url.ParseQuery(u.RawQuery)
		for _, vs := range q {
			for _, v := range vs {
				add(v)
				add(url.QueryEscape(v))
			}
		}
	}
	add(u.Fragment)
	if u.User != nil {
		add(u.User.String())
		if p, ok := u.User.Password(); ok {
			add(p)
			add(url.QueryEscape(p))
		}
	}
	slices.SortStableFunc(out, func(a, b string) int { return len(b) - len(a) })
	return out
}

// redactedError is err with every secret of the Prometheus URL prom
// replaced (urlSecrets): an error from the HTTP client quotes the request's
// whole URL, query included. It unwraps to err, so it keeps err's exit code
// (exitCode) and what errors.Is finds in it.
func redactedError(err error, prom string) error {
	if err == nil || prom == "" {
		return err
	}
	msg := err.Error()
	clean := msg
	for _, s := range urlSecrets(prom) {
		clean = strings.ReplaceAll(clean, s, "REDACTED")
	}
	if clean == msg {
		return err
	}
	return redacted{msg: clean, err: err}
}

// redacted is an error whose message is msg, a redacted copy of err's,
// that unwraps to err so that errors.Is and errors.As still find what it
// wraps.
type redacted struct {
	msg string
	err error
}

func (r redacted) Error() string { return r.msg }

func (r redacted) Unwrap() error { return r.err }
