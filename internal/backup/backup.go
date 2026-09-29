// Package backup exports and imports a tenant's hr data (spec FR-058, US8):
// absence types, pools, departments, members, holidays, allowances, requests
// with their charges. The archive is gzip-compressed JSON with a schema
// version; it never contains e-mail addresses (members carry ids and cached
// names only) or signing documents (requests keep their submission
// reference; the documents stay in the signing module). Only HR
// administrators export and import, always into their own tenant (SR-008).
package backup

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/go-tangra/go-tangra-hr/v4/internal/apperr"
	"github.com/go-tangra/go-tangra-hr/v4/internal/audit"
	"github.com/go-tangra/go-tangra-hr/v4/internal/authz"
	"github.com/go-tangra/go-tangra-hr/v4/internal/repo"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

// SchemaVersion of the archive.
const SchemaVersion = 1

// Import modes.
const (
	ModeSkip      = "skip"      // keep existing rows
	ModeOverwrite = "overwrite" // replace existing rows (requests and charges are never overwritten)
)

// Archive is the backup content.
type Archive struct {
	Schema      int                 `json:"schema"`
	Module      string              `json:"module"`
	ExportedAt  time.Time           `json:"exported_at"`
	Pools       []store.Pool        `json:"pools"`
	Types       []store.AbsenceType `json:"absence_types"`
	Departments []store.Department  `json:"departments"`
	Members     []store.Member      `json:"members"`
	Holidays    []store.Holiday     `json:"holidays"`
	Allowances  []store.Allowance   `json:"allowances"`
	Requests    []store.Request     `json:"requests"`
	Charges     []store.Charge      `json:"charges"`
}

// Counts summarises an archive or an import.
type Counts struct {
	Pools, Types, Departments, Members, Holidays, Allowances, Requests, Charges int
}

// Result of an import.
type Result struct {
	Created Counts
	Updated Counts
	Skipped int
}

// Deps wires the service.
type Deps struct {
	Store    repo.Store
	Audit    audit.Recorder
	Checker  authz.Checker
	Now      func() time.Time
	MaxBytes int64 // archive size limit (compressed and uncompressed)
}

// Service backs up and restores.
type Service struct{ d Deps }

// New builds the service.
func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.MaxBytes <= 0 {
		d.MaxBytes = 256 << 20
	}
	return &Service{d: d}
}

func (s *Service) record(ctx context.Context, subj authz.Subjects, t audit.EventType, err error, detail map[string]any) {
	e := audit.Event{TenantID: subj.TenantID, EventType: t, ActorKind: audit.ActorOf(subj.ActorKind), ActorID: subj.ActorID(),
		SubjectKind: audit.SubjectBackup, SubjectID: subj.TenantID, Outcome: audit.OutcomeOK, Details: detail}
	if err != nil {
		e.Outcome, e.Reason = audit.OutcomeRefused, "error"
		if ae, ok := apperr.As(err); ok {
			e.Reason = ae.Reason
		}
	}
	audit.Emit(ctx, s.d.Audit, e)
}

// Collect reads the caller's tenant into an archive.
func (s *Service) Collect(ctx context.Context, subj authz.Subjects) (a Archive, err error) {
	if err := authz.Require(ctx, s.d.Checker, subj, authz.Manage); err != nil {
		return a, apperr.Forbidden
	}
	t := subj.TenantID
	a = Archive{Schema: SchemaVersion, Module: "hr", ExportedAt: s.d.Now().UTC()}
	if a.Pools, err = s.d.Store.ListPools(ctx, t); err != nil {
		return a, err
	}
	if a.Types, err = s.d.Store.ListAbsenceTypes(ctx, t); err != nil {
		return a, err
	}
	if a.Departments, err = s.d.Store.ListDepartments(ctx, t); err != nil {
		return a, err
	}
	if a.Members, err = s.d.Store.ListMembers(ctx, t); err != nil {
		return a, err
	}
	if a.Holidays, err = s.d.Store.ListHolidays(ctx, t); err != nil {
		return a, err
	}
	if a.Allowances, _, err = s.d.Store.ListAllowances(ctx, t, repo.AllowanceFilter{All: true}); err != nil {
		return a, err
	}
	if a.Requests, _, err = s.d.Store.ListRequests(ctx, t, repo.RequestFilter{All: true}); err != nil {
		return a, err
	}
	for _, r := range a.Requests {
		cs, err := s.d.Store.Charges(ctx, t, r.ID)
		if err != nil {
			return a, err
		}
		a.Charges = append(a.Charges, cs...)
	}
	return a, nil
}

func count(a Archive) Counts {
	return Counts{len(a.Pools), len(a.Types), len(a.Departments), len(a.Members), len(a.Holidays), len(a.Allowances), len(a.Requests), len(a.Charges)}
}

type limitWriter struct {
	w   io.Writer
	n   int64
	max int64
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n+int64(len(p)) > l.max {
		return 0, apperr.PayloadTooLarge
	}
	l.n += int64(len(p))
	return l.w.Write(p)
}

// Export writes the caller's tenant as a gzip archive to w.
func (s *Service) Export(ctx context.Context, subj authz.Subjects, w io.Writer) (err error) {
	var a Archive
	defer func() { s.record(ctx, subj, audit.BackupExport, err, map[string]any{"records": total(count(a))}) }()
	if a, err = s.Collect(ctx, subj); err != nil {
		return err
	}
	zw := gzip.NewWriter(&limitWriter{w: w, max: s.d.MaxBytes})
	if err := json.NewEncoder(zw).Encode(a); err != nil {
		return err
	}
	return zw.Close()
}

func total(c Counts) int {
	return c.Pools + c.Types + c.Departments + c.Members + c.Holidays + c.Allowances + c.Requests + c.Charges
}

// Read decodes and validates an archive (size-limited).
func (s *Service) Read(r io.Reader) (Archive, error) {
	var a Archive
	zr, err := gzip.NewReader(io.LimitReader(r, s.d.MaxBytes))
	if err != nil {
		return a, apperr.InvalidBackup
	}
	defer func() { _ = zr.Close() }()
	lr := &io.LimitedReader{R: zr, N: s.d.MaxBytes + 1}
	dec := json.NewDecoder(lr)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		if lr.N <= 0 {
			return a, apperr.PayloadTooLarge
		}
		return a, apperr.InvalidBackup
	}
	if a.Schema != SchemaVersion || a.Module != "hr" {
		return a, apperr.InvalidBackup.WithField("schema")
	}
	return a, nil
}

// Import restores an archive into the caller's tenant (every row is moved to
// it). Everything happens in one transaction.
func (s *Service) Import(ctx context.Context, subj authz.Subjects, mode string, r io.Reader) (res Result, err error) {
	defer func() {
		s.record(ctx, subj, audit.BackupImport, err, map[string]any{"mode": mode, "created": total(res.Created),
			"updated": total(res.Updated), "skipped": res.Skipped})
	}()
	if err := authz.Require(ctx, s.d.Checker, subj, authz.Manage); err != nil {
		return res, apperr.Forbidden
	}
	if mode != ModeSkip && mode != ModeOverwrite {
		return res, apperr.Validation.WithField("mode")
	}
	a, err := s.Read(r)
	if err != nil {
		return res, err
	}
	err = s.d.Store.Tx(ctx, subj.TenantID, func(tx repo.Store) error {
		res = Result{}
		if err := tx.EnsureTenant(ctx, subj.TenantID); err != nil {
			return err
		}
		return restore(ctx, tx, subj.TenantID, mode, a, &res)
	})
	if err != nil {
		res = Result{}
		if errors.Is(err, repo.ErrConflict) || errors.Is(err, repo.ErrOverlap) {
			return res, apperr.InvalidBackup.WithDetail(map[string]any{"reason": "conflicting_rows"})
		}
		if _, ok := apperr.As(err); !ok {
			return res, fmt.Errorf("backup import: %w", err)
		}
	}
	return res, err
}

// upsert creates a row or, when it exists, skips or overwrites it.
func upsert(mode string, exists bool, create, update func() error, created, updated, skipped *int) error {
	switch {
	case !exists:
		*created++
		return create()
	case mode == ModeOverwrite && update != nil:
		*updated++
		return update()
	default:
		*skipped++
		return nil
	}
}

func restore(ctx context.Context, tx repo.Store, t, mode string, a Archive, res *Result) error {
	c, u := &res.Created, &res.Updated
	for _, p := range a.Pools {
		p.TenantID = t
		_, err := tx.GetPool(ctx, t, p.ID)
		if err := upsert(mode, err == nil, func() error { return tx.CreatePool(ctx, p) }, func() error { return tx.UpdatePool(ctx, p) },
			&c.Pools, &u.Pools, &res.Skipped); err != nil {
			return err
		}
	}
	for _, ty := range a.Types {
		ty.TenantID = t
		_, err := tx.GetAbsenceType(ctx, t, ty.ID)
		if err := upsert(mode, err == nil, func() error { return tx.CreateAbsenceType(ctx, ty) },
			func() error { return tx.UpdateAbsenceType(ctx, ty) }, &c.Types, &u.Types, &res.Skipped); err != nil {
			return err
		}
	}
	// Departments parents first.
	depts := append([]store.Department(nil), a.Departments...)
	depth := map[string]int{}
	byID := map[string]store.Department{}
	for _, d := range depts {
		byID[d.ID] = d
	}
	var level func(id string, n int) int
	level = func(id string, n int) int {
		d, ok := byID[id]
		if !ok || d.ParentID == "" || n > len(byID) {
			return n
		}
		return level(d.ParentID, n+1)
	}
	for _, d := range depts {
		depth[d.ID] = level(d.ID, 0)
	}
	sort.SliceStable(depts, func(i, j int) bool { return depth[depts[i].ID] < depth[depts[j].ID] })
	for _, d := range depts {
		d.TenantID = t
		_, err := tx.GetDepartment(ctx, t, d.ID)
		if err := upsert(mode, err == nil, func() error { return tx.CreateDepartment(ctx, d) },
			func() error { return tx.UpdateDepartment(ctx, d) }, &c.Departments, &u.Departments, &res.Skipped); err != nil {
			return err
		}
	}
	for _, m := range a.Members {
		m.TenantID = t
		_, err := tx.GetMember(ctx, t, m.UserID)
		up := func() error { return tx.UpsertMember(ctx, m) }
		if err := upsert(mode, err == nil, up, up, &c.Members, &u.Members, &res.Skipped); err != nil {
			return err
		}
	}
	for _, h := range a.Holidays {
		h.TenantID = t
		_, err := tx.GetHoliday(ctx, t, h.ID)
		if err := upsert(mode, err == nil, func() error { return tx.CreateHoliday(ctx, h) }, func() error { return tx.UpdateHoliday(ctx, h) },
			&c.Holidays, &u.Holidays, &res.Skipped); err != nil {
			return err
		}
	}
	for _, al := range a.Allowances {
		al.TenantID = t
		cur, err := tx.GetAllowance(ctx, t, al.ID)
		update := func() error {
			if err := tx.UpdateAllowance(ctx, al); err != nil {
				return err
			}
			if d := al.Used - cur.Used; d != 0 {
				return tx.AddUsed(ctx, t, al.ID, d, al.UpdatedAt)
			}
			return nil
		}
		if err := upsert(mode, err == nil, func() error { return tx.CreateAllowance(ctx, al) }, update,
			&c.Allowances, &u.Allowances, &res.Skipped); err != nil {
			return err
		}
	}
	created := map[string]bool{}
	for _, r := range a.Requests {
		r.TenantID = t
		_, err := tx.GetRequest(ctx, t, r.ID)
		if err := upsert(mode, err == nil, func() error { created[r.ID] = true; return tx.CreateRequest(ctx, r) }, nil,
			&c.Requests, &u.Requests, &res.Skipped); err != nil {
			return err
		}
	}
	var charges []store.Charge
	for _, ch := range a.Charges {
		if created[ch.RequestID] {
			ch.TenantID = t
			charges = append(charges, ch)
		}
	}
	c.Charges = len(charges)
	return tx.AddCharges(ctx, charges)
}
