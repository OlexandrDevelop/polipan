// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadBindSource(t *testing.T) {
	root := t.TempDir()
	write := func(rel string, b []byte) {
		t.Helper()
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("ok.conf", []byte("a\n"))
	write("big.conf", bytes.Repeat([]byte("a"), maxConfigFileBytes+1))
	write("bin.dat", []byte{0, 1, 2})
	write("outside.txt", []byte("secret"))
	write("dir/a.conf", []byte("a"))
	write("dir/b.conf", []byte("b"))
	write("linkdir/a.conf", []byte("a"))
	write("many/placeholder", nil)
	for i := 0; i < maxBindDirFiles+1; i++ {
		write(filepath.Join("many", string(rune('a'+i))), []byte("x"))
	}
	write("fat/a", bytes.Repeat([]byte("a"), maxBindDirBytes+1))
	if err := os.Symlink("outside.txt", filepath.Join(root, "link.conf")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("ok.conf", filepath.Join(root, "linkdir", "l.conf")); err != nil {
		t.Fatal(err)
	}
	files, dirs, skipped := map[string][]byte{}, map[string]map[string][]byte{}, map[string]string{}
	for _, p := range []string{"ok.conf", "big.conf", "bin.dat", "link.conf", "dir", "linkdir", "many", "fat", "gone.conf"} {
		readBindSource(root, p, files, dirs, skipped)
	}
	if string(files["ok.conf"]) != "a\n" || len(dirs["dir"]) != 2 {
		t.Fatalf("files=%v dirs=%v", files, dirs)
	}
	for _, p := range []string{"big.conf", "bin.dat", "link.conf", "linkdir", "many", "fat"} {
		if skipped[p] == "" {
			t.Errorf("%s was not skipped", p)
		}
		if _, ok := files[p]; ok {
			t.Errorf("%s was read", p)
		}
		if _, ok := dirs[p]; ok {
			t.Errorf("%s was read", p)
		}
	}
	if _, ok := skipped["gone.conf"]; ok || len(skipped) != 6 {
		t.Fatalf("skipped = %v", skipped)
	}
	if !strings.Contains(skipped["link.conf"], "symbolic") {
		t.Fatalf("link reason: %s", skipped["link.conf"])
	}
}
