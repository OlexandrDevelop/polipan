// SPDX-License-Identifier: Apache-2.0

package tpl_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/polipan/polipan/server/internal/tpl"
)

const bindFile = `
services:
  nginx:
    image: nginx
    volumes:
      - ./docker/nginx/default.conf.template:/etc/nginx/templates/default.conf.template:ro
      - ./docker/nginx/certs/site.pem:/etc/ssl/site.pem:ro
      - ./docker/nginx/server.conf:/etc/nginx/extra.conf
      - ./docker/php:/usr/local/etc/php/conf.d:ro
`

func bindOpts(o tpl.ComposeOptions) tpl.ComposeOptions {
	o.Repo = &tpl.ComposeRepo{URL: "https://example.com/a/b.git", Branch: "main"}
	o.SkipUnsafe = true
	return o
}

func lossAt(res *tpl.ComposeResult, key string) *tpl.Loss {
	for i, l := range res.Losses {
		if l.Path == "services.nginx.volumes["+key+"]" {
			return &res.Losses[i]
		}
	}
	return nil
}

func TestComposeBindSources(t *testing.T) {
	got := tpl.ComposeBindSources([]byte(bindFile), nil, "")
	want := []string{"docker/nginx/default.conf.template", "docker/nginx/server.conf", "docker/php"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("sources = %v, want %v (the .pem file is secret-like)", got, want)
	}
	if got := tpl.ComposeBindSources([]byte(bindFile), nil, "deploy"); got[0] != "deploy/docker/nginx/default.conf.template" {
		t.Fatalf("folder of the Compose file is not used: %v", got)
	}
}

// A tracked file becomes a file of the service, byte for byte, and reaches the config of the planned service.
func TestComposeBindTrackedFile(t *testing.T) {
	tmpl := "server { listen ${PORT}; location / { fastcgi_pass app:9000; } }\n"
	res, err := tpl.ImportComposeWith([]byte(bindFile), bindOpts(tpl.ComposeOptions{
		BindFiles: map[string][]byte{"docker/nginx/default.conf.template": []byte(tmpl)},
	}))
	if err != nil {
		t.Fatal(err)
	}
	files := res.Manifest.Services["nginx"].Files
	if len(files) != 1 || files[0].Path != "/etc/nginx/templates/default.conf.template" || files[0].Content != tmpl {
		t.Fatalf("files = %+v", files)
	}
	l := lossAt(res, "0")
	if l == nil || l.Disposition != tpl.LossImported || strings.Contains(l.Lost+l.Replacement, "fastcgi_pass") {
		t.Fatalf("loss = %+v", l)
	}
	if strings.Contains(res.ReplacementYAML, "TODO: paste the content of ./docker/nginx/default") {
		t.Fatalf("a placeholder is in the YAML for an imported file:\n%s", res.ReplacementYAML)
	}
	plan, err := res.Manifest.Expand(res.Inputs, tpl.Options{ServiceIDs: map[string]string{"nginx": "svc_1"}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(plan.Services[0].Config["files"])
	if !strings.Contains(string(b), "default.conf.template") || !strings.Contains(string(b), "${PORT}") {
		t.Fatalf("config files = %s", b)
	}
}

// A file that is not in the repository keeps the placeholder and a dropped loss. The reason is told.
func TestComposeBindMissingOrSkipped(t *testing.T) {
	res, err := tpl.ImportComposeWith([]byte(bindFile), bindOpts(tpl.ComposeOptions{
		BindSkipped: map[string]string{"docker/nginx/server.conf": "the file is larger than 1 MiB"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(res.Manifest.Services["nginx"].Files); n != 0 {
		t.Fatalf("%d files imported", n)
	}
	l := lossAt(res, "0")
	if l == nil || l.Disposition != tpl.LossDropped || !strings.Contains(l.Lost, "not in the repository") {
		t.Fatalf("missing file loss = %+v", l)
	}
	l = lossAt(res, "2")
	if l == nil || l.Disposition != tpl.LossDropped || !strings.Contains(l.Lost, "larger than 1 MiB") {
		t.Fatalf("skipped file loss = %+v", l)
	}
	if !strings.Contains(res.ReplacementYAML, "TODO: paste the content") {
		t.Fatal("the placeholder is gone")
	}
}

// A pasted file has no repository: nothing is read.
func TestComposeBindPasted(t *testing.T) {
	res, err := tpl.ImportComposeWith([]byte(bindFile), tpl.ComposeOptions{SkipUnsafe: true,
		BindFiles: map[string][]byte{"docker/nginx/default.conf.template": []byte("x")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Manifest.Services["nginx"].Files) != 0 {
		t.Fatal("a file was imported from a pasted Compose file")
	}
}

// A secret-like file is never imported, even when its content is given.
func TestComposeBindSecretLike(t *testing.T) {
	res, err := tpl.ImportComposeWith([]byte(bindFile), bindOpts(tpl.ComposeOptions{
		BindFiles: map[string][]byte{"docker/nginx/certs/site.pem": []byte("-----BEGIN PRIVATE KEY-----")},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Manifest.Services["nginx"].Files) != 0 || strings.Contains(res.ReplacementYAML, "BEGIN PRIVATE") {
		t.Fatalf("secret-like file imported: %+v", res.Manifest.Services["nginx"].Files)
	}
	if !strings.Contains(res.ReplacementYAML, "SITE_FILE") {
		t.Fatalf("no secret reference:\n%s", res.ReplacementYAML)
	}
}

func TestComposeBindFolderAndMarker(t *testing.T) {
	res, err := tpl.ImportComposeWith([]byte(bindFile), bindOpts(tpl.ComposeOptions{
		BindFiles: map[string][]byte{"docker/nginx/server.conf": []byte("a ${{secrets.X}} b")},
		BindDirs:  map[string]map[string][]byte{"docker/php": {"a.ini": []byte("a=1\n"), "sub/b.ini": []byte("b=2\n")}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	files := res.Manifest.Services["nginx"].Files
	if len(files) != 2 || files[0].Path != "/usr/local/etc/php/conf.d/a.ini" || files[1].Path != "/usr/local/etc/php/conf.d/sub/b.ini" {
		t.Fatalf("files = %+v", files)
	}
	if l := lossAt(res, "2"); l == nil || l.Disposition != tpl.LossDropped || !strings.Contains(l.Lost, "${{") {
		t.Fatalf("marker loss = %+v", l)
	}
}
