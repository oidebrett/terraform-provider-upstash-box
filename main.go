package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/ivobrett/terraform-provider-upstash-box/internal/provider"
)

var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run with support for debuggers like delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		// Must match required_providers.source in the .tf files and the
		// ~/.terraform.d/plugins/<address>/<version>/<os_arch>/ install path.
		Address: "registry.example.com/mattercoder/upstash-box",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err.Error())
	}
}
