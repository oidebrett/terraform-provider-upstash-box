package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ivobrett/terraform-provider-upstash-box/internal/client"
)

var _ resource.Resource = &publicURLResource{}

// publicURLResource exposes one box port at https://<box>-<port>.preview.box.upstash.com.
// Upstash returns the token/password only at creation, so they live in state.
type publicURLResource struct {
	client *client.Client
}

type publicURLModel struct {
	ID          types.String `tfsdk:"id"`
	BoxID       types.String `tfsdk:"box_id"`
	Port        types.Int64  `tfsdk:"port"`
	BearerToken types.Bool   `tfsdk:"bearer_token"`
	BasicAuth   types.Bool   `tfsdk:"basic_auth"`
	URL         types.String `tfsdk:"url"`
	Hostname    types.String `tfsdk:"hostname"`
	Token       types.String `tfsdk:"token"`
	Username    types.String `tfsdk:"username"`
	Password    types.String `tfsdk:"password"`
}

func NewPublicURLResource() resource.Resource { return &publicURLResource{} }

func (r *publicURLResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_public_url"
}

func (r *publicURLResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	replaceBool := []planmodifier.Bool{boolplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "Public HTTPS URL for a box port (WebSockets work). Traffic to a paused box wakes it. " +
			"bearer_token only works as an Authorization header, so leave both auth flags off for browser " +
			"dashboards that do their own login (OpenClaw token, Hermes basic auth).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, PlanModifiers: keep},
			"box_id": schema.StringAttribute{
				Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"port": schema.Int64Attribute{
				Required: true, PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"bearer_token": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false), PlanModifiers: replaceBool,
			},
			"basic_auth": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false), PlanModifiers: replaceBool,
			},
			"url":      schema.StringAttribute{Computed: true, PlanModifiers: keep},
			"hostname": schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "url without the scheme."},
			"token":    schema.StringAttribute{Computed: true, Sensitive: true, PlanModifiers: keep},
			"username": schema.StringAttribute{Computed: true, PlanModifiers: keep},
			"password": schema.StringAttribute{Computed: true, Sensitive: true, PlanModifiers: keep},
		},
	}
}

func (r *publicURLResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	c, ok := clientFrom(req.ProviderData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("got %T", req.ProviderData))
		return
	}
	r.client = c
}

func (r *publicURLResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan publicURLModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	u, err := r.client.CreatePublicURL(ctx, plan.BoxID.ValueString(), plan.Port.ValueInt64(),
		plan.BearerToken.ValueBool(), plan.BasicAuth.ValueBool())
	if err != nil {
		resp.Diagnostics.AddError("Creating public URL failed", err.Error())
		return
	}
	plan.ID = types.StringValue(fmt.Sprintf("%s-%d", plan.BoxID.ValueString(), plan.Port.ValueInt64()))
	plan.URL = types.StringValue(u.URL)
	plan.Hostname = types.StringValue(trimScheme(u.URL))
	plan.Token = types.StringValue(u.Token)
	plan.Username = types.StringValue(u.Username)
	plan.Password = types.StringValue(u.Password)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *publicURLResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state publicURLModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	items, err := r.client.ListPublicURLs(ctx, state.BoxID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Listing public URLs failed", err.Error())
		return
	}
	for _, it := range items {
		if it.Port == state.Port.ValueInt64() {
			state.URL = types.StringValue(it.URL)
			state.Hostname = types.StringValue(trimScheme(it.URL))
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			return
		}
	}
	resp.State.RemoveResource(ctx) // deleted outside Terraform
}

// Update is never reached: every configurable attribute forces replacement.
func (r *publicURLResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Unexpected update", "upstashbox_public_url has no in-place updates")
}

func (r *publicURLResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state publicURLModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.DeletePublicURL(ctx, state.BoxID.ValueString(), state.Port.ValueInt64())
	if err != nil && !errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Deleting public URL failed", err.Error())
	}
}

func trimScheme(u string) string {
	for _, p := range []string{"https://", "http://"} {
		if len(u) > len(p) && u[:len(p)] == p {
			return u[len(p):]
		}
	}
	return u
}
