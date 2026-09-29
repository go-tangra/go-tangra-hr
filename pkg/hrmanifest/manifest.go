// Package hrmanifest declares what the hr module registers with the
// application gateway: routes derived from the embedded OpenAPI document, the
// API permissions, the CASL abilities and the navigation entries; plus the
// module roles and built-in role grants it registers with the auth service
// (spec FR-060). The module's gRPC surface (the scheduler's task executor) is
// service to service and is not proxied by the gateway.
package hrmanifest

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/go-tangra/go-tangra-auth/sdk/v4/pkg/authclient"
	"github.com/go-tangra/go-tangra-hr/v4/api/openapi"
	"github.com/go-tangra/go-tangra-portal/sdk/v4/pkg/gatewayclient"
)

// Module identity.
const (
	Module       = "hr"
	DisplayName  = "HR"
	Version      = "1.0.0"
	RemotePrefix = "/ui"
	APIPrefix    = "/api/hr"
)

// OpenAPI operation extensions.
const (
	PermissionExtension    = "x-freya-permission"
	PublicExtension        = "x-freya-public"
	BodyLimitExtension     = "x-freya-max-body-bytes"
	ClientAddressExtension = "x-freya-client-address"
	TimeoutExtension       = "x-freya-timeout-seconds"
)

// Permissions the module registers (spec FR-060).
var Permissions = []gatewayclient.Permission{
	{Resource: "hr", Action: "calendar", Description: "See the team calendar: who is absent when, with the absence type"},
	{Resource: "hr", Action: "request", Description: "Request leave for myself, see my requests and my balance (managers also approve for their departments)"},
	{Resource: "hr", Action: "read", Description: "Read every leave request, balance, allowance, absence type, department and holiday of the tenant"},
	{Resource: "hr", Action: "manage", Description: "Manage absence types, pools, allowances, departments and holidays, act for anyone, run carry-over and backups"},
}

// Permission sets of the module roles.
var (
	adminPermissions    = PermissionRefs()
	viewerPermissions   = []string{"hr:calendar", "hr:read"}
	employeePermissions = []string{"hr:calendar", "hr:request"}
	calendarPermissions = []string{"hr:calendar"}
)

// Roles is the module's role set (feature 019): ready-made roles auth offers in
// every tenant, locked there (administrators assign or clone them).
var Roles = []authclient.ModuleRole{
	{
		Slug: "administrator", DisplayName: DisplayName + " administrator",
		Description: "Everything: absence types, pools, allowances, departments, holidays, approvals of anyone, carry-over and backups",
		Permissions: adminPermissions,
	},
	{
		Slug: "viewer", DisplayName: DisplayName + " viewer",
		Description: "Read every request, balance and setting (payroll, auditors); change nothing",
		Permissions: viewerPermissions,
	},
	{
		Slug: "employee", DisplayName: DisplayName + " employee",
		Description: "Request leave, see my requests and balance and the team calendar",
		Permissions: employeePermissions,
	},
	{
		Slug: "calendar-viewer", DisplayName: DisplayName + " calendar viewer",
		Description: "See the team calendar only",
		Permissions: calendarPermissions,
	},
}

// Grants maps the platform's built-in role slugs to permission sets: owners
// and admins hold the administrator set, auditors the viewer set, operators
// and members the employee set (every member may request leave).
var Grants = map[string][]string{
	"owner":    adminPermissions,
	"admin":    adminPermissions,
	"operator": employeePermissions,
	"auditor":  viewerPermissions,
	"member":   employeePermissions,
}

// Methods proxied by the gateway: none.
var Methods []gatewayclient.Method

// Abilities are the CASL rules bound to the permissions.
var Abilities = []gatewayclient.Ability{
	{Action: []string{"read"}, Subject: []string{"HrCalendar"}, Requires: "hr:calendar"},
	{Action: []string{"create", "read"}, Subject: []string{"HrRequest"}, Requires: "hr:request"},
	{Action: []string{"read"}, Subject: []string{"HrRequest", "HrAllowance", "HrAbsenceType", "HrDepartment", "HrStats"}, Requires: "hr:read"},
	{Action: []string{"manage"}, Subject: []string{"HrRequest", "HrAllowance", "HrAbsenceType", "HrDepartment", "HrHoliday", "HrBackup"}, Requires: "hr:manage"},
}

// Nav lists the navigation contributions.
var Nav = []gatewayclient.NavEntry{
	{Title: "Leave calendar", Path: "/hr", Icon: "mdi-calendar-month-outline", Order: 940, Requires: "hr:calendar"},
	{Title: "My leave", Path: "/hr/requests", Icon: "mdi-beach", Order: 941, Requires: "hr:request"},
	{Title: "To review", Path: "/hr/review", Icon: "mdi-clipboard-check-outline", Order: 942, Requires: "hr:request"},
	{Title: "Allowances", Path: "/hr/allowances", Icon: "mdi-scale-balance", Order: 943, Requires: "hr:read"},
	{Title: "Absence types", Path: "/hr/absence-types", Icon: "mdi-shape-outline", Order: 944, Requires: "hr:read"},
	{Title: "Departments", Path: "/hr/departments", Icon: "mdi-sitemap-outline", Order: 945, Requires: "hr:read"},
	{Title: "Holidays", Path: "/hr/holidays", Icon: "mdi-party-popper", Order: 946, Requires: "hr:read"},
	{Title: "HR statistics", Path: "/hr/stats", Icon: "mdi-chart-box-outline", Order: 947, Requires: "hr:read"},
}

// PermissionRefs lists "resource:action" for every declared permission.
func PermissionRefs() []string {
	out := make([]string, 0, len(Permissions))
	for _, p := range Permissions {
		out = append(out, p.Resource+":"+p.Action)
	}
	return out
}

// Routes derives the gateway routes from the OpenAPI document.
func Routes(doc *openapi3.T) ([]gatewayclient.Route, error) {
	known := map[string]bool{}
	for _, p := range PermissionRefs() {
		known[p] = true
	}
	var routes []gatewayclient.Route
	for p, item := range doc.Paths.Map() {
		for m, op := range item.Operations() {
			r := gatewayclient.Route{Method: strings.ToUpper(m), Path: p}
			perm, _ := op.Extensions[PermissionExtension].(string)
			public, _ := op.Extensions[PublicExtension].(bool)
			switch {
			case public:
				r.Public = true
			case perm == "":
				return nil, fmt.Errorf("hrmanifest: %s %s declares no permission", r.Method, p)
			case !known[perm]:
				return nil, fmt.Errorf("hrmanifest: %s %s uses undeclared permission %q", r.Method, p, perm)
			default:
				r.Permission = perm
			}
			if v, ok := op.Extensions[BodyLimitExtension]; ok {
				n, ok := v.(float64)
				if !ok || n <= 0 {
					return nil, fmt.Errorf("hrmanifest: %s %s has a bad body limit", r.Method, p)
				}
				r.MaxBodyBytes = uint64(n)
			}
			if v, ok := op.Extensions[ClientAddressExtension].(bool); ok && v {
				r.ClientAddress = true
			}
			if v, ok := op.Extensions[TimeoutExtension]; ok {
				n, ok := v.(float64)
				if !ok || n <= 0 || n > 600 {
					return nil, fmt.Errorf("hrmanifest: %s %s has a bad timeout", r.Method, p)
				}
				r.Timeout = time.Duration(n) * time.Second
			}
			routes = append(routes, r)
		}
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].Method+" "+routes[i].Path < routes[j].Method+" "+routes[j].Path })
	return routes, nil
}

// Load parses the embedded document.
func Load() (*openapi3.T, error) {
	return openapi3.NewLoader().LoadFromData(openapi.HR)
}

// Manifest builds the gateway manifest from the embedded OpenAPI document.
func Manifest() (gatewayclient.Manifest, error) {
	doc, err := Load()
	if err != nil {
		return gatewayclient.Manifest{}, err
	}
	routes, err := Routes(doc)
	if err != nil {
		return gatewayclient.Manifest{}, err
	}
	return gatewayclient.Manifest{
		Module: Module, DisplayName: DisplayName, Version: Version,
		Prefixes:    []string{APIPrefix},
		Routes:      routes,
		Methods:     Methods,
		Permissions: Permissions,
		Abilities:   Abilities,
		Exposes:     []string{"./routes", "./nav"},
		Nav:         Nav,
	}, nil
}

// BuiltinRoles lists the built-in role slugs granted, in a stable order.
var BuiltinRoles = []string{"owner", "admin", "member", "auditor", "operator"}

// Registration is what the module registers with auth: every module
// permission, the module roles and the built-in role grants.
func Registration() authclient.Registration {
	reg := authclient.Registration{Module: Module, DisplayName: DisplayName, Roles: Roles, BuiltinGrants: Grants}
	for _, p := range Permissions {
		reg.Permissions = append(reg.Permissions, authclient.Permission{Resource: p.Resource, Action: p.Action, Description: p.Description})
	}
	return reg
}
