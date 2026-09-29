// Package authz is the hr module's access model. Every browser route declares
// one API permission (x-freya-permission) or "member" (any signed-in user of
// the tenant); the module enforces it itself (defence in depth behind the
// gateway) through a Checker (the auth service's Authorization/Check). A
// caller acts within its own tenant.
//
// Relationships are checked in code on top of the permissions (spec SR-003,
// SR-004):
//   - owner: the person a request or balance belongs to;
//   - approver: the request is routed to the caller (department manager,
//     computed by internal/routing);
//   - manager: the caller manages the person's department or an ancestor
//     department (read access to requests and balances, FR-043);
//   - reviewer: the caller decided the request.
//
// Nobody reviews their own request, HR administrators included (FR-022).
package authz

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Errors of the access model; handlers map them to 404/403 codes.
var (
	ErrForbidden  = errors.New("authz: forbidden")
	ErrSelfReview = errors.New("authz: own request")
	ErrNotRouted  = errors.New("authz: request not routed to caller")
)

// Actor kinds (closed set; the audit vocabulary uses the same names).
const (
	ActorUser    = "user"    // a signed-in platform user (via the gateway)
	ActorService = "service" // a mesh peer (SPIFFE)
	ActorSystem  = "system"  // scheduled task types, the event consumer
)

// API permissions (resource:action).
const (
	Calendar = "hr:calendar" // the team calendar
	Request  = "hr:request"  // own requests and balance
	Read     = "hr:read"     // read everything of the tenant
	Manage   = "hr:manage"   // administer, act for anyone
)

// Permissions lists every module permission.
var Permissions = []string{Calendar, Request, Read, Manage}

// RolePlatformAdmin confers the cross-tenant scope.
const RolePlatformAdmin = "platform-admin"

// Subjects is the authenticated caller.
type Subjects struct {
	TenantID  string
	UserID    string
	Roles     []string
	ActorKind string // user | service | system
}

// Checker answers "does this user hold this API permission in this tenant". A
// failed lookup is a "no".
type Checker interface {
	Has(ctx context.Context, tenantID, userID, permission string) bool
}

// CheckerFunc adapts a function to Checker.
type CheckerFunc func(ctx context.Context, tenantID, userID, permission string) bool

// Has implements Checker.
func (f CheckerFunc) Has(ctx context.Context, tenantID, userID, permission string) bool {
	return f(ctx, tenantID, userID, permission)
}

// Static is a Checker over a fixed user → permissions map (tests, dev).
type Static map[string][]string

// Has implements Checker.
func (s Static) Has(_ context.Context, _, userID, permission string) bool {
	return slices.Contains(s[userID], permission)
}

// User returns the subject of a signed-in user.
func User(tenantID, userID string, roles []string) Subjects {
	return Subjects{TenantID: tenantID, UserID: userID, Roles: roles, ActorKind: ActorUser}
}

// System returns the trusted internal subject acting in tenantID.
func System(tenantID string) Subjects {
	return Subjects{TenantID: tenantID, UserID: ActorSystem, ActorKind: ActorSystem}
}

// Service returns the subject of a mesh peer.
func Service(spiffeID string) Subjects { return Subjects{UserID: spiffeID, ActorKind: ActorService} }

// HasRole reports whether the caller carries role.
func (s Subjects) HasRole(role string) bool { return slices.Contains(s.Roles, role) }

// DefaultPlatformTenant is the platform operators' tenant.
const DefaultPlatformTenant = "00000000-0000-0000-0000-000000000001"

// PlatformTenant is the tenant whose administrators and owners act as
// platform administrators (config platform_tenant_id; set once at start).
var PlatformTenant = DefaultPlatformTenant

// IsPlatformAdmin reports whether the caller has the cross-tenant scope: the
// system subject, a user with the platform-admin role, or an admin or owner
// of the platform tenant.
func (s Subjects) IsPlatformAdmin() bool {
	if s.ActorKind == ActorSystem {
		return true
	}
	if s.ActorKind != ActorUser {
		return false
	}
	return s.HasRole(RolePlatformAdmin) ||
		(s.TenantID != "" && s.TenantID == PlatformTenant && (s.HasRole("admin") || s.HasRole("owner")))
}

// IsMember reports whether the caller is a signed-in user of a tenant.
func (s Subjects) IsMember() bool {
	return s.ActorKind == ActorUser && s.TenantID != "" && s.UserID != ""
}

// ActorID is the user id, falling back to the actor kind.
func (s Subjects) ActorID() string {
	if s.UserID != "" {
		return s.UserID
	}
	return s.ActorKind
}

// Known reports whether perm is a module permission.
func Known(perm string) bool { return slices.Contains(Permissions, perm) }

// Require checks that the caller holds perm. Users are checked through c in
// their own tenant (nil c refuses); the system subject is allowed everything;
// services hold no browser permission.
func Require(ctx context.Context, c Checker, s Subjects, perm string) error {
	if !Known(perm) {
		return fmt.Errorf("%w: unknown permission %q", ErrForbidden, perm)
	}
	switch s.ActorKind {
	case ActorSystem:
		return nil
	case ActorUser:
		if c != nil && s.IsMember() && c.Has(ctx, s.TenantID, s.UserID, perm) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s required", ErrForbidden, perm)
}

// Allowed is Require as a boolean.
func Allowed(ctx context.Context, c Checker, s Subjects, perm string) bool {
	return Require(ctx, c, s, perm) == nil
}

// RequirePlatformAdmin permits only platform administrators (or the system).
func RequirePlatformAdmin(s Subjects) error {
	if s.IsPlatformAdmin() {
		return nil
	}
	return fmt.Errorf("%w: platform-admin required", ErrForbidden)
}

// Is reports whether the caller is the member userID.
func Is(s Subjects, userID string) bool {
	return s.IsMember() && userID != "" && s.UserID == userID
}

// In reports whether the caller is one of ids.
func In(s Subjects, ids []string) bool {
	return s.IsMember() && slices.Contains(ids, s.UserID)
}

// Facts are the relationships of one request (or one person, for balances).
type Facts struct {
	OwnerID     string
	ApproverIDs []string // routed approvers (internal/routing)
	ManagerIDs  []string // managers of the owner's department and its ancestors
	ReviewedBy  string
}

// ActFor checks that the caller may create or edit requests for userID:
// themselves with hr:request, anyone with hr:manage.
func ActFor(ctx context.Context, c Checker, s Subjects, userID string) error {
	if Is(s, userID) {
		return Require(ctx, c, s, Request)
	}
	return Require(ctx, c, s, Manage)
}

// CanViewPerson reports whether the caller may read a person's balance and
// request list: the person, one of their managers, or hr:read.
func CanViewPerson(ctx context.Context, c Checker, s Subjects, f Facts) bool {
	return s.ActorKind == ActorSystem || Is(s, f.OwnerID) || In(s, f.ManagerIDs) || Allowed(ctx, c, s, Read)
}

// CanViewRequest reports whether the caller may read a request's details
// (reason, notes, review notes): owner, approver, manager, reviewer or
// hr:read (SR-004).
func CanViewRequest(ctx context.Context, c Checker, s Subjects, f Facts) bool {
	return CanViewPerson(ctx, c, s, f) || In(s, f.ApproverIDs) || Is(s, f.ReviewedBy)
}

// Review checks that the caller may approve, reject or revoke a request:
// never the owner; a routed approver, or hr:manage (FR-022, FR-023).
func Review(ctx context.Context, c Checker, s Subjects, f Facts) error {
	if s.ActorKind == ActorSystem {
		return nil
	}
	if Is(s, f.OwnerID) {
		return ErrSelfReview
	}
	if In(s, f.ApproverIDs) || Allowed(ctx, c, s, Manage) {
		return nil
	}
	return ErrNotRouted
}

// CanDownload reports whether the caller may download a request's signed
// document: owner, reviewer, approver, manager, or hr:manage (FR-038).
func CanDownload(ctx context.Context, c Checker, s Subjects, f Facts) bool {
	return s.ActorKind == ActorSystem || Is(s, f.OwnerID) || Is(s, f.ReviewedBy) || In(s, f.ApproverIDs) ||
		In(s, f.ManagerIDs) || Allowed(ctx, c, s, Manage)
}

// Split returns the resource and action of a "resource:action" permission.
func Split(perm string) (resource, action string, ok bool) {
	resource, action, ok = strings.Cut(perm, ":")
	return resource, action, ok && resource != "" && action != ""
}
