// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/polipan/terraform-provider-polipan/internal/client"
)

// dataSource is a read-only object. lookup finds the object from the config and fills the model.
type dataSource[M any] struct {
	name   string
	schema schema.Schema
	lookup func(ctx context.Context, c *client.Client, m *M) error
	c      *client.Client
}

func newDataSource[M any](name string, s schema.Schema, lookup func(context.Context, *client.Client, *M) error) func() datasource.DataSource {
	return func() datasource.DataSource { return &dataSource[M]{name: name, schema: s, lookup: lookup} }
}

func (d *dataSource[M]) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + d.name
}

func (d *dataSource[M]) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = d.schema
}

func (d *dataSource[M]) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Wrong provider data", fmt.Sprintf("expected *client.Client, got %T", req.ProviderData))
		return
	}
	d.c = c
}

func (d *dataSource[M]) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m M
	if resp.Diagnostics.Append(req.Config.Get(ctx, &m)...); resp.Diagnostics.HasError() {
		return
	}
	if err := d.lookup(ctx, d.c, &m); err != nil {
		resp.Diagnostics.AddError("Cannot read "+d.name, err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// pick finds one object by id or by another key. It is an error when none or both keys are missing.
func pick(ctx context.Context, c *client.Client, id types.String, key string, keyValue types.String, getPath, listPath string) (client.Obj, error) {
	switch {
	case !id.IsNull() && !id.IsUnknown():
		return c.Get(ctx, getPath+id.ValueString())
	case !keyValue.IsNull() && !keyValue.IsUnknown():
		o, err := c.Find(ctx, listPath, key, keyValue.ValueString())
		if client.IsNotFound(err) {
			return nil, fmt.Errorf("no object with %s %q", key, keyValue.ValueString())
		}
		return o, err
	}
	return nil, errors.New("set id or " + key)
}

// ---- polipan_node ----

type nodeDataModel struct {
	ID       types.String `tfsdk:"id"`
	Name     types.String `tfsdk:"name"`
	State    types.String `tfsdk:"state"`
	PublicIP types.String `tfsdk:"public_ip"`
	MeshIP   types.String `tfsdk:"mesh_ip"`
	OS       types.String `tfsdk:"os"`
	Arch     types.String `tfsdk:"arch"`
	Labels   types.Map    `tfsdk:"labels"`
}

var newNodeDataSource = newDataSource("node", schema.Schema{
	Description: "Reads a node by id or by name.",
	Attributes: map[string]schema.Attribute{
		"id":        schema.StringAttribute{Optional: true, Computed: true, Description: "Node id. Set `id` or `name`."},
		"name":      schema.StringAttribute{Optional: true, Computed: true, Description: "Node name. Set `id` or `name`."},
		"state":     schema.StringAttribute{Computed: true, Description: "Node state, for example `ready`."},
		"public_ip": schema.StringAttribute{Computed: true, Description: "Public IP address."},
		"mesh_ip":   schema.StringAttribute{Computed: true, Description: "Address in the private mesh."},
		"os":        schema.StringAttribute{Computed: true, Description: "Operating system."},
		"arch":      schema.StringAttribute{Computed: true, Description: "CPU architecture."},
		"labels":    schema.MapAttribute{Computed: true, ElementType: types.StringType, Description: "Node labels."},
	},
}, func(ctx context.Context, c *client.Client, m *nodeDataModel) error {
	o, err := pick(ctx, c, m.ID, "name", m.Name, "/nodes/", "/nodes")
	if err != nil {
		return err
	}
	setStr(&m.ID, o, "id")
	setStr(&m.Name, o, "name")
	setStr(&m.State, o, "state")
	setStr(&m.PublicIP, o, "public_ip")
	setStr(&m.MeshIP, o, "mesh_ip")
	setStr(&m.OS, o, "os")
	setStr(&m.Arch, o, "arch")
	setStrMap(&m.Labels, client.Sub(o, "labels"))
	return nil
})

// ---- polipan_project ----

type projectDataModel struct {
	ID   types.String `tfsdk:"id"`
	Name types.String `tfsdk:"name"`
	Slug types.String `tfsdk:"slug"`
}

var newProjectDataSource = newDataSource("project", schema.Schema{
	Description: "Reads a project by id or by slug.",
	Attributes: map[string]schema.Attribute{
		"id":   schema.StringAttribute{Optional: true, Computed: true, Description: "Project id. Set `id` or `slug`."},
		"slug": schema.StringAttribute{Optional: true, Computed: true, Description: "Project slug. Set `id` or `slug`."},
		"name": schema.StringAttribute{Computed: true, Description: "Project name."},
	},
}, func(ctx context.Context, c *client.Client, m *projectDataModel) error {
	o, err := pick(ctx, c, m.ID, "slug", m.Slug, "/projects/", "/projects")
	if err != nil {
		return err
	}
	setStr(&m.ID, o, "id")
	setStr(&m.Name, o, "name")
	setStr(&m.Slug, o, "slug")
	return nil
})

// ---- polipan_service ----

type serviceDataModel struct {
	ID            types.String `tfsdk:"id"`
	EnvironmentID types.String `tfsdk:"environment_id"`
	Name          types.String `tfsdk:"name"`
	Type          types.String `tfsdk:"type"`
	Image         types.String `tfsdk:"image"`
	Status        types.String `tfsdk:"status"`
}

var newServiceDataSource = newDataSource("service", schema.Schema{
	Description: "Reads a service by id, or by name in an environment.",
	Attributes: map[string]schema.Attribute{
		"id":             schema.StringAttribute{Optional: true, Computed: true, Description: "Service id. Set `id`, or `environment_id` and `name`."},
		"environment_id": schema.StringAttribute{Optional: true, Computed: true, Description: "Environment id. Needed with `name`."},
		"name":           schema.StringAttribute{Optional: true, Computed: true, Description: "Service name. Needs `environment_id`."},
		"type":           schema.StringAttribute{Computed: true, Description: "`app` or `database`."},
		"image":          schema.StringAttribute{Computed: true, Description: "Container image."},
		"status":         schema.StringAttribute{Computed: true, Description: "Service status, for example `running`."},
	},
}, func(ctx context.Context, c *client.Client, m *serviceDataModel) error {
	var o client.Obj
	var err error
	switch {
	case !m.ID.IsNull() && !m.ID.IsUnknown():
		o, err = c.Get(ctx, "/services/"+m.ID.ValueString())
	case !m.EnvironmentID.IsNull() && !m.Name.IsNull() && !m.Name.IsUnknown():
		o, err = c.Find(ctx, "/environments/"+m.EnvironmentID.ValueString()+"/services", "name", m.Name.ValueString())
		if client.IsNotFound(err) {
			return fmt.Errorf("no service %q in the environment", m.Name.ValueString())
		}
	default:
		return errors.New("set id, or environment_id and name")
	}
	if err != nil {
		return err
	}
	setStr(&m.ID, o, "id")
	setStr(&m.EnvironmentID, o, "environment_id")
	setStr(&m.Name, o, "name")
	setStr(&m.Type, o, "type")
	setStr(&m.Image, client.Sub(o, "config"), "image")
	setStr(&m.Status, o, "status")
	return nil
})

// ---- polipan_template ----

type templateDataModel struct {
	Slug        types.String `tfsdk:"slug"`
	Name        types.String `tfsdk:"name"`
	Version     types.String `tfsdk:"version"`
	Description types.String `tfsdk:"description"`
	Builtin     types.Bool   `tfsdk:"builtin"`
}

var newTemplateDataSource = newDataSource("template", schema.Schema{
	Description: "Reads a service template by slug, for example `postgres`.",
	Attributes: map[string]schema.Attribute{
		"slug":        schema.StringAttribute{Required: true, Description: "Template slug."},
		"name":        schema.StringAttribute{Computed: true, Description: "Template name."},
		"version":     schema.StringAttribute{Computed: true, Description: "Template version."},
		"description": schema.StringAttribute{Computed: true, Description: "What the template makes."},
		"builtin":     schema.BoolAttribute{Computed: true, Description: "True for a template that comes with Polipan."},
	},
}, func(ctx context.Context, c *client.Client, m *templateDataModel) error {
	o, err := c.Get(ctx, "/templates/"+m.Slug.ValueString())
	if client.IsNotFound(err) {
		return fmt.Errorf("no template %q", m.Slug.ValueString())
	}
	if err != nil {
		return err
	}
	setStr(&m.Name, o, "name")
	setStr(&m.Version, o, "version")
	setStr(&m.Description, o, "description")
	setBool(&m.Builtin, o, "builtin")
	return nil
})
