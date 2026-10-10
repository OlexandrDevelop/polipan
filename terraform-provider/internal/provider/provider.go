// SPDX-License-Identifier: Apache-2.0

// Package provider is the Terraform provider of Polipan (REQ-TF-001). It is built with
// terraform-plugin-framework and talks only to the public API.
package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/polipan/terraform-provider-polipan/internal/client"
)

// Provider is the provider object.
type Provider struct{ version string }

// New returns the provider factory.
func New(version string) func() provider.Provider {
	return func() provider.Provider { return &Provider{version: version} }
}

type providerModel struct {
	URL   types.String `tfsdk:"url"`
	Token types.String `tfsdk:"token"`
}

func (p *Provider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "polipan"
	resp.Version = p.version
}

func (p *Provider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manage a Polipan server: nodes, projects, services, domains, volumes, variables, secrets, backups, alerts and access.",
		Attributes: map[string]schema.Attribute{
			"url": schema.StringAttribute{
				Optional:    true,
				Description: "Address of the Polipan server, for example `https://panel.example.com`. Can be set with the `POLIPAN_URL` environment variable.",
			},
			"token": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "API token with the `admin` scope. Can be set with the `POLIPAN_TOKEN` environment variable.",
			},
		},
	}
}

func (p *Provider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	if resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...); resp.Diagnostics.HasError() {
		return
	}
	if cfg.URL.IsUnknown() || cfg.Token.IsUnknown() {
		resp.Diagnostics.AddError("Unknown provider value", "url and token must be known when the provider is configured.")
		return
	}
	url, token := os.Getenv("POLIPAN_URL"), os.Getenv("POLIPAN_TOKEN")
	if !cfg.URL.IsNull() {
		url = cfg.URL.ValueString()
	}
	if !cfg.Token.IsNull() {
		token = cfg.Token.ValueString()
	}
	if url == "" {
		resp.Diagnostics.AddError("Missing url", "Set the url attribute or the POLIPAN_URL environment variable.")
	}
	if token == "" {
		resp.Diagnostics.AddError("Missing token", "Set the token attribute or the POLIPAN_TOKEN environment variable.")
	}
	if resp.Diagnostics.HasError() {
		return
	}
	c := client.New(url, token)
	resp.ResourceData = c
	resp.DataSourceData = c
}

func (p *Provider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		newResource(nodeTokenSpec),
		newResource(projectSpec),
		newResource(environmentSpec),
		newResource(serviceSpec),
		newResource(domainSpec),
		newResource(volumeSpec),
		newResource(variableSpec),
		newResource(secretSpec),
		newResource(backupTargetSpec),
		newResource(backupPolicySpec),
		newResource(alertChannelSpec),
		newResource(alertRuleSpec),
		newResource(teamSpec),
		newResource(memberSpec),
		newResource(ssoProviderSpec),
		newResource(apiTokenSpec),
	}
}

func (p *Provider) DataSources(context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		newNodeDataSource,
		newProjectDataSource,
		newServiceDataSource,
		newTemplateDataSource,
	}
}
