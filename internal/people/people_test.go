package people

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	authv1 "github.com/go-tangra/go-tangra-auth/sdk/v4/api/proto/auth/v1"
)

const tenant = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"

// fakeAuth serves Profiles over 2500 members; ids "m0000".."m2499", every
// tenth without an e-mail; fail makes every call fail.
type fakeAuth struct {
	authv1.UnimplementedProfilesServer
	fail          bool
	lookupFail    bool
	contactCalls  int
	maxContactReq int
}

func member(i int) string { return fmt.Sprintf("m%04d", i) }

func (f *fakeAuth) Contacts(_ context.Context, r *authv1.LookupContactsRequest) (*authv1.LookupContactsResponse, error) {
	if f.fail {
		return nil, status.Error(codes.Unavailable, "down")
	}
	f.contactCalls++
	if len(r.GetUserIds()) > f.maxContactReq {
		f.maxContactReq = len(r.GetUserIds())
	}
	out := &authv1.LookupContactsResponse{}
	for _, id := range r.GetUserIds() {
		var n int
		if _, err := fmt.Sscanf(id, "m%04d", &n); err != nil || n%10 == 0 {
			continue
		}
		out.Contacts = append(out.Contacts, &authv1.Contact{UserId: id, DisplayName: "User " + id, Email: id + "@example.org"})
	}
	return out, nil
}

func (f *fakeAuth) ListMembers(_ context.Context, r *authv1.ListMembersRequest) (*authv1.ListMembersResponse, error) {
	if f.fail {
		return nil, status.Error(codes.Unavailable, "down")
	}
	start := 0
	if r.GetCursor() != "" {
		_, _ = fmt.Sscanf(r.GetCursor(), "m%04d", &start)
		start++
	}
	res := &authv1.ListMembersResponse{}
	for i := start; i < 2500 && len(res.UserIds) < int(r.GetLimit()); i++ {
		res.UserIds = append(res.UserIds, member(i))
	}
	if len(res.UserIds) == int(r.GetLimit()) {
		res.NextCursor = res.UserIds[len(res.UserIds)-1]
	}
	return res, nil
}

func (f *fakeAuth) Lookup(_ context.Context, r *authv1.LookupProfilesRequest) (*authv1.LookupProfilesResponse, error) {
	if len(r.GetUserIds()) > 100 || f.lookupFail {
		return nil, status.Error(codes.InvalidArgument, "too many")
	}
	res := &authv1.LookupProfilesResponse{}
	for _, id := range r.GetUserIds() {
		name := "User " + id
		if id == "m0007" {
			name = "Мария Иванова"
		}
		res.Profiles = append(res.Profiles, &authv1.PublicProfile{UserId: id, DisplayName: name})
	}
	return res, nil
}

func client(t *testing.T, srv *fakeAuth) Client {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	g := grpc.NewServer()
	authv1.RegisterProfilesServer(g, srv)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	}), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return Client{Dial: func(context.Context) (grpc.ClientConnInterface, error) { return conn, nil }}
}

func TestContacts(t *testing.T) {
	srv := &fakeAuth{}
	c := client(t, srv)
	ids := make([]string, 0, 250)
	for i := 1; i <= 250; i++ {
		ids = append(ids, member(i))
	}
	ids = append(ids, member(1), "") // duplicates and blanks collapse
	got, err := c.Contacts(context.Background(), tenant, ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 225 || got["m0001"].Email != "m0001@example.org" || got["m0010"].Email != "" {
		t.Fatalf("contacts = %d %+v", len(got), got["m0010"])
	}
	if srv.contactCalls != 3 || srv.maxContactReq > 100 {
		t.Fatalf("batching: %d calls, max %d ids", srv.contactCalls, srv.maxContactReq)
	}
	if got, err := c.Contacts(context.Background(), tenant, nil); err != nil || len(got) != 0 {
		t.Fatal("empty request")
	}
	srv.fail = true
	if _, err := c.Contacts(context.Background(), tenant, ids); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("failure: %v", err)
	}
}

func TestMembers(t *testing.T) {
	srv := &fakeAuth{}
	c := client(t, srv)
	all, err := c.Members(context.Background(), tenant)
	if err != nil || len(all) != 2500 {
		t.Fatalf("members = %d %v", len(all), err)
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].DisplayName > all[i].DisplayName && all[i-1].DisplayName != "Мария Иванова" && all[i].DisplayName != "Мария Иванова" {
			t.Fatalf("not sorted: %q > %q", all[i-1].DisplayName, all[i].DisplayName)
		}
	}
	srv.fail = true
	if _, err := c.Members(context.Background(), tenant); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("failure: %v", err)
	}
	srv.fail = false
	srv.lookupFail = true
	if _, err := c.Members(context.Background(), tenant); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("lookup failure: %v", err)
	}
	down := Client{Dial: func(context.Context) (grpc.ClientConnInterface, error) { return nil, errors.New("no route") }}
	if _, err := down.Members(context.Background(), tenant); !errors.Is(err, ErrUnavailable) {
		t.Fatal("dial failure")
	}
	if _, err := down.Contacts(context.Background(), tenant, []string{"x"}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("dial failure")
	}
}

func TestFake(t *testing.T) {
	f := &Fake{Users: map[string][]Contact{tenant: {{UserID: "a", DisplayName: "Ann", Email: "a@x"}, {UserID: "b", DisplayName: "Bob"}}}}
	got, _ := f.Contacts(context.Background(), tenant, []string{"a", "b"})
	if len(got) != 1 || got["a"].Email != "a@x" {
		t.Fatal("fake contacts")
	}
	if m, _ := f.Members(context.Background(), tenant); len(m) != 2 || m[1].UserID != "b" {
		t.Fatal("fake members")
	}
	f.Err = ErrUnavailable
	if _, err := f.Contacts(context.Background(), tenant, nil); err == nil {
		t.Fatal("fake error")
	}
	if _, err := f.Members(context.Background(), tenant); err == nil {
		t.Fatal("fake error")
	}
}
