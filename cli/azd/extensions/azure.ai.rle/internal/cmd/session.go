// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package cmd

import (
	"errors"
	"net"
	"os"
	"path/filepath"

	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
)

// resolveLocalImage resolves an image reference for the local build/invoke
// flows, which run against a local container engine and therefore do not need a
// registry-qualified reference. Priority: explicit override, then the
// manifest/state image, then a local tag derived from the environment (or
// folder) name. It also seeds a default RLE_ACR_IMAGE so legacy manifests that
// still reference ${RLE_ACR_IMAGE} continue to load.
func resolveLocalImage(override string) (string, error) {
	if override != "" {
		if err := os.Setenv("RLE_ACR_IMAGE", override); err != nil {
			return "", err
		}
	} else if _, ok := os.LookupEnv("RLE_ACR_IMAGE"); !ok {
		if err := os.Setenv("RLE_ACR_IMAGE", localImageFromDir()); err != nil {
			return "", err
		}
	}

	state, err := loadSessionState()
	if err != nil {
		return "", err
	}

	if override != "" {
		return override, nil
	}
	if image, err := resolveRecipeImage(state.Recipe, state.Image); err == nil && image != "" {
		return image, nil
	}

	name := state.Name
	if name == "" {
		return localImageFromDir(), nil
	}
	return slug(name) + ":latest", nil
}

// localImageFromDir derives a local image tag from the current folder name.
func localImageFromDir() string {
	dir, err := os.Getwd()
	if err != nil {
		return "rle-env:latest"
	}
	tag := slug(filepath.Base(dir))
	if tag == "" {
		tag = "rle-env"
	}
	return tag + ":latest"
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
