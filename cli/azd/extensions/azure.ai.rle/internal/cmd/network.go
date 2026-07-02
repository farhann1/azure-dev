// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"github.com/spf13/cobra"
)

const (
	defaultNetworkName           = "rle-1p"
	defaultNetworkLocation       = "eastus2"
	defaultAddressPrefix         = "10.60.0.0/16"
	defaultTrainingSubnetPrefix  = "10.60.1.0/24"
	defaultSandboxSubnetPrefix   = "10.60.2.0/23"
	defaultSandboxRuntimePort    = 8000
	privateEndpointVisibility    = "private_1p_vnet"
	azureContainerAppsDelegation = "Microsoft.App/environments"
)

type rleNetworkFlags struct {
	resourceGroup        string
	location             string
	name                 string
	addressPrefix        string
	trainingSubnetPrefix string
	sandboxSubnetPrefix  string
	sandboxImage         string
	sandboxAppName       string
	containerEnvName     string
	workspaceName        string
	sandboxTargetPort    int
	sandboxIngressPort   int
	skipSandboxApp       bool
}

type rleNetworkOutput struct {
	ResourceGroup               string            `json:"resourceGroup"`
	Location                    string            `json:"location"`
	VnetName                    string            `json:"vnetName"`
	TrainingSubnetName          string            `json:"trainingSubnetName"`
	SandboxSubnetName           string            `json:"sandboxSubnetName"`
	SandboxNsgName              string            `json:"sandboxNsgName"`
	ContainerAppEnvironmentName string            `json:"containerAppEnvironmentName"`
	PrivateDnsZoneName          string            `json:"privateDnsZoneName,omitempty"`
	SandboxAppName              string            `json:"sandboxAppName,omitempty"`
	RuntimeEndpoint             string            `json:"runtimeEndpoint,omitempty"`
	ControlPlaneOverride        map[string]string `json:"controlPlaneOverride,omitempty"`
}

func newNetworkCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "network",
		Short: "Manage 1P RLE network simulation resources",
	}
	cmd.AddCommand(newNetworkProvisionCommand())
	return cmd
}

func newNetworkProvisionCommand() *cobra.Command {
	flags := &rleNetworkFlags{
		location:             defaultNetworkLocation,
		name:                 defaultNetworkName,
		addressPrefix:        defaultAddressPrefix,
		trainingSubnetPrefix: defaultTrainingSubnetPrefix,
		sandboxSubnetPrefix:  defaultSandboxSubnetPrefix,
		sandboxTargetPort:    defaultSandboxRuntimePort,
		sandboxIngressPort:   443,
	}

	cmd := &cobra.Command{
		Use:   "provision",
		Short: "Provision a 1P VNet simulation where only the training subnet can reach sandbox APIs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := applyNetworkDefaults(flags); err != nil {
				return err
			}
			if err := validateNetworkFlags(flags); err != nil {
				return err
			}
			if _, err := exec.LookPath("az"); err != nil {
				return &azdext.LocalError{
					Message:    "Could not find Azure CLI \"az\" on PATH.",
					Code:       "rle_az_cli_not_found",
					Category:   azdext.LocalErrorCategoryUser,
					Suggestion: "Install Azure CLI, run az login, then retry the command.",
				}
			}

			result, err := provisionRleNetwork(cmd, flags)
			if err != nil {
				return err
			}

			body, err := json.MarshalIndent(result, "", "  ")
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(body))
			return err
		},
	}

	cmd.Flags().StringVar(&flags.resourceGroup, "resource-group", "", "Azure resource group to create/use.")
	cmd.Flags().StringVar(&flags.location, "location", flags.location, "Azure region for the simulation resources.")
	cmd.Flags().StringVar(&flags.name, "name", flags.name, "Base name used for generated Azure resources.")
	cmd.Flags().StringVar(&flags.addressPrefix, "address-prefix", flags.addressPrefix, "VNet address prefix.")
	cmd.Flags().StringVar(
		&flags.trainingSubnetPrefix,
		"training-subnet-prefix",
		flags.trainingSubnetPrefix,
		"CIDR prefix for the 1P training subnet.",
	)
	cmd.Flags().StringVar(
		&flags.sandboxSubnetPrefix,
		"sandbox-subnet-prefix",
		flags.sandboxSubnetPrefix,
		"CIDR prefix for the private sandbox subnet.",
	)
	cmd.Flags().StringVar(&flags.sandboxImage, "sandbox-image", "", "Sandbox container image. Defaults to rle.yaml image.")
	cmd.Flags().StringVar(&flags.sandboxAppName, "sandbox-app-name", "", "Container app name for the sandbox runtime.")
	cmd.Flags().StringVar(&flags.containerEnvName, "container-env-name", "", "Container Apps environment name.")
	cmd.Flags().StringVar(&flags.workspaceName, "logs-workspace-name", "", "Log Analytics workspace name.")
	cmd.Flags().IntVar(&flags.sandboxTargetPort, "sandbox-target-port", flags.sandboxTargetPort, "Sandbox container target port.")
	cmd.Flags().IntVar(&flags.sandboxIngressPort, "sandbox-ingress-port", flags.sandboxIngressPort, "Port clients use to reach sandbox ingress from the training subnet.")
	cmd.Flags().BoolVar(&flags.skipSandboxApp, "skip-sandbox-app", false, "Create only network resources, not a sandbox app.")

	return cmd
}

func applyNetworkDefaults(flags *rleNetworkFlags) error {
	if flags.sandboxImage != "" || flags.skipSandboxApp {
		return nil
	}

	state, stateErr := loadRleState()
	manifest, manifestErr := loadRleManifest(rleManifestFile)
	if manifestErr == nil {
		manifestState, err := stateFromManifest(manifest)
		if err != nil {
			return err
		}
		flags.sandboxImage = firstNonEmpty(manifestState.Image, manifestState.LocalImage)
		return nil
	}
	if stateErr == nil {
		flags.sandboxImage = firstNonEmpty(state.Image, state.LocalImage)
	}
	return nil
}

func validateNetworkFlags(flags *rleNetworkFlags) error {
	if strings.TrimSpace(flags.resourceGroup) == "" {
		return &azdext.LocalError{
			Message:    "Azure resource group is required for network provisioning.",
			Code:       "rle_network_resource_group_required",
			Category:   azdext.LocalErrorCategoryUser,
			Suggestion: "Pass --resource-group <name>.",
		}
	}
	if flags.sandboxTargetPort <= 0 {
		return &azdext.LocalError{
			Message:    "Sandbox container target port must be greater than 0.",
			Code:       "rle_network_invalid_sandbox_port",
			Category:   azdext.LocalErrorCategoryUser,
			Suggestion: "Pass --sandbox-target-port 8000 or another valid container port.",
		}
	}
	if flags.sandboxIngressPort <= 0 {
		return &azdext.LocalError{
			Message:    "Sandbox ingress port must be greater than 0.",
			Code:       "rle_network_invalid_sandbox_ingress_port",
			Category:   azdext.LocalErrorCategoryUser,
			Suggestion: "Pass --sandbox-ingress-port 443 or another valid ingress port.",
		}
	}
	if !flags.skipSandboxApp && strings.TrimSpace(flags.sandboxImage) == "" {
		return &azdext.LocalError{
			Message:    "Sandbox image is required unless --skip-sandbox-app is set.",
			Code:       "rle_network_sandbox_image_required",
			Category:   azdext.LocalErrorCategoryUser,
			Suggestion: "Pass --sandbox-image <image>, or run from a folder with rle.yaml.",
		}
	}
	return nil
}

func provisionRleNetwork(cmd *cobra.Command, flags *rleNetworkFlags) (*rleNetworkOutput, error) {
	resourceGroup := strings.TrimSpace(flags.resourceGroup)
	location := strings.TrimSpace(flags.location)
	baseName := slug(firstNonEmpty(flags.name, defaultNetworkName))
	vnetName := baseName + "-vnet"
	trainingSubnetName := "training-subnet"
	sandboxSubnetName := "sandbox-subnet"
	sandboxNsgName := baseName + "-sandbox-nsg"
	containerEnvName := firstNonEmpty(flags.containerEnvName, baseName+"-aca-env")
	workspaceName := firstNonEmpty(flags.workspaceName, baseName+"-logs")
	sandboxAppName := firstNonEmpty(flags.sandboxAppName, baseName+"-sandbox")

	az := func(args ...string) error {
		return runAz(cmd, args...)
	}
	azOut := func(args ...string) (string, error) {
		return runAzOutput(cmd, args...)
	}

	if err := az("group", "create", "--name", resourceGroup, "--location", location, "--output", "none"); err != nil {
		return nil, err
	}
	if err := az(
		"network", "vnet", "create",
		"--resource-group", resourceGroup,
		"--location", location,
		"--name", vnetName,
		"--address-prefixes", flags.addressPrefix,
		"--subnet-name", trainingSubnetName,
		"--subnet-prefixes", flags.trainingSubnetPrefix,
		"--output", "none"); err != nil {
		return nil, err
	}
	if err := az(
		"network", "vnet", "subnet", "create",
		"--resource-group", resourceGroup,
		"--vnet-name", vnetName,
		"--name", sandboxSubnetName,
		"--address-prefixes", flags.sandboxSubnetPrefix,
		"--delegations", azureContainerAppsDelegation,
		"--output", "none"); err != nil {
		return nil, err
	}
	if err := az(
		"network", "nsg", "create",
		"--resource-group", resourceGroup,
		"--name", sandboxNsgName,
		"--output", "none"); err != nil {
		return nil, err
	}
	ingressPort := fmt.Sprintf("%d", flags.sandboxIngressPort)
	targetPort := fmt.Sprintf("%d", flags.sandboxTargetPort)
	if err := az(
		"network", "nsg", "rule", "create",
		"--resource-group", resourceGroup,
		"--nsg-name", sandboxNsgName,
		"--name", "AllowTrainingToSandboxRuntime",
		"--priority", "100",
		"--direction", "Inbound",
		"--access", "Allow",
		"--protocol", "Tcp",
		"--source-address-prefixes", flags.trainingSubnetPrefix,
		"--destination-port-ranges", ingressPort,
		"--output", "none"); err != nil {
		return nil, err
	}
	if err := az(
		"network", "nsg", "rule", "create",
		"--resource-group", resourceGroup,
		"--nsg-name", sandboxNsgName,
		"--name", "DenyOtherVNetSandboxRuntime",
		"--priority", "200",
		"--direction", "Inbound",
		"--access", "Deny",
		"--protocol", "Tcp",
		"--source-address-prefixes", "VirtualNetwork",
		"--destination-port-ranges", ingressPort,
		"--output", "none"); err != nil {
		return nil, err
	}
	if err := az(
		"network", "vnet", "subnet", "update",
		"--resource-group", resourceGroup,
		"--vnet-name", vnetName,
		"--name", sandboxSubnetName,
		"--network-security-group", sandboxNsgName,
		"--output", "none"); err != nil {
		return nil, err
	}

	sandboxSubnetId, err := azOut(
		"network", "vnet", "subnet", "show",
		"--resource-group", resourceGroup,
		"--vnet-name", vnetName,
		"--name", sandboxSubnetName,
		"--query", "id",
		"--output", "tsv")
	if err != nil {
		return nil, err
	}

	if err := az(
		"monitor", "log-analytics", "workspace", "create",
		"--resource-group", resourceGroup,
		"--location", location,
		"--workspace-name", workspaceName,
		"--output", "none"); err != nil {
		return nil, err
	}
	workspaceId, err := azOut(
		"monitor", "log-analytics", "workspace", "show",
		"--resource-group", resourceGroup,
		"--workspace-name", workspaceName,
		"--query", "customerId",
		"--output", "tsv")
	if err != nil {
		return nil, err
	}
	workspaceKey, err := azOut(
		"monitor", "log-analytics", "workspace", "get-shared-keys",
		"--resource-group", resourceGroup,
		"--workspace-name", workspaceName,
		"--query", "primarySharedKey",
		"--output", "tsv")
	if err != nil {
		return nil, err
	}

	if err := az(
		"containerapp", "env", "create",
		"--resource-group", resourceGroup,
		"--location", location,
		"--name", containerEnvName,
		"--infrastructure-subnet-resource-id", sandboxSubnetId,
		"--logs-workspace-id", workspaceId,
		"--logs-workspace-key", workspaceKey,
		"--internal-only",
		"--output", "none"); err != nil {
		return nil, err
	}

	defaultDomain, err := azOut(
		"containerapp", "env", "show",
		"--resource-group", resourceGroup,
		"--name", containerEnvName,
		"--query", "properties.defaultDomain",
		"--output", "tsv")
	if err != nil {
		return nil, err
	}
	staticIp, err := azOut(
		"containerapp", "env", "show",
		"--resource-group", resourceGroup,
		"--name", containerEnvName,
		"--query", "properties.staticIp",
		"--output", "tsv")
	if err != nil {
		return nil, err
	}
	if err := ensurePrivateDns(cmd, resourceGroup, vnetName, defaultDomain, staticIp); err != nil {
		return nil, err
	}

	runtimeEndpoint := ""
	if !flags.skipSandboxApp {
		if err := az(
			"containerapp", "create",
			"--resource-group", resourceGroup,
			"--name", sandboxAppName,
			"--environment", containerEnvName,
			"--image", flags.sandboxImage,
			"--target-port", targetPort,
			"--ingress", "internal",
			"--transport", "auto",
			"--min-replicas", "1",
			"--output", "none"); err != nil {
			return nil, err
		}
		fqdn, err := azOut(
			"containerapp", "show",
			"--resource-group", resourceGroup,
			"--name", sandboxAppName,
			"--query", "properties.configuration.ingress.fqdn",
			"--output", "tsv")
		if err != nil {
			return nil, err
		}
		runtimeEndpoint = "https://" + strings.TrimSpace(fqdn)
	}

	output := &rleNetworkOutput{
		ResourceGroup:               resourceGroup,
		Location:                    location,
		VnetName:                    vnetName,
		TrainingSubnetName:          trainingSubnetName,
		SandboxSubnetName:           sandboxSubnetName,
		SandboxNsgName:              sandboxNsgName,
		ContainerAppEnvironmentName: containerEnvName,
		PrivateDnsZoneName:          strings.TrimSpace(defaultDomain),
		SandboxAppName:              sandboxAppName,
		RuntimeEndpoint:             runtimeEndpoint,
	}
	if runtimeEndpoint != "" {
		output.ControlPlaneOverride = map[string]string{
			"RLESandboxDiskImage:Provider":                  "static-private-endpoint",
			"RLESandboxDiskImage:PrivateRuntimeEndpoint":    runtimeEndpoint,
			"RLESandboxDiskImage:PrivateEndpointVisibility": privateEndpointVisibility,
		}
	}
	return output, nil
}

func ensurePrivateDns(cmd *cobra.Command, resourceGroup string, vnetName string, zoneName string, staticIp string) error {
	zoneName = strings.TrimSpace(zoneName)
	staticIp = strings.TrimSpace(staticIp)
	if zoneName == "" || staticIp == "" {
		return nil
	}
	if err := runAz(
		cmd,
		"network", "private-dns", "zone", "create",
		"--resource-group", resourceGroup,
		"--name", zoneName,
		"--output", "none"); err != nil {
		return err
	}
	vnetId, err := runAzOutput(
		cmd,
		"network", "vnet", "show",
		"--resource-group", resourceGroup,
		"--name", vnetName,
		"--query", "id",
		"--output", "tsv")
	if err != nil {
		return err
	}
	if err := runAz(
		cmd,
		"network", "private-dns", "link", "vnet", "create",
		"--resource-group", resourceGroup,
		"--zone-name", zoneName,
		"--name", "rle-1p-vnet-link",
		"--virtual-network", strings.TrimSpace(vnetId),
		"--registration-enabled", "false",
		"--output", "none"); err != nil {
		return err
	}
	if err := runAz(
		cmd,
		"network", "private-dns", "record-set", "a", "create",
		"--resource-group", resourceGroup,
		"--zone-name", zoneName,
		"--name", "*",
		"--output", "none"); err != nil {
		return err
	}
	return runAz(
		cmd,
		"network", "private-dns", "record-set", "a", "add-record",
		"--resource-group", resourceGroup,
		"--zone-name", zoneName,
		"--record-set-name", "*",
		"--ipv4-address", staticIp,
		"--output", "none")
}

func runAz(cmd *cobra.Command, args ...string) error {
	_, err := runAzOutput(cmd, args...)
	return err
}

func runAzOutput(cmd *cobra.Command, args ...string) (string, error) {
	//nolint:gosec // az arguments are constructed from explicit command flags and passed without a shell.
	process := exec.CommandContext(cmd.Context(), "az", args...)
	process.Env = os.Environ()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	process.Stdout = &stdout
	process.Stderr = &stderr
	if err := process.Run(); err != nil {
		return "", &azdext.ServiceError{
			Message:     fmt.Sprintf("az %s failed: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String())),
			ServiceName: "azure-cli",
			Suggestion:  "Run az login, verify your subscription, and retry the command.",
		}
	}
	return strings.TrimSpace(stdout.String()), nil
}
