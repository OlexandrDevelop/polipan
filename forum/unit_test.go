// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"
	"time"
)

func TestRenderPostEscapesHTML(t *testing.T) {
	cases := []string{
		`<script>alert(1)</script>`,
		`<img src=x onerror=alert(1)>`,
		`"><svg onload=alert(1)>`,
		"```\n<script>alert(1)</script>\n```",
		`javascript:alert(1)`,
		`https://example.com/"onmouseover="alert(1)`,
		`https://example.com/<script>alert(1)</script>`,
	}
	for _, in := range cases {
		out := string(renderPost(in))
		for _, bad := range []string{"<script", "<img", "<svg", `href="javascript`} {
			if strings.Contains(out, bad) {
				t.Errorf("%q gave %q (has %q)", in, out, bad)
			}
		}
	}
}

func TestRenderPostLinksAndCode(t *testing.T) {
	out := string(renderPost("See https://polipan.dev/docs.\n\nSecond paragraph\nnext line"))
	if !strings.Contains(out, `<a href="https://polipan.dev/docs" rel="nofollow noopener ugc">https://polipan.dev/docs</a>.`) {
		t.Errorf("link not found in %q", out)
	}
	if strings.Count(out, "<p>") != 2 || !strings.Contains(out, "<br>") {
		t.Errorf("paragraphs wrong in %q", out)
	}
	code := string(renderPost("before\n```\na < b\n\nhttps://x.example\n```\nafter"))
	if !strings.Contains(code, "<pre><code>a &lt; b\n\nhttps://x.example</code></pre>") {
		t.Errorf("code block wrong: %q", code)
	}
	// An opening fence without an end is plain text.
	open := string(renderPost("```\nnot closed"))
	if strings.Contains(open, "<pre>") {
		t.Errorf("open fence made a code block: %q", open)
	}
}

func TestUsernameAndPasswordRules(t *testing.T) {
	for _, ok := range []string{"abc", "Anna_1", "a-b-c", strings.Repeat("a", 24)} {
		if msg := validUsername(ok); msg != "" {
			t.Errorf("%q: %s", ok, msg)
		}
	}
	for _, bad := range []string{"ab", "a b", "ann@", "Admin", "ROOT", "polipan", strings.Repeat("a", 25), "é-user", ""} {
		if validUsername(bad) == "" {
			t.Errorf("%q should be rejected", bad)
		}
	}
	if validPassword("short") == "" || validPassword(strings.Repeat("x", 201)) == "" || validPassword(strings.Repeat("x", 10)) != "" {
		t.Error("password length rules are wrong")
	}
	if validTitle("ab") == "" || validTitle(strings.Repeat("t", 121)) == "" || validTitle("Good title") != "" {
		t.Error("title rules are wrong")
	}
	if validBody("   ") == "" || validBody(strings.Repeat("b", 10001)) == "" || validBody("ok") != "" {
		t.Error("body rules are wrong")
	}
}

func TestPasswordHash(t *testing.T) {
	h1, err := hashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := hashPassword("correct horse battery")
	if h1 == h2 {
		t.Error("the salt must be different every time")
	}
	if !strings.HasPrefix(h1, "$argon2id$") || !verifyPassword("correct horse battery", h1) {
		t.Error("the right password must work")
	}
	if verifyPassword("wrong password!!", h1) || verifyPassword("x", "garbage") || verifyPassword("x", "") {
		t.Error("a wrong password or a bad hash must fail")
	}
}

func TestCSRFAndTimeTrap(t *testing.T) {
	secret := []byte(strings.Repeat("s", 32))
	token := csrfToken(secret, "cookie-value")
	if !csrfValid(secret, "cookie-value", token) {
		t.Error("the token must match its cookie")
	}
	if csrfValid(secret, "other-cookie", token) || csrfValid(secret, "", token) || csrfValid(secret, "cookie-value", "") ||
		csrfValid([]byte(strings.Repeat("z", 32)), "cookie-value", token) {
		t.Error("a wrong token must fail")
	}
	now := time.Now()
	trap := trapToken(secret, now)
	if trapValid(secret, trap, now.Add(time.Second)) {
		t.Error("a form sent after 1 second is too fast")
	}
	if !trapValid(secret, trap, now.Add(10*time.Second)) {
		t.Error("a form sent after 10 seconds is fine")
	}
	if trapValid(secret, trap, now.Add(3*time.Hour)) {
		t.Error("an old form must fail")
	}
	if trapValid(secret, "123.abc", now) || trapValid(secret, "", now) || trapValid(secret, strings.Replace(trap, ".", "0.", 1), now.Add(10*time.Second)) {
		t.Error("a forged value must fail")
	}
}

func TestLimiter(t *testing.T) {
	l := newLimiter()
	now := time.Now()
	l.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if !l.allow("k", 3, time.Hour) {
			t.Fatalf("event %d should pass", i)
		}
	}
	if l.allow("k", 3, time.Hour) {
		t.Error("the 4th event must be blocked")
	}
	if !l.allow("other", 3, time.Hour) {
		t.Error("another key has its own count")
	}
	now = now.Add(2 * time.Hour)
	if !l.allow("k", 3, time.Hour) {
		t.Error("the window has passed")
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{"/t/5": "/t/5", "//evil.example": "/", "https://evil.example": "/", "/\\evil": "/", "": "/", "/a\r\nb": "/"} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}
