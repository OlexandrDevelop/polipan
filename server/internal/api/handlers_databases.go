// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/server/internal/dbengine"
	"github.com/polipan/polipan/server/internal/logging"
	"github.com/polipan/polipan/server/internal/store"
)

// Codes of warnings in the answer of a database service.
const (
	warnZeroDowntime    = "zero_downtime_not_available"
	warnDatabaseExposed = "database_exposed"
)

// BackupSuggestion is the default policy that the UI offers for a new database (REQ-DB-006).
var BackupSuggestion = map[string]any{
	"schedule":               "0 2 * * *",
	"retention":              map[string]int{"keep_daily": 7, "keep_weekly": 4, "keep_monthly": 6},
	"restore_check_schedule": "0 3 * * 0",
}

// databaseConfigKeys are the keys of the config that a user may set on a database service.
var databaseConfigKeys = map[string]bool{"resources": true, "host_port": true, "strategy": true, "stop_grace_period": true}

// createDatabase makes a database service (REQ-DB-001 to REQ-DB-003, REQ-DB-005): the official image,
// one volume "data", recreate, a generated password as the secret DB_PASSWORD, DB_USER and DB_NAME as variables.
func (h *Handlers) createDatabase(w http.ResponseWriter, r *http.Request, env *store.Environment, body gen.ServiceCreate) {
	if body.DbEngine == nil {
		invalid(w, r, "db_engine", "db_engine is required for a database (postgres, mysql, mariadb, mongo or redis)")
		return
	}
	eng := dbengine.Get(string(*body.DbEngine))
	if eng == nil {
		invalid(w, r, "db_engine", "db_engine must be postgres, mysql, mariadb, mongo or redis")
		return
	}
	if body.Source != nil {
		invalid(w, r, "source", "a database has no source: Polipan runs the official image")
	}
	version := ""
	if body.DbVersion != nil {
		version = *body.DbVersion
	}
	spec, err := eng.NewService(version, "")
	if err != nil {
		invalid(w, r, "db_version", err.Error())
		return
	}
	var warnings []gen.Warning
	if body.Config != nil {
		for k, v := range *body.Config {
			if !databaseConfigKeys[k] {
				invalid(w, r, "config."+k, "config."+k+" cannot be set on a database")
				return
			}
			spec.Config[k] = v
		}
	}
	if w2, ok := h.databaseConfigRules(w, r, env.Protected, spec.Config); !ok {
		return
	} else {
		warnings = w2
	}
	raw, err := checkImageConfig(jsonRoundTrip(spec.Config))
	if err != nil {
		invalid(w, r, "config", err.Error())
		return
	}
	if err := h.Settings.panelPortClash(raw); err != nil {
		invalid(w, r, "config", err.Error())
		return
	}
	in := store.ServiceInput{Name: body.Name, Type: "database", DBEngine: eng.Name, DBVersion: spec.Version, Config: raw, Env: spec.Env,
		Volumes: []store.Volume{{Name: "data", Kind: "volume", MountPath: spec.DataPath, AccessMode: "single_writer"}}}
	if h.Secrets == nil {
		internalError(w, r, errors.New("the secrets store is not set up"))
		return
	}
	ids, err := h.Store.CreateServices(r.Context(), h.Secrets.Provider, h.orgID(r), env.ID, []store.ServiceInput{in}, nil, actor(r))
	if h.groupFailed(w, r, err) {
		return
	}
	s, err := h.Store.GetService(r.Context(), h.orgID(r), ids[0])
	if fail(w, r, err) {
		return
	}
	out := h.decorateService(r.Context(), serviceJSON(s), s)
	if len(warnings) > 0 {
		out.Warnings = &warnings
	}
	WriteJSON(w, http.StatusCreated, out)
}

// databaseConfigRules applies the rules of a database config: blue_green becomes recreate with a
// warning (REQ-DB-002), and a host port needs an admin on a protected environment and gives a warning (REQ-DB-005).
func (h *Handlers) databaseConfigRules(w http.ResponseWriter, r *http.Request, protected bool, cfg map[string]any) ([]gen.Warning, bool) {
	var warnings []gen.Warning
	if s, _ := cfg["strategy"].(string); s == "blue_green" {
		cfg["strategy"] = "recreate"
		warnings = append(warnings, gen.Warning{Code: ptr(warnZeroDowntime),
			Message: ptr("Zero downtime is not available for a database: the old container stops before the new one starts. The strategy is recreate.")})
	}
	if _, ok := cfg["host_port"]; ok {
		if protected && requireAdmin(w, r, "exposing a database of a protected environment") {
			return nil, false
		}
		warnings = append(warnings, gen.Warning{Code: ptr(warnDatabaseExposed),
			Message: ptr("The database is reachable from the internet on this port. Anyone who can reach the node can try to log in.")})
	}
	cfg["strategy"] = "recreate"
	return warnings, true
}

// groupFailed maps the errors of a group of services (a database or a template). It returns true when it wrote an answer.
func (h *Handlers) groupFailed(w http.ResponseWriter, r *http.Request, err error) bool {
	var used *store.DomainUsedError
	switch {
	case err == nil:
		return false
	case errors.Is(err, store.ErrServiceNameUsed):
		WriteError(w, r, http.StatusConflict, CodeNameConflict, "a service with this name exists in the environment", map[string]any{"field": "name"})
	case errors.As(err, &used):
		WriteError(w, r, http.StatusConflict, CodeDomainInUse, "the domain "+used.Host+" is in use. Nothing was created", map[string]any{"host": used.Host})
	default:
		return configFailed(w, r, err)
	}
	return true
}

// decorateService adds the database and template fields of a service.
func (h *Handlers) decorateService(ctx context.Context, out gen.Service, s *store.Service) gen.Service {
	m, err := h.Store.GetServiceM4(ctx, s.ID)
	if err != nil {
		logging.From(ctx).Warn("service m4 fields", "service_id", s.ID, "error", err)
		return out
	}
	out.TemplateInstanceId = m.TemplateInstance
	if s.Type != "database" || s.DBEngine == nil {
		return out
	}
	if eng := dbengine.Get(*s.DBEngine); eng != nil {
		var cfg struct {
			Image string `json:"image"`
		}
		_ = json.Unmarshal(s.Config, &cfg)
		if tag := eng.TagOf(cfg.Image); tag != "" {
			out.DbVersion = &tag
		} else {
			out.DbVersion = m.DBVersion
		}
	}
	status := gen.ServiceBackupStatusNone
	switch {
	case m.HasBackupPolicies:
		status = gen.ServiceBackupStatusConfigured
	case m.BackupSkipped:
		status = gen.ServiceBackupStatusSkipped
	}
	out.Backup = &gen.ServiceBackup{Status: &status}
	if status == gen.ServiceBackupStatusNone {
		out.Backup.Suggestion = &struct {
			RestoreCheckSchedule *string         `json:"restore_check_schedule,omitempty"`
			Retention            *map[string]int `json:"retention,omitempty"`
			Schedule             *string         `json:"schedule,omitempty"`
		}{RestoreCheckSchedule: ptr("0 3 * * 0"), Retention: &map[string]int{"keep_daily": 7, "keep_weekly": 4, "keep_monthly": 6}, Schedule: ptr("0 2 * * *")}
	}
	return out
}

// checkDatabaseUpdate applies the rules of REQ-DB-007 and REQ-DB-002 to the new config of a database.
// It returns the config to store and the warnings.
func (h *Handlers) checkDatabaseUpdate(w http.ResponseWriter, r *http.Request, cur *store.Service, cfg map[string]any) ([]gen.Warning, bool) {
	eng := dbengine.Get(deref(cur.DBEngine))
	if eng == nil {
		return nil, true
	}
	var old struct {
		Image string `json:"image"`
	}
	_ = json.Unmarshal(cur.Config, &old)
	if img, _ := cfg["image"].(string); img != "" {
		if err := eng.CheckVersionChange(old.Image, img); err != nil {
			invalid(w, r, "config.image", err.Error())
			return nil, false
		}
	}
	return h.databaseConfigRules(w, r, cur.EnvProtected, cfg)
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// jsonRoundTrip gives a config the types of a decoded request (numbers are float64), as the checks expect.
func jsonRoundTrip(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return out
}
