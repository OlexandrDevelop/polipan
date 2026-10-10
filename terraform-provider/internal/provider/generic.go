// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"

	"github.com/polipan/terraform-provider-polipan/internal/client"
)

// spec describes one resource. M is the model: a struct with tfsdk tags. Every model has the
// attribute "id". The functions work on the model and talk to the API with the client.
type spec[M any] struct {
	// name is the resource name without the provider prefix, for example "project".
	name   string
	schema schema.Schema
	// create sends the object to the API and fills the computed values in m.
	create func(ctx context.Context, c *client.Client, m *M) error
	// read refreshes m from the API. It returns false when the object is gone.
	read func(ctx context.Context, c *client.Client, m *M) (bool, error)
	// update applies the plan. state is the current state.
	update func(ctx context.Context, c *client.Client, plan, state *M) error
	// del removes the object. A missing object is not an error.
	del func(ctx context.Context, c *client.Client, m *M) error
	// importID turns the import id into the attributes the read function needs. Nil means id only.
	importID func(id string) (map[string]any, error)
}

type genericResource[M any] struct {
	s spec[M]
	c *client.Client
}

// newResource makes the factory the provider needs.
func newResource[M any](s spec[M]) func() resource.Resource {
	return func() resource.Resource { return &genericResource[M]{s: s} }
}

func (r *genericResource[M]) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.s.name
}

func (r *genericResource[M]) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = r.s.schema
}

func (r *genericResource[M]) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Wrong provider data", fmt.Sprintf("expected *client.Client, got %T", req.ProviderData))
		return
	}
	r.c = c
}

func (r *genericResource[M]) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m M
	if resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...); resp.Diagnostics.HasError() {
		return
	}
	if err := r.s.create(ctx, r.c, &m); err != nil {
		resp.Diagnostics.AddError("Cannot create "+r.s.name, err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *genericResource[M]) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m M
	if resp.Diagnostics.Append(req.State.Get(ctx, &m)...); resp.Diagnostics.HasError() {
		return
	}
	found, err := r.s.read(ctx, r.c, &m)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read "+r.s.name, err.Error())
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *genericResource[M]) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state M
	if resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...); resp.Diagnostics.HasError() {
		return
	}
	if resp.Diagnostics.Append(req.State.Get(ctx, &state)...); resp.Diagnostics.HasError() {
		return
	}
	if err := r.s.update(ctx, r.c, &plan, &state); err != nil {
		resp.Diagnostics.AddError("Cannot update "+r.s.name, err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *genericResource[M]) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m M
	if resp.Diagnostics.Append(req.State.Get(ctx, &m)...); resp.Diagnostics.HasError() {
		return
	}
	if err := r.s.del(ctx, r.c, &m); err != nil {
		resp.Diagnostics.AddError("Cannot delete "+r.s.name, err.Error())
	}
}

// ImportState supports import by id (REQ-TF-004). Objects that the API reads only through their
// parent use "parent_id/id", see the docs of each resource.
func (r *genericResource[M]) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	attrs := map[string]any{"id": req.ID}
	if r.s.importID != nil {
		var err error
		if attrs, err = r.s.importID(req.ID); err != nil {
			resp.Diagnostics.AddError("Bad import id", err.Error())
			return
		}
	}
	for k, v := range attrs {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(k), v)...)
	}
}
