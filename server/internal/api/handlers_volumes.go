// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"regexp"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/server/internal/store"
	"github.com/polipan/polipan/server/internal/vars"
)

var volumeNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)

// maxFileMount is the size limit of a file mount (REQ-VOL-006).
const maxFileMount = 1 << 20

// validMountPath checks a path in the container: absolute, clean and not the root.
func validMountPath(p string) bool {
	return len(p) > 1 && len(p) <= 4096 && p[0] == '/' && path.Clean(p) == p
}

// checkMountConfig checks the files and secret_files of a service config (REQ-VOL-006, REQ-SEC-005).
// files: [{path, content}] up to 1 MiB each, secret_files: [{path, secret}] where secret is a secret name.
func checkMountConfig(cfg map[string]any) error {
	paths := map[string]bool{}
	use := func(p string) error {
		if !validMountPath(p) {
			return fmt.Errorf("the path %q must be a clean absolute path", p)
		}
		if paths[p] {
			return fmt.Errorf("two mounts use the path %q", p)
		}
		paths[p] = true
		return nil
	}
	decode := func(key string, dst any) error {
		raw, ok := cfg[key]
		if !ok {
			return nil
		}
		b, _ := json.Marshal(raw)
		if err := json.Unmarshal(b, dst); err != nil {
			return fmt.Errorf("config.%s must be a list of objects", key)
		}
		return nil
	}
	var files []map[string]any
	if err := decode("files", &files); err != nil {
		return err
	}
	for _, f := range files {
		p, _ := f["path"].(string)
		c, ok := f["content"].(string)
		if !ok || len(f) != 2 {
			return errors.New("config.files items need path and content (text) only")
		}
		if len(c) > maxFileMount {
			return fmt.Errorf("config.files %s is larger than 1 MiB", p)
		}
		if err := use(p); err != nil {
			return fmt.Errorf("config.files: %w", err)
		}
	}
	var secretFiles []map[string]any
	if err := decode("secret_files", &secretFiles); err != nil {
		return err
	}
	for _, f := range secretFiles {
		p, _ := f["path"].(string)
		name, _ := f["secret"].(string)
		if len(f) != 2 || vars.CheckName(name) != nil {
			return errors.New("config.secret_files items need path and secret (a secret name) only")
		}
		if err := use(p); err != nil {
			return fmt.Errorf("config.secret_files: %w", err)
		}
	}
	return nil
}

// configPaths returns the file mount paths of a stored service config.
func configPaths(raw json.RawMessage) map[string]bool {
	var c struct {
		Files       []vars.File       `json:"files"`
		SecretFiles []vars.SecretFile `json:"secret_files"`
	}
	_ = json.Unmarshal(raw, &c)
	out := map[string]bool{}
	for _, f := range c.Files {
		out[f.Path] = true
	}
	for _, f := range c.SecretFiles {
		out[f.Path] = true
	}
	return out
}

func volumeJSON(v *store.Volume) gen.Volume {
	kind, mode := gen.VolumeKind(v.Kind), gen.VolumeAccessMode(v.AccessMode)
	out := gen.Volume{Id: &v.ID, Name: &v.Name, Kind: &kind, MountPath: &v.MountPath, NodeId: v.NodeID, AccessMode: &mode}
	if v.SizeLimitBytes != nil {
		out.SizeLimitBytes = ptr(int(*v.SizeLimitBytes))
	}
	return out
}

// mountPathFree checks that no other volume and no file mount of the service uses the path (C06 edge cases).
func (h *Handlers) mountPathFree(w http.ResponseWriter, r *http.Request, svc *store.Service, mountPath, exceptID string) bool {
	if !validMountPath(mountPath) {
		invalid(w, r, "mount_path", "mount_path must be a clean absolute path, not /")
		return false
	}
	list, err := h.Store.ListVolumes(r.Context(), svc.ID)
	if fail(w, r, err) {
		return false
	}
	for _, v := range list {
		if v.ID != exceptID && v.MountPath == mountPath {
			invalid(w, r, "mount_path", "another volume of the service uses this path")
			return false
		}
	}
	if configPaths(svc.Config)[mountPath] {
		invalid(w, r, "mount_path", "a file mount of the service uses this path")
		return false
	}
	return true
}

// checkVolumeFields checks access mode and size limit and fills the defaults.
func checkVolumeFields(w http.ResponseWriter, r *http.Request, body gen.VolumeWrite, v *store.Volume) bool {
	if body.AccessMode != nil {
		v.AccessMode = string(*body.AccessMode)
	}
	if v.AccessMode != "single_writer" && v.AccessMode != "shared" {
		invalid(w, r, "access_mode", "access_mode must be single_writer or shared")
		return false
	}
	if body.SizeLimitBytes != nil {
		if *body.SizeLimitBytes < 0 {
			invalid(w, r, "size_limit_bytes", "size_limit_bytes must be 0 or more")
			return false
		}
		n := int64(*body.SizeLimitBytes)
		v.SizeLimitBytes = &n
		if n == 0 {
			v.SizeLimitBytes = nil
		}
	}
	return true
}

// ListVolumes lists the volumes of a service.
func (h *Handlers) ListVolumes(w http.ResponseWriter, r *http.Request, serviceID gen.ServiceId) {
	if _, err := h.Store.GetService(r.Context(), h.orgID(r), serviceID); fail(w, r, err) {
		return
	}
	list, err := h.Store.ListVolumes(r.Context(), serviceID)
	if fail(w, r, err) {
		return
	}
	out := make([]gen.Volume, len(list))
	for i := range list {
		out[i] = volumeJSON(&list[i])
	}
	WriteJSON(w, http.StatusOK, out)
}

// CreateVolume adds a volume. It is mounted from the next deployment on (no restart now).
func (h *Handlers) CreateVolume(w http.ResponseWriter, r *http.Request, serviceID gen.ServiceId) {
	var body gen.VolumeWrite
	if !decodeJSON(w, r, &body) {
		return
	}
	svc, err := h.Store.GetService(r.Context(), h.orgID(r), serviceID)
	if fail(w, r, err) {
		return
	}
	if denyProtected(w, r, "volume:manage", svc.EnvProtected) {
		return
	}
	if h.denyManaged(w, r, serviceID, "volumes") {
		return
	}
	v := store.Volume{Kind: "volume", AccessMode: "single_writer"}
	if body.Name != nil {
		v.Name = *body.Name
	}
	if body.Kind != nil {
		v.Kind = string(*body.Kind)
	}
	if body.MountPath != nil {
		v.MountPath = *body.MountPath
	}
	switch {
	case !volumeNameRe.MatchString(v.Name):
		invalid(w, r, "name", "name must use lower case letters, digits, '.', '_' and '-' (1 to 63 characters)")
		return
	case v.Kind != "volume" && v.Kind != "bind" && v.Kind != "tmpfs":
		invalid(w, r, "kind", "kind must be volume, bind or tmpfs")
		return
	case !checkVolumeFields(w, r, body, &v), !h.mountPathFree(w, r, svc, v.MountPath, ""):
		return
	}
	out, err := h.Store.CreateVolume(r.Context(), svc, v, actor(r))
	if errors.Is(err, store.ErrNameConflict) {
		WriteError(w, r, http.StatusConflict, CodeNameConflict, "a volume with this name exists in the service", map[string]any{"field": "name"})
		return
	}
	if fail(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusCreated, volumeJSON(out))
}

// UpdateVolume changes the mount path, the size limit or the access mode. Name and kind cannot change.
func (h *Handlers) UpdateVolume(w http.ResponseWriter, r *http.Request, volumeID gen.VolumeId) {
	var body gen.VolumeWrite
	if !decodeJSON(w, r, &body) {
		return
	}
	cur, err := h.Store.GetVolume(r.Context(), h.orgID(r), volumeID)
	if fail(w, r, err) {
		return
	}
	svc, err := h.Store.GetService(r.Context(), h.orgID(r), cur.ServiceID)
	if fail(w, r, err) {
		return
	}
	if denyProtected(w, r, "volume:manage", svc.EnvProtected) {
		return
	}
	if h.denyManaged(w, r, cur.ServiceID, "volumes") {
		return
	}
	if (body.Name != nil && *body.Name != cur.Name) || (body.Kind != nil && string(*body.Kind) != cur.Kind) {
		invalid(w, r, "name", "name and kind of a volume cannot change")
		return
	}
	v := *cur
	if body.MountPath != nil {
		v.MountPath = *body.MountPath
	}
	if !checkVolumeFields(w, r, body, &v) || !h.mountPathFree(w, r, svc, v.MountPath, cur.ID) {
		return
	}
	out, err := h.Store.UpdateVolume(r.Context(), svc, cur, v, actor(r))
	if fail(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusOK, volumeJSON(out))
}

// DeleteVolume removes a volume from the service. The next deployment does not mount it.
func (h *Handlers) DeleteVolume(w http.ResponseWriter, r *http.Request, volumeID gen.VolumeId, params gen.DeleteVolumeParams) {
	cur, err := h.Store.GetVolume(r.Context(), h.orgID(r), volumeID)
	if fail(w, r, err) {
		return
	}
	svc, err := h.Store.GetService(r.Context(), h.orgID(r), cur.ServiceID)
	if fail(w, r, err) {
		return
	}
	if denyProtected(w, r, "volume:manage", svc.EnvProtected) {
		return
	}
	if h.denyManaged(w, r, cur.ServiceID, "volumes") {
		return
	}
	deleteData := params.DeleteData != nil && *params.DeleteData
	if fail(w, r, h.Store.DeleteVolume(r.Context(), svc.OrgID, svc.ProjectID, cur, deleteData, actor(r))) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListOrphanVolumes lists the volumes on a node whose service was deleted (REQ-VOL-004).
func (h *Handlers) ListOrphanVolumes(w http.ResponseWriter, r *http.Request, nodeID gen.NodeId) {
	if _, err := h.Store.GetNode(r.Context(), h.orgID(r), nodeID); fail(w, r, err) {
		return
	}
	list, err := h.Store.ListOrphanVolumes(r.Context(), h.orgID(r), nodeID)
	if fail(w, r, err) {
		return
	}
	type orphan struct {
		Name        string `json:"name"`
		SizeBytes   *int   `json:"size_bytes,omitempty"`
		LastService string `json:"last_service"`
	}
	out := make([]orphan, len(list))
	for i, o := range list {
		out[i] = orphan{Name: o.Name, LastService: o.LastService}
	}
	WriteJSON(w, http.StatusOK, out)
}

// DeleteOrphanVolume removes an orphan volume record. Deleting the data on the node needs an
// agent command that the proto does not have yet.
func (h *Handlers) DeleteOrphanVolume(w http.ResponseWriter, r *http.Request, nodeID gen.NodeId, name string) {
	if fail(w, r, h.Store.DeleteOrphanVolume(r.Context(), h.orgID(r), nodeID, name, actor(r))) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
