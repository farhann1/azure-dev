// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package cmd

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
)

// ensureLocalImageEnv makes the local build/invoke flows usable without an ACR
// image configured. When image is provided it takes precedence; otherwise an
// existing RLE_ACR_IMAGE is kept, and as a last resort a local tag derived from
// the session folder name is used so manifest expansion succeeds.
func ensureLocalImageEnv(image string) error {
	if image != "" {
		return os.Setenv("RLE_ACR_IMAGE", image)
	}
	if _, ok := os.LookupEnv("RLE_ACR_IMAGE"); ok {
		return nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	tag := strings.ToLower(filepath.Base(dir))
	if tag == "" || tag == "." || tag == string(filepath.Separator) {
		tag = "rle-env"
	}
	return os.Setenv("RLE_ACR_IMAGE", tag+":latest")
}


// loadSessionState loads the local RLE state and overlays any values from the
// rle.yaml manifest in the current session folder. It is shared by the build,
// deploy, and invoke commands so they resolve the environment name, project,
// and image consistently.
func loadSessionState() (rleState, error) {
	state, err := loadRleState()
	if err != nil {
		return rleState{}, err
	}

	manifest, err := loadRleManifest(rleManifestFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return rleState{}, err
	}
	if err == nil {
		manifestState, err := stateFromManifest(manifest)
		if err != nil {
			return rleState{}, err
		}
		state.Name = firstNonEmpty(manifestState.Name, state.Name)
		state.Account = firstNonEmpty(manifestState.Account, state.Account)
		state.Project = firstNonEmpty(manifestState.Project, state.Project)
		state.Endpoint = firstNonEmpty(manifestState.Endpoint, state.Endpoint)
		state.Image = firstNonEmpty(manifestState.Image, state.Image)
	}

	return state, nil
}

// resolveSessionImage resolves the container image reference for the session,
// preferring the recipe/manifest image and falling back to RLE_ACR_IMAGE.
func resolveSessionImage(state rleState) (string, error) {
	return resolveRecipeImage(state.Recipe, state.Image)
}

// requireDeployedEnvironment returns a user error when the session has not been
// deployed to the control plane yet.
func requireDeployedEnvironment(state rleState) error {
	if state.EnvironmentId == "" {
		return &azdext.LocalError{
			Message:    "RLE environment has not been deployed.",
			Code:       "rle_environment_not_deployed",
			Category:   azdext.LocalErrorCategoryUser,
			Suggestion: "Run azd ai rle deploy from this session folder first.",
		}
	}
	return nil
}

// freeLocalPort asks the OS for an available TCP port on the loopback
// interface and returns it. The listener is closed before returning, so the
// port is briefly racy but adequate for local testing.
func freeLocalPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}
