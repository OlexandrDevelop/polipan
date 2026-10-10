// SPDX-License-Identifier: Apache-2.0

// Command terraform-provider-polipan is the Terraform provider of Polipan.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/polipan/terraform-provider-polipan/internal/provider"
)

// version is set by the release build.
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run with support for debuggers")
	flag.Parse()
	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/polipan/polipan",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
