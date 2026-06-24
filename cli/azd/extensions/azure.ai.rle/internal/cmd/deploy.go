// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

type rleDeployFlags struct {
	project   string
	skipBuild bool
	skipPush  bool
}

func newDeployCommand() *cobra.Command {
	flags := &rleDeployFlags{}

	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Build, push to ACR, and register the RLE environment",
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := loadSessionState()
			if err != nil {
				return err
			}
			state.Project = firstNonEmpty(flags.project, state.Project)

			image, err := resolveSessionImage(state)
			if err != nil {
				return err
			}

			if err := buildAndPushImage(cmd, image, flags); err != nil {
				return err
			}

			environmentId := firstNonEmpty(state.EnvironmentId, slug(state.Name))
			client := newRleClient(resolveControlPlaneEndpoint(""))
			request := v1EnvironmentRequest{
				Name:         state.Name,
				AcrImagePath: image,
			}

			var environment *environmentResource
			created := state.EnvironmentId == ""
			action := "Creating"
			if !created {
				action = "Updating"
			}

			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s environment '%s' (image=%s) ...\n", action, state.Name, image); err != nil {
				return err
			}
			if state.EnvironmentId == "" {
				environment, err = client.createV1Environment(cmd.Context(), state.Project, request)
			} else {
				environment, err = client.updateV1Environment(cmd.Context(), state.Project, environmentId, request)
				if isNotFoundError(err) {
					// The recorded environment no longer exists in the target project
					// (e.g. the project changed or the control plane was reset). Recreate it.
					if _, msgErr := fmt.Fprintf(
						cmd.OutOrStdout(),
						"Environment '%s' not found in project '%s'; creating a new one.\n",
						environmentId,
						state.Project,
					); msgErr != nil {
						return msgErr
					}
					created = true
					environment, err = client.createV1Environment(cmd.Context(), state.Project, request)
				}
			}
			if err != nil {
				return serviceError(err)
			}
			state.EnvironmentId = environment.Id
			state.EnvironmentVersion = firstNonEmpty(environment.Version, environment.VersionLabel, environment.Manifest.VersionLabel)
			state.InstanceId = ""
			state.InstanceEndpoint = ""
			if err := saveRleState(state); err != nil {
				return err
			}

			label := "Created"
			if !created {
				label = "Updated"
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "\n%s environment '%s' (%s).\n", label, state.Name, state.EnvironmentId); err != nil {
				return err
			}
			body, err := json.MarshalIndent(environmentOutput{
				Id:           environment.Id,
				ProjectId:    firstNonEmpty(environment.ProjectId, state.Project),
				Name:         firstNonEmpty(environment.Name, state.Name),
				AcrImagePath: firstNonEmpty(environment.AcrImagePath, image),
				Version:      state.EnvironmentVersion,
				CreatedAtUtc: environment.CreatedAtUtc,
				UpdatedAtUtc: environment.UpdatedAtUtc,
			}, "", "  ")
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), string(body)); err != nil {
				return err
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&flags.project, "project", "", "RLE project name. Defaults to the project saved in .azd-rle.json.")
	cmd.Flags().BoolVar(&flags.skipBuild, "skip-build", false, "Skip building the container image and reuse the existing image.")
	cmd.Flags().BoolVar(&flags.skipPush, "skip-push", false, "Skip pushing the image to ACR (only register with the control plane).")
	return cmd
}

// buildAndPushImage builds the session image and pushes it to its ACR registry
// before the environment is registered with the control plane. Building and
// pushing require a container engine (docker or podman); when
// --skip-build/--skip-push are set or the image is not an ACR reference, the
// corresponding step is skipped.
func buildAndPushImage(cmd *cobra.Command, image string, flags *rleDeployFlags) error {
	if flags.skipBuild && flags.skipPush {
		fmt.Fprintf(cmd.OutOrStdout(), "Skipping build and push; using existing image '%s'.\n", image)
		return nil
	}

	engine, err := resolveContainerEngine()
	if err != nil {
		return err
	}

	if !flags.skipBuild {
		fmt.Fprintf(cmd.OutOrStdout(), "Building image '%s' with %s ...\n", image, engine)
		if err := containerBuild(cmd, engine, image, "."); err != nil {
			return err
		}
	}

	if flags.skipPush {
		fmt.Fprintf(cmd.OutOrStdout(), "Skipping push; image '%s' was not pushed to ACR.\n", image)
		return nil
	}

	registry := acrNameFromImage(image)
	if registry == "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Image '%s' is not an ACR reference; skipping ACR login and push.\n", image)
		return nil
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Logging in to ACR '%s' ...\n", registry)
	if err := acrLogin(cmd, engine, registry); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Pushing image '%s' ...\n", image)
	return containerPush(cmd, engine, image)
}

type environmentOutput struct {
	Id           string `json:"id"`
	ProjectId    string `json:"projectId"`
	Name         string `json:"name"`
	AcrImagePath string `json:"acrImagePath"`
	Version      string `json:"version"`
	CreatedAtUtc string `json:"createdAtUtc"`
	UpdatedAtUtc string `json:"updatedAtUtc"`
}
