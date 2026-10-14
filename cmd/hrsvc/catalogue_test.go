package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra/v4/catalogue"

	"github.com/go-tangra/go-tangra-hr/v4/internal/config"
	"github.com/go-tangra/go-tangra-hr/v4/pkg/hrmanifest"
)

var placeholderRE = regexp.MustCompile(`\$\{([A-Z][A-Z0-9_]*)\}`)

// tangra-module.yaml is a valid catalogue descriptor for the module the
// manifest registers, scoped to the routes it serves.
func TestCatalogueDescriptor(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "tangra-module.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := catalogue.ParseDescriptor(raw)
	if err != nil {
		t.Fatal(err)
	}
	if d.Module != hrmanifest.Module || !slices.Contains(d.Routes.Prefixes, hrmanifest.APIPrefix) || !slices.Contains(d.Routes.Prefixes, "/m/"+hrmanifest.Module) {
		t.Fatalf("%+v", d)
	}
	for _, n := range hrmanifest.Nav {
		if !slices.ContainsFunc(d.Routes.Prefixes, func(p string) bool { return n.Path == p || strings.HasPrefix(n.Path, p+"/") }) {
			t.Fatalf("nav path %s outside the routes %v", n.Path, d.Routes.Prefixes)
		}
	}
}

// The join bundle's templates, rendered with values of the shape the gateway
// fills in (core values, generated secrets, host inputs), are a valid
// production configuration and policy, and the compose file uses no value
// the gateway does not write to .env.
func TestJoinBundleRendersToValidProductionConfig(t *testing.T) {
	values := map[string]string{
		"TRUST_DOMAIN": "infra.example.org", "GATEWAY_ISSUER": "https://portal.example.org:8443",
		"LCM_ENROLL_URL": "https://portal.example.org:8443/api/lcm/v1/enroll", "LCM_GRPC": "portal.example.org:9945",
		"GATEWAY_GRPC": "portal.example.org:9643", "AUTH_GRPC": "portal.example.org:9543", "MESH_TENANT_ID": "00000000-0000-0000-0000-000000000001",
		"MODULE": "hr", "MODULE_VERSION": "4.1.0", "MODULE_IMAGE": "ghcr.io/go-tangra/go-tangra-hr",
		"GEN_PASSWORD_1": strings.Repeat("a", 48), "GEN_PASSWORD_2": strings.Repeat("b", 48),
		"MODULE_ADVERTISE_HOST": "hr.example.org", "MODULE_BIND_IP": "10.0.0.20",
		"HR_VALKEY_ADDRESS": "portal.example.org:6380", "HR_VALKEY_PASSWORD": "c0ffee-Valkey.pw+/=",
		"HR_NOTIFICATION_GRPC": "portal.example.org:9943", "HR_SIGNING_GRPC": "portal.example.org:9915",
		"HR_SCHEDULER_GRPC": "portal.example.org:9905",
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "tangra-module.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := catalogue.ParseDescriptor(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range d.HostInputs {
		if !regexp.MustCompile(h.Pattern).MatchString(values[h.Key]) {
			t.Fatalf("sample %s=%q does not match %s", h.Key, values[h.Key], h.Pattern)
		}
	}
	dir := t.TempDir()
	for _, name := range []string{"config.yaml", "policy.yaml", "compose.yaml"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "bundle", name))
		if err != nil {
			t.Fatal(err)
		}
		var missing []string
		rendered := placeholderRE.ReplaceAllStringFunc(string(raw), func(m string) string {
			k := placeholderRE.FindStringSubmatch(m)[1]
			v, ok := values[k]
			if !ok {
				missing = append(missing, k)
			}
			return v
		})
		if len(missing) > 0 {
			t.Fatalf("%s uses placeholders the gateway does not fill: %v", name, missing)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(rendered), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("rendered config invalid: %v", err)
	}
	if !cfg.IsProduction() || cfg.Gateway.Issuer != "https://portal.example.org:8443" || cfg.Valkey.Password != values["HR_VALKEY_PASSWORD"] {
		t.Fatalf("%+v %+v", cfg.Gateway, cfg.Valkey.Addresses)
	}
	pol, _ := os.ReadFile(filepath.Join(dir, "policy.yaml"))
	if !strings.Contains(string(pol), "spiffe://infra.example.org/svc/gateway") || !strings.Contains(string(pol), "spiffe://infra.example.org/svc/scheduler") {
		t.Fatal(string(pol))
	}
}
