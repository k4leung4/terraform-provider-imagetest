package provider

import (
	"context"
	"fmt"

	"github.com/chainguard-dev/terraform-provider-imagetest/internal/inventory"
	"github.com/chainguard-dev/terraform-provider-imagetest/internal/log"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// HarnessResource provides common methods for all HarnessResource
// implementations.
type HarnessResource struct {
	store *ProviderStore
}

// FeatureHarnessResourceModel is the common data model all harnesses output to
// be passed into dependent features.
type FeatureHarnessResourceModel struct {
	Id        types.String             `tfsdk:"id"`
	Name      types.String             `tfsdk:"name"`
	Inventory InventoryDataSourceModel `tfsdk:"inventory"`
	Skipped   types.Bool               `tfsdk:"skipped"`
}

func (r *HarnessResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	// Prevent panic if the provider has not been configured.
	if req.ProviderData == nil {
		return
	}

	store, ok := req.ProviderData.(*ProviderStore)
	if !ok {
		resp.Diagnostics.AddError("invalid provider data", "...")
		return
	}

	r.store = store
}

// ModifyPlan adds the harness to the inventory during both the plan and apply
// phase. This uses the more verbose GetAttribute() instead of Get() because
// terraform-plugin-framework does not support embedding models without nesting.
func (r *HarnessResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	ctx = log.WithCtx(ctx, r.store.Logger())

	if !req.State.Raw.IsNull() {
		// TODO: This currently exists to handle `terraform destroy` which occurs
		// during acceptance testing. In the future, we should properly handle any
		// pre-existing state
		return
	}

	inv := InventoryDataSourceModel{}
	if diags := req.Config.GetAttribute(ctx, path.Root("inventory"), &inv); diags.HasError() {
		resp.Diagnostics.AddError("failed to add harness", "retrieving inventory")
		return
	}

	var name string
	if diags := req.Config.GetAttribute(ctx, path.Root("name"), &name); diags.HasError() {
		resp.Diagnostics.Append(diags.Errors()...)
		resp.Diagnostics.AddError("failed to add harness", "getting harness name")
		return
	}

	// the ID is the {name}-{inventory-hash}. its intentially chose to be more
	// user friendly than just a hash, since it is prepended to resources the
	// harnesses will create.
	invEnc, err := r.store.Encode(inv.Seed.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("failed to add harness", "encoding harness id")
		return
	}

	id := fmt.Sprintf("%s-%s", name, invEnc)

	if diag := resp.Plan.SetAttribute(ctx, path.Root("id"), id); diag.HasError() {
		resp.Diagnostics.Append(diag.Errors()...)
		resp.Diagnostics.AddError("failed to set harness id", "...")
		return
	}

	added, err := r.store.Inventory(inv).AddHarness(ctx, inventory.Harness(id))
	if err != nil {
		resp.Diagnostics.AddError("failed to add harness", err.Error())
	}

	if added {
		log.Info(ctx, fmt.Sprintf("Harness.ModifyPlan() | harness [%s] added to inventory", id))
	}
}

func (r *HarnessResource) ShouldSkip(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) bool {
	ctx = log.WithCtx(ctx, r.store.Logger())

	inv := InventoryDataSourceModel{}
	if diags := req.Config.GetAttribute(ctx, path.Root("inventory"), &inv); diags.HasError() {
		resp.Diagnostics.AddError("failed to add harness", "retrieving inventory")
		return false
	}

	var id string
	if diags := req.Plan.GetAttribute(ctx, path.Root("id"), &id); diags.HasError() {
		resp.Diagnostics.Append(diags.Errors()...)
		resp.Diagnostics.AddError("failed to add harness", "getting harness id")
		return false
	}

	feats, err := r.store.Inventory(inv).GetFeatures(ctx, inventory.Harness(id))
	if err != nil {
		resp.Diagnostics.AddError("failed to get features from harness", err.Error())
		return false
	}

	// skipping is only possible when labels are specified
	if len(r.store.labels) == 0 {
		return false
	}

	skip := false
	for _, feat := range feats {
		for pk, pv := range r.store.labels {
			fv, ok := feat.Labels[pk]
			if ok && (fv != pv) {
				// if the feature label exists but the value doesn't match, skip
				skip = true
				break
			}
		}
	}

	if skip {
		resp.Diagnostics.AddWarning(
			fmt.Sprintf("skipping harness [%s] creation", id),
			"given provider runtime labels do not match feature labels")
	}

	return skip
}

//  AddHarnessSchemaAttributes adds common attributes to the given map. values
// provided in attrs will override any specified defaults.
func addHarnessResourceSchemaAttributes(attrs map[string]schema.Attribute) map[string]schema.Attribute {
	defaults := map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Description: "The unique identifier for the harness. This is generated from the inventory seed and harness name.",
			Computed:    true,
		},
		"name": schema.StringAttribute{
			Description: "The name of the harness. This must be unique within the scope of the provided inventory.",
			Required:    true,
		},
		"inventory": schema.SingleNestedAttribute{
			Description: "The inventory this harness belongs to. This is received as a direct input from a data.imagetest_inventory data source.",
			Required:    true,
			Attributes: map[string]schema.Attribute{
				"seed": schema.StringAttribute{
					Required: true,
				},
			},
		},
		"skipped": schema.BoolAttribute{
			Description: "Whether or not to skip creating the harness based on runtime inputs and the dependent features within this inventory.",
			Computed:    true,
		},
	}

	for k, v := range defaults {
		attrs[k] = v
	}

	return attrs
}

func addFeatureHarnessResourceSchemaAttributes(attrs map[string]schema.Attribute) map[string]schema.Attribute {
	defaults := map[string]schema.Attribute{
		"harness": schema.SingleNestedAttribute{
			Required: true,
			Attributes: map[string]schema.Attribute{
				"id": schema.StringAttribute{
					Required: true,
				},
				"name": schema.StringAttribute{
					Required: true,
				},
				"skipped": schema.BoolAttribute{
					Required: true,
				},
				"inventory": schema.SingleNestedAttribute{
					Required: true,
					Attributes: map[string]schema.Attribute{
						"seed": schema.StringAttribute{
							Required: true,
						},
					},
				},
			},
		},
	}

	for k, v := range defaults {
		attrs[k] = v
	}

	return attrs
}
