// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func testServer(t *testing.T, h http.Handler) *HTTPClient {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "d.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewUnstartedServer(h)
	s.Listener = l
	s.Start()
	t.Cleanup(s.Close)
	return New(sock)
}

func TestCreateBody(t *testing.T) {
	var got map[string]any
	var name string
	started := false
	c := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/create"):
			name = r.URL.Query().Get("name")
			_ = json.NewDecoder(r.Body).Decode(&got)
			_, _ = w.Write([]byte(`{"Id":"abc"}`))
		case strings.HasSuffix(r.URL.Path, "/containers/abc/start"):
			started = true
			w.WriteHeader(204)
		}
	}))
	id, err := c.CreateAndStart(context.Background(), ContainerSpec{
		Name: "pp-1", Image: "nginx@sha256:aa", Network: "polipan", Aliases: []string{"pp-1", "web"},
		Env: map[string]string{"A": "b"}, RestartPolicy: "always", DNS: []string{"172.30.0.1"},
		Ports: []PortBinding{{HostPort: 8080, ContainerPort: 80, Proto: "tcp"}},
	})
	if err != nil || id != "abc" || !started || name != "pp-1" {
		t.Fatalf("id=%q err=%v started=%v name=%q", id, err, started, name)
	}
	host := got["HostConfig"].(map[string]any)
	if host["NetworkMode"] != "polipan" || host["RestartPolicy"].(map[string]any)["Name"] != "always" {
		t.Fatalf("host config: %v", host)
	}
	if _, ok := host["PortBindings"].(map[string]any)["80/tcp"]; !ok {
		t.Fatalf("port bindings: %v", host["PortBindings"])
	}
	nc := got["NetworkingConfig"].(map[string]any)["EndpointsConfig"].(map[string]any)["polipan"].(map[string]any)
	if len(nc["Aliases"].([]any)) != 2 {
		t.Fatalf("aliases: %v", nc)
	}
}

func frame(stream byte, s string) []byte {
	b := make([]byte, 8, 8+len(s))
	b[0] = stream
	binary.BigEndian.PutUint32(b[4:], uint32(len(s)))
	return append(b, s...)
}

func TestLogsDemux(t *testing.T) {
	c := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("tail") != "200" {
			t.Errorf("tail = %q", r.URL.Query().Get("tail"))
		}
		_, _ = w.Write(frame(1, "hello\n"))
		_, _ = w.Write(frame(2, "oops\n"))
	}))
	out, err := c.Logs(context.Background(), "x", 200)
	if err != nil || out != "hello\noops\n" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestInspectNotFound(t *testing.T) {
	c := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"message":"No such container"}`))
	}))
	_, err := c.Inspect(context.Background(), "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}
