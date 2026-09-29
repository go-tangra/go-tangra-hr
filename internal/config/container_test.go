package config

import "testing"

// The shipped example configuration loads and validates.
func TestContainerConfig(t *testing.T) {
	c, err := Load("../../deploy/container.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Server.GRPCAddr != "0.0.0.0:9925" || c.Admin.Addr != "127.0.0.1:9870" || c.Signing.Service != "signing" {
		t.Fatalf("container config: %+v", c.Server)
	}
}
