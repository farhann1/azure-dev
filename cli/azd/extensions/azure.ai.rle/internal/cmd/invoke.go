// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"github.com/spf13/cobra"
)

const (
	invokeTargetLocal  = "local"
	invokeTargetDocker = "docker"
	invokeTargetRemote = "remote"
)

type rleInvokeFlags struct {
	target       string
	port         int
	endpoint     string
	healthWait   time.Duration
	healthPoll   time.Duration
	keepInstance bool
}

func newInvokeCommand() *cobra.Command {
	flags := &rleInvokeFlags{
		target:     invokeTargetLocal,
		healthWait: 60 * time.Second,
		healthPoll: time.Second,
	}

	cmd := &cobra.Command{
		Use:   "invoke",
		Short: "Interactively test the RLE environment (reset/step/state)",
		Long: "Start an interactive OpenEnv session against the environment.\n\n" +
			"Targets:\n" +
			"  local   Build the container image and run it locally.\n" +
			"  docker  Run a previously built container image locally (skip build).\n" +
			"  remote  Lease a sandbox from the control plane and test the deployed endpoint.",
		RunE: func(cmd *cobra.Command, args []string) error {
			switch strings.ToLower(flags.target) {
			case invokeTargetLocal:
				return runInvokeLocal(cmd, flags)
			case invokeTargetDocker:
				return runInvokeDocker(cmd, flags)
			case invokeTargetRemote:
				return runInvokeRemote(cmd, flags)
			default:
				return &azdext.LocalError{
					Message:    fmt.Sprintf("Unknown invoke target %q.", flags.target),
					Code:       "rle_invalid_invoke_target",
					Category:   azdext.LocalErrorCategoryUser,
					Suggestion: "Use --target local, docker, or remote.",
				}
			}
		},
	}

	cmd.Flags().StringVar(&flags.target, "target", flags.target, "Where to run the environment: local, docker, or remote.")
	cmd.Flags().IntVar(&flags.port, "port", 0, "Local port to bind for local/docker targets. Defaults to a free port.")
	cmd.Flags().StringVar(&flags.endpoint, "endpoint", "", "Control plane endpoint for the remote target. Defaults to the configured RLE control plane.")
	cmd.Flags().DurationVar(&flags.healthWait, "health-timeout", flags.healthWait, "Maximum time to wait for the environment to become healthy.")
	cmd.Flags().DurationVar(&flags.healthPoll, "health-interval", flags.healthPoll, "Interval between health checks while waiting.")
	cmd.Flags().BoolVar(&flags.keepInstance, "keep-instance", false, "For the remote target, do not delete the leased instance on exit.")
	return cmd
}

func runInvokeLocal(cmd *cobra.Command, flags *rleInvokeFlags) error {
	if _, err := loadSessionState(); err != nil {
		return err
	}

	image, err := resolveLocalImage("")
	if err != nil {
		return err
	}
	engine, err := resolveContainerEngine()
	if err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Building image '%s' with %s from the current session folder ...\n", image, engine)
	if err := containerBuild(cmd, engine, image, "."); err != nil {
		return err
	}

	port := flags.port
	if port == 0 {
		port, err = freeLocalPort()
		if err != nil {
			return err
		}
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Starting container from image '%s' with %s on port %d ...\n", image, engine, port)
	containerID, err := containerRunDetached(cmd.Context(), engine, image, port)
	if err != nil {
		return err
	}
	defer func() {
		fmt.Fprintf(cmd.OutOrStdout(), "Stopping container %s ...\n", shortID(containerID))
		if stopErr := containerStop(context.Background(), engine, containerID); stopErr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v\n", stopErr)
		}
	}()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	return connectAndRepl(cmd, baseURL, flags)
}

func runInvokeDocker(cmd *cobra.Command, flags *rleInvokeFlags) error {
	image, err := resolveLocalImage("")
	if err != nil {
		return err
	}
	engine, err := resolveContainerEngine()
	if err != nil {
		return err
	}

	port := flags.port
	if port == 0 {
		port, err = freeLocalPort()
		if err != nil {
			return err
		}
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Starting container from image '%s' with %s on port %d ...\n", image, engine, port)
	containerID, err := containerRunDetached(cmd.Context(), engine, image, port)
	if err != nil {
		return err
	}
	defer func() {
		fmt.Fprintf(cmd.OutOrStdout(), "Stopping container %s ...\n", shortID(containerID))
		if stopErr := containerStop(context.Background(), engine, containerID); stopErr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v\n", stopErr)
		}
	}()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	return connectAndRepl(cmd, baseURL, flags)
}

func runInvokeRemote(cmd *cobra.Command, flags *rleInvokeFlags) error {
	state, err := loadSessionState()
	if err != nil {
		return err
	}
	if err := requireDeployedEnvironment(state); err != nil {
		return err
	}

	client := newRleClient(resolveControlPlaneEndpoint(flags.endpoint))
	fmt.Fprintf(cmd.OutOrStdout(), "Creating sandbox for environment %s ...\n", state.EnvironmentId)
	sandbox, err := client.createSandbox(
		cmd.Context(),
		state.Account,
		state.Project,
		state.EnvironmentId,
		sandboxCreateRequest{Version: state.EnvironmentVersion},
	)
	if err != nil {
		return sandboxCreateError(err)
	}
	if strings.TrimSpace(sandbox.Url) == "" {
		return &azdext.LocalError{
			Message:    "Control plane did not return a sandbox URL.",
			Code:       "rle_sandbox_url_missing",
			Category:   azdext.LocalErrorCategoryInternal,
			Suggestion: "Check the control plane logs and retry.",
		}
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Sandbox %s ready at %s\n", sandbox.Id, sandbox.Url)
	return connectAndRepl(cmd, sandbox.Url, flags)
}

func connectAndRepl(cmd *cobra.Command, baseURL string, flags *rleInvokeFlags) error {
	client := newOpenEnvClient(baseURL)

	fmt.Fprintf(cmd.OutOrStdout(), "Waiting for %s/health ...\n", baseURL)
	if err := client.waitForHealth(cmd.Context(), flags.healthWait, flags.healthPoll); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Connected to %s\n\n", baseURL)

	return runOpenEnvRepl(cmd.Context(), client, os.Stdin, cmd.OutOrStdout())
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
