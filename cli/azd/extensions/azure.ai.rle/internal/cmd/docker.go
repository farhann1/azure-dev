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

// acrTokenUsername is the well-known username used for token-based ACR logins.
const acrTokenUsername = "00000000-0000-0000-0000-000000000000"

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
// The container's port 8000 is published to hostPort on localhost.
func containerRunDetached(ctx context.Context, engine string, image string, hostPort int) (string, error) {
	args := []string{
		"run", "--rm", "-d",
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

// containerPush pushes a tagged image to its registry.
func containerPush(cmd *cobra.Command, engine string, image string) error {
	return runStreamingCommand(cmd, "", engine, "push", image)
}

// acrLogin authenticates the container engine to the given ACR registry. It
// fetches a short-lived access token with the Azure CLI and pipes it to the
// engine's login command, which works for both docker and podman.
func acrLogin(cmd *cobra.Command, engine string, registry string) error {
	if _, err := exec.LookPath("az"); err != nil {
		return &azdext.LocalError{
			Message:    "Could not find \"az\" on PATH.",
			Code:       "rle_az_not_found",
			Category:   azdext.LocalErrorCategoryUser,
			Suggestion: "Install the Azure CLI and run 'az login', then try again.",
		}
	}

	tokenOut, err := exec.CommandContext( //nolint:gosec // Args are fixed plus a resolved registry name.
		cmd.Context(), "az", "acr", "login", "--name", registry, "--expose-token", "--output", "tsv", "--query", "accessToken",
	).Output()
	if err != nil {
		return &azdext.LocalError{
			Message:    fmt.Sprintf("Failed to obtain an ACR access token for %q.", registry),
			Code:       "rle_acr_token_failed",
			Category:   azdext.LocalErrorCategoryUser,
			Suggestion: "Run 'az login' and ensure you have access to the registry, then try again.",
		}
	}
	token := strings.TrimSpace(string(tokenOut))

	loginServer := registry
	if !strings.Contains(loginServer, ".") {
		loginServer = registry + ".azurecr.io"
	}

	login := exec.CommandContext( //nolint:gosec // Engine resolved; args fixed plus resolved login server.
		cmd.Context(), engine, "login", loginServer,
		"--username", acrTokenUsername, "--password-stdin",
	)
	login.Stdin = strings.NewReader(token)
	login.Stdout = cmd.OutOrStdout()
	login.Stderr = cmd.ErrOrStderr()
	if err := login.Run(); err != nil {
		return fmt.Errorf("%s login %s: %w", engine, loginServer, err)
	}
	return nil
}

// registryFromImage extracts the registry login server (e.g. "myacr.azurecr.io")
// from a fully-qualified image reference. It returns an empty string when the
// image has no registry host component (e.g. "echo_env:latest").
func registryFromImage(image string) string {
	slash := strings.IndexByte(image, '/')
	if slash < 0 {
		return ""
	}
	host := image[:slash]
	if strings.ContainsAny(host, ".:") {
		return host
	}
	return ""
}

// acrNameFromImage returns the ACR resource name (the portion before the first
// dot of the login server) for an image hosted in *.azurecr.io. It returns an
// empty string when the image is not an ACR reference.
func acrNameFromImage(image string) string {
	host := registryFromImage(image)
	if host == "" {
		return ""
	}
	if !strings.Contains(host, ".azurecr.io") {
		return ""
	}
	return strings.SplitN(host, ".", 2)[0]
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
