// SPDX-License-Identifier: Apache-2.0

// Command forum is a small forum for the Polipan community.
package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed templates/*.html
var templateFiles embed.FS

//go:embed static
var staticFiles embed.FS

type App struct {
	cfg  *Config
	db   *pgxpool.Pool
	tmpl map[string]*template.Template
	lim  *limiter
	now  func() time.Time
}

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "healthcheck":
		os.Exit(healthcheck())
	case "serve":
		if err := serve(); err != nil {
			log.Fatalf("forum: %v", err)
		}
	case "make-admin":
		if len(os.Args) != 3 {
			log.Fatal("usage: forum make-admin <username>")
		}
		if err := makeAdmin(os.Args[2]); err != nil {
			log.Fatalf("forum: %v", err)
		}
	default:
		log.Fatalf("unknown command %q (use serve, make-admin or healthcheck)", cmd)
	}
}

func healthcheck() int {
	addr := env("FORUM_ADDR", ":8080")
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	client := http.Client{Timeout: 3 * time.Second}
	res, err := client.Get("http://" + addr + "/healthz")
	if err != nil || res.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func openApp(ctx context.Context) (*App, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	if err := migrate(ctx, pool); err != nil {
		return nil, err
	}
	a := &App{cfg: cfg, db: pool, lim: newLimiter(), now: time.Now}
	if err := a.loadTemplates(); err != nil {
		return nil, err
	}
	return a, nil
}

func makeAdmin(username string) error {
	ctx := context.Background()
	a, err := openApp(ctx)
	if err != nil {
		return err
	}
	defer a.db.Close()
	tag, err := a.db.Exec(ctx, `UPDATE users SET role = 'admin' WHERE username_lower = $1`, strings.ToLower(username))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("no such user")
	}
	fmt.Printf("%s is an admin now\n", username)
	return nil
}

func serve() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	a, err := openApp(ctx)
	if err != nil {
		return err
	}
	defer a.db.Close()
	go a.cleanSessions(ctx)

	srv := &http.Server{
		Addr:              a.cfg.Addr,
		Handler:           a.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Printf("forum: listening on %s", a.cfg.Addr)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

func (a *App) cleanSessions(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = a.db.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now()`)
		}
	}
}

func (a *App) loadTemplates() error {
	funcs := template.FuncMap{
		"render": renderPost,
		"date":   func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04") },
		"ago":    ago,
		"add":    func(a, b int) int { return a + b },
		"sub":    func(a, b int) int { return a - b },
		"deref":  func(t *time.Time) time.Time { return *t },
	}
	pages, err := fs.Glob(templateFiles, "templates/*.html")
	if err != nil {
		return err
	}
	a.tmpl = map[string]*template.Template{}
	for _, p := range pages {
		name := strings.TrimSuffix(strings.TrimPrefix(p, "templates/"), ".html")
		if name == "layout" {
			continue
		}
		t, err := template.New("layout").Funcs(funcs).ParseFS(templateFiles, "templates/layout.html", p)
		if err != nil {
			return err
		}
		a.tmpl[name] = t
	}
	return nil
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return t.UTC().Format("2006-01-02")
}
