package provider

import (
	"context"
	"os"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/oidebrett/terraform-provider-upstash-box/internal/client"
)

type upstashBoxProviderModel struct {
	APIKey  types.String `tfsdk:"api_key"`
	BaseURL types.String `tfsdk:"base_url"`
}

type upstashBoxProvider struct {
	version string
}

var _ provider.Provider = &upstashBoxProvider{}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &upstashBoxProvider{version: version}
	}
}

func (p *upstashBoxProvider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "upstashbox" // resources are upstashbox_box, upstashbox_exec, upstashbox_public_url
	resp.Version = p.version
}

func (p *upstashBoxProvider) Schema(ctx context.Context, req provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Provisions Upstash Box containers (https://upstash.com/docs/box).",
		Attributes: map[string]schema.Attribute{
			"api_key": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "Upstash Box API key (not the Redis developer key). Falls back to UPSTASH_BOX_API_KEY.",
			},
			"base_url": schema.StringAttribute{
				Optional:    true,
				Description: "Box API base URL. Falls back to UPSTASH_BOX_BASE_URL, then " + client.DefaultBaseURL + ".",
			},
		},
	}
}

func (p *upstashBoxProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var data upstashBoxProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiKey := os.Getenv("UPSTASH_BOX_API_KEY")
	if !data.APIKey.IsNull() && !data.APIKey.IsUnknown() {
		apiKey = data.APIKey.ValueString()
	}
	baseURL := os.Getenv("UPSTASH_BOX_BASE_URL")
	if !data.BaseURL.IsNull() && !data.BaseURL.IsUnknown() {
		baseURL = data.BaseURL.ValueString()
	}
	if apiKey == "" {
		resp.Diagnostics.AddError("Missing Upstash Box API key",
			"Set api_key in the provider block or the UPSTASH_BOX_API_KEY environment variable.")
		return
	}

	c := client.New(baseURL, apiKey)
	resp.ResourceData = c
	resp.DataSourceData = c
}

func (p *upstashBoxProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewBoxResource,
		NewExecResource,
		NewPublicURLResource,
	}
}

func (p *upstashBoxProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return nil
}

// clientFrom extracts the configured client in a resource's Configure.
func clientFrom(data any) (*client.Client, bool) {
	if data == nil {
		return nil, true // provider not configured yet (validate/plan)
	}
	c, ok := data.(*client.Client)
	return c, ok
}

func parseTimeout(s types.String, def time.Duration) (time.Duration, error) {
	if s.IsNull() || s.IsUnknown() || s.ValueString() == "" {
		return def, nil
	}
	return time.ParseDuration(s.ValueString())
}
