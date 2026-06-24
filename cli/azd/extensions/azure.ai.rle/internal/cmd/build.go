// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

type rleBuildFlags struct {
	image string
}

func newBuildCommand() *cobra.Command {
	flags := &rleBuildFlags{}

	cmd := &cobra.Command{
		Use:   "build",
		Short: "Build the RLE environment container image locally",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := ensureLocalImageEnv(flags.image); err != nil {
				return err
			}

			state, err := loadSessionState()
			if err != nil {
				return err
			}

			image := flags.image
			if image == "" {
				image, err = resolveSessionImage(state)
				if err != nil {
					return err
				}
			}

			engine, err := resolveContainerEngine()
			if err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Building image '%s' with %s from the current session folder ...\n", image, engine)
			if err := containerBuild(cmd, engine, image, "."); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "\nBuilt image '%s'.\nNext:\n  azd ai rle invoke --target docker\n", image)
			return nil
		},
	}

	cmd.Flags().StringVar(&flags.image, "image", "", "Image tag to build. Defaults to the image resolved from rle.yaml/RLE_ACR_IMAGE.")
	return cmd
}
