// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package cmd

import "testing"

func TestValidateNetworkFlagsRequiresResourceGroup(t *testing.T) {
	err := validateNetworkFlags(&rleNetworkFlags{sandboxTargetPort: 8000, sandboxIngressPort: 443, sandboxImage: "example.azurecr.io/rle:latest"})
	if err == nil {
		t.Fatal("expected missing resource group to fail")
	}
}

func TestValidateNetworkFlagsRequiresImageUnlessSkipped(t *testing.T) {
	err := validateNetworkFlags(&rleNetworkFlags{resourceGroup: "rg", sandboxTargetPort: 8000, sandboxIngressPort: 443})
	if err == nil {
		t.Fatal("expected missing image to fail")
	}

	err = validateNetworkFlags(&rleNetworkFlags{resourceGroup: "rg", sandboxTargetPort: 8000, sandboxIngressPort: 443, skipSandboxApp: true})
	if err != nil {
		t.Fatalf("expected skip-sandbox-app to allow empty image: %v", err)
	}
}

func TestValidateNetworkFlagsRejectsInvalidPort(t *testing.T) {
	err := validateNetworkFlags(&rleNetworkFlags{resourceGroup: "rg", sandboxTargetPort: 0, sandboxIngressPort: 443, sandboxImage: "image"})
	if err == nil {
		t.Fatal("expected invalid port to fail")
	}
}

func TestValidateNetworkFlagsRejectsInvalidIngressPort(t *testing.T) {
	err := validateNetworkFlags(&rleNetworkFlags{resourceGroup: "rg", sandboxTargetPort: 8000, sandboxIngressPort: 0, sandboxImage: "image"})
	if err == nil {
		t.Fatal("expected invalid ingress port to fail")
	}
}
