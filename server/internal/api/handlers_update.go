// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/server/internal/version"
)

// DefaultUpdaterDir is the folder the panel shares with the updater of the host (deploy/install.sh,
// polipan-update). The host writes status.json. The panel writes request (a check or an update) and auto
// (on or off). Every file is written whole and then renamed, so the host never reads half a file.
const DefaultUpdaterDir = "/var/lib/polipan/updater"

// updaterState is status.json of the host.
type updaterState struct {
	State     string `json:"state"`
	Message   string `json:"message"`
	Latest    string `json:"latest"`
	Version   string `json:"version"`
	CheckedAt string `json:"checked_at"`
	UpdatedAt string `json:"updated_at"`
}

func (h *Handlers) updaterDir() string {
	if h.UpdaterDir != "" {
		return h.UpdaterDir
	}
	return DefaultUpdaterDir
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func parseTime(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return &t
}

// systemUpdate reads the state of the updater. The folder is there only when the panel was installed with the
// install script: then Available is true.
func (h *Handlers) systemUpdate() gen.SystemUpdate {
	dir := h.updaterDir()
	available := false
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		available = true
	}
	out := gen.SystemUpdate{Available: &available}
	state := "unknown"
	var s updaterState
	if raw, err := os.ReadFile(filepath.Join(dir, "status.json")); err == nil && json.Unmarshal(raw, &s) == nil {
		if s.State != "" {
			state = s.State
		}
		out.Message = &s.Message
		out.LatestVersion = &s.Version
		out.CheckedAt = parseTime(s.CheckedAt)
		out.UpdatedAt = parseTime(s.UpdatedAt)
	}
	stateName := gen.SystemUpdateState(state)
	out.State = &stateName
	v := version.Version
	out.Version = &v
	auto := false
	if raw, err := os.ReadFile(filepath.Join(dir, "auto")); err == nil {
		auto = strings.TrimSpace(string(raw)) == "on"
	}
	out.AutoUpdate = &auto
	_, err := os.Stat(filepath.Join(dir, "request"))
	pending := err == nil
	out.Pending = &pending
	return out
}

// GetSystemUpdate answers with the version of the panel and the state of the update.
func (h *Handlers) GetSystemUpdate(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, h.systemUpdate())
}

// UpdateSystem asks the host to look for a new version or to update, or turns the nightly update on or off.
// The panel never runs the update itself: it runs on the host, as root, and restarts this server.
func (h *Handlers) UpdateSystem(w http.ResponseWriter, r *http.Request) {
	var body gen.SystemUpdateRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	dir := h.updaterDir()
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		WriteError(w, r, http.StatusConflict, "update_unavailable", "Updates need a panel that was installed with the install script.", nil)
		return
	}
	cur := h.systemUpdate()
	switch body.Action {
	case gen.Check, gen.Apply:
		if *cur.Pending || (cur.State != nil && *cur.State == "updating") {
			WriteError(w, r, http.StatusConflict, "update_in_progress", "An update or a check is already running.", nil)
			return
		}
		if err := writeAtomic(filepath.Join(dir, "request"), []byte(string(body.Action)+"\n")); err != nil {
			internalError(w, r, err)
			return
		}
		pending := true
		cur.Pending = &pending
	case gen.AutoOn, gen.AutoOff:
		value := "off"
		if body.Action == gen.AutoOn {
			value = "on"
		}
		if err := writeAtomic(filepath.Join(dir, "auto"), []byte(value+"\n")); err != nil {
			internalError(w, r, err)
			return
		}
		on := value == "on"
		cur.AutoUpdate = &on
	default:
		invalid(w, r, "action", "must be check, apply, auto_on or auto_off")
		return
	}
	p := PrincipalFrom(r.Context())
	h.auditUser(r, p.OrgID, "", "system.update", "org", p.OrgID, map[string]any{"action": string(body.Action)})
	WriteJSON(w, http.StatusAccepted, cur)
}
