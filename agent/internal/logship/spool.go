// SPDX-License-Identifier: Apache-2.0

package logship

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Cursor is the read position in the log of one container. TS is the time of the last line read, in
// nanoseconds, and N is how many lines with exactly that time were read. A new reader starts at TS
// and skips N lines with that time, so a restart loses and repeats nothing (REQ-OBS-102).
type Cursor struct {
	TS int64 `json:"ts"`
	N  int   `json:"n"`
}

// stateFile is state.json. It holds what is already confirmed by the server (or dropped).
type stateFile struct {
	Epoch   string            `json:"epoch"`
	Next    uint64            `json:"next"` // the next batch number
	Cursors map[string]Cursor `json:"cursors"`
}

// header is the first line of a batch file. The rest of the file is the body that goes to the server.
type header struct {
	N       uint64            `json:"n"`
	Lines   int               `json:"lines"`
	Cursors map[string]Cursor `json:"cursors"`
}

type batchFile struct {
	n    uint64
	size int64
	path string
}

// spool keeps the batches on disk. A batch file holds the lines and the cursors that come after them, so
// they are saved in one atomic step (write to a temp file, fsync, rename). state.json moves on only after
// the server has taken the batch. On start, the cursors of the batches still on disk are applied on top of
// state.json, so the readers continue after the last batch that was written.
type spool struct {
	dir string

	mu    sync.Mutex
	st    stateFile
	live  map[string]Cursor // state cursors + the cursors of the batches on disk
	files []batchFile       // sorted by n
	total int64
}

func openSpool(dir string) (_ *spool, err error) {
	if err := os.MkdirAll(filepath.Join(dir, "batches"), 0o700); err != nil {
		return nil, err
	}
	sp := &spool{dir: dir}
	if b, err := os.ReadFile(sp.statePath()); err == nil {
		if json.Unmarshal(b, &sp.st) != nil || sp.st.Epoch == "" {
			sp.st = stateFile{} // damaged: start a new epoch, the server sees it as a new agent run
		}
	}
	if sp.st.Epoch == "" {
		var r [8]byte
		if _, err := rand.Read(r[:]); err != nil {
			return nil, err
		}
		sp.st.Epoch = hex.EncodeToString(r[:])
		// Saved at once: the batches that are written later use this epoch, also after a crash.
		defer func() {
			if err == nil {
				err = sp.saveState()
			}
		}()
	}
	if sp.st.Next == 0 {
		sp.st.Next = 1
	}
	if sp.st.Cursors == nil {
		sp.st.Cursors = map[string]Cursor{}
	}
	sp.live = map[string]Cursor{}
	for k, v := range sp.st.Cursors {
		sp.live[k] = v
	}
	ents, err := os.ReadDir(filepath.Join(dir, "batches"))
	if err != nil {
		return nil, err
	}
	for _, e := range ents {
		p := filepath.Join(dir, "batches", e.Name())
		if strings.HasSuffix(e.Name(), ".tmp") {
			_ = os.Remove(p) // a write that did not finish: its lines are read again
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSuffix(e.Name(), ".batch"), 10, 64)
		if err != nil {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		sp.files = append(sp.files, batchFile{n: n, size: fi.Size(), path: p})
	}
	sort.Slice(sp.files, func(i, j int) bool { return sp.files[i].n < sp.files[j].n })
	good := sp.files[:0]
	for _, f := range sp.files {
		h, _, err := readBatch(f.path)
		if err != nil {
			_ = os.Remove(f.path)
			continue
		}
		for k, v := range h.Cursors {
			sp.live[k] = v
		}
		if f.n >= sp.st.Next {
			sp.st.Next = f.n + 1
		}
		sp.total += f.size
		good = append(good, f)
	}
	sp.files = good
	return sp, nil
}

func (sp *spool) statePath() string { return filepath.Join(sp.dir, "state.json") }

func (sp *spool) batchPath(n uint64) string {
	return filepath.Join(sp.dir, "batches", fmt.Sprintf("%012d.batch", n))
}

func (sp *spool) epoch() string {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	return sp.st.Epoch
}

// cursor returns the position after the last batch that was written (or the last line the server took).
func (sp *spool) cursor(id string) Cursor {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	return sp.live[id]
}

// add writes a batch and moves the live cursors in one step. It returns the batch number.
func (sp *spool) add(cursors map[string]Cursor, lines int, body []byte) (uint64, error) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	n := sp.st.Next
	hb, _ := json.Marshal(header{N: n, Lines: lines, Cursors: cursors})
	data := append(append(hb, '\n'), body...)
	p := sp.batchPath(n)
	if err := writeFileSync(p, data); err != nil {
		return 0, err
	}
	sp.st.Next = n + 1
	for k, v := range cursors {
		sp.live[k] = v
	}
	sp.files = append(sp.files, batchFile{n: n, size: int64(len(data)), path: p})
	sp.total += int64(len(data))
	return n, nil
}

// oldest returns the first batch that is waiting, if any.
func (sp *spool) oldest() (batchFile, bool) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if len(sp.files) == 0 {
		return batchFile{}, false
	}
	return sp.files[0], true
}

// finish ends a batch that the server took (or that is dropped): the state moves on, then the file goes.
// If the process dies between the two steps, the batch is sent again and the server ignores it.
func (sp *spool) finish(n uint64) error {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	i := -1
	for j, f := range sp.files {
		if f.n == n {
			i = j
			break
		}
	}
	if i < 0 {
		return nil // dropped by the size cap while it was sent
	}
	f := sp.files[i]
	if h, _, err := readBatch(f.path); err == nil {
		for k, v := range h.Cursors {
			sp.st.Cursors[k] = v
		}
	}
	if err := sp.saveState(); err != nil {
		return err
	}
	if err := os.Remove(f.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	sp.files = append(sp.files[:i], sp.files[i+1:]...)
	sp.total -= f.size
	return nil
}

// trim drops the oldest batches until the spool is not larger than max bytes. It returns how many it dropped.
// A batch that is dropped moves the state too, so its lines are not read again.
func (sp *spool) trim(max int64) (dropped int, err error) {
	for {
		sp.mu.Lock()
		over := sp.total > max && len(sp.files) > 1 // the newest batch stays
		var n uint64
		if over {
			n = sp.files[0].n
		}
		sp.mu.Unlock()
		if !over {
			return dropped, nil
		}
		if err := sp.finish(n); err != nil {
			return dropped, err
		}
		dropped++
	}
}

// prune forgets the cursors of containers that are gone.
func (sp *spool) prune(keep func(id string) bool) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	for id := range sp.st.Cursors {
		if !keep(id) {
			delete(sp.st.Cursors, id)
		}
	}
	for id := range sp.live {
		if !keep(id) {
			delete(sp.live, id)
		}
	}
}

func (sp *spool) saveState() error {
	b, _ := json.Marshal(sp.st)
	return writeFileSync(sp.statePath(), b)
}

func (sp *spool) size() int64 {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	return sp.total
}

// readBatch returns the header and the body of a batch file.
func readBatch(path string) (header, []byte, error) {
	var h header
	b, err := os.ReadFile(path)
	if err != nil {
		return h, nil, err
	}
	i := bytes.IndexByte(b, '\n')
	if i < 0 {
		return h, nil, errors.New("logship: batch file has no header")
	}
	if err := json.Unmarshal(b[:i], &h); err != nil {
		return h, nil, err
	}
	return h, b[i+1:], nil
}

// writeFileSync writes the file in one atomic step: temp file, fsync, rename, fsync of the folder.
func writeFileSync(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
