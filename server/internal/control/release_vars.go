// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	pb "github.com/polipan/polipan/proto/agentv1"
	"github.com/polipan/polipan/server/internal/mask"
	"github.com/polipan/polipan/server/internal/secrets"
	"github.com/polipan/polipan/server/internal/store"
	"github.com/polipan/polipan/server/internal/vars"
)

// Failure reasons of variable resolution (C05: 422 unresolved_reference, reference_cycle).
const (
	ReasonUnresolvedReference = "unresolved_reference"
	ReasonReferenceCycle      = "reference_cycle"
)

// pickNodeForService returns the node of the volumes of the service when they are pinned
// (REQ-VOL-003), else the first ready node. A pinned node that is not ready is an error.
func (c *Control) pickNodeForService(ctx context.Context, serviceID string) (readyNode, error) {
	var pinned *string
	err := c.DB.QueryRow(ctx, `SELECT node_id FROM volumes WHERE service_id = $1 AND kind <> 'tmpfs' AND node_id IS NOT NULL
		ORDER BY name LIMIT 1`, serviceID).Scan(&pinned)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && pinned == nil) {
		return c.pickNode(ctx)
	}
	if err != nil {
		return readyNode{}, err
	}
	var n readyNode
	err = c.DB.QueryRow(ctx, `SELECT id FROM nodes WHERE id = $1 AND state = 'ready'`, *pinned).Scan(&n.id)
	return n, err
}

// resolveRelease pins the volumes of the service to the node and resolves the variables
// and secrets of the service into the release snapshot (03-data-model section 4).
func (c *Control) resolveRelease(ctx context.Context, serviceID, nodeID string) (*vars.Snapshot, *deployErr) {
	if _, err := c.DB.Exec(ctx, `UPDATE volumes SET node_id = $2 WHERE service_id = $1 AND node_id IS NULL AND kind <> 'tmpfs'`,
		serviceID, nodeID); err != nil {
		return nil, &deployErr{reason: ReasonInvalidConfig, detail: "pin volumes: " + err.Error()}
	}
	snap, err := vars.Build(ctx, c.DB, serviceID)
	var unresolved *vars.UnresolvedError
	var cycle *vars.CycleError
	switch {
	case errors.As(err, &unresolved):
		return nil, &deployErr{reason: ReasonUnresolvedReference, detail: unresolved.Error()}
	case errors.As(err, &cycle):
		return nil, &deployErr{reason: ReasonReferenceCycle, detail: cycle.Error()}
	case err != nil:
		return nil, &deployErr{reason: ReasonInvalidConfig, detail: "resolve variables: " + err.Error()}
	}
	if len(snap.Secrets) > 0 && c.Secrets == nil {
		return nil, &deployErr{reason: ReasonInvalidConfig, detail: "the service uses secrets but the secrets store is not set up"}
	}
	return snap, nil
}

// releaseColumns are the variable_snapshot and secret_versions of a release.
func releaseColumns(snap *vars.Snapshot) (variables, versions []byte) {
	variables, _ = json.Marshal(snap.Variables)
	versions, _ = json.Marshal(snap.SecretVersions)
	return variables, versions
}

// auditSecretUse writes secret.use with the ids and versions of the release (REQ-SEC-004). No values.
func auditSecretUse(ctx context.Context, tx pgx.Tx, snap *vars.Snapshot, releaseID, actorID string) error {
	if len(snap.Secrets) == 0 {
		return nil
	}
	type use struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Version int    `json:"version"`
	}
	list := make([]use, 0, len(snap.Secrets))
	for name, e := range snap.Secrets {
		list = append(list, use{e.ID, name, e.Version})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	typ := "user"
	if actorID == "" {
		typ = "system"
	}
	return secrets.WriteAudit(ctx, tx, secrets.AuditEvent{OrgID: snap.Scope.OrgID, ActorType: typ, ActorID: actorID, Action: "secret.use",
		TargetType: "release", TargetID: releaseID, ProjectID: snap.Scope.ProjectID, Diff: map[string]any{"release_id": releaseID, "secrets": list}})
}

// fillWorkload adds the env, the secret env keys and the mounts of the release to a workload.
// Secret values come from the versions stored in the release, so a rollback or a redeploy of
// an old release gets the same values (REQ-SEC-003).
func (c *Control) fillWorkload(ctx context.Context, q dbq, nodeID string, w *pb.WorkloadSpec) error {
	var cfgRaw, varsRaw []byte
	var commit, nodeName string
	err := q.QueryRow(ctx, `SELECT r.config_snapshot, r.variable_snapshot, COALESCE(r.source->>'commit_sha', ''), COALESCE(n.name, '')
		FROM releases r LEFT JOIN nodes n ON n.id = $2 WHERE r.id = $1`, w.ReleaseId, nodeID).Scan(&cfgRaw, &varsRaw, &commit, &nodeName)
	if err != nil {
		return err
	}
	scope, err := vars.LoadScope(ctx, q, w.ServiceId)
	if err != nil {
		return err
	}
	var cfg struct {
		Volumes      []vars.Volume     `json:"volumes"`
		Files        []vars.File       `json:"files"`
		SecretFiles  []vars.SecretFile `json:"secret_files"`
		EnvTemplates map[string]string `json:"env_templates"`
	}
	if err := json.Unmarshal(cfgRaw, &cfg); err != nil {
		return fmt.Errorf("release config of %s: %w", w.ReleaseId, err)
	}
	env := map[string]string{}
	if err := json.Unmarshal(varsRaw, &env); err != nil {
		return fmt.Errorf("variables of %s: %w", w.ReleaseId, err)
	}

	// Secrets: decrypt the versions of the release.
	refs, err := secrets.ReleaseRefs(ctx, q, w.ReleaseId)
	if err != nil {
		return err
	}
	values := map[string]string{}
	if len(refs) > 0 {
		if c.Secrets == nil {
			return errors.New("the release uses secrets but the secrets store is not set up")
		}
		for name, ref := range refs {
			v, err := c.Secrets.Provider.Get(ctx, q, scope.ProjectID, ref.SecretID, ref.Version)
			if err != nil {
				return fmt.Errorf("secret %s version %d of release %s: %w", name, ref.Version, w.ReleaseId, err)
			}
			values[name] = string(v)
		}
	}
	var secretKeys []string
	for name, v := range values {
		if strings.Contains(name, ".") {
			continue // a secret of a referenced database (db.main.PASSWORD): used in templates only
		}
		env[name] = v
		secretKeys = append(secretKeys, name)
	}
	for name, tpl := range cfg.EnvTemplates {
		env[name] = vars.Expand(tpl, func(ref string) (string, bool) { v, ok := values[ref]; return v, ok })
		secretKeys = append(secretKeys, name)
	}
	sort.Strings(secretKeys)

	// Names set by Polipan (REQ-ENV-010). They win over everything.
	env["POLIPAN_SERVICE"] = scope.ServiceName
	env["POLIPAN_ENV"] = scope.EnvSlug
	env["POLIPAN_RELEASE"] = w.ReleaseId
	env["POLIPAN_COMMIT"] = commit
	env["POLIPAN_NODE"] = nodeName
	w.Env, w.SecretEnvKeys = env, secretKeys

	for _, v := range cfg.Volumes {
		// The agent refuses a second writer of a volume that is not shared (volume_in_use, 08 section 7a).
		m := &pb.Mount{Kind: v.Kind, Target: v.MountPath, Shared: v.AccessMode == "shared"}
		if v.SizeLimitBytes != nil && *v.SizeLimitBytes > 0 {
			m.SizeLimitBytes = uint64(*v.SizeLimitBytes)
		}
		switch v.Kind {
		case "volume":
			m.Source = store.VolumeDockerName(v.ID)
		case "bind":
			m.Source = v.Name // the agent puts it under <binds>/<service id>/
		}
		w.Mounts = append(w.Mounts, m)
	}
	for _, f := range cfg.Files {
		w.Mounts = append(w.Mounts, &pb.Mount{Kind: "file", Source: path.Base(f.Path), Target: f.Path, ReadOnly: true, Content: []byte(f.Content)})
	}
	for _, f := range cfg.SecretFiles {
		v, ok := values[f.Secret]
		if !ok {
			return fmt.Errorf("release %s has no secret %s for %s", w.ReleaseId, f.Secret, f.Path)
		}
		w.Mounts = append(w.Mounts, &pb.Mount{Kind: "file", Source: path.Base(f.Path), Target: f.Path, ReadOnly: true, Secret: true, Content: []byte(v)})
	}
	return nil
}

// maskStep hides the secret values of the service of a deployment in a log line (REQ-SEC-007).
// When the values cannot be read the line is replaced, so no secret can leak.
func (c *Control) maskStep(ctx context.Context, depID, line string) string {
	if c.Secrets == nil {
		return line
	}
	var serviceID string
	if err := c.DB.QueryRow(ctx, `SELECT service_id FROM deployments WHERE id = $1`, depID).Scan(&serviceID); err != nil {
		return line // no deployment row yet: the line is one of ours, without secrets
	}
	values, err := c.Secrets.ValuesForService(ctx, serviceID)
	if err != nil {
		c.Log.Error("secret masker", "deployment", depID, "err", err)
		return "[line hidden: secret values could not be read]"
	}
	return mask.New(values).Mask(line)
}
