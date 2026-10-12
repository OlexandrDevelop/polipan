// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	pageSize      = 20
	sessionCookie = "forum_session"
	csrfCookie    = "forum_csrf"
	sessionLife   = 30 * 24 * time.Hour
)

// View is the data of every page.
type View struct {
	Title      string
	Cfg        *Config
	User       *User
	CSRF       string
	TS         string
	Error      string
	Flash      string
	Categories []Category
	Category   *Category
	Topics     []TopicRow
	Topic      *TopicRow
	Posts      []PostRow
	Post       *PostRow
	Results    []SearchRow
	Users      []User
	Query      string
	Page       int
	Pages      int
	Base       string
	Form       map[string]string
	Next       string
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFiles, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheFor(http.FileServerFS(static), "public, max-age=86400")))
	mux.HandleFunc("GET /{$}", a.index)
	mux.HandleFunc("GET /c/{slug}", a.category)
	mux.HandleFunc("GET /t/{id}", a.topic)
	mux.HandleFunc("GET /new", a.newForm)
	mux.HandleFunc("POST /new", a.post(a.newTopic))
	mux.HandleFunc("POST /t/{id}/reply", a.post(a.reply))
	mux.HandleFunc("POST /t/{id}/{action}", a.post(a.topicAction))
	mux.HandleFunc("GET /p/{id}/edit", a.editForm)
	mux.HandleFunc("POST /p/{id}/edit", a.post(a.edit))
	mux.HandleFunc("POST /p/{id}/delete", a.post(a.removePost))
	mux.HandleFunc("GET /register", a.registerForm)
	mux.HandleFunc("POST /register", a.post(a.register))
	mux.HandleFunc("GET /login", a.loginForm)
	mux.HandleFunc("POST /login", a.post(a.login))
	mux.HandleFunc("POST /logout", a.post(a.logout))
	mux.HandleFunc("GET /search", a.searchPage)
	mux.HandleFunc("GET /mod", a.modPage)
	mux.HandleFunc("POST /mod/ban", a.post(a.ban))
	mux.HandleFunc("GET /feed.xml", a.feed)
	mux.HandleFunc("GET /healthz", a.healthz)
	mux.HandleFunc("GET /robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "User-agent: *\nDisallow: /search\nDisallow: /mod\nDisallow: /login\nDisallow: /register\nDisallow: /new\n")
	})
	return a.headers(mux)
}

func cacheFor(h http.Handler, value string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", value)
		h.ServeHTTP(w, r)
	})
}

// headers adds the security headers to every answer.
func (a *App) headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; font-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), interest-cohort=()")
		if a.cfg.secure() {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if err := a.db.Ping(ctx); err != nil {
		http.Error(w, "database down", http.StatusServiceUnavailable)
		return
	}
	fmt.Fprintln(w, "ok")
}

// ---- helpers ----

func (a *App) clientIP(r *http.Request) string {
	if a.cfg.TrustedProxy {
		// The proxy adds the address of its client at the end of the list.
		parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		if ip := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(ip) != nil {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (a *App) setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true,
		Secure: a.cfg.secure(), SameSite: http.SameSiteLaxMode})
}

func (a *App) currentUser(w http.ResponseWriter, r *http.Request) *User {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	var u User
	var expires time.Time
	err = a.db.QueryRow(r.Context(), `
SELECT u.id, u.username, u.role, u.banned, u.created_at, s.expires_at
FROM sessions s JOIN users u ON u.id = s.user_id
WHERE s.token_hash = $1 AND s.expires_at > now()`, tokenHash(c.Value)).
		Scan(&u.ID, &u.Username, &u.Role, &u.Banned, &u.Created, &expires)
	if err != nil {
		return nil
	}
	// Sliding expiry: extend it when more than a day has passed.
	if time.Until(expires) < sessionLife-24*time.Hour {
		_, _ = a.db.Exec(r.Context(), `UPDATE sessions SET expires_at = $2 WHERE token_hash = $1`,
			tokenHash(c.Value), a.now().Add(sessionLife))
		a.setCookie(w, sessionCookie, c.Value, int(sessionLife.Seconds()))
	}
	return &u
}

func (a *App) csrfValue(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(csrfCookie); err == nil && len(c.Value) >= 20 {
		return csrfToken(a.cfg.Secret, c.Value)
	}
	token, err := randomToken()
	if err != nil {
		return ""
	}
	a.setCookie(w, csrfCookie, token, int((365 * 24 * time.Hour).Seconds()))
	return csrfToken(a.cfg.Secret, token)
}

func (a *App) view(w http.ResponseWriter, r *http.Request, title string) *View {
	return &View{Title: title, Cfg: a.cfg, User: a.currentUser(w, r), CSRF: a.csrfValue(w, r),
		TS: trapToken(a.cfg.Secret, a.now()), Form: map[string]string{}}
}

func (a *App) render(w http.ResponseWriter, name string, status int, v *View) {
	t, ok := a.tmpl[name]
	if !ok {
		http.Error(w, "page missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := t.ExecuteTemplate(w, "layout", v); err != nil {
		log.Printf("forum: render %s: %v", name, err)
	}
}

func (a *App) fail(w http.ResponseWriter, r *http.Request, status int, message string) {
	v := a.view(w, r, message)
	v.Error = message
	a.render(w, "error", status, v)
}

func (a *App) serverError(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("forum: %s %s: %v", r.Method, r.URL.Path, err)
	a.fail(w, r, http.StatusInternalServerError, "Something went wrong. Try again later.")
}

// post wraps a POST handler: the body is small and the CSRF token must match.
func (a *App) post(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if err := r.ParseForm(); err != nil {
			a.fail(w, r, http.StatusRequestEntityTooLarge, "The form is too big.")
			return
		}
		c, _ := r.Cookie(csrfCookie)
		cookie := ""
		if c != nil {
			cookie = c.Value
		}
		if !csrfValid(a.cfg.Secret, cookie, r.PostFormValue("_csrf")) {
			a.fail(w, r, http.StatusForbidden, "The form is not valid any more. Go back, reload the page and try again.")
			return
		}
		next(w, r)
	}
}

// spam returns a text when the form looks like it was sent by a bot.
func (a *App) spam(r *http.Request) string {
	if r.PostFormValue("website") != "" {
		return "The form was not accepted."
	}
	if !trapValid(a.cfg.Secret, r.PostFormValue("ts"), a.now()) {
		return "Please wait a few seconds and send the form again."
	}
	return ""
}

func pageNumber(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || n < 1 || n > 100000 {
		return 1
	}
	return n
}

func pagesFor(total int) int {
	if total <= 0 {
		return 1
	}
	return (total + pageSize - 1) / pageSize
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// safeNext only accepts a path on this site.
func safeNext(next string) string {
	if strings.HasPrefix(next, "/") && !strings.HasPrefix(next, "//") && !strings.ContainsAny(next, "\\\r\n") {
		return next
	}
	return "/"
}

// ---- reading ----

func (a *App) index(w http.ResponseWriter, r *http.Request) {
	v := a.view(w, r, "")
	cats, err := a.categories(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	v.Categories = cats
	v.Topics, err = a.topicList(r.Context(), "", nil, 10, 0)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, "index", http.StatusOK, v)
}

func (a *App) category(w http.ResponseWriter, r *http.Request) {
	cat, err := a.categoryBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		a.fail(w, r, http.StatusNotFound, "This category does not exist.")
		return
	}
	v := a.view(w, r, cat.Name)
	v.Category = cat
	v.Page = pageNumber(r)
	where, args := ` AND t.category_id = $1`, []any{cat.ID}
	total, err := a.countTopics(r.Context(), where, args)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	v.Pages = pagesFor(total)
	v.Base = "/c/" + cat.Slug
	v.Topics, err = a.topicList(r.Context(), where, args, pageSize, (v.Page-1)*pageSize)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, "category", http.StatusOK, v)
}

func (a *App) topic(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		a.fail(w, r, http.StatusNotFound, "This topic does not exist.")
		return
	}
	t, err := a.topicByID(r.Context(), id)
	v := a.view(w, r, "")
	if err != nil || (t.Deleted && !v.User.IsStaff()) {
		a.fail(w, r, http.StatusNotFound, "This topic does not exist.")
		return
	}
	v.Title, v.Topic = t.Title, t
	v.Page = pageNumber(r)
	v.Pages = pagesFor(t.Replies + 1)
	v.Base = "/t/" + strconv.FormatInt(t.ID, 10)
	v.Posts, err = a.posts(r.Context(), id, pageSize, (v.Page-1)*pageSize, v.User, a.now())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, "topic", http.StatusOK, v)
}

func (a *App) searchPage(w http.ResponseWriter, r *http.Request) {
	v := a.view(w, r, "Search")
	v.Query = strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(v.Query)) > 100 {
		v.Query = string([]rune(v.Query)[:100])
	}
	if v.Query != "" {
		if !a.lim.allow("search:"+a.clientIP(r), 30, time.Minute) {
			a.fail(w, r, http.StatusTooManyRequests, "Too many searches. Wait a minute.")
			return
		}
		var err error
		if v.Results, err = a.search(r.Context(), v.Query, 50); err != nil {
			a.serverError(w, r, err)
			return
		}
	}
	a.render(w, "search", http.StatusOK, v)
}

type rss struct {
	XMLName xml.Name `xml:"rss"`
	Version string   `xml:"version,attr"`
	Channel struct {
		Title string    `xml:"title"`
		Link  string    `xml:"link"`
		Desc  string    `xml:"description"`
		Items []rssItem `xml:"item"`
	} `xml:"channel"`
}

type rssItem struct {
	Title string `xml:"title"`
	Link  string `xml:"link"`
	GUID  string `xml:"guid"`
	Date  string `xml:"pubDate"`
	Cat   string `xml:"category"`
}

func (a *App) feed(w http.ResponseWriter, r *http.Request) {
	topics, err := a.topicList(r.Context(), "", nil, 30, 0)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	f := rss{Version: "2.0"}
	f.Channel.Title, f.Channel.Link, f.Channel.Desc = a.cfg.SiteName, a.cfg.BaseURL, "Latest topics"
	for _, t := range topics {
		link := fmt.Sprintf("%s/t/%d", a.cfg.BaseURL, t.ID)
		f.Channel.Items = append(f.Channel.Items, rssItem{Title: t.Title, Link: link, GUID: link,
			Date: t.LastPostAt.UTC().Format(time.RFC1123Z), Cat: t.CategoryName})
	}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	fmt.Fprint(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(f)
}

// ---- writing ----

func (a *App) newForm(w http.ResponseWriter, r *http.Request) {
	v := a.view(w, r, "New topic")
	if v.User == nil {
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
		return
	}
	cats, err := a.categories(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	v.Categories = cats
	v.Form["category"] = r.URL.Query().Get("c")
	a.render(w, "new", http.StatusOK, v)
}

func (a *App) newTopic(w http.ResponseWriter, r *http.Request) {
	v := a.view(w, r, "New topic")
	if v.User == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	v.Categories, _ = a.categories(r.Context())
	v.Form = map[string]string{"category": r.PostFormValue("category"), "title": r.PostFormValue("title"), "body": r.PostFormValue("body")}
	again := func(status int, message string) {
		v.Error = message
		a.render(w, "new", status, v)
	}
	if v.User.Banned {
		again(http.StatusForbidden, "Your account cannot post.")
		return
	}
	if msg := a.spam(r); msg != "" {
		again(http.StatusBadRequest, msg)
		return
	}
	cat, err := a.categoryBySlug(r.Context(), v.Form["category"])
	if err != nil {
		again(http.StatusBadRequest, "Choose a category.")
		return
	}
	if cat.StaffOnly && !v.User.IsStaff() {
		again(http.StatusForbidden, "Only the team can post in this category.")
		return
	}
	if msg := validTitle(v.Form["title"]); msg != "" {
		again(http.StatusBadRequest, msg)
		return
	}
	if msg := validBody(v.Form["body"]); msg != "" {
		again(http.StatusBadRequest, msg)
		return
	}
	if !a.lim.allow("topic:"+strconv.FormatInt(v.User.ID, 10), 5, time.Hour) {
		again(http.StatusTooManyRequests, "You made many topics. Try again later.")
		return
	}
	id, err := a.createTopic(r.Context(), cat.ID, v.User.ID, v.Form["title"], v.Form["body"])
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/t/%d", id), http.StatusSeeOther)
}

func (a *App) reply(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		a.fail(w, r, http.StatusNotFound, "This topic does not exist.")
		return
	}
	user := a.currentUser(w, r)
	if user == nil {
		http.Redirect(w, r, "/login?next="+url.QueryEscape(fmt.Sprintf("/t/%d", id)), http.StatusSeeOther)
		return
	}
	t, err := a.topicByID(r.Context(), id)
	if err != nil || t.Deleted {
		a.fail(w, r, http.StatusNotFound, "This topic does not exist.")
		return
	}
	show := func(status int, message string) {
		v := a.view(w, r, t.Title)
		v.Topic, v.Error = t, message
		v.Form["body"] = r.PostFormValue("body")
		v.Page = pagesFor(t.Replies + 1)
		v.Pages = v.Page
		v.Base = fmt.Sprintf("/t/%d", t.ID)
		v.Posts, _ = a.posts(r.Context(), id, pageSize, (v.Page-1)*pageSize, v.User, a.now())
		a.render(w, "topic", status, v)
	}
	switch {
	case user.Banned:
		show(http.StatusForbidden, "Your account cannot post.")
		return
	case t.Locked && !user.IsStaff():
		show(http.StatusForbidden, "This topic is locked.")
		return
	}
	if msg := a.spam(r); msg != "" {
		show(http.StatusBadRequest, msg)
		return
	}
	if msg := validBody(r.PostFormValue("body")); msg != "" {
		show(http.StatusBadRequest, msg)
		return
	}
	if !a.lim.allow("post:"+strconv.FormatInt(user.ID, 10), 30, time.Hour) {
		show(http.StatusTooManyRequests, "You wrote many posts. Try again later.")
		return
	}
	postID, err := a.createReply(r.Context(), id, user.ID, r.PostFormValue("body"))
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	page := pagesFor(t.Replies + 2)
	http.Redirect(w, r, fmt.Sprintf("/t/%d?page=%d#p%d", id, page, postID), http.StatusSeeOther)
}

func (a *App) editForm(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	v := a.view(w, r, "Edit post")
	if !ok || v.User == nil {
		a.fail(w, r, http.StatusNotFound, "This post does not exist.")
		return
	}
	p, err := a.postByID(r.Context(), id)
	if err != nil {
		a.fail(w, r, http.StatusNotFound, "This post does not exist.")
		return
	}
	if canEdit, _ := canChange(v.User, p, a.now()); !canEdit {
		a.fail(w, r, http.StatusForbidden, "You cannot edit this post.")
		return
	}
	v.Post = &p
	v.Form["body"] = p.Body
	a.render(w, "edit", http.StatusOK, v)
}

func (a *App) edit(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	user := a.currentUser(w, r)
	if !ok || user == nil {
		a.fail(w, r, http.StatusNotFound, "This post does not exist.")
		return
	}
	p, err := a.postByID(r.Context(), id)
	if err != nil {
		a.fail(w, r, http.StatusNotFound, "This post does not exist.")
		return
	}
	if canEdit, _ := canChange(user, p, a.now()); !canEdit {
		a.fail(w, r, http.StatusForbidden, "You cannot edit this post.")
		return
	}
	if msg := validBody(r.PostFormValue("body")); msg != "" {
		v := a.view(w, r, "Edit post")
		v.Post, v.Error = &p, msg
		v.Form["body"] = r.PostFormValue("body")
		a.render(w, "edit", http.StatusBadRequest, v)
		return
	}
	if _, err := a.db.Exec(r.Context(), `UPDATE posts SET body = $2, edited_at = now() WHERE id = $1 AND NOT deleted`,
		id, strings.TrimSpace(r.PostFormValue("body"))); err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/t/%d#p%d", p.TopicID, p.ID), http.StatusSeeOther)
}

func (a *App) removePost(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	user := a.currentUser(w, r)
	if !ok || user == nil {
		a.fail(w, r, http.StatusNotFound, "This post does not exist.")
		return
	}
	p, err := a.postByID(r.Context(), id)
	if err != nil {
		a.fail(w, r, http.StatusNotFound, "This post does not exist.")
		return
	}
	if _, canDelete := canChange(user, p, a.now()); !canDelete {
		a.fail(w, r, http.StatusForbidden, "You cannot delete this post.")
		return
	}
	if err := a.deletePost(r.Context(), p); err != nil {
		a.serverError(w, r, err)
		return
	}
	if p.First {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/t/%d", p.TopicID), http.StatusSeeOther)
}

// topicAction is for the team: lock, unlock, pin, unpin, delete.
func (a *App) topicAction(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	user := a.currentUser(w, r)
	if !ok || !user.IsStaff() || user.Banned {
		a.fail(w, r, http.StatusForbidden, "Only the team can do this.")
		return
	}
	var query string
	switch r.PathValue("action") {
	case "lock":
		query = `UPDATE topics SET locked = true WHERE id = $1`
	case "unlock":
		query = `UPDATE topics SET locked = false WHERE id = $1`
	case "pin":
		query = `UPDATE topics SET pinned = true WHERE id = $1`
	case "unpin":
		query = `UPDATE topics SET pinned = false WHERE id = $1`
	case "delete":
		query = `UPDATE topics SET deleted = true WHERE id = $1`
	default:
		a.fail(w, r, http.StatusNotFound, "Unknown action.")
		return
	}
	if _, err := a.db.Exec(r.Context(), query, id); err != nil {
		a.serverError(w, r, err)
		return
	}
	if r.PathValue("action") == "delete" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/t/%d", id), http.StatusSeeOther)
}

func (a *App) modPage(w http.ResponseWriter, r *http.Request) {
	v := a.view(w, r, "Moderation")
	if !v.User.IsStaff() {
		a.fail(w, r, http.StatusForbidden, "Only the team can see this page.")
		return
	}
	v.Query = strings.TrimSpace(r.URL.Query().Get("q"))
	rows, err := a.db.Query(r.Context(), `
SELECT id, username, role, banned, created_at FROM users
WHERE $1 = '' OR username_lower LIKE '%' || lower($1) || '%'
ORDER BY id DESC LIMIT 50`, v.Query)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &u.Banned, &u.Created); err != nil {
			a.serverError(w, r, err)
			return
		}
		v.Users = append(v.Users, u)
	}
	a.render(w, "mod", http.StatusOK, v)
}

func (a *App) ban(w http.ResponseWriter, r *http.Request) {
	user := a.currentUser(w, r)
	if !user.IsStaff() || user.Banned {
		a.fail(w, r, http.StatusForbidden, "Only the team can do this.")
		return
	}
	id, err := strconv.ParseInt(r.PostFormValue("user"), 10, 64)
	if err != nil {
		a.fail(w, r, http.StatusBadRequest, "Unknown user.")
		return
	}
	ban := r.PostFormValue("ban") == "1"
	// A moderator cannot ban the team. Nobody bans themselves.
	tag, err := a.db.Exec(r.Context(), `UPDATE users SET banned = $2 WHERE id = $1 AND role = 'member' AND id <> $3`, id, ban, user.ID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		a.fail(w, r, http.StatusBadRequest, "This user cannot be changed.")
		return
	}
	if ban {
		_, _ = a.db.Exec(r.Context(), `DELETE FROM sessions WHERE user_id = $1`, id)
	}
	http.Redirect(w, r, "/mod", http.StatusSeeOther)
}

// ---- accounts ----

func (a *App) registerForm(w http.ResponseWriter, r *http.Request) {
	v := a.view(w, r, "Register")
	if v.User != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	a.render(w, "register", http.StatusOK, v)
}

func (a *App) register(w http.ResponseWriter, r *http.Request) {
	v := a.view(w, r, "Register")
	v.Form = map[string]string{"username": strings.TrimSpace(r.PostFormValue("username")), "email": strings.TrimSpace(r.PostFormValue("email"))}
	again := func(status int, message string) {
		v.Error = message
		a.render(w, "register", status, v)
	}
	if msg := a.spam(r); msg != "" {
		again(http.StatusBadRequest, msg)
		return
	}
	username, email, password := v.Form["username"], v.Form["email"], r.PostFormValue("password")
	if msg := validUsername(username); msg != "" {
		again(http.StatusBadRequest, msg)
		return
	}
	if addr, err := mail.ParseAddress(email); err != nil || addr.Address != email || len(email) > 254 {
		again(http.StatusBadRequest, "Enter a valid email address.")
		return
	}
	if msg := validPassword(password); msg != "" {
		again(http.StatusBadRequest, msg)
		return
	}
	if !a.lim.allow("register:"+a.clientIP(r), 5, time.Hour) {
		again(http.StatusTooManyRequests, "Too many new accounts from your address. Try again later.")
		return
	}
	hash, err := hashPassword(password)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	role := "member"
	if a.cfg.FirstUserAdmin {
		var n int
		if err := a.db.QueryRow(r.Context(), `SELECT count(*) FROM users`).Scan(&n); err == nil && n == 0 {
			role = "admin"
		}
	}
	var id int64
	err = a.db.QueryRow(r.Context(), `INSERT INTO users (username, username_lower, email_lower, pass_hash, role)
VALUES ($1, lower($1), lower($2), $3, $4) RETURNING id`, username, email, hash, role).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		again(http.StatusConflict, "This name or email is already used.")
		return
	}
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if err := a.startSession(w, r, id); err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *App) startSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	token, err := randomToken()
	if err != nil {
		return err
	}
	if _, err := a.db.Exec(r.Context(), `INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		tokenHash(token), userID, a.now().Add(sessionLife)); err != nil {
		return err
	}
	a.setCookie(w, sessionCookie, token, int(sessionLife.Seconds()))
	return nil
}

func (a *App) loginForm(w http.ResponseWriter, r *http.Request) {
	v := a.view(w, r, "Log in")
	v.Next = safeNext(r.URL.Query().Get("next"))
	a.render(w, "login", http.StatusOK, v)
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	v := a.view(w, r, "Log in")
	v.Next = safeNext(r.PostFormValue("next"))
	username := strings.TrimSpace(r.PostFormValue("username"))
	v.Form["username"] = username
	again := func(status int, message string) {
		v.Error = message
		a.render(w, "login", status, v)
	}
	if r.PostFormValue("website") != "" {
		again(http.StatusBadRequest, "The form was not accepted.")
		return
	}
	ip := a.clientIP(r)
	if !a.lim.allow("login-ip:"+ip, 30, 15*time.Minute) || !a.lim.allow("login:"+ip+":"+strings.ToLower(username), 10, 15*time.Minute) {
		again(http.StatusTooManyRequests, "Too many tries. Wait 15 minutes.")
		return
	}
	var id int64
	var hash string
	var banned bool
	err := a.db.QueryRow(r.Context(), `SELECT id, pass_hash, banned FROM users WHERE username_lower = lower($1)`, username).Scan(&id, &hash, &banned)
	if errors.Is(err, pgx.ErrNoRows) {
		hash = dummyHash
	} else if err != nil {
		a.serverError(w, r, err)
		return
	}
	ok := verifyPassword(r.PostFormValue("password"), hash) && err == nil
	if !ok {
		again(http.StatusUnauthorized, "Wrong name or password.")
		return
	}
	if banned {
		again(http.StatusForbidden, "This account is blocked.")
		return
	}
	// A new session at every login. Old ones of this browser are removed.
	if c, err := r.Cookie(sessionCookie); err == nil {
		_, _ = a.db.Exec(r.Context(), `DELETE FROM sessions WHERE token_hash = $1`, tokenHash(c.Value))
	}
	if err := a.startSession(w, r, id); err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, v.Next, http.StatusSeeOther)
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_, _ = a.db.Exec(r.Context(), `DELETE FROM sessions WHERE token_hash = $1`, tokenHash(c.Value))
	}
	a.setCookie(w, sessionCookie, "", -1)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
