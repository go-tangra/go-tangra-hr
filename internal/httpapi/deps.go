package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/go-tangra/go-tangra-hr/v4/internal/allowances"
	"github.com/go-tangra/go-tangra-hr/v4/internal/authz"
	"github.com/go-tangra/go-tangra-hr/v4/internal/backup"
	"github.com/go-tangra/go-tangra-hr/v4/internal/catalog"
	"github.com/go-tangra/go-tangra-hr/v4/internal/departments"
	"github.com/go-tangra/go-tangra-hr/v4/internal/people"
	"github.com/go-tangra/go-tangra-hr/v4/internal/repo"
	"github.com/go-tangra/go-tangra-hr/v4/internal/requests"
	"github.com/go-tangra/go-tangra-hr/v4/internal/routing"
	"github.com/go-tangra/go-tangra-hr/v4/internal/signingmap"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
	"github.com/go-tangra/go-tangra-hr/v4/internal/stream"
)

// Prefix of the browser API.
const Prefix = "/api/hr/v1"

// People answers who the tenant's active members are (people.Cache).
type People interface {
	Members(ctx context.Context, tenant string) ([]people.Member, error)
	Names(ctx context.Context, tenant string, ids []string) (map[string]string, error)
}

// SigningTemplates lists the tenant's signing templates and re-validates
// absence type settings (signing.Adapter).
type SigningTemplates interface {
	Templates(ctx context.Context, tenantID string) ([]signingmap.Template, error)
	CheckSettings(ctx context.Context, tenantID string, s store.SigningSettings) (store.SigningSettings, error)
}

// Deps wire the HTTP handlers. A route whose service is not wired answers 501
// not_implemented.
type Deps struct {
	Hub         *stream.Hub
	Health      func() map[string]string
	Store       repo.Store // statistics
	Checker     authz.Checker
	Tree        func(ctx context.Context, tenantID string) (routing.Tree, error)
	People      People
	Catalog     *catalog.Service
	Allowances  *allowances.Service
	Departments *departments.Service
	Requests    *requests.Service
	Signing     SigningTemplates
	Backup      *backup.Service
	MaxBackup   int64 // import bound
	MaxImport   int64 // holiday import bound
	MaxPageSize int
	Now         func() time.Time
}

// Register mounts the handlers of every wired dependency.
func (s *Server) Register(d Deps) {
	if d.Now == nil {
		d.Now = time.Now
	}
	s.MustHandle("GET", Prefix+"/health", func(w http.ResponseWriter, _ *http.Request) {
		out := map[string]any{"status": "ok"}
		if d.Health != nil {
			comps := d.Health()
			for _, v := range comps {
				if v != "ok" {
					out["status"] = "degraded"
				}
			}
			out["components"] = comps
		}
		WriteJSON(w, http.StatusOK, out)
	})
	if d.Hub != nil {
		s.RegisterStream(d.Hub)
	}
	h := &handlers{s: s, d: d}
	if d.Tree != nil && d.People != nil {
		h.registerPeople()
	}
	if d.Catalog != nil {
		h.registerCatalog()
	}
	if d.Allowances != nil {
		h.registerAllowances()
	}
	if d.Departments != nil {
		h.registerDepartments()
	}
	if d.Requests != nil && d.People != nil && d.Tree != nil {
		h.registerRequests()
	}
	if d.Store != nil && d.People != nil {
		h.registerStats()
	}
	if d.Backup != nil {
		h.registerBackup()
	}
}

type handlers struct {
	s *Server
	d Deps
}

// withSubject wraps a handler needing the verified caller.
func (s *Server) withSubject(method, path string, fn func(w http.ResponseWriter, r *http.Request, subj authz.Subjects)) {
	s.MustHandle(method, path, func(w http.ResponseWriter, r *http.Request) {
		subj, err := Subjects(r)
		if err != nil {
			Fail(w, r, nil, err)
			return
		}
		fn(w, r, subj)
	})
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) { Fail(w, r, s.log, err) }

func queryInt(r *http.Request, name string) int {
	n, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return 0
	}
	return n
}

func queryBool(r *http.Request, name string) bool {
	v := r.URL.Query().Get(name)
	return v == "true" || v == "1"
}

func queryDate(r *http.Request, name string) (*time.Time, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", v)
	if err != nil {
		return nil, ErrValidation
	}
	return &t, nil
}
