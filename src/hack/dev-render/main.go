// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

// Command dev-render creates an isolated development bundle and catalog context.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	options := Options{}
	var cleanOutput, validateOnlyVersion, validateOnlyImage string
	flag.StringVar(&options.SourceRoot, "source-root", ".", "source tree containing bundle and catalog")
	flag.StringVar(&options.OutputContext, "output-context", "bin/dev/context", "directory for the rendered context")
	flag.StringVar(&options.Registry, "registry", "", "registry host[:port] for development images (required)")
	flag.StringVar(&options.Version, "version", "", "unique prerelease SemVer for development images (required)")
	flag.StringVar(&options.KeycloakImage, "keycloak-image", "", "Keycloak image; defaults to the registry image")
	flag.StringVar(&options.ImagePullSecret, "image-pull-secret", "", "optional image pull Secret name")
	flag.StringVar(&cleanOutput, "clean-output", "", "remove a managed development output context and exit")
	flag.StringVar(&validateOnlyVersion, "validate-version", "", "validate a SemVer prerelease and exit")
	flag.StringVar(&validateOnlyImage, "validate-image", "", "validate an OCI-like image reference and exit")
	flag.Parse()
	if cleanOutput != "" {
		if err := Clean(options.SourceRoot, cleanOutput); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if validateOnlyVersion != "" || validateOnlyImage != "" {
		if validateOnlyVersion != "" {
			if err := validateVersion(validateOnlyVersion); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
		if validateOnlyImage != "" {
			if err := validateImage(validateOnlyImage); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
		return
	}

	if err := Render(options); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
