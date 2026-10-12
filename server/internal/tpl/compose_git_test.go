// SPDX-License-Identifier: Apache-2.0

package tpl_test

import (
	"strings"
	"testing"

	"github.com/polipan/polipan/server/internal/tpl"
)

// A Compose file like the one of a Laravel development stack: services that are built from the repository,
// a service with the Docker socket, bind mounts of the host, and variables without a value.
const stackFile = `
services:
  portainer:
    image: portainer/portainer
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - /opt/portainer:/data
  nginx:
    build:
      context: ./dockerfiles
      dockerfile: nginx.dockerfile
      args:
        - UID=${UID:-1000}
        - GID=${GID:-1000}
    ports:
      - "${FORWARD_NGINX_PORT:-80}:443"
    volumes:
      - ./src:/var/www/html:delegated
      - ./ssl:/etc/ssl
    depends_on:
      - php
      - postgresql
  php:
    build:
      context: ./dockerfiles
      dockerfile: php.dockerfile
    volumes:
      - ./src:/var/www/html
  postgresql:
    image: postgres:16.2
    environment:
      POSTGRES_DB: ${DB_DATABASE}
      PGDATA: /data/postgres
    volumes:
      - postgresql:/data/postgres
    ports:
      - "5432:5432"
  composer:
    build:
      context: ./dockerfiles
      dockerfile: php.dockerfile
    entrypoint: ["composer"]
volumes:
  postgresql: {}
`

func TestComposeFileRefusedWithoutSkip(t *testing.T) {
	_, err := tpl.ImportComposeWith([]byte(stackFile), tpl.ComposeOptions{Repo: &tpl.ComposeRepo{URL: "https://example.com/a/b.git", Branch: "main"}})
	var unsafe *tpl.UnsafeError
	if err == nil || !strings.Contains(err.Error(), "not safe") {
		t.Fatalf("an unsafe file must be refused, got %v %T", err, unsafe)
	}
}

func TestComposeFromRepoSkipsUnsafeAndBuilds(t *testing.T) {
	res, err := tpl.ImportComposeWith([]byte(stackFile), tpl.ComposeOptions{
		Repo:       &tpl.ComposeRepo{URL: "https://example.com/a/b.git", Branch: "main", Dir: ""},
		SkipUnsafe: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.Manifest.Services["portainer"]; ok {
		t.Error("the service with the Docker socket must be skipped")
	}
	for _, name := range []string{"nginx", "php", "postgresql", "composer"} {
		if _, ok := res.Manifest.Services[name]; !ok {
			t.Errorf("service %s is missing", name)
		}
	}
	nginx := res.Manifest.Services["nginx"]
	if nginx.Source == nil || nginx.Source.Git == nil {
		t.Fatal("nginx must be built from the repository")
	}
	g := nginx.Source.Git
	if g.Repo != "https://example.com/a/b.git" || g.Branch != "main" || g.Path != "/dockerfiles" || g.Dockerfile != "nginx.dockerfile" {
		t.Errorf("wrong source: %+v", g)
	}
	if g.BuildArgs["UID"] != "1000" || g.BuildArgs["GID"] != "1000" {
		t.Errorf("the defaults of the arguments must be filled in: %v", g.BuildArgs)
	}
	if len(nginx.Volumes) != 0 || len(res.Manifest.Services["php"].Volumes) != 0 {
		t.Errorf("the bind mounts of host folders must be dropped: %+v", nginx.Volumes)
	}
	if db := res.Manifest.Services["postgresql"]; db.Source == nil || db.Source.Image == nil || db.Source.Image.Name != "postgres:16.2" || len(db.Volumes) != 1 {
		t.Errorf("the database must stay an image with its named volume: %+v", db)
	}
	all := strings.Join(res.Warnings, "\n")
	for _, want := range []string{"service portainer is skipped", "Docker socket", "DB_DATABASE", "looks like a command that runs once"} {
		if !strings.Contains(all, want) {
			t.Errorf("warnings must say %q:\n%s", want, all)
		}
	}
}

func TestComposeBuildContextStaysInside(t *testing.T) {
	repo := &tpl.ComposeRepo{URL: "https://example.com/a/b.git", Branch: "main", Dir: "deploy"}
	for _, ctx := range []string{"../../etc", "/abs", "https://example.com/x.git"} {
		doc := "services:\n  a:\n    build: " + ctx + "\n"
		if _, err := tpl.ImportComposeWith([]byte(doc), tpl.ComposeOptions{Repo: repo}); err == nil {
			t.Errorf("context %q must be refused", ctx)
		}
	}
	res, err := tpl.ImportComposeWith([]byte("services:\n  a:\n    build: ./app\n"), tpl.ComposeOptions{Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Manifest.Services["a"].Source.Git.Path; got != "/deploy/app" {
		t.Errorf("the folder of the file is part of the path, got %s", got)
	}
}

func TestComposeBuildWithoutRepoIsStillRefused(t *testing.T) {
	_, err := tpl.ImportCompose([]byte("services:\n  a:\n    build: .\n"))
	if err == nil {
		t.Fatal("build: needs a repository")
	}
}

func TestComposeVariables(t *testing.T) {
	res, err := tpl.ImportComposeWith([]byte("services:\n  db:\n    image: postgres:16\n    environment:\n      POSTGRES_DB: ${NAME}\n      X: ${MISSING:-fallback}\n"),
		tpl.ComposeOptions{Vars: map[string]string{"NAME": "app"}})
	if err != nil {
		t.Fatal(err)
	}
	env := res.Manifest.Services["db"].Env
	if env["POSTGRES_DB"] != "app" || env["X"] != "fallback" {
		t.Errorf("variables are not filled in: %v", env)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("no warning is expected: %v", res.Warnings)
	}
}
