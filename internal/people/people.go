// Package people is the hr module's view of the tenant's users, asked from
// the auth service over the mesh (research D5): the active members with their
// display names (ListMembers + Lookup) for the calendar, pickers and member
// sync, and the e-mail address of mail recipients (auth.v1.Profiles/Contacts,
// admitted by auth's policy for svc/hr). E-mail addresses are only used by the
// outbox worker and are never sent to the browser, stored or logged.
package people

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"google.golang.org/grpc"

	authv1 "github.com/go-tangra/go-tangra-auth/sdk/v4/api/proto/auth/v1"
)

// ErrUnavailable is returned when auth cannot be asked.
var ErrUnavailable = errors.New("people: auth unavailable")

// Contact is one member's contact.
type Contact struct {
	UserID, DisplayName, Email string
}

// Member is one active member (no e-mail).
type Member struct {
	UserID, DisplayName string
}

// Directory answers the module's questions about users.
type Directory interface {
	// Contacts returns the active members among ids that have an e-mail, keyed by user id.
	Contacts(ctx context.Context, tenant string, ids []string) (map[string]Contact, error)
	// Members lists every active member of the tenant with its display name
	// (at most maxScan), sorted by name.
	Members(ctx context.Context, tenant string) ([]Member, error)
}

// Limits of the auth RPCs.
const (
	lookupBatch  = 100
	membersBatch = 1000
	maxScan      = 20000 // members listed per tenant
)

// Client asks auth over the mesh.
type Client struct {
	Dial    func(ctx context.Context) (grpc.ClientConnInterface, error)
	Timeout time.Duration
}

func (c Client) profiles(ctx context.Context) (authv1.ProfilesClient, context.Context, context.CancelFunc, error) {
	t := c.Timeout
	if t <= 0 {
		t = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, t)
	conn, err := c.Dial(ctx)
	if err != nil {
		cancel()
		return nil, nil, nil, ErrUnavailable
	}
	return authv1.NewProfilesClient(conn), ctx, cancel, nil
}

func unique(ids []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// Contacts implements Directory.
func (c Client) Contacts(ctx context.Context, tenant string, ids []string) (map[string]Contact, error) {
	ids = unique(ids)
	out := map[string]Contact{}
	if len(ids) == 0 {
		return out, nil
	}
	pc, ctx, cancel, err := c.profiles(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	for i := 0; i < len(ids); i += lookupBatch {
		end := min(i+lookupBatch, len(ids))
		res, err := pc.Contacts(ctx, &authv1.LookupContactsRequest{TenantId: tenant, UserIds: ids[i:end]})
		if err != nil {
			return nil, ErrUnavailable
		}
		for _, ct := range res.GetContacts() {
			out[ct.GetUserId()] = Contact{UserID: ct.GetUserId(), DisplayName: ct.GetDisplayName(), Email: ct.GetEmail()}
		}
	}
	return out, nil
}

// Members implements Directory: pages active member ids and resolves their
// names, sorted by name (then id).
func (c Client) Members(ctx context.Context, tenant string) ([]Member, error) {
	pc, ctx, cancel, err := c.profiles(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	var out []Member
	cursor := ""
	for len(out) < maxScan {
		page, err := pc.ListMembers(ctx, &authv1.ListMembersRequest{TenantId: tenant, Cursor: cursor, Limit: membersBatch})
		if err != nil {
			return nil, ErrUnavailable
		}
		ids := page.GetUserIds()
		for i := 0; i < len(ids); i += lookupBatch {
			end := min(i+lookupBatch, len(ids))
			res, err := pc.Lookup(ctx, &authv1.LookupProfilesRequest{TenantId: tenant, UserIds: ids[i:end]})
			if err != nil {
				return nil, ErrUnavailable
			}
			for _, p := range res.GetProfiles() {
				out = append(out, Member{UserID: p.GetUserId(), DisplayName: p.GetDisplayName()})
			}
		}
		if cursor = page.GetNextCursor(); cursor == "" {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].DisplayName), strings.ToLower(out[j].DisplayName)
		if a != b {
			return a < b
		}
		return out[i].UserID < out[j].UserID
	})
	return out, nil
}

// Fake is an in-memory Directory for tests: users by tenant.
type Fake struct {
	Users map[string][]Contact // tenant → users
	Err   error
}

// Contacts implements Directory.
func (f *Fake) Contacts(_ context.Context, tenant string, ids []string) (map[string]Contact, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	out := map[string]Contact{}
	for _, u := range f.Users[tenant] {
		if want[u.UserID] && u.Email != "" {
			out[u.UserID] = u
		}
	}
	return out, nil
}

// Members implements Directory.
func (f *Fake) Members(_ context.Context, tenant string) ([]Member, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	var out []Member
	for _, u := range f.Users[tenant] {
		out = append(out, Member{UserID: u.UserID, DisplayName: u.DisplayName})
	}
	return out, nil
}

var (
	_ Directory = Client{}
	_ Directory = (*Fake)(nil)
)
