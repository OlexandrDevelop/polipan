// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/polipan/terraform-provider-polipan/internal/client"
)

// Plan modifier lists used by many attributes.
var (
	idMods       = []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	replaceMods  = []planmodifier.String{stringplanmodifier.RequiresReplace()}
	computedKeep = []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
)

func idAttr(desc string) schema.StringAttribute {
	return schema.StringAttribute{Computed: true, Description: desc, PlanModifiers: idMods}
}

// splitImport splits "a/b/..." into n parts.
func splitImport(id string, n int, form string) ([]string, error) {
	parts := strings.Split(id, "/")
	if len(parts) != n {
		return nil, fmt.Errorf("the import id must look like %q", form)
	}
	for _, p := range parts {
		if p == "" {
			return nil, fmt.Errorf("the import id must look like %q", form)
		}
	}
	return parts, nil
}

// ---- polipan_node_token ----

type nodeTokenModel struct {
	ID             types.String `tfsdk:"id"`
	Name           types.String `tfsdk:"name"`
	Labels         types.Map    `tfsdk:"labels"`
	Token          types.String `tfsdk:"token"`
	InstallCommand types.String `tfsdk:"install_command"`
	ExpiresAt      types.String `tfsdk:"expires_at"`
}

var nodeTokenSpec = spec[nodeTokenModel]{
	name: "node_token",
	schema: schema.Schema{
		Description: "A join token for a new node. The node is created in the `pending` state. The token is shown by the API only once, so it is kept in the state (sensitive).",
		Attributes: map[string]schema.Attribute{
			"id":              idAttr("The node id."),
			"name":            schema.StringAttribute{Required: true, Description: "Node name. Lower case letters, digits and dashes."},
			"labels":          schema.MapAttribute{Optional: true, ElementType: types.StringType, Description: "Node labels, for example `ingress = \"true\"`."},
			"token":           schema.StringAttribute{Computed: true, Sensitive: true, Description: "The join token for the agent.", PlanModifiers: computedKeep},
			"install_command": schema.StringAttribute{Computed: true, Sensitive: true, Description: "The command to install the agent on the node. It contains the token.", PlanModifiers: computedKeep},
			"expires_at":      schema.StringAttribute{Computed: true, Description: "When the join token expires.", PlanModifiers: computedKeep},
		},
	},
	create: func(ctx context.Context, c *client.Client, m *nodeTokenModel) error {
		body := client.Obj{"name": m.Name.ValueString()}
		put(body, "labels", m.Labels)
		o, err := c.Post(ctx, "/nodes", body)
		if err != nil {
			return err
		}
		setStr(&m.ID, o, "node_id")
		setStr(&m.Token, o, "token")
		setStr(&m.InstallCommand, o, "install_command")
		setStr(&m.ExpiresAt, o, "expires_at")
		return nil
	},
	read: func(ctx context.Context, c *client.Client, m *nodeTokenModel) (bool, error) {
		o, err := c.Get(ctx, "/nodes/"+m.ID.ValueString())
		if client.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		setStr(&m.Name, o, "name")
		optStrMap(&m.Labels, client.Sub(o, "labels"))
		return true, nil
	},
	update: func(ctx context.Context, c *client.Client, plan, state *nodeTokenModel) error {
		body := client.Obj{"name": plan.Name.ValueString()}
		if plan.Labels.IsNull() {
			body["labels"] = map[string]string{}
		} else {
			put(body, "labels", plan.Labels)
		}
		_, err := c.Patch(ctx, "/nodes/"+state.ID.ValueString(), body)
		return err
	},
	del: func(ctx context.Context, c *client.Client, m *nodeTokenModel) error {
		return c.Delete(ctx, "/nodes/"+m.ID.ValueString())
	},
}

// ---- polipan_project ----

type projectModel struct {
	ID                  types.String `tfsdk:"id"`
	Name                types.String `tfsdk:"name"`
	Slug                types.String `tfsdk:"slug"`
	DefaultEnvironments types.Bool   `tfsdk:"default_environments"`
	RetainReleases      types.Int64  `tfsdk:"retain_releases"`
	BuildTimeoutMinutes types.Int64  `tfsdk:"build_timeout_minutes"`
}

func projectSettings(m *projectModel) client.Obj {
	s := client.Obj{}
	put(s, "retain_releases", m.RetainReleases)
	put(s, "build_timeout_minutes", m.BuildTimeoutMinutes)
	return s
}

var projectSpec = spec[projectModel]{
	name: "project",
	schema: schema.Schema{
		Description: "A project groups environments and services.",
		Attributes: map[string]schema.Attribute{
			"id":   idAttr("The project id."),
			"name": schema.StringAttribute{Required: true, Description: "Project name."},
			"slug": schema.StringAttribute{Optional: true, Computed: true, Description: "URL name of the project. Made from the name when not set. A change replaces the project.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplaceIfConfigured()}},
			"default_environments": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "Create the `production` and `staging` environments with the project. Used only when the project is created."},
			"retain_releases":       schema.Int64Attribute{Optional: true, Description: "How many releases are kept per service."},
			"build_timeout_minutes": schema.Int64Attribute{Optional: true, Description: "Build time limit in minutes."},
		},
	},
	create: func(ctx context.Context, c *client.Client, m *projectModel) error {
		body := client.Obj{"name": m.Name.ValueString()}
		put(body, "slug", m.Slug)
		put(body, "default_environments", m.DefaultEnvironments)
		o, err := c.Post(ctx, "/projects", body)
		if err != nil {
			return err
		}
		setStr(&m.ID, o, "id")
		setStr(&m.Slug, o, "slug")
		if s := projectSettings(m); len(s) > 0 {
			if _, err := c.Patch(ctx, "/projects/"+m.ID.ValueString(), client.Obj{"settings": s}); err != nil {
				return fmt.Errorf("the project %s was created, but its settings failed: %w", m.ID.ValueString(), err)
			}
		}
		return nil
	},
	read: func(ctx context.Context, c *client.Client, m *projectModel) (bool, error) {
		o, err := c.Get(ctx, "/projects/"+m.ID.ValueString())
		if client.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		setStr(&m.Name, o, "name")
		setStr(&m.Slug, o, "slug")
		s := client.Sub(o, "settings")
		optInt(&m.RetainReleases, s, "retain_releases")
		optInt(&m.BuildTimeoutMinutes, s, "build_timeout_minutes")
		return true, nil
	},
	update: func(ctx context.Context, c *client.Client, plan, state *projectModel) error {
		body := client.Obj{"name": plan.Name.ValueString()}
		if s := projectSettings(plan); len(s) > 0 {
			body["settings"] = s
		}
		_, err := c.Patch(ctx, "/projects/"+state.ID.ValueString(), body)
		return err
	},
	del: func(ctx context.Context, c *client.Client, m *projectModel) error {
		return c.Delete(ctx, "/projects/"+m.ID.ValueString()+"?confirm="+url.QueryEscape(m.Slug.ValueString()))
	},
	importID: func(id string) (map[string]any, error) {
		return map[string]any{"id": id, "default_environments": true}, nil
	},
}

// ---- polipan_environment ----

type environmentModel struct {
	ID        types.String `tfsdk:"id"`
	ProjectID types.String `tfsdk:"project_id"`
	Name      types.String `tfsdk:"name"`
	Kind      types.String `tfsdk:"kind"`
	Protected types.Bool   `tfsdk:"protected"`
	Slug      types.String `tfsdk:"slug"`
}

var environmentSpec = spec[environmentModel]{
	name: "environment",
	schema: schema.Schema{
		Description: "An environment of a project, for example `staging`.",
		Attributes: map[string]schema.Attribute{
			"id":         idAttr("The environment id."),
			"project_id": schema.StringAttribute{Required: true, Description: "The project of the environment. A change replaces the environment.", PlanModifiers: replaceMods},
			"name":       schema.StringAttribute{Required: true, Description: "Environment name."},
			"kind":       schema.StringAttribute{Required: true, Description: "`production`, `staging` or `custom`. A change replaces the environment.", PlanModifiers: replaceMods},
			"protected": schema.BoolAttribute{Optional: true, Computed: true, Description: "A protected environment needs an admin for risky changes. Default: true for `production`, false otherwise.",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}},
			"slug": schema.StringAttribute{Computed: true, Description: "URL name of the environment.", PlanModifiers: computedKeep},
		},
	},
	create: func(ctx context.Context, c *client.Client, m *environmentModel) error {
		body := client.Obj{"name": m.Name.ValueString(), "kind": m.Kind.ValueString()}
		put(body, "protected", m.Protected)
		o, err := c.Post(ctx, "/projects/"+m.ProjectID.ValueString()+"/environments", body)
		if err != nil {
			return err
		}
		setStr(&m.ID, o, "id")
		setStr(&m.Slug, o, "slug")
		setBool(&m.Protected, o, "protected")
		return nil
	},
	read: func(ctx context.Context, c *client.Client, m *environmentModel) (bool, error) {
		o, err := c.Get(ctx, "/environments/"+m.ID.ValueString())
		if client.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		setStr(&m.ProjectID, o, "project_id")
		setStr(&m.Name, o, "name")
		setStr(&m.Kind, o, "kind")
		setStr(&m.Slug, o, "slug")
		setBool(&m.Protected, o, "protected")
		return true, nil
	},
	update: func(ctx context.Context, c *client.Client, plan, state *environmentModel) error {
		body := client.Obj{"name": plan.Name.ValueString()}
		if !plan.Protected.IsUnknown() && !plan.Protected.Equal(state.Protected) {
			put(body, "protected", plan.Protected)
		}
		o, err := c.Patch(ctx, "/environments/"+state.ID.ValueString(), body)
		if err != nil {
			return err
		}
		setStr(&plan.Slug, o, "slug")
		setBool(&plan.Protected, o, "protected")
		return nil
	},
	del: func(ctx context.Context, c *client.Client, m *environmentModel) error {
		return c.Delete(ctx, "/environments/"+m.ID.ValueString())
	},
}

// ---- polipan_service ----

type serviceModel struct {
	ID            types.String `tfsdk:"id"`
	EnvironmentID types.String `tfsdk:"environment_id"`
	Name          types.String `tfsdk:"name"`
	Type          types.String `tfsdk:"type"`
	Image         types.String `tfsdk:"image"`
	Port          types.Int64  `tfsdk:"port"`
	Command       types.List   `tfsdk:"command"`
	RestartPolicy types.String `tfsdk:"restart_policy"`
	DBEngine      types.String `tfsdk:"db_engine"`
	DBVersion     types.String `tfsdk:"db_version"`
	ConfigJSON    types.String `tfsdk:"config_json"`
}

// serviceConfig builds the config of a service from the attributes. base is the current config
// from the server (nil on create): keys the provider does not manage stay as they are.
func serviceConfig(m *serviceModel, base client.Obj) (client.Obj, error) {
	cfg := client.Obj{}
	if m.Type.ValueString() != "database" {
		for k, v := range base {
			cfg[k] = v
		}
		put(cfg, "image", m.Image)
		put(cfg, "restart_policy", m.RestartPolicy)
		if m.Port.IsNull() {
			delete(cfg, "port")
		} else {
			// The server made a tcp health check from the old port. It is made again for the new port.
			if h := client.Sub(cfg, "health"); len(h) == 2 && h["type"] == "tcp" && h["port"] != float64(m.Port.ValueInt64()) {
				delete(cfg, "health")
			}
			put(cfg, "port", m.Port)
		}
		if m.Command.IsNull() {
			delete(cfg, "command")
		} else {
			put(cfg, "command", m.Command)
		}
	}
	extra, err := parseJSONObj(m.ConfigJSON)
	if err != nil {
		return nil, fmt.Errorf("config_json: %w", err)
	}
	for k, v := range extra {
		cfg[k] = v
	}
	return cfg, nil
}

func readService(m *serviceModel, o client.Obj) {
	setStr(&m.EnvironmentID, o, "environment_id")
	setStr(&m.Name, o, "name")
	setStr(&m.Type, o, "type")
	cfg := client.Sub(o, "config")
	setStr(&m.Image, cfg, "image")
	setStr(&m.RestartPolicy, cfg, "restart_policy")
	optInt(&m.Port, cfg, "port")
	optList(&m.Command, cfg, "command")
	optStr(&m.DBEngine, o, "db_engine")
	optStr(&m.DBVersion, o, "db_version")
	syncJSON(&m.ConfigJSON, cfg)
}

var serviceSpec = spec[serviceModel]{
	name: "service",
	schema: schema.Schema{
		Description: "A service: an app from a container image, or a managed database. The provider defines the service. It does not start a deployment.",
		Attributes: map[string]schema.Attribute{
			"id":             idAttr("The service id."),
			"environment_id": schema.StringAttribute{Required: true, Description: "The environment of the service. A change replaces the service.", PlanModifiers: replaceMods},
			"name":           schema.StringAttribute{Required: true, Description: "Service name. Lower case letters, digits and dashes, 1 to 40 characters. A change replaces the service.", PlanModifiers: replaceMods},
			"type": schema.StringAttribute{Optional: true, Computed: true, Description: "`app` (default) or `database`. A change replaces the service.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplaceIfConfigured()}},
			"image":          schema.StringAttribute{Optional: true, Computed: true, Description: "Container image of an app, for example `nginx:1.27`. Needed for an app.", PlanModifiers: computedKeep},
			"port":           schema.Int64Attribute{Optional: true, Description: "The port of the app in the container."},
			"command":        schema.ListAttribute{Optional: true, ElementType: types.StringType, Description: "Command that replaces the image command."},
			"restart_policy": schema.StringAttribute{Optional: true, Computed: true, Description: "`always` (default), `on-failure`, `unless-stopped` or `no`.", PlanModifiers: computedKeep},
			"db_engine": schema.StringAttribute{Optional: true, Description: "Database engine: `postgres`, `mysql`, `mariadb`, `mongo` or `redis`. Only for `type = \"database\"`. A change replaces the service.",
				PlanModifiers: replaceMods},
			"db_version":  schema.StringAttribute{Optional: true, Description: "Database version tag. A change replaces the service.", PlanModifiers: replaceMods},
			"config_json": schema.StringAttribute{Optional: true, Description: "More config keys as JSON, for example `jsonencode({ health = { type = \"http\", path = \"/\" } })`. They are merged into the config. Only the keys you write are compared."},
		},
	},
	create: func(ctx context.Context, c *client.Client, m *serviceModel) error {
		if m.Type.IsNull() || m.Type.IsUnknown() {
			m.Type = types.StringValue("app")
		}
		cfg, err := serviceConfig(m, nil)
		if err != nil {
			return err
		}
		body := client.Obj{"name": m.Name.ValueString(), "type": m.Type.ValueString(), "config": cfg}
		put(body, "db_engine", m.DBEngine)
		put(body, "db_version", m.DBVersion)
		o, err := c.Post(ctx, "/environments/"+m.EnvironmentID.ValueString()+"/services", body)
		if err != nil {
			return err
		}
		setStr(&m.ID, o, "id")
		stored := client.Sub(o, "config")
		setStr(&m.Image, stored, "image")
		setStr(&m.RestartPolicy, stored, "restart_policy")
		return nil
	},
	read: func(ctx context.Context, c *client.Client, m *serviceModel) (bool, error) {
		o, err := c.Get(ctx, "/services/"+m.ID.ValueString())
		if client.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		readService(m, o)
		return true, nil
	},
	update: func(ctx context.Context, c *client.Client, plan, state *serviceModel) error {
		cur, err := c.Get(ctx, "/services/"+state.ID.ValueString())
		if err != nil {
			return err
		}
		cfg, err := serviceConfig(plan, client.Sub(cur, "config"))
		if err != nil {
			return err
		}
		o, err := c.Patch(ctx, "/services/"+state.ID.ValueString(), client.Obj{"config": cfg})
		if err != nil {
			return err
		}
		stored := client.Sub(o, "config")
		setStr(&plan.Image, stored, "image")
		setStr(&plan.RestartPolicy, stored, "restart_policy")
		return nil
	},
	del: func(ctx context.Context, c *client.Client, m *serviceModel) error {
		return c.Delete(ctx, "/services/"+m.ID.ValueString()+"?confirm="+url.QueryEscape(m.Name.ValueString()))
	},
}
