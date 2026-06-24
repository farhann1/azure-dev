// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
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
			"  local   Run the FastAPI server locally with uvicorn (no Docker).\n" +
			"  docker  Run the built container image locally (requires azd ai rle build).\n" +
			"  remote  Lease an instance from the control plane and test the deployed endpoint.",
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

	pythonExe, err := resolvePython()
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

	fmt.Fprintf(cmd.OutOrStdout(), "Starting local server with uvicorn on port %d ...\n", port)
	server := exec.CommandContext( //nolint:gosec // Args are fixed plus a resolved interpreter path and numeric port.
		cmd.Context(),
		pythonExe, "-m", "uvicorn", "server.app:app",
		"--host", "127.0.0.1", "--port", fmt.Sprintf("%d", port),
	)
	server.Stdout = cmd.ErrOrStderr()
	server.Stderr = cmd.ErrOrStderr()
	server.Env = os.Environ()
	if err := server.Start(); err != nil {
		return fmt.Errorf("start uvicorn server: %w", err)
	}
	defer func() {
		if server.Process != nil {
			_ = server.Process.Kill()
			_, _ = server.Process.Wait()
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
	fmt.Fprintf(cmd.OutOrStdout(), "Leasing instance for environment %s ...\n", state.EnvironmentId)
	instance, err := client.createEnvironmentInstance(
		cmd.Context(),
		state.Account,
		state.Project,
		state.EnvironmentId,
		environmentInstanceCreateRequest{VersionLabel: state.EnvironmentVersion},
	)
	if err != nil {
		return serviceError(err)
	}
	if strings.TrimSpace(instance.Endpoint) == "" {
		return &azdext.LocalError{
			Message:    "Control plane did not return an instance endpoint.",
			Code:       "rle_instance_endpoint_missing",
			Category:   azdext.LocalErrorCategoryInternal,
			Suggestion: "Check the control plane logs and retry.",
		}
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Instance %s ready at %s\n", instance.Id, instance.Endpoint)
	return connectAndRepl(cmd, instance.Endpoint, flags)
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

func resolvePython() (string, error) {
	for _, candidate := range []string{"python3", "python"} {
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
	}
	return "", &azdext.LocalError{
		Message:    "Could not find \"python3\" or \"python\" on PATH.",
		Code:       "rle_python_not_found",
		Category:   azdext.LocalErrorCategoryUser,
		Suggestion: "Install Python and the environment requirements (pip install -r requirements.txt), then try again.",
	}
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
