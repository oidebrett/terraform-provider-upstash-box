package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// The framework validates every schema (e.g. Default without Computed is an
// error) when the provider schema is served, so this catches schema mistakes
// without needing a terraform binary.
func TestProviderSchemasAreValid(t *testing.T) {
	srv, err := providerserver.NewProtocol6WithError(New("test")())()
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.GetProviderSchema(context.Background(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range resp.Diagnostics {
		t.Errorf("%s: %s", d.Summary, d.Detail)
	}
	for _, name := range []string{"upstashbox_box", "upstashbox_exec", "upstashbox_public_url"} {
		if _, ok := resp.ResourceSchemas[name]; !ok {
			t.Errorf("missing resource %s", name)
		}
	}
}

func TestSecretsAreSensitive(t *testing.T) {
	srv, _ := providerserver.NewProtocol6WithError(New("test")())()
	resp, _ := srv.GetProviderSchema(context.Background(), &tfprotov6.GetProviderSchemaRequest{})
	want := map[string][]string{
		"upstashbox_box":        {"env"},
		"upstashbox_exec":       {"script", "output_tail"},
		"upstashbox_public_url": {"token", "password"},
	}
	for res, attrs := range want {
		for _, a := range resp.ResourceSchemas[res].Block.Attributes {
			for _, name := range attrs {
				if a.Name == name && !a.Sensitive {
					t.Errorf("%s.%s must be sensitive", res, name)
				}
			}
		}
	}
	for _, a := range resp.Provider.Block.Attributes {
		if a.Name == "api_key" && !a.Sensitive {
			t.Error("provider api_key must be sensitive")
		}
	}
}
