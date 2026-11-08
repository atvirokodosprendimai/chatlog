// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package redact

import (
	"strings"
	"testing"
)

// Every value below is invented. Each case names a shape the transcripts can hold.
func TestShapes(t *testing.T) {
	cases := []struct{ name, in, secret, keep string }{
		{"anthropic oauth", "use sk-ant-oat01-INVENTEDinvented_123-abc now", "INVENTEDinvented", "use "},
		{"generic sk key", "OPENAI sk-abcdefghijklmnopqrstuvwx1234 here", "abcdefghijklmnop", "OPENAI"},
		{"github token", "ghp_abcdefghijklmnopqrstuvwxyz0123456789", "abcdefghijklmnop", ""},
		{"aws key id", "id AKIAABCDEFGHIJKLMNOP end", "ABCDEFGHIJKLMNOP", " end"},
		{"key=value", "API_KEY=hunter2abc next", "hunter2abc", "API_KEY="},
		{"token: value", `token: "abc.def-ghi" rest`, "abc.def-ghi", "token"},
		{"password=value", "mysql --password=s3cr3t!x db", "s3cr3t!x", " db"},
		{"env oauth", "CLAUDE_CODE_OAUTH_TOKEN=whatever123", "whatever123", "CLAUDE_CODE_OAUTH_TOKEN="},
		{"json escaped", `{\"client_secret\":\"zzzsecretzzz\"}`, "zzzsecretzzz", "client_secret"},
		{"bearer", "Authorization: Bearer abc123.def456-ghi", "abc123.def456", "Authorization"},
		{"basic auth header", "Authorization: Basic dXNlcjpwYXNz", "dXNlcjpwYXNz", "Authorization"},
		{"jwt", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.abcDEF123_-x", "eyJzdWIiOiIxMjM0In0", ""},
		{"private key", "a\n-----BEGIN OPENSSH PRIVATE KEY-----\nAAAAinvented\nBBBB\n-----END OPENSSH PRIVATE KEY-----\nb", "AAAAinvented", "\nb"},
		{"private key unterminated", "-----BEGIN RSA PRIVATE KEY-----\nCCCCinvented\n", "CCCCinvented", ""},
		{"dsn", "postgres://app:pa55word@db.example:5432/x", "pa55word", "postgres://app:"},
		{"dsn keeps host", "mysql://root:toor1@h1/db", "toor1", "@h1/db"},
		{"url query", "https://x.example/a?access_token=qqq111&b=2", "qqq111", "&b=2"},
		{"api key header", "curl -H 'X-Api-Key: k3y-invented-77' x", "k3y-invented-77", "X-Api-Key: "},
		{"cookie header", "Cookie: sid=abcinvented42", "abcinvented42", "Cookie: "},
		{"bare authorization", "Authorization: abcdefINVENTED0123456789", "abcdefINVENTED", "Authorization: "},
		{"db pass", "DB_PASS=hunter2x psql", "hunter2x", "DB_PASS="},
		{"passphrase", "passphrase: correcthorse", "correcthorse", "passphrase"},
		{"auth key", `{"auth":"abcAUTHinvented"}`, "abcAUTHinvented", "auth"},
		{"session key", "session=sessINVENTED99", "sessINVENTED99", "session="},
		{"cookie whole header", "Cookie: a=1; session=abcdefINVENTED", "abcdefINVENTED", "Cookie: "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := String(c.in)
			if strings.Contains(got, c.secret) {
				t.Fatalf("secret left in %q", got)
			}
			if !strings.Contains(got, Marker) {
				t.Fatalf("no marker in %q", got)
			}
			if c.keep != "" && !strings.Contains(got, c.keep) {
				t.Fatalf("lost %q in %q", c.keep, got)
			}
		})
	}
}

// Ordinary talk about tokens must survive, or search for those words would find nothing.
func TestLeavesProse(t *testing.T) {
	for _, s := range []string{
		"bge m3 tai 4000 tokens should fit",
		"the password prompt appeared",
		"https://example.com/path:8080",
		"token budget is small",
		"Author: Jane Doe <jane@example.com>",
		"the session ended at noon",
		"compass: north",
		"bypass=on",
		"oauth=enabled",
	} {
		if got := String(s); got != s {
			t.Fatalf("changed %q to %q", s, got)
		}
	}
}
