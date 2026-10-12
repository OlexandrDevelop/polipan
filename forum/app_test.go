// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests need a PostgreSQL database that can be erased: set FORUM_TEST_DATABASE_URL.
// The name of the database must contain "test".
type env_ struct {
	app    *App
	server *httptest.Server
	clock  time.Time
}

func newEnv(t *testing.T) *env_ {
	t.Helper()
	dsn := os.Getenv("FORUM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("FORUM_TEST_DATABASE_URL is not set")
	}
	if !strings.Contains(dsn, "test") {
		t.Fatal("the test database name must contain \"test\"")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	e := &env_{clock: time.Now()}
	e.app = &App{cfg: &Config{Secret: []byte(strings.Repeat("k", 32)), BaseURL: "http://forum.test", SiteName: "Test Forum",
		MainURL: "https://polipan.dev", DocsURL: "https://docs.polipan.dev"}, db: pool, lim: newLimiter(),
		now: func() time.Time { return e.clock }}
	if err := e.app.loadTemplates(); err != nil {
		t.Fatal(err)
	}
	e.server = httptest.NewServer(e.app.routes())
	t.Cleanup(e.server.Close)
	return e
}

type browser struct {
	t      *testing.T
	e      *env_
	client *http.Client
}

func (e *env_) browser(t *testing.T) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, e: e, client: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (b *browser) get(path string) (int, string, http.Header) {
	b.t.Helper()
	res, err := b.client.Get(b.e.server.URL + path)
	if err != nil {
		b.t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body), res.Header
}

var hidden = regexp.MustCompile(`name="(_csrf|ts)" value="([^"]*)"`)

// submit loads the form page for the tokens, waits 10 seconds (the time trap), and posts the values.
func (b *browser) submit(formPage, action string, values url.Values) (int, string, http.Header) {
	b.t.Helper()
	_, page, _ := b.get(formPage)
	if values == nil {
		values = url.Values{}
	}
	for _, m := range hidden.FindAllStringSubmatch(page, -1) {
		if values.Get(m[1]) == "" {
			values.Set(m[1], m[2])
		}
	}
	b.e.clock = b.e.clock.Add(10 * time.Second)
	res, err := b.client.PostForm(b.e.server.URL+action, values)
	if err != nil {
		b.t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body), res.Header
}

func (b *browser) register(name string) {
	b.t.Helper()
	status, body, _ := b.submit("/register", "/register", url.Values{"username": {name}, "email": {name + "@example.com"}, "password": {"a long password " + name}})
	if status != http.StatusSeeOther {
		b.t.Fatalf("register %s: %d %s", name, status, body)
	}
}

func (b *browser) mustRedirect(status int, header http.Header, prefix string) string {
	b.t.Helper()
	loc := header.Get("Location")
	if status != http.StatusSeeOther || !strings.HasPrefix(loc, prefix) {
		b.t.Fatalf("want redirect to %s, got %d %q", prefix, status, loc)
	}
	return loc
}

func (e *env_) sql(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := e.app.db.Exec(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func TestForumFlow(t *testing.T) {
	e := newEnv(t)
	anna, bob := e.browser(t), e.browser(t)

	// Pages render and carry the security headers.
	status, body, h := anna.get("/")
	if status != 200 || !strings.Contains(body, "Announcements") || !strings.Contains(h.Get("Content-Security-Policy"), "default-src 'self'") ||
		h.Get("X-Content-Type-Options") != "nosniff" || h.Get("X-Frame-Options") != "DENY" {
		t.Fatalf("index: %d %v", status, h)
	}
	if status, _, _ = anna.get("/healthz"); status != 200 {
		t.Fatalf("healthz %d", status)
	}

	// Register: bad input, then good, then a duplicate.
	st, body, _ := anna.submit("/register", "/register", url.Values{"username": {"admin"}, "email": {"a@example.com"}, "password": {"a long password"}})
	if st != 400 || !strings.Contains(body, "reserved") {
		t.Errorf("reserved name: %d", st)
	}
	st, _, _ = anna.submit("/register", "/register", url.Values{"username": {"anna"}, "email": {"not-an-email"}, "password": {"a long password"}})
	if st != 400 {
		t.Errorf("bad email: %d", st)
	}
	st, _, _ = anna.submit("/register", "/register", url.Values{"username": {"anna"}, "email": {"a@example.com"}, "password": {"short"}})
	if st != 400 {
		t.Errorf("short password: %d", st)
	}
	anna.register("anna")
	bob.register("bob")
	st, _, _ = e.browser(t).submit("/register", "/register", url.Values{"username": {"ANNA"}, "email": {"other@example.com"}, "password": {"a long password"}})
	if st != http.StatusConflict {
		t.Errorf("duplicate name: %d", st)
	}
	var stored string
	if err := e.app.db.QueryRow(context.Background(), `SELECT pass_hash FROM users WHERE username = 'anna'`).Scan(&stored); err != nil || !strings.HasPrefix(stored, "$argon2id$") {
		t.Errorf("the password must be stored as an argon2id hash: %q %v", stored, err)
	}

	// Login: wrong password, then right.
	fresh := e.browser(t)
	st, body, _ = fresh.submit("/login", "/login", url.Values{"username": {"anna"}, "password": {"wrong password!!"}})
	if st != 401 || !strings.Contains(body, "Wrong name or password") {
		t.Errorf("wrong password: %d", st)
	}
	st, _, h = fresh.submit("/login", "/login", url.Values{"username": {"anna"}, "password": {"a long password anna"}, "next": {"//evil.example"}})
	fresh.mustRedirect(st, h, "/")
	if h.Get("Location") != "/" {
		t.Errorf("open redirect: %q", h.Get("Location"))
	}

	// A member cannot post in a staff-only category.
	st, _, _ = anna.submit("/new", "/new", url.Values{"category": {"announcements"}, "title": {"Hello world"}, "body": {"text"}})
	if st != http.StatusForbidden {
		t.Errorf("announcements: %d", st)
	}

	// Create a topic, reply, edit, delete.
	st, _, h = anna.submit("/new", "/new", url.Values{"category": {"help"}, "title": {"How to install?"}, "body": {"I need <b>help</b> with https://polipan.dev"}})
	topic := anna.mustRedirect(st, h, "/t/")
	_, page, _ := bob.get(topic)
	if !strings.Contains(page, "How to install?") || strings.Contains(page, "<b>help</b>") || !strings.Contains(page, "&lt;b&gt;help&lt;/b&gt;") {
		t.Errorf("the topic page must show escaped text")
	}
	st, _, h = bob.submit(topic, topic+"/reply", url.Values{"body": {"Use the install script"}})
	bob.mustRedirect(st, h, topic)
	_, page, _ = anna.get(topic)
	if !strings.Contains(page, "Use the install script") || !strings.Contains(page, "1 replies") {
		t.Errorf("the reply is missing")
	}
	// Bots: the honeypot and the time trap.
	st, _, _ = bob.submit(topic, topic+"/reply", url.Values{"body": {"spam"}, "website": {"http://spam.example"}})
	if st != 400 {
		t.Errorf("honeypot: %d", st)
	}
	_, form, _ := bob.get(topic)
	values := url.Values{"body": {"too fast"}}
	for _, m := range hidden.FindAllStringSubmatch(form, -1) {
		values.Set(m[1], m[2])
	}
	res, _ := bob.client.PostForm(e.server.URL+topic+"/reply", values)
	res.Body.Close()
	if res.StatusCode != 400 {
		t.Errorf("time trap: %d", res.StatusCode)
	}
	// No CSRF token.
	res, _ = bob.client.PostForm(e.server.URL+topic+"/reply", url.Values{"body": {"x"}, "ts": {values.Get("ts")}})
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("csrf: %d", res.StatusCode)
	}
	// Not logged in.
	if st, _, _ = e.browser(t).submit(topic, topic+"/reply", url.Values{"body": {"anonymous"}}); st != http.StatusForbidden {
		t.Errorf("anonymous reply: %d", st)
	}

	var replyID, firstID int64
	_ = e.app.db.QueryRow(context.Background(), `SELECT min(id), max(id) FROM posts`).Scan(&firstID, &replyID)
	// Others cannot edit; the author can, until 30 minutes.
	if st, _, _ = anna.get("/p/" + itoa(replyID) + "/edit"); st != http.StatusForbidden {
		t.Errorf("edit of another user: %d", st)
	}
	st, _, h = bob.submit("/p/"+itoa(replyID)+"/edit", "/p/"+itoa(replyID)+"/edit", url.Values{"body": {"Edited reply"}})
	bob.mustRedirect(st, h, "/t/")
	if _, page, _ = anna.get(topic); !strings.Contains(page, "Edited reply") || !strings.Contains(page, "edited") {
		t.Errorf("edit missing")
	}
	e.clock = e.clock.Add(31 * time.Minute)
	if st, _, _ = bob.get("/p/" + itoa(replyID) + "/edit"); st != http.StatusForbidden {
		t.Errorf("edit after 30 minutes: %d", st)
	}
	// The author cannot delete the first post.
	st, _, _ = anna.submit(topic, "/p/"+itoa(firstID)+"/delete", nil)
	if st != http.StatusForbidden {
		t.Errorf("delete first post: %d", st)
	}

	// Search and the feed.
	if _, page, _ = anna.get("/search?q=install"); !strings.Contains(page, "How to install?") {
		t.Errorf("search by title failed")
	}
	if _, page, _ = anna.get("/search?q=edited"); !strings.Contains(page, "How to install?") {
		t.Errorf("search by text failed")
	}
	if _, page, h = anna.get("/feed.xml"); !strings.Contains(h.Get("Content-Type"), "rss") || !strings.Contains(page, "How to install?") {
		t.Errorf("feed failed")
	}

	// A member cannot use the team pages or actions.
	if st, _, _ = anna.get("/mod"); st != http.StatusForbidden {
		t.Errorf("mod page: %d", st)
	}
	if st, _, _ = anna.submit(topic, topic+"/lock", nil); st != http.StatusForbidden {
		t.Errorf("lock by member: %d", st)
	}

	// Make bob a moderator: lock, locked reply, pin, ban.
	e.sql(t, `UPDATE users SET role = 'moderator' WHERE username = 'bob'`)
	if st, _, _ = bob.get("/mod"); st != 200 {
		t.Errorf("mod page for staff: %d", st)
	}
	st, _, h = bob.submit(topic, topic+"/lock", nil)
	bob.mustRedirect(st, h, topic)
	if st, _, _ = anna.submit(topic, topic+"/reply", url.Values{"body": {"after lock"}}); st != http.StatusForbidden {
		t.Errorf("reply to a locked topic: %d", st)
	}
	st, _, h = bob.submit("/new", "/new", url.Values{"category": {"announcements"}, "title": {"Version one"}, "body": {"Released"}})
	bob.mustRedirect(st, h, "/t/")
	var annaID int64
	_ = e.app.db.QueryRow(context.Background(), `SELECT id FROM users WHERE username = 'anna'`).Scan(&annaID)
	st, _, h = bob.submit("/mod", "/mod/ban", url.Values{"user": {itoa(annaID)}, "ban": {"1"}})
	bob.mustRedirect(st, h, "/mod")
	if st, _, _ = anna.submit("/new", "/new", url.Values{"category": {"help"}, "title": {"Banned post"}, "body": {"x"}}); st == http.StatusSeeOther {
		t.Errorf("a banned user must not post")
	}
	if st, _, _ = e.browser(t).submit("/login", "/login", url.Values{"username": {"anna"}, "password": {"a long password anna"}}); st != http.StatusForbidden {
		t.Errorf("a banned user must not log in: %d", st)
	}
	// Bob cannot ban himself or another staff member.
	var bobID int64
	_ = e.app.db.QueryRow(context.Background(), `SELECT id FROM users WHERE username = 'bob'`).Scan(&bobID)
	if st, _, _ = bob.submit("/mod", "/mod/ban", url.Values{"user": {itoa(bobID)}, "ban": {"1"}}); st != http.StatusBadRequest {
		t.Errorf("self ban: %d", st)
	}
	// Delete the topic as staff.
	st, _, h = bob.submit(topic, topic+"/delete", nil)
	bob.mustRedirect(st, h, "/")
	if st, _, _ = e.browser(t).get(topic); st != http.StatusNotFound {
		t.Errorf("a deleted topic is hidden: %d", st)
	}

	// Logout ends the session.
	st, _, h = bob.submit("/", "/logout", nil)
	bob.mustRedirect(st, h, "/")
	if st, _, _ = bob.get("/mod"); st != http.StatusForbidden {
		t.Errorf("after logout: %d", st)
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newEnv(t)
	b := e.browser(t)
	b.register("carol")
	limited := false
	for i := 0; i < 14; i++ {
		st, _, _ := e.browser(t).submit("/login", "/login", url.Values{"username": {"carol"}, "password": {"wrong password!!"}})
		if st == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("too many wrong passwords must be limited")
	}
}

func TestPagination(t *testing.T) {
	e := newEnv(t)
	b := e.browser(t)
	b.register("dora")
	var userID int64
	_ = e.app.db.QueryRow(context.Background(), `SELECT id FROM users`).Scan(&userID)
	for i := 0; i < 45; i++ {
		if _, err := e.app.createTopic(context.Background(), 2, userID, "Topic "+itoa(int64(i)), "text"); err != nil {
			t.Fatal(err)
		}
	}
	_, page, _ := b.get("/c/help")
	if !strings.Contains(page, "page 1 / 3") || !strings.Contains(page, "?page=2") {
		t.Error("the pager is missing")
	}
	if st, page, _ := b.get("/c/help?page=3"); st != 200 || !strings.Contains(page, "page 3 / 3") {
		t.Error("page 3 is missing")
	}
	if st, _, _ := b.get("/c/help?page=abc"); st != 200 {
		t.Error("a bad page number must not fail")
	}
	if st, _, _ := b.get("/t/99999"); st != 404 {
		t.Error("a missing topic is 404")
	}
	if st, _, _ := b.get("/t/abc"); st != 404 {
		t.Error("a bad topic id is 404")
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
