// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

// Package redact removes known secret shapes from text before it is stored or sent anywhere.
// It is best-effort: it knows shapes, not secrets, and a password said in plain prose passes.
package redact

import "regexp"

// Marker replaces every secret value.
const Marker = "[REDACTED]"

// rule is one shape; repl keeps the parts of the match that are not secret.
type rule struct {
	re   *regexp.Regexp
	repl string
}

// The order matters: whole-token shapes first, so the key=value rule does not leave half of one.
var rules = []rule{
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|\z)`), Marker},
	{regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_\-]{8,}`), Marker},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_\-]{20,}`), Marker},
	{regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}|\bgithub_pat_[A-Za-z0-9_]{20,}`), Marker},
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`), Marker},
	{regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9\-]{10,}`), Marker},
	{regexp.MustCompile(`\bhf_[A-Za-z0-9]{20,}`), Marker},
	{regexp.MustCompile(`\btskey-[A-Za-z0-9\-]{10,}`), Marker},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{5,}\.[A-Za-z0-9_\-]{5,}\.[A-Za-z0-9_\-]*`), Marker},
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=\-]{6,}`), "$1 " + Marker},
	// headers that carry a credential as their whole value; "Authorization: Bearer x" is the rule above, so here only a
	// long bare token after Authorization counts.
	{regexp.MustCompile(`(?i)\b(x-api-key|api-key|x-auth-token)(\s*:\s*)[^\s"'\\]+`), "${1}${2}" + Marker},
	// a cookie header is all name=value pairs, any of them a session: the whole value to the end of the line goes
	{regexp.MustCompile(`(?i)\b(cookie|set-cookie)(\s*:\s*)[^\r\n"'\\]+`), "${1}${2}" + Marker},
	{regexp.MustCompile(`(?i)\b(authorization\s*:\s*)[A-Za-z0-9._~+/=\-]{16,}`), "${1}" + Marker},
	// scheme://user:password@host: the password is the only secret part.
	{regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://[^\s:/@'"]*:)[^\s/@'"]+(@)`), "${1}" + Marker + "${2}"},
	// key=value and "key": "value", also JSON escaped one level, as tool input holds it.
	// \b before the keyword is absent so that ACCESS_TOKEN and client_secret match; a value is required,
	// and "tokens" does not match because the separator must follow the keyword.
	{regexp.MustCompile(`(?i)([a-z0-9_\-]*(?:password|passwd|pwd|secret|token|api[_\-]?key|access[_\-]?key|private[_\-]?key|credential)[a-z0-9_\-]*\\?["']?\s*[:=]\s*\\?["']?)([^\s"'\\&,;}\])]{3,})`), "${1}" + Marker},
	// short keywords that are also parts of ordinary words (author, compass, bypass, oauth, sessions): only as a whole
	// word, alone or after a "_"/"-" joined prefix, and right before the separator: DB_PASS= and "auth": match,
	// "Author:" and "compass:" do not.
	{regexp.MustCompile(`(?i)((?:^|[^a-z0-9_\-])(?:[a-z0-9]+[_\-])*(?:pass|passphrase|auth|session|sessionid|sid)\\?["']?\s*[:=]\s*\\?["']?)([^\s"'\\&,;}\])]{3,})`), "${1}" + Marker},
}

// String returns s with every known secret shape replaced by Marker.
func String(s string) string {
	for _, r := range rules {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}

// dotenv matches a dotenv file named in a text, alone or inside a command, in any case: .env, .env.<name>, .env*,
// .envrc and <name>.env, after a space, a quote, a path separator, a redirection or a list separator, and before the
// same or a glob, brace or slash (cat .env*, cp .env{,.bak}, ls .env/).
var dotenv = regexp.MustCompile(`(?i)(?:^|[\s/'"=<>,:;|&(\[{])((?:[\w.-]*\.env|\.env[\w.-]*))(?:$|[\s'";|&)<>,:*{}\]/])`)

// DotenvNames returns the dotenv file names s mentions; none for a text that names none.
func DotenvNames(s string) []string {
	var out []string
	for _, m := range dotenv.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}
