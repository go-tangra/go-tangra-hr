package hrmanifest

import (
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/go-tangra/go-tangra-hr/v4/api/openapi"
	"github.com/go-tangra/go-tangra-hr/v4/internal/authz"
)

func TestOpenAPIParsesAndValidates(t *testing.T) {
	openapi3.DefineStringFormatValidator("uuid", openapi3.NewRegexpFormatValidator(openapi3.FormatOfStringForUUIDOfRFC9562))
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(openapi.HR)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := doc.Validate(loader.Context, openapi3.DisableExamplesValidation()); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestManifestBuilds(t *testing.T) {
	m, err := Manifest()
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if m.Module != "hr" || m.DisplayName != "HR" || len(m.Prefixes) != 1 || m.Prefixes[0] != "/api/hr" {
		t.Fatalf("identity = %+v", m)
	}
	if len(m.Routes) != 49 {
		t.Fatalf("routes = %d", len(m.Routes))
	}
	public := 0
	for _, r := range m.Routes {
		if !strings.HasPrefix(r.Path, "/api/hr/v1/") {
			t.Fatalf("route outside prefix: %s", r.Path)
		}
		if r.Public {
			public++
			if r.Path != "/api/hr/v1/health" {
				t.Fatalf("unexpected public route %s", r.Path)
			}
		}
		if r.ClientAddress {
			t.Fatalf("hr forwards no client address: %s", r.Path)
		}
		if r.Path == "/api/hr/v1/holidays/import" && r.MaxBodyBytes != 64<<10 {
			t.Fatalf("import body limit = %d", r.MaxBodyBytes)
		}
	}
	if public != 1 {
		t.Fatalf("public routes = %d", public)
	}
	if len(m.Nav) != 8 || m.Nav[0].Path != "/hr" || strings.Join(m.Exposes, ",") != "./routes,./nav" || len(m.Methods) != 0 {
		t.Fatalf("nav/exposes: %+v %v", m.Nav, m.Exposes)
	}
	if len(m.Abilities) != 4 || len(m.Permissions) != 4 {
		t.Fatalf("abilities/permissions = %d/%d", len(m.Abilities), len(m.Permissions))
	}
	for _, p := range m.Permissions {
		if p.Description == "" {
			t.Fatalf("permission %s:%s lacks a description", p.Resource, p.Action)
		}
	}
}

// Routes open to the calendar permission are the reads everyone may see and
// the decisions the module re-checks against the approval routing.
func TestCalendarRoutesAreRelationChecked(t *testing.T) {
	m, err := Manifest()
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		"GET /api/hr/v1/me": true, "GET /api/hr/v1/people": true, "GET /api/hr/v1/calendar": true, "GET /api/hr/v1/absence-types": true,
		"GET /api/hr/v1/absence-types/{id}": true, "GET /api/hr/v1/departments": true, "GET /api/hr/v1/holidays": true,
		"GET /api/hr/v1/stream": true, "GET /api/hr/v1/requests/{id}": true, "GET /api/hr/v1/requests/{id}/signed-document": true,
		"GET /api/hr/v1/allowances": true, "GET /api/hr/v1/allowances/{id}": true, "GET /api/hr/v1/balance/{user_id}": true,
		"GET /api/hr/v1/requests": true,
		"POST /api/hr/v1/requests/{id}/approve": true, "POST /api/hr/v1/requests/{id}/reject": true, "POST /api/hr/v1/requests/{id}/revoke": true,
	}
	for _, r := range m.Routes {
		if r.Permission == "hr:calendar" && !allowed[r.Method+" "+r.Path] {
			t.Errorf("%s %s is open to calendar viewers", r.Method, r.Path)
		}
	}
}

// The manifest's permission vocabulary is exactly the module's authz set.
func TestPermissionsMatchAuthz(t *testing.T) {
	got := PermissionRefs()
	want := append([]string(nil), authz.Permissions...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("manifest %v != authz %v", got, want)
	}
}

// TestRoles pins the module role set (FR-060).
func TestRoles(t *testing.T) {
	want := map[string][]string{
		"administrator":   {"hr:calendar", "hr:request", "hr:read", "hr:manage"},
		"viewer":          {"hr:calendar", "hr:read"},
		"employee":        {"hr:calendar", "hr:request"},
		"calendar-viewer": {"hr:calendar"},
	}
	names := map[string]string{"administrator": "HR administrator", "viewer": "HR viewer", "employee": "HR employee",
		"calendar-viewer": "HR calendar viewer"}
	if len(Roles) != len(want) {
		t.Fatalf("want %d roles, got %d", len(want), len(Roles))
	}
	slug := regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$`)
	for _, r := range Roles {
		if !slug.MatchString(r.Slug) || r.DisplayName != names[r.Slug] || r.Description == "" {
			t.Errorf("role %q: %q %q", r.Slug, r.DisplayName, r.Description)
		}
		if !reflect.DeepEqual(r.Permissions, want[r.Slug]) {
			t.Errorf("role %q: permissions %v, want %v", r.Slug, r.Permissions, want[r.Slug])
		}
	}
}

// TestBuiltinGrants: owners and admins administer, auditors read, everyone
// else may request leave.
func TestBuiltinGrants(t *testing.T) {
	if !reflect.DeepEqual(Grants["member"], []string{"hr:calendar", "hr:request"}) || !reflect.DeepEqual(Grants["owner"], Grants["admin"]) ||
		!reflect.DeepEqual(Grants["auditor"], []string{"hr:calendar", "hr:read"}) || len(BuiltinRoles) != len(Grants) {
		t.Fatalf("grants: %v", Grants)
	}
	for _, slug := range BuiltinRoles {
		if len(Grants[slug]) == 0 || Grants[slug][0] != "hr:calendar" {
			t.Fatalf("built-in role %s without the calendar: %v", slug, Grants[slug])
		}
	}
}

// TestRegistration: the auth registration carries the module identity, every
// permission, the complete role set and the built-in grants, and is valid.
func TestRegistration(t *testing.T) {
	reg := Registration()
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
	req := reg.Request()
	if req.GetModule() != "hr" || req.GetModuleDisplayName() != "HR" || !req.GetDeclaresRoles() {
		t.Fatalf("module %q display %q declares_roles %v", req.GetModule(), req.GetModuleDisplayName(), req.GetDeclaresRoles())
	}
	if len(req.GetPermissions()) != 4 || len(req.GetRoles()) != 4 {
		t.Fatalf("%d permissions, %d roles", len(req.GetPermissions()), len(req.GetRoles()))
	}
	got := map[string][]string{}
	for _, g := range req.GetBuiltinGrants() {
		got[g.GetRole()] = g.GetPermissions()
	}
	if !reflect.DeepEqual(got, Grants) {
		t.Fatalf("builtin grants %v", got)
	}
}

func docWith(ext map[string]any) *openapi3.T {
	paths := openapi3.NewPaths()
	paths.Set("/x", &openapi3.PathItem{Get: &openapi3.Operation{Extensions: ext}})
	return &openapi3.T{Paths: paths}
}

func TestRoutesValidationBranches(t *testing.T) {
	routes, err := Routes(docWith(map[string]any{PublicExtension: true}))
	if err != nil || !routes[0].Public {
		t.Fatalf("public: %v", err)
	}
	routes, err = Routes(docWith(map[string]any{PermissionExtension: "hr:read", BodyLimitExtension: float64(2048),
		TimeoutExtension: float64(30), ClientAddressExtension: true}))
	if err != nil || routes[0].MaxBodyBytes != 2048 || routes[0].Timeout.Seconds() != 30 || !routes[0].ClientAddress {
		t.Fatalf("valid: %v %+v", err, routes)
	}
	bad := []map[string]any{
		{},
		{PermissionExtension: "made:up"},
		{PermissionExtension: "hr:read", BodyLimitExtension: float64(0)},
		{PermissionExtension: "hr:read", BodyLimitExtension: "big"},
		{PermissionExtension: "hr:read", TimeoutExtension: float64(9999)},
		{PermissionExtension: "hr:read", TimeoutExtension: "slow"},
	}
	for i, ext := range bad {
		if _, err := Routes(docWith(ext)); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}

// The gateway refuses a manifest whose routes exceed its limits
// (portal internal/manifest: timeout ≤ 5 min, body ≤ 1 GiB).
func TestRoutesWithinGatewayLimits(t *testing.T) {
	m, err := Manifest()
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.Proto()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range p.GetRoutes() {
		if d := r.GetTimeout().AsDuration(); d > 5*time.Minute {
			t.Errorf("%s %s: timeout %v above the gateway's 5 min", r.GetMethod(), r.GetPath(), d)
		}
		if r.GetMaxBodyBytes() > 1<<30 {
			t.Errorf("%s %s: body bound %d above the gateway's 1 GiB", r.GetMethod(), r.GetPath(), r.GetMaxBodyBytes())
		}
	}
}
