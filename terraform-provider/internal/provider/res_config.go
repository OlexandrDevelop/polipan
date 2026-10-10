// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/polipan/terraform-provider-polipan/internal/client"
)

// ---- polipan_domain ----

type domainModel struct {
	ID            types.String `tfsdk:"id"`
	ServiceID     types.String `tfsdk:"service_id"`
	Host          types.String `tfsdk:"host"`
	PathPrefix    types.String `tfsdk:"path_prefix"`
	ContainerPort types.Int64  `tfsdk:"container_port"`
	HTTPS         types.Bool   `tfsdk:"https"`
	RedirectWWW   types.String `tfsdk:"redirect_www"`
}

func domainBody(m *domainModel) client.Obj {
	body := client.Obj{}
	put(body, "host", m.Host)
	put(body, "path_prefix", m.PathPrefix)
	put(body, "container_port", m.ContainerPort)
	put(body, "https", m.HTTPS)
	put(body, "redirect_www", m.RedirectWWW)
	return body
}

func readDomain(m *domainModel, o client.Obj) {
	setStr(&m.ServiceID, o, "service_id")
	setStr(&m.Host, o, "host")
	setStr(&m.PathPrefix, o, "path_prefix")
	setInt(&m.ContainerPort, o, "container_port")
	setBool(&m.HTTPS, o, "https")
	optStr(&m.RedirectWWW, o, "redirect_www")
}

var domainSpec = spec[domainModel]{
	name: "domain",
	schema: schema.Schema{
		Description: "A domain that routes to a service. Import with `service_id/domain_id`.",
		Attributes: map[string]schema.Attribute{
			"id":          idAttr("The domain id."),
			"service_id":  schema.StringAttribute{Required: true, Description: "The service. A change replaces the domain.", PlanModifiers: replaceMods},
			"host":        schema.StringAttribute{Required: true, Description: "Host name in lower case, for example `app.example.com`. No wildcard, no IP address."},
			"path_prefix": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("/"), Description: "Path prefix. Default `/`."},
			"container_port": schema.Int64Attribute{Optional: true, Computed: true, Description: "Port in the container. Default: the port of the service.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}},
			"https":        schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), Description: "Use HTTPS with an automatic certificate. Default true."},
			"redirect_www": schema.StringAttribute{Optional: true, Description: "`to_www` or `to_apex`."},
		},
	},
	create: func(ctx context.Context, c *client.Client, m *domainModel) error {
		o, err := c.Post(ctx, "/services/"+m.ServiceID.ValueString()+"/domains", domainBody(m))
		if err != nil {
			return err
		}
		setStr(&m.ID, o, "id")
		setInt(&m.ContainerPort, o, "container_port")
		return nil
	},
	read: func(ctx context.Context, c *client.Client, m *domainModel) (bool, error) {
		o, err := c.Find(ctx, "/services/"+m.ServiceID.ValueString()+"/domains", "id", m.ID.ValueString())
		if client.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		readDomain(m, o)
		return true, nil
	},
	update: func(ctx context.Context, c *client.Client, plan, state *domainModel) error {
		o, err := c.Patch(ctx, "/domains/"+state.ID.ValueString(), domainBody(plan))
		if err != nil {
			return err
		}
		setInt(&plan.ContainerPort, o, "container_port")
		return nil
	},
	del: func(ctx context.Context, c *client.Client, m *domainModel) error {
		return c.Delete(ctx, "/domains/"+m.ID.ValueString())
	},
	importID: func(id string) (map[string]any, error) {
		p, err := splitImport(id, 2, "service_id/domain_id")
		if err != nil {
			return nil, err
		}
		return map[string]any{"service_id": p[0], "id": p[1]}, nil
	},
}

// ---- polipan_volume ----

type volumeModel struct {
	ID             types.String `tfsdk:"id"`
	ServiceID      types.String `tfsdk:"service_id"`
	Name           types.String `tfsdk:"name"`
	Kind           types.String `tfsdk:"kind"`
	MountPath      types.String `tfsdk:"mount_path"`
	AccessMode     types.String `tfsdk:"access_mode"`
	SizeLimitBytes types.Int64  `tfsdk:"size_limit_bytes"`
}

var volumeSpec = spec[volumeModel]{
	name: "volume",
	schema: schema.Schema{
		Description: "A volume mounted into a service. Import with `service_id/volume_id`.",
		Attributes: map[string]schema.Attribute{
			"id":         idAttr("The volume id."),
			"service_id": schema.StringAttribute{Required: true, Description: "The service. A change replaces the volume.", PlanModifiers: replaceMods},
			"name":       schema.StringAttribute{Required: true, Description: "Volume name. A change replaces the volume.", PlanModifiers: replaceMods},
			"kind": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("volume"),
				Description: "`volume` (default), `bind` or `tmpfs`. A change replaces the volume.", PlanModifiers: replaceMods},
			"mount_path":       schema.StringAttribute{Required: true, Description: "Path in the container."},
			"access_mode":      schema.StringAttribute{Optional: true, Computed: true, Description: "`single_writer` (default) or `shared`.", PlanModifiers: computedKeep},
			"size_limit_bytes": schema.Int64Attribute{Optional: true, Description: "Size limit in bytes."},
		},
	},
	create: func(ctx context.Context, c *client.Client, m *volumeModel) error {
		body := client.Obj{}
		put(body, "name", m.Name)
		put(body, "kind", m.Kind)
		put(body, "mount_path", m.MountPath)
		put(body, "access_mode", m.AccessMode)
		put(body, "size_limit_bytes", m.SizeLimitBytes)
		o, err := c.Post(ctx, "/services/"+m.ServiceID.ValueString()+"/volumes", body)
		if err != nil {
			return err
		}
		setStr(&m.ID, o, "id")
		setStr(&m.AccessMode, o, "access_mode")
		return nil
	},
	read: func(ctx context.Context, c *client.Client, m *volumeModel) (bool, error) {
		o, err := c.Find(ctx, "/services/"+m.ServiceID.ValueString()+"/volumes", "id", m.ID.ValueString())
		if client.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		setStr(&m.Name, o, "name")
		setStr(&m.Kind, o, "kind")
		setStr(&m.MountPath, o, "mount_path")
		setStr(&m.AccessMode, o, "access_mode")
		optInt(&m.SizeLimitBytes, o, "size_limit_bytes")
		return true, nil
	},
	update: func(ctx context.Context, c *client.Client, plan, state *volumeModel) error {
		body := client.Obj{}
		put(body, "mount_path", plan.MountPath)
		put(body, "access_mode", plan.AccessMode)
		put(body, "size_limit_bytes", plan.SizeLimitBytes)
		o, err := c.Patch(ctx, "/volumes/"+state.ID.ValueString(), body)
		if err != nil {
			return err
		}
		setStr(&plan.AccessMode, o, "access_mode")
		return nil
	},
	del: func(ctx context.Context, c *client.Client, m *volumeModel) error {
		return c.Delete(ctx, "/volumes/"+m.ID.ValueString())
	},
	importID: func(id string) (map[string]any, error) {
		p, err := splitImport(id, 2, "service_id/volume_id")
		if err != nil {
			return nil, err
		}
		return map[string]any{"service_id": p[0], "id": p[1]}, nil
	},
}

// ---- polipan_variable ----

type variableModel struct {
	ID            types.String `tfsdk:"id"`
	ProjectID     types.String `tfsdk:"project_id"`
	Name          types.String `tfsdk:"name"`
	Value         types.String `tfsdk:"value"`
	EnvironmentID types.String `tfsdk:"environment_id"`
	ServiceID     types.String `tfsdk:"service_id"`
	Preview       types.Bool   `tfsdk:"preview"`
	BuildTime     types.Bool   `tfsdk:"build_time"`
}

func varBody(name, value types.String, preview, build types.Bool) client.Obj {
	body := client.Obj{}
	put(body, "name", name)
	put(body, "value", value)
	put(body, "preview", preview)
	put(body, "build_time", build)
	return body
}

// scopeAttrs are the environment and service of a variable or secret. The scope cannot change.
func scopeAttrs() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"environment_id": schema.StringAttribute{Optional: true, Description: "Limit to one environment. Empty means the whole project. A change replaces the object.", PlanModifiers: replaceMods},
		"service_id":     schema.StringAttribute{Optional: true, Description: "Limit to one service. A change replaces the object.", PlanModifiers: replaceMods},
		"preview":        schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "Use only in preview environments."},
		"build_time":     schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "Available at build time."},
	}
}

var variableSpec = spec[variableModel]{
	name: "variable",
	schema: schema.Schema{
		Description: "A plain (not secret) variable. Import with `project_id/variable_id`. For secret values use `polipan_secret`.",
		Attributes: merge(map[string]schema.Attribute{
			"id":         idAttr("The variable id."),
			"project_id": schema.StringAttribute{Required: true, Description: "The project. A change replaces the variable.", PlanModifiers: replaceMods},
			"name":       schema.StringAttribute{Required: true, Description: "Variable name, for example `LOG_LEVEL`."},
			"value":      schema.StringAttribute{Required: true, Description: "Value. It is not secret and it is shown in plans."},
		}, scopeAttrs()),
	},
	create: func(ctx context.Context, c *client.Client, m *variableModel) error {
		body := varBody(m.Name, m.Value, m.Preview, m.BuildTime)
		put(body, "environment_id", m.EnvironmentID)
		put(body, "service_id", m.ServiceID)
		o, err := c.Post(ctx, "/projects/"+m.ProjectID.ValueString()+"/variables", body)
		if err != nil {
			return err
		}
		setStr(&m.ID, o, "id")
		return nil
	},
	read: func(ctx context.Context, c *client.Client, m *variableModel) (bool, error) {
		o, err := c.Find(ctx, "/projects/"+m.ProjectID.ValueString()+"/variables", "id", m.ID.ValueString())
		if client.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		setStr(&m.Name, o, "name")
		setStr(&m.Value, o, "value")
		optStr(&m.EnvironmentID, o, "environment_id")
		optStr(&m.ServiceID, o, "service_id")
		setBool(&m.Preview, o, "preview")
		setBool(&m.BuildTime, o, "build_time")
		return true, nil
	},
	update: func(ctx context.Context, c *client.Client, plan, state *variableModel) error {
		_, err := c.Patch(ctx, "/variables/"+state.ID.ValueString(), varBody(plan.Name, plan.Value, plan.Preview, plan.BuildTime))
		return err
	},
	del: func(ctx context.Context, c *client.Client, m *variableModel) error {
		return c.Delete(ctx, "/variables/"+m.ID.ValueString())
	},
	importID: func(id string) (map[string]any, error) {
		p, err := splitImport(id, 2, "project_id/variable_id")
		if err != nil {
			return nil, err
		}
		return map[string]any{"project_id": p[0], "id": p[1]}, nil
	},
}

func merge(a, b map[string]schema.Attribute) map[string]schema.Attribute {
	for k, v := range b {
		a[k] = v
	}
	return a
}

// ---- polipan_secret ----

type secretModel struct {
	ID            types.String `tfsdk:"id"`
	ProjectID     types.String `tfsdk:"project_id"`
	Name          types.String `tfsdk:"name"`
	Value         types.String `tfsdk:"value"`
	EnvironmentID types.String `tfsdk:"environment_id"`
	ServiceID     types.String `tfsdk:"service_id"`
	Preview       types.Bool   `tfsdk:"preview"`
	BuildTime     types.Bool   `tfsdk:"build_time"`
	Version       types.Int64  `tfsdk:"version"`
}

// secretVersionModifier keeps the version of the state in the plan, and makes it unknown when
// the value or a flag changes: an update then makes a new version on the server.
type secretVersionModifier struct{}

func (secretVersionModifier) Description(context.Context) string {
	return "The version stays the same until the value or a flag changes."
}
func (m secretVersionModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (secretVersionModifier) PlanModifyInt64(ctx context.Context, req planmodifier.Int64Request, resp *planmodifier.Int64Response) {
	if req.State.Raw.IsNull() { // create
		return
	}
	var planValue, stateValue types.String
	var planFlags, stateFlags [2]types.Bool
	for _, g := range []struct {
		name  string
		plan  any
		state any
	}{
		{"value", &planValue, &stateValue},
		{"preview", &planFlags[0], &stateFlags[0]},
		{"build_time", &planFlags[1], &stateFlags[1]},
	} {
		if resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root(g.name), g.plan)...); resp.Diagnostics.HasError() {
			return
		}
		if resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root(g.name), g.state)...); resp.Diagnostics.HasError() {
			return
		}
	}
	if !planValue.Equal(stateValue) || !planFlags[0].Equal(stateFlags[0]) || !planFlags[1].Equal(stateFlags[1]) {
		return // the plan value stays unknown
	}
	resp.PlanValue = req.StateValue
}

var secretSpec = spec[secretModel]{
	name: "secret",
	schema: schema.Schema{
		Description: "A secret value, encrypted by the server. The API never returns the value (write-only). " +
			"The Terraform state keeps only the value you gave (sensitive). A change made outside Terraform is found by the version number. " +
			"Import with `project_id/secret_id`: the value is then null in the state, and the next apply writes the value from the config.",
		Attributes: merge(map[string]schema.Attribute{
			"id":         idAttr("The secret id."),
			"project_id": schema.StringAttribute{Required: true, Description: "The project. A change replaces the secret.", PlanModifiers: replaceMods},
			"name":       schema.StringAttribute{Required: true, Description: "Secret name, for example `DATABASE_PASSWORD`. A change replaces the secret.", PlanModifiers: replaceMods},
			"value":      schema.StringAttribute{Required: true, Sensitive: true, Description: "The secret value. Sent to the server, never read back."},
			"version": schema.Int64Attribute{Computed: true, Description: "Current version number on the server. If it differs from the state, the value changed outside Terraform and the next apply writes the value again.",
				PlanModifiers: []planmodifier.Int64{secretVersionModifier{}}},
		}, scopeAttrs()),
	},
	create: func(ctx context.Context, c *client.Client, m *secretModel) error {
		body := varBody(m.Name, m.Value, m.Preview, m.BuildTime)
		put(body, "environment_id", m.EnvironmentID)
		put(body, "service_id", m.ServiceID)
		o, err := c.Post(ctx, "/projects/"+m.ProjectID.ValueString()+"/secrets", body)
		if err != nil {
			return err
		}
		setStr(&m.ID, o, "id")
		setInt(&m.Version, o, "current_version")
		return nil
	},
	read: func(ctx context.Context, c *client.Client, m *secretModel) (bool, error) {
		o, err := c.Find(ctx, "/projects/"+m.ProjectID.ValueString()+"/secrets", "id", m.ID.ValueString())
		if client.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		setStr(&m.Name, o, "name")
		optStr(&m.EnvironmentID, o, "environment_id")
		optStr(&m.ServiceID, o, "service_id")
		setBool(&m.Preview, o, "preview")
		setBool(&m.BuildTime, o, "build_time")
		var version types.Int64
		setInt(&version, o, "current_version")
		if !m.Version.Equal(version) {
			// The value changed outside Terraform (or this is an import). The state value is not
			// valid any more: null makes the next plan write the value from the config.
			m.Value = types.StringNull()
		}
		m.Version = version
		return true, nil
	},
	update: func(ctx context.Context, c *client.Client, plan, state *secretModel) error {
		body := client.Obj{}
		put(body, "value", plan.Value)
		put(body, "preview", plan.Preview)
		put(body, "build_time", plan.BuildTime)
		o, err := c.Put(ctx, "/secrets/"+state.ID.ValueString(), body)
		if err != nil {
			return err
		}
		setInt(&plan.Version, o, "current_version")
		return nil
	},
	del: func(ctx context.Context, c *client.Client, m *secretModel) error {
		return c.Delete(ctx, "/secrets/"+m.ID.ValueString())
	},
	importID: func(id string) (map[string]any, error) {
		p, err := splitImport(id, 2, "project_id/secret_id")
		if err != nil {
			return nil, err
		}
		return map[string]any{"project_id": p[0], "id": p[1]}, nil
	},
}
