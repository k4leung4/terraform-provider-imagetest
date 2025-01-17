package provider

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/chainguard-dev/terraform-provider-imagetest/internal/harnesses/k3s"
	"github.com/chainguard-dev/terraform-provider-imagetest/internal/log"
	"github.com/docker/docker/api/types/mount"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

const (
	defaultHarnessK3sCreateTimeout = 5 * time.Minute
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ resource.Resource                = &HarnessK3sResource{}
	_ resource.ResourceWithConfigure   = &HarnessK3sResource{}
	_ resource.ResourceWithImportState = &HarnessK3sResource{}
	_ resource.ResourceWithModifyPlan  = &HarnessK3sResource{}
)

func NewHarnessK3sResource() resource.Resource {
	return &HarnessK3sResource{}
}

// HarnessK3sResource defines the resource implementation.
type HarnessK3sResource struct {
	HarnessResource
}

// HarnessK3sResourceModel describes the resource data model.
type HarnessK3sResourceModel struct {
	Id        types.String             `tfsdk:"id"`
	Name      types.String             `tfsdk:"name"`
	Inventory InventoryDataSourceModel `tfsdk:"inventory"`
	Skipped   types.Bool               `tfsdk:"skipped"`

	Image                types.String                             `tfsdk:"image"`
	DisableCni           types.Bool                               `tfsdk:"disable_cni"`
	DisableTraefik       types.Bool                               `tfsdk:"disable_traefik"`
	DisableMetricsServer types.Bool                               `tfsdk:"disable_metrics_server"`
	Registries           map[string]RegistryResourceModel         `tfsdk:"registries"`
	Networks             map[string]ContainerResourceModelNetwork `tfsdk:"networks"`
	Sandbox              types.Object                             `tfsdk:"sandbox"`
	Timeouts             timeouts.Value                           `tfsdk:"timeouts"`
}

type RegistryResourceModel struct {
	Auth   *RegistryResourceAuthModel   `tfsdk:"auth"`
	Tls    *RegistryResourceTlsModel    `tfsdk:"tls"`
	Mirror *RegistryResourceMirrorModel `tfsdk:"mirror"`
}

type RegistryResourceAuthModel struct {
	Username types.String `tfsdk:"username"`
	Password types.String `tfsdk:"password"`
	Auth     types.String `tfsdk:"auth"`
}

type RegistryResourceTlsModel struct {
	CertFile types.String `tfsdk:"cert_file"`
	KeyFile  types.String `tfsdk:"key_file"`
	CaFile   types.String `tfsdk:"ca_file"`
}

type RegistryResourceMirrorModel struct {
	Endpoints types.List `tfsdk:"endpoints"`
}

type HarnessK3sSandboxResourceModel struct {
	Image      types.String                             `tfsdk:"image"`
	Privileged types.Bool                               `tfsdk:"privileged"`
	Envs       types.Map                                `tfsdk:"envs"`
	Mounts     []ContainerResourceMountModel            `tfsdk:"mounts"`
	Networks   map[string]ContainerResourceModelNetwork `tfsdk:"networks"`
}

func (r *HarnessK3sResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_harness_k3s"
}

func (r *HarnessK3sResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: `A harness that runs steps in a sandbox container networked to a running k3s cluster.`,

		Attributes: addHarnessResourceSchemaAttributes(map[string]schema.Attribute{
			"disable_cni": schema.BoolAttribute{
				Description: "When true, the builtin (flannel) CNI will be disabled.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"disable_traefik": schema.BoolAttribute{
				Description: "When true, the builtin traefik ingress controller will be disabled.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
			},
			"disable_metrics_server": schema.BoolAttribute{
				Description: "When true, the builtin metrics server will be disabled.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
			},
			"image": schema.StringAttribute{
				Description: "The full image reference to use for the k3s container.",
				Optional:    true,
			},
			"registries": schema.MapNestedAttribute{
				Description: "A map of registries containing configuration for optional auth, tls, and mirror configuration.",
				Optional:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"auth": schema.SingleNestedAttribute{
							Optional: true,
							Attributes: map[string]schema.Attribute{
								"username": schema.StringAttribute{
									Optional: true,
								},
								"password": schema.StringAttribute{
									Optional:  true,
									Sensitive: true,
								},
								"auth": schema.StringAttribute{
									Optional: true,
								},
							},
						},
						"tls": schema.SingleNestedAttribute{
							Optional: true,
							Attributes: map[string]schema.Attribute{
								"cert_file": schema.StringAttribute{
									Optional: true,
								},
								"key_file": schema.StringAttribute{
									Optional: true,
								},
								"ca_file": schema.StringAttribute{
									Optional: true,
								},
							},
						},
						"mirror": schema.SingleNestedAttribute{
							Optional: true,
							Attributes: map[string]schema.Attribute{
								"endpoints": schema.ListAttribute{
									ElementType: basetypes.StringType{},
									Optional:    true,
								},
							},
						},
					},
				},
			},
			"networks": schema.MapNestedAttribute{
				Description: "A map of existing networks to attach the harness containers to.",
				Optional:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							Description: "The name of the existing network to attach the harness containers to.",
							Required:    true,
						},
					},
				},
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{
				Create:            true,
				CreateDescription: "The maximum time to wait for the k3s harness to be created.",
			}),
			"sandbox": schema.SingleNestedAttribute{
				Description: "A map of configuration for the sandbox container.",
				Optional:    true,
				Attributes: addContainerResourceSchemaAttributes(
					map[string]schema.Attribute{
						// Override the default image to use one with kubectl instead
						"image": schema.StringAttribute{
							Description: "The full image reference to use for the container.",
							Optional:    true,
							Computed:    true,
							Default:     stringdefault.StaticString("cgr.dev/chainguard/kubectl:latest-dev"),
						},
					},
				),
			},
		}),
	}
}

func (r *HarnessK3sResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	ctx = log.WithCtx(ctx, r.store.Logger())

	var data HarnessK3sResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	skipped := r.ShouldSkip(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		return
	}
	data.Skipped = types.BoolValue(skipped)

	if data.Skipped.ValueBool() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
		return
	}

	timeout, diags := data.Timeouts.Create(ctx, defaultHarnessK3sCreateTimeout)
	resp.Diagnostics.Append(diags...)

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	kopts := []k3s.Option{
		k3s.WithCniDisabled(data.DisableCni.ValueBool()),
		k3s.WithTraefikDisabled(data.DisableTraefik.ValueBool()),
		k3s.WithMetricsServerDisabled(data.DisableMetricsServer.ValueBool()),
	}

	if !data.Image.IsNull() {
		ref, err := name.ParseReference(data.Image.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("invalid resource input", fmt.Sprintf("invalid image reference: %s", err))
			return
		}
		kopts = append(kopts, k3s.WithImageRef(ref))
	}

	if !data.Sandbox.IsNull() {
		sandbox := &HarnessK3sSandboxResourceModel{}
		resp.Diagnostics.Append(data.Sandbox.As(ctx, &sandbox, basetypes.ObjectAsOptions{})...)
		if resp.Diagnostics.HasError() {
			return
		}

		if !sandbox.Image.IsNull() {
			ref, err := name.ParseReference(sandbox.Image.ValueString())
			if err != nil {
				resp.Diagnostics.AddError("invalid resource input", fmt.Sprintf("invalid sandbox image reference: %s", err))
				return
			}
			kopts = append(kopts, k3s.WithSandboxImageRef(ref))
		}

		for _, m := range sandbox.Mounts {
			src, err := filepath.Abs(m.Source.ValueString())
			if err != nil {
				resp.Diagnostics.AddError("invalid resource input", fmt.Sprintf("invalid mount source: %s", err))
				return
			}

			kopts = append(kopts, k3s.WithSandboxMounts(mount.Mount{
				Type:   mount.TypeBind,
				Source: src,
				Target: m.Destination.ValueString(),
			}))
		}

		for _, n := range sandbox.Networks {
			kopts = append(kopts, k3s.WithSandboxNetworks(n.Name.ValueString()))
		}

		envs := make(map[string]string)
		if diags := sandbox.Envs.ElementsAs(ctx, &envs, false); diags.HasError() {
			resp.Diagnostics.AddError("invalid resource input", fmt.Sprintf("invalid envs input: %s", diags.Errors()))
			return
		}
		kopts = append(kopts, k3s.WithSandboxEnv(envs))
	}

	registries := make(map[string]RegistryResourceModel)
	if data.Registries != nil {
		registries = data.Registries
	}

	networks := []string{}
	if data.Networks != nil {
		for _, v := range data.Networks {
			networks = append(networks, v.Name.ValueString())
		}
	}

	if r.store.providerResourceData.Harnesses != nil {
		if pc := r.store.providerResourceData.Harnesses.K3s; pc != nil {
			for k, v := range pc.Registries {
				registries[k] = v
			}

			for _, v := range pc.Networks {
				networks = append(networks, v.Name.ValueString())
			}
		}
	}

	for rname, rdata := range registries {
		if rdata.Auth != nil {
			if rdata.Auth.Auth.IsNull() && rdata.Auth.Password.IsNull() && rdata.Auth.Username.IsNull() {
				kopts = append(kopts, k3s.WithAuthFromKeychain(rname))
			} else {
				kopts = append(kopts, k3s.WithAuthFromStatic(rname, rdata.Auth.Username.ValueString(), rdata.Auth.Password.ValueString(), rdata.Auth.Auth.ValueString()))
			}
		}

		if rdata.Mirror != nil {
			endpoints := []string{}
			if diags := rdata.Mirror.Endpoints.ElementsAs(ctx, &endpoints, false); diags.HasError() {
				resp.Diagnostics.AddError("failed to convert mirror endpoints", "...")
				return
			}
			kopts = append(kopts, k3s.WithRegistryMirror(rname, endpoints...))
		}
	}

	kopts = append(kopts, k3s.WithNetworks(networks...))

	harness, err := k3s.New(data.Id.ValueString(), r.store.cli, kopts...)
	if err != nil {
		resp.Diagnostics.AddError("failed to initialize k3s harness", err.Error())
		return
	}
	r.store.harnesses.Set(data.Id.ValueString(), harness)

	log.Info(ctx, fmt.Sprintf("creating k3s harness [%s]", data.Id.ValueString()))

	// Finally, create the harness
	// TODO: Change this signature
	if _, err := harness.Setup()(ctx); err != nil {
		resp.Diagnostics.AddError("failed to setup harness", err.Error())
		return
	}

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *HarnessK3sResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data HarnessK3sResourceModel

	// Read Terraform prior state data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	// Save updated data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *HarnessK3sResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data HarnessK3sResourceModel

	// Read Terraform plan data into the model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	// Save updated data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *HarnessK3sResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data HarnessK3sResourceModel

	// Read Terraform prior state data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}
}

func (r *HarnessK3sResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
