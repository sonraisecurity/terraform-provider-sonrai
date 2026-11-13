package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/sonraisecurity/terraform-provider-sonrai/internal/provider"
)

// version is overwritten at release time by the linker.
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run with support for debuggers like delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/sonraisecurity/sonrai",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
