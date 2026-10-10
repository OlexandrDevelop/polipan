// SPDX-License-Identifier: Apache-2.0

package state

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/polipan/polipan/proto/agentv1"
	"google.golang.org/protobuf/proto"
)

func sample(version int64) *agentv1.DesiredState {
	return &agentv1.DesiredState{Version: version, Workloads: []*agentv1.WorkloadSpec{{
		PlacementId: "plc_1", ContainerName: "pp-plc_1", ImageRef: "nginx@sha256:aa",
		Env:           map[string]string{"PLAIN": "visible", "DB_PASSWORD": "s3cret-value"},
		SecretEnvKeys: []string{"DB_PASSWORD"},
		Mounts: []*agentv1.Mount{{Kind: "file", Source: "key.pem", Target: "/etc/key.pem", Content: []byte("file-secret-text"), Secret: true},
			{Kind: "file", Source: "app.conf", Target: "/etc/app.conf", Content: []byte("plain-file-text")}},
		RegistryAuthRef: "reg_1",
	}}, RegistryAuths: []*agentv1.RegistryAuth{{Ref: "reg_1", Server: "ghcr.io", Username: "bot", Password: "registry-pass-value"}}}
}

// The state file holds the env secrets, the secret files and the registry passwords only sealed, with mode 0600.
func TestSecretsSealedAndRoundTrip(t *testing.T) {
	dir := t.TempDir()
	_ = os.Chmod(dir, 0o755)
	s, _ := New(dir)
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", st.Mode())
	}
	ok, err := s.SaveDesired(sample(3))
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "desired.json"))
	for _, secret := range []string{"s3cret-value", "registry-pass-value", "file-secret-text"} {
		if bytes.Contains(raw, []byte(secret)) || bytes.Contains(raw, []byte(base64.StdEncoding.EncodeToString([]byte(secret)))) {
			t.Fatalf("secret %q is readable in desired.json", secret)
		}
	}
	if !bytes.Contains(raw, []byte("visible")) {
		t.Fatal("plain value should stay readable")
	}
	st, _ := os.Stat(filepath.Join(dir, "desired.json"))
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	s2, _ := New(dir)
	got, err := s2.LoadDesired()
	if err != nil || !proto.Equal(got, sample(3)) {
		t.Fatalf("load: %v %v", got, err)
	}
	if ok, _ := s2.SaveDesired(sample(3)); ok {
		t.Fatal("same version must not replace")
	}
	if ok, _ := s2.SaveDesired(sample(2)); ok {
		t.Fatal("lower version must not replace")
	}
	if ok, _ := s2.SaveDesired(sample(4)); !ok {
		t.Fatal("higher version must replace")
	}
}

func TestCommandAcksKeepLast1000(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	for i := 0; i < 1005; i++ {
		id := "cmd_" + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676))
		_ = s.PutAck(&agentv1.CommandAck{CommandId: id, Ok: true, ResultJson: []byte("{}")})
	}
	if len(s.cmdAcks) != 1000 {
		t.Fatalf("kept %d", len(s.cmdAcks))
	}
	s2, _ := New(dir)
	if err := s2.LoadCommands(); err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.Ack("cmd_aaa"); ok {
		t.Fatal("oldest should be dropped")
	}
	if _, ok := s2.Ack(s.cmdOrder[999]); !ok {
		t.Fatal("newest should be kept")
	}
}
