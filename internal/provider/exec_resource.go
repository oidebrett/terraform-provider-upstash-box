package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/oidebrett/terraform-provider-upstash-box/internal/client"
)

var _ resource.Resource = &execResource{}

// execResource runs a script once inside a box: the stand-in for cloud-init
// user_data, which boxes don't have. It re-runs only when replaced (box_id,
// script or triggers change).
type execResource struct {
	client *client.Client
}

type execModel struct {
	ID          types.String `tfsdk:"id"`
	BoxID       types.String `tfsdk:"box_id"`
	Script      types.String `tfsdk:"script"`
	Interpreter types.String `tfsdk:"interpreter"`
	Triggers    types.Map    `tfsdk:"triggers"`
	Timeout     types.String `tfsdk:"timeout"`
	FailOnError types.Bool   `tfsdk:"fail_on_error"`
	ExitCode    types.Int64  `tfsdk:"exit_code"`
	OutputTail  types.String `tfsdk:"output_tail"`
	LogPath     types.String `tfsdk:"log_path"`
}

func NewExecResource() resource.Resource { return &execResource{} }

func (r *execResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_exec"
}

func (r *execResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "Runs a script once in a box as boxuser (use `sudo -n` for root). The script runs detached " +
			"and is polled, so long installs are fine. Destroying this resource does nothing on the box.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"box_id": schema.StringAttribute{Required: true, PlanModifiers: replace},
			"script": schema.StringAttribute{
				Required: true, Sensitive: true, PlanModifiers: replace,
				Description: "Script body. Sensitive because bootstrap scripts carry API keys.",
			},
			"interpreter": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("bash"),
				PlanModifiers: replace,
			},
			"triggers": schema.MapAttribute{
				ElementType: types.StringType, Optional: true,
				Description:   "Arbitrary values; changing any of them re-runs the script.",
				PlanModifiers: []planmodifier.Map{mapplanmodifier.RequiresReplace()},
			},
			"timeout": schema.StringAttribute{
				Optional:    true,
				Description: "Go duration, default 30m.",
			},
			"fail_on_error": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "Fail the apply when the script exits non-zero (the run is then retried on the next apply).",
			},
			"exit_code":   schema.Int64Attribute{Computed: true},
			"output_tail": schema.StringAttribute{Computed: true, Sensitive: true, Description: "Last 4 KB of the log."},
			"log_path":    schema.StringAttribute{Computed: true, Description: "Full log inside the box."},
		},
	}
}

func (r *execResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	c, ok := clientFrom(req.ProviderData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("got %T", req.ProviderData))
		return
	}
	r.client = c
}

func (r *execResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan execModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, err := parseTimeout(plan.Timeout, 30*time.Minute)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("timeout"), "Invalid duration", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	boxID := plan.BoxID.ValueString()
	if err := r.client.EnsureAwake(ctx, boxID); err != nil {
		resp.Diagnostics.AddError("Box is not available", err.Error())
		return
	}
	runID := fmt.Sprintf("tfexec-%d", time.Now().UnixNano())
	res, err := r.client.RunDetached(ctx, boxID, runID, plan.Interpreter.ValueString(), plan.Script.ValueString(), 5*time.Second)
	if err != nil {
		resp.Diagnostics.AddError("Running script in box failed", err.Error())
		return
	}
	if res.ExitCode != 0 && plan.FailOnError.ValueBool() {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Script exited with code %d", res.ExitCode),
			fmt.Sprintf("Full log in the box at /tmp/%s.log. Last output:\n%s", runID, strings.TrimSpace(res.LogTail)))
		return
	}

	plan.ID = types.StringValue(boxID + "/" + runID)
	plan.ExitCode = types.Int64Value(res.ExitCode)
	plan.OutputTail = types.StringValue(res.LogTail)
	plan.LogPath = types.StringValue("/tmp/" + runID + ".log")
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read keeps state as is: a finished run has nothing to refresh. If the box is
// replaced, box_id changes and this resource is replaced (re-run) with it.
func (r *execResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
}

// Update only happens for timeout/fail_on_error, which don't affect a finished run.
func (r *execResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state execModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID, plan.ExitCode, plan.OutputTail, plan.LogPath = state.ID, state.ExitCode, state.OutputTail, state.LogPath
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *execResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
}
