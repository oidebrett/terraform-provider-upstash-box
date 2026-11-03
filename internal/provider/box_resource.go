package provider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/oidebrett/terraform-provider-upstash-box/internal/client"
)

var (
	_ resource.Resource                = &boxResource{}
	_ resource.ResourceWithImportState = &boxResource{}
)

type boxResource struct {
	client *client.Client
}

type boxModel struct {
	ID                  types.String  `tfsdk:"id"`
	Name                types.String  `tfsdk:"name"`
	Size                types.String  `tfsdk:"size"`
	Runtime             types.String  `tfsdk:"runtime"`
	KeepAlive           types.Bool    `tfsdk:"keep_alive"`
	Labels              types.List    `tfsdk:"labels"`
	Env                 types.Map     `tfsdk:"env"`
	InitCommand         types.String  `tfsdk:"init_command"`
	CreateTimeout       types.String  `tfsdk:"create_timeout"`
	Status              types.String  `tfsdk:"status"`
	StorageGB           types.Int64   `tfsdk:"storage_gb"`
	SSHHost             types.String  `tfsdk:"ssh_host"`
	TotalComputeCostUSD types.Float64 `tfsdk:"total_compute_cost_usd"`
}

func NewBoxResource() resource.Resource { return &boxResource{} }

func (r *boxResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_box"
}

func (r *boxResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "An Upstash Box: a Debian container (arm64) running as boxuser with passwordless sudo. " +
			"No Docker, systemd or kernel features inside — run processes directly and start them from init_command.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Box id, e.g. holy-beetle-40678.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured(), stringplanmodifier.UseStateForUnknown()},
			},
			"size": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("small"),
				Description:   "small (2 vCPU/4 GB), medium (4/8), large (8/16).",
				PlanModifiers: replace,
				Validators:    []validator.String{stringvalidator.OneOf("small", "medium", "large")},
			},
			"runtime": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("node"),
				Description:   "Pre-installed runtime image: node, python, golang, ruby, rust (or *-alpine).",
				PlanModifiers: replace,
			},
			"keep_alive": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "true = always on, fixed monthly price (paid plan only). " +
					"false = serverless: pauses when idle, billed per active CPU time, woken by public URL traffic.",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"labels": schema.ListAttribute{
				ElementType: types.StringType, Optional: true,
				Description:   "Up to 5 labels (max 20 chars each).",
				PlanModifiers: []planmodifier.List{listplanmodifier.RequiresReplace()},
			},
			"env": schema.MapAttribute{
				ElementType: types.StringType, Optional: true, Sensitive: true,
				Description:   "Environment variables set at creation. Changing them replaces the box.",
				PlanModifiers: []planmodifier.Map{mapplanmodifier.RequiresReplace()},
			},
			"init_command": schema.StringAttribute{
				Optional: true,
				Description: "Runs on every container start (create and each resume). Use it to start long-running " +
					"processes; do the one-time install with upstashbox_exec. Updated in place.",
			},
			"create_timeout": schema.StringAttribute{
				Optional:    true,
				Description: "How long to wait for the box to leave 'creating' (Go duration, default 5m).",
			},
			"status": schema.StringAttribute{
				Computed: true, Description: "Status at the last refresh: idle, running, paused, error.",
			},
			"storage_gb": schema.Int64Attribute{
				Computed: true,
			},
			"ssh_host": schema.StringAttribute{
				Computed:      true,
				Description:   "SSH target <box-id>@<api host>; the password is the Box API key.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"total_compute_cost_usd": schema.Float64Attribute{
				Computed: true, Description: "Compute cost so far, as reported by Upstash.",
			},
		},
	}
}

func (r *boxResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	c, ok := clientFrom(req.ProviderData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("got %T", req.ProviderData))
		return
	}
	r.client = c
}

func (r *boxResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan boxModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, err := parseTimeout(plan.CreateTimeout, 5*time.Minute)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("create_timeout"), "Invalid duration", err.Error())
		return
	}

	in := client.CreateBoxRequest{
		Name:      plan.Name.ValueString(),
		Size:      plan.Size.ValueString(),
		Runtime:   plan.Runtime.ValueString(),
		KeepAlive: plan.KeepAlive.ValueBool(),
	}
	if !plan.InitCommand.IsNull() {
		s := plan.InitCommand.ValueString()
		in.InitCommand = &s
	}
	if !plan.Labels.IsNull() {
		resp.Diagnostics.Append(plan.Labels.ElementsAs(ctx, &in.Labels, false)...)
	}
	if !plan.Env.IsNull() {
		resp.Diagnostics.Append(plan.Env.ElementsAs(ctx, &in.EnvVars, false)...)
	}
	if resp.Diagnostics.HasError() {
		return
	}

	box, err := r.client.CreateBox(ctx, in)
	if err != nil {
		resp.Diagnostics.AddError("Creating Upstash box failed", err.Error())
		return
	}
	// Save the id straight away so a failed wait still leaves the box in state
	// (tainted) and `terraform destroy` removes it instead of orphaning it.
	plan.ID = types.StringValue(box.ID)
	r.fill(&plan, box)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)

	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	box, err = r.client.WaitReady(wctx, box.ID)
	if err != nil {
		resp.Diagnostics.AddError("Upstash box did not become ready", err.Error())
		return
	}
	r.fill(&plan, box)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *boxResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state boxModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	box, err := r.client.GetBox(ctx, state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Reading Upstash box failed", err.Error())
		return
	}
	r.fill(&state, box)
	if !state.InitCommand.IsNull() {
		// Only track drift when Terraform manages the init command.
		cmd, err := r.client.GetInitCommand(ctx, box.ID)
		if err != nil {
			resp.Diagnostics.AddError("Reading init command failed", err.Error())
			return
		}
		state.InitCommand = types.StringValue(cmd)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *boxResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// Every attribute except init_command (and create_timeout) forces replacement.
	var plan, state boxModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !plan.InitCommand.Equal(state.InitCommand) {
		if err := r.client.SetInitCommand(ctx, state.ID.ValueString(), plan.InitCommand.ValueString()); err != nil {
			resp.Diagnostics.AddError("Updating init command failed", err.Error())
			return
		}
	}
	box, err := r.client.GetBox(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Reading Upstash box failed", err.Error())
		return
	}
	plan.ID = state.ID
	r.fill(&plan, box)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *boxResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state boxModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteBox(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Deleting Upstash box failed", err.Error())
	}
}

func (r *boxResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// fill copies server-side values into the model. Size/runtime/keep_alive are
// only copied when the API returns them, so an omitted field never shows drift.
func (r *boxResource) fill(m *boxModel, b *client.Box) {
	m.Status = types.StringValue(b.Status)
	m.StorageGB = types.Int64Value(b.StorageGB)
	m.TotalComputeCostUSD = types.Float64Value(b.TotalComputeCostUSD)
	m.SSHHost = types.StringValue(fmt.Sprintf("%s@%s", b.ID, hostOf(r.client.BaseURL)))
	if b.Size != "" {
		m.Size = types.StringValue(b.Size)
	}
	if b.Runtime != "" {
		m.Runtime = types.StringValue(b.Runtime)
	}
	m.KeepAlive = types.BoolValue(b.KeepAlive)
	m.Name = types.StringValue(b.Name)
}

func hostOf(baseURL string) string {
	if u, err := url.Parse(baseURL); err == nil && u.Host != "" {
		return u.Host
	}
	return baseURL
}
