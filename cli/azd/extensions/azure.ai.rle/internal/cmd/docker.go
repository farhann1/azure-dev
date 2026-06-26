// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"github.com/spf13/cobra"
)

// resolveContainerEngine returns the container engine CLI to use. It honors the
// RLE_CONTAINER_ENGINE override and otherwise prefers docker, falling back to
// podman. Both expose a docker-compatible CLI surface for the subcommands used
// here (build, run, stop, push, login).
func resolveContainerEngine() (string, error) {
	if override := strings.TrimSpace(os.Getenv("RLE_CONTAINER_ENGINE")); override != "" {
		if _, err := exec.LookPath(override); err != nil {
			return "", &azdext.LocalError{
				Message:    fmt.Sprintf("Could not find container engine %q (from RLE_CONTAINER_ENGINE) on PATH.", override),
				Code:       "rle_container_engine_not_found",
				Category:   azdext.LocalErrorCategoryUser,
				Suggestion: "Install the named engine or unset RLE_CONTAINER_ENGINE, then try again.",
			}
		}
		return override, nil
	}

	for _, candidate := range []string{"docker", "podman"} {
		if _, err := exec.LookPath(candidate); err == nil {
			return candidate, nil
		}
	}

	return "", &azdext.LocalError{
		Message:    "Could not find a container engine (\"docker\" or \"podman\") on PATH.",
		Code:       "rle_container_engine_not_found",
		Category:   azdext.LocalErrorCategoryUser,
		Suggestion: "Install Docker or Podman (or set RLE_CONTAINER_ENGINE), then try again.",
	}
}

// containerBuild builds an image from the session directory using its Dockerfile.
func containerBuild(cmd *cobra.Command, engine string, image string, contextDir string) error {
	return runStreamingCommand(cmd, contextDir, engine, "build", "--platform", "linux/amd64", "-t", image, contextDir)
}

// containerRunDetached starts a container in the background and returns its id.
// The container's port 8000 is published to hostPort on localhost. The optional
// browser web console (/web) is enabled so it is reachable during local invoke.
func containerRunDetached(ctx context.Context, engine string, image string, hostPort int) (string, error) {
	args := []string{
		"run", "--rm", "-d",
		"-e", "ENABLE_WEB_INTERFACE=true",
		"-p", fmt.Sprintf("127.0.0.1:%d:8000", hostPort),
		image,
	}
	out, err := exec.CommandContext(ctx, engine, args...).Output() //nolint:gosec // Args are fixed shapes plus a resolved image reference.
	if err != nil {
		return "", fmt.Errorf("start container from image %q: %w", image, containerExecError(err))
	}
	return strings.TrimSpace(string(out)), nil
}

// containerStop stops a running container by id. Errors are returned for logging
// but are generally non-fatal during cleanup.
func containerStop(ctx context.Context, engine string, containerID string) error {
	if strings.TrimSpace(containerID) == "" {
		return nil
	}
	if err := exec.CommandContext(ctx, engine, "stop", containerID).Run(); err != nil { //nolint:gosec // Engine is resolved; id is engine-generated.
		return fmt.Errorf("stop container %s: %w", containerID, err)
	}
	return nil
}

func runStreamingCommand(cmd *cobra.Command, dir string, name string, args ...string) error {
	process := exec.CommandContext(cmd.Context(), name, args...) //nolint:gosec // Args are fixed command shapes plus resolved image/registry values.
	process.Dir = dir
	process.Stdout = cmd.OutOrStdout()
	process.Stderr = cmd.ErrOrStderr()
	process.Stdin = os.Stdin
	process.Env = os.Environ()
	if err := process.Run(); err != nil {
		return fmt.Errorf("run %s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func containerExecError(err error) error {
	if exitErr, ok := err.(*exec.ExitError); ok {
		stderr := strings.TrimSpace(string(exitErr.Stderr))
		if stderr != "" {
			return fmt.Errorf("%s", stderr)
		}
	}
	return err
}
