// Package repodb binds repo.Store to TimescaleDB via *store.Store. Tenant
// methods run in a tenant transaction (RLS) — the caller's when invoked inside
// Tx, else their own; *System methods run under the system scope. Lock*
// methods take row locks (SELECT … FOR UPDATE, allowances in id order).
// Unique and foreign-key violations map to repo.ErrConflict, the overlap
// exclusion constraint to repo.ErrOverlap and malformed ids to
// repo.ErrNotFound. Days travel as numeric text so no float is involved.
package repodb

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/go-tangra/go-tangra-hr/v4/internal/leavedays"
	"github.com/go-tangra/go-tangra-hr/v4/internal/repo"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

// DB implements repo.Store over *store.Store.
type DB struct {
	St *store.Store
	tx pgx.Tx // set inside Tx
}

var _ repo.Store = (*DB)(nil)

// New wraps the store.
func New(st *store.Store) *DB { return &DB{St: st} }

func (d *DB) run(ctx context.Context, tenantID string, fn func(tx pgx.Tx) error) error {
	if d.tx != nil {
		return mapErr(fn(d.tx))
	}
	if tenantID == "" {
		return repo.ErrNotFound
	}
	return mapErr(d.St.Tx(ctx, store.Scope{TenantID: tenantID}, fn))
}

func (d *DB) system(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return mapErr(d.St.Tx(ctx, store.Scope{System: true}, fn))
}

// Tx implements repo.Store.
func (d *DB) Tx(ctx context.Context, tenantID string, fn func(repo.Store) error) error {
	if d.tx != nil {
		return fn(d)
	}
	if tenantID == "" {
		return repo.ErrNotFound
	}
	return mapErr(d.St.Tx(ctx, store.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return fn(&DB{St: d.St, tx: tx})
	}))
}

type scanner interface{ Scan(dest ...any) error }

// mapErr maps store errors: no row / malformed id → ErrNotFound, unique or
// foreign-key violation → ErrConflict, exclusion violation → ErrOverlap.
func mapErr(err error) error {
	if err == nil || errors.Is(err, repo.ErrNotFound) || errors.Is(err, repo.ErrConflict) || errors.Is(err, repo.ErrOverlap) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return repo.ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23505", "23503", "23514": // unique, foreign key, check
			return repo.ErrConflict
		case "23P01": // exclusion
			return repo.ErrOverlap
		case "22P02": // invalid_text_representation (a non-uuid id)
			return repo.ErrNotFound
		}
	}
	return err
}

func js(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func unjs(b []byte, v any) error {
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

func pageArgs(p, size int) (int, int) {
	p, size = store.Page(p, size, 500)
	return size, (p - 1) * size
}

func affected(tag pgconn.CommandTag) error {
	if tag.RowsAffected() == 0 {
		return repo.ErrNotFound
	}
	return nil
}

func days(s string) (leavedays.Tenths, error) { return leavedays.Parse(s) }

func capText(c *leavedays.Tenths) *string {
	if c == nil {
		return nil
	}
	s := c.String()
	return &s
}

func capParse(s *string) (*leavedays.Tenths, error) {
	if s == nil {
		return nil, nil
	}
	v, err := leavedays.Parse(*s)
	return &v, err
}

// ---------------------------------------------------------------- tenants

// EnsureTenant implements repo.Store.
func (d *DB) EnsureTenant(ctx context.Context, tenantID string) error {
	return d.run(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO hr_tenants (tenant_id) VALUES ($1) ON CONFLICT DO NOTHING`, tenantID)
		return err
	})
}

// TenantsSystem implements repo.Store.
func (d *DB) TenantsSystem(ctx context.Context) (out []store.TenantState, err error) {
	err = d.system(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT tenant_id::text, stream_cursor, cursor_at, members_synced_at FROM hr_tenants ORDER BY tenant_id`)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (t store.TenantState, err error) {
			err = r.Scan(&t.TenantID, &t.StreamCursor, &t.CursorAt, &t.MembersSyncedAt)
			return t, err
		})
		return err
	})
	return out, err
}

// SetCursor implements repo.Store.
func (d *DB) SetCursor(ctx context.Context, tenantID, cursor string, at time.Time) error {
	return d.run(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE hr_tenants SET stream_cursor = $2, cursor_at = $3 WHERE tenant_id = $1`, tenantID, cursor, at)
		if err != nil {
			return err
		}
		return affected(tag)
	})
}

// SetMembersSynced implements repo.Store.
func (d *DB) SetMembersSynced(ctx context.Context, tenantID string, at time.Time) error {
	return d.run(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE hr_tenants SET members_synced_at = $2 WHERE tenant_id = $1`, tenantID, at)
		if err != nil {
			return err
		}
		return affected(tag)
	})
}

// ----------------------------------------------------------- absence types

const typeCols = `id::text, tenant_id::text, name, description, color, icon, sort_order, active, metadata, deducts,
	requires_approval, COALESCE(pool_id::text, ''), carry_over_cap::text, requires_signing, signing,
	created_at, created_by, updated_at, updated_by`

func scanType(sc scanner) (t store.AbsenceType, err error) {
	var meta, signing []byte
	var cap *string
	if err = sc.Scan(&t.ID, &t.TenantID, &t.Name, &t.Description, &t.Color, &t.Icon, &t.SortOrder, &t.Active, &meta, &t.Deducts,
		&t.RequiresApproval, &t.PoolID, &cap, &t.RequiresSigning, &signing, &t.CreatedAt, &t.CreatedBy, &t.UpdatedAt, &t.UpdatedBy); err != nil {
		return t, err
	}
	if err = unjs(meta, &t.Metadata); err != nil {
		return t, err
	}
	if len(signing) > 0 && string(signing) != "null" {
		t.Signing = &store.SigningSettings{}
		if err = unjs(signing, t.Signing); err != nil {
			return t, err
		}
	}
	t.CarryOverCap, err = capParse(cap)
	return t, err
}

func typeArgs(t store.AbsenceType) []any {
	meta := t.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	var signing any
	if t.Signing != nil {
		signing = js(t.Signing)
	}
	return []any{t.ID, t.TenantID, t.Name, t.Description, t.Color, t.Icon, t.SortOrder, t.Active, js(meta), t.Deducts,
		t.RequiresApproval, t.PoolID, capText(t.CarryOverCap), t.RequiresSigning, signing, t.CreatedAt, t.CreatedBy, t.UpdatedAt, t.UpdatedBy}
}

// CreateAbsenceType implements repo.Store.
func (d *DB) CreateAbsenceType(ctx context.Context, t store.AbsenceType) error {
	return d.run(ctx, t.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO hr_absence_types (id, tenant_id, name, description, color, icon, sort_order, active, metadata,
			deducts, requires_approval, pool_id, carry_over_cap, requires_signing, signing, created_at, created_by, updated_at, updated_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,NULLIF($12,'')::uuid,$13::numeric,$14,$15,$16,$17,$18,$19)`, typeArgs(t)...)
		return err
	})
}

// GetAbsenceType implements repo.Store.
func (d *DB) GetAbsenceType(ctx context.Context, tenantID, id string) (t store.AbsenceType, err error) {
	err = d.run(ctx, tenantID, func(tx pgx.Tx) error {
		t, err = scanType(tx.QueryRow(ctx, `SELECT `+typeCols+` FROM hr_absence_types WHERE tenant_id = $1 AND id = $2`, tenantID, id))
		return err
	})
	return t, err
}

// ListAbsenceTypes implements repo.Store.
func (d *DB) ListAbsenceTypes(ctx context.Context, tenantID string) (out []store.AbsenceType, err error) {
	err = d.run(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+typeCols+` FROM hr_absence_types WHERE tenant_id = $1 ORDER BY sort_order, lower(name)`, tenantID)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (store.AbsenceType, error) { return scanType(r) })
		return err
	})
	return out, err
}

// UpdateAbsenceType implements repo.Store.
func (d *DB) UpdateAbsenceType(ctx context.Context, t store.AbsenceType) error {
	return d.run(ctx, t.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE hr_absence_types SET name=$3, description=$4, color=$5, icon=$6, sort_order=$7, active=$8,
			metadata=$9, deducts=$10, requires_approval=$11, pool_id=NULLIF($12,'')::uuid, carry_over_cap=$13::numeric,
			requires_signing=$14, signing=$15, updated_at=$16, updated_by=$17 WHERE id=$1 AND tenant_id=$2`, append(typeArgs(t)[:15], t.UpdatedAt, t.UpdatedBy)...)
		if err != nil {
			return err
		}
		return affected(tag)
	})
}

// AbsenceTypeInUse implements repo.Store.
func (d *DB) AbsenceTypeInUse(ctx context.Context, tenantID, id string) (used bool, err error) {
	err = d.run(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hr_allowances WHERE tenant_id=$1 AND absence_type_id=$2)
			OR EXISTS (SELECT 1 FROM hr_requests WHERE tenant_id=$1 AND absence_type_id=$2)`, tenantID, id).Scan(&used)
	})
	return used, err
}

// DeleteAbsenceType implements repo.Store.
func (d *DB) DeleteAbsenceType(ctx context.Context, tenantID, id string) error {
	return d.run(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM hr_absence_types WHERE tenant_id=$1 AND id=$2`, tenantID, id)
		if err != nil {
			return err
		}
		return affected(tag)
	})
}

// ------------------------------------------------------------------ pools

const poolCols = `id::text, tenant_id::text, name, description, color, icon, carry_over_cap::text, created_at, created_by, updated_at, updated_by`

func scanPool(sc scanner) (p store.Pool, err error) {
	var cap *string
	if err = sc.Scan(&p.ID, &p.TenantID, &p.Name, &p.Description, &p.Color, &p.Icon, &cap, &p.CreatedAt, &p.CreatedBy, &p.UpdatedAt, &p.UpdatedBy); err != nil {
		return p, err
	}
	p.CarryOverCap, err = capParse(cap)
	return p, err
}

// CreatePool implements repo.Store.
func (d *DB) CreatePool(ctx context.Context, p store.Pool) error {
	return d.run(ctx, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO hr_pools (id, tenant_id, name, description, color, icon, carry_over_cap, created_at, created_by, updated_at, updated_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8,$9,$10,$11)`, p.ID, p.TenantID, p.Name, p.Description, p.Color, p.Icon, capText(p.CarryOverCap),
			p.CreatedAt, p.CreatedBy, p.UpdatedAt, p.UpdatedBy)
		return err
	})
}

// GetPool implements repo.Store.
func (d *DB) GetPool(ctx context.Context, tenantID, id string) (p store.Pool, err error) {
	err = d.run(ctx, tenantID, func(tx pgx.Tx) error {
		p, err = scanPool(tx.QueryRow(ctx, `SELECT `+poolCols+` FROM hr_pools WHERE tenant_id=$1 AND id=$2`, tenantID, id))
		return err
	})
	return p, err
}

// ListPools implements repo.Store.
func (d *DB) ListPools(ctx context.Context, tenantID string) (out []store.Pool, err error) {
	err = d.run(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+poolCols+` FROM hr_pools WHERE tenant_id=$1 ORDER BY lower(name)`, tenantID)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (store.Pool, error) { return scanPool(r) })
		return err
	})
	return out, err
}

// UpdatePool implements repo.Store.
func (d *DB) UpdatePool(ctx context.Context, p store.Pool) error {
	return d.run(ctx, p.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE hr_pools SET name=$3, description=$4, color=$5, icon=$6, carry_over_cap=$7::numeric,
			updated_at=$8, updated_by=$9 WHERE id=$1 AND tenant_id=$2`, p.ID, p.TenantID, p.Name, p.Description, p.Color, p.Icon,
			capText(p.CarryOverCap), p.UpdatedAt, p.UpdatedBy)
		if err != nil {
			return err
		}
		return affected(tag)
	})
}

// PoolInUse implements repo.Store.
func (d *DB) PoolInUse(ctx context.Context, tenantID, id string) (used bool, err error) {
	err = d.run(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hr_absence_types WHERE tenant_id=$1 AND pool_id=$2)
			OR EXISTS (SELECT 1 FROM hr_allowances WHERE tenant_id=$1 AND pool_id=$2)`, tenantID, id).Scan(&used)
	})
	return used, err
}

// DeletePool implements repo.Store.
func (d *DB) DeletePool(ctx context.Context, tenantID, id string) error {
	return d.run(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM hr_pools WHERE tenant_id=$1 AND id=$2`, tenantID, id)
		if err != nil {
			return err
		}
		return affected(tag)
	})
}

// ------------------------------------------------------------- allowances

const allowanceCols = `id::text, tenant_id::text, user_id, year, COALESCE(absence_type_id::text, ''), COALESCE(pool_id::text, ''),
	total_days::text, carried_over::text, used_days::text, COALESCE(carried_from_run::text, ''), notes,
	created_at, created_by, updated_at, updated_by`

func scanAllowance(sc scanner) (a store.Allowance, err error) {
	var total, carried, used string
	if err = sc.Scan(&a.ID, &a.TenantID, &a.UserID, &a.Year, &a.AbsenceTypeID, &a.PoolID, &total, &carried, &used, &a.CarriedFromRun, &a.Notes,
		&a.CreatedAt, &a.CreatedBy, &a.UpdatedAt, &a.UpdatedBy); err != nil {
		return a, err
	}
	if a.Total, err = days(total); err != nil {
		return a, err
	}
	if a.Carried, err = days(carried); err != nil {
		return a, err
	}
	a.Used, err = days(used)
	return a, err
}

// CreateAllowance implements repo.Store.
func (d *DB) CreateAllowance(ctx context.Context, a store.Allowance) error {
	return d.run(ctx, a.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO hr_allowances (id, tenant_id, user_id, year, absence_type_id, pool_id, total_days, carried_over,
			used_days, carried_from_run, notes, created_at, created_by, updated_at, updated_by)
			VALUES ($1,$2,$3,$4,NULLIF($5,'')::uuid,NULLIF($6,'')::uuid,$7::numeric,$8::numeric,$9::numeric,NULLIF($10,'')::uuid,$11,$12,$13,$14,$15)`,
			a.ID, a.TenantID, a.UserID, a.Year, a.AbsenceTypeID, a.PoolID, a.Total.String(), a.Carried.String(), a.Used.String(),
			a.CarriedFromRun, a.Notes, a.CreatedAt, a.CreatedBy, a.UpdatedAt, a.UpdatedBy)
		return err
	})
}

// GetAllowance implements repo.Store.
func (d *DB) GetAllowance(ctx context.Context, tenantID, id string) (a store.Allowance, err error) {
	err = d.run(ctx, tenantID, func(tx pgx.Tx) error {
		a, err = scanAllowance(tx.QueryRow(ctx, `SELECT `+allowanceCols+` FROM hr_allowances WHERE tenant_id=$1 AND id=$2`, tenantID, id))
		return err
	})
	return a, err
}

// ListAllowances implements repo.Store.
func (d *DB) ListAllowances(ctx context.Context, tenantID string, f repo.AllowanceFilter) (out []store.Allowance, total int, err error) {
	where := `tenant_id=$1 AND ($2 = '' OR user_id=$2) AND ($3::text[] IS NULL OR user_id = ANY($3)) AND ($4 = 0 OR year=$4)
		AND ($5 = '' OR absence_type_id::text=$5) AND ($6 = '' OR pool_id::text=$6)`
	var ids any
	if f.UserIDs != nil {
		ids = f.UserIDs
	}
	args := []any{tenantID, f.UserID, ids, f.Year, f.AbsenceTypeID, f.PoolID}
	err = d.run(ctx, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM hr_allowances WHERE `+where, args...).Scan(&total); err != nil {
			return err
		}
		q := `SELECT ` + allowanceCols + ` FROM hr_allowances WHERE ` + where + ` ORDER BY year DESC, user_id, id`
		if !f.All {
			lim, off := pageArgs(f.Page, f.PageSize)
			q += ` LIMIT $7 OFFSET $8`
			args = append(args, lim, off)
		}
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (store.Allowance, error) { return scanAllowance(r) })
		return err
	})
	return out, total, err
}

// FindAllowance implements repo.Store.
func (d *DB) FindAllowance(ctx context.Context, tenantID, userID string, year int, absenceTypeID, poolID string) (a store.Allowance, err error) {
	err = d.run(ctx, tenantID, func(tx pgx.Tx) error {
		a, err = scanAllowance(tx.QueryRow(ctx, `SELECT `+allowanceCols+` FROM hr_allowances WHERE tenant_id=$1 AND user_id=$2 AND year=$3
			AND COALESCE(absence_type_id::text,'')=$4 AND COALESCE(pool_id::text,'')=$5`, tenantID, userID, year, absenceTypeID, poolID))
		return err
	})
	return a, err
}

// UpdateAllowance implements repo.Store (used days are kept).
func (d *DB) UpdateAllowance(ctx context.Context, a store.Allowance) error {
	return d.run(ctx, a.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE hr_allowances SET user_id=$3, year=$4, absence_type_id=NULLIF($5,'')::uuid, pool_id=NULLIF($6,'')::uuid,
			total_days=$7::numeric, carried_over=$8::numeric, carried_from_run=NULLIF($9,'')::uuid, notes=$10, updated_at=$11, updated_by=$12
			WHERE id=$1 AND tenant_id=$2`, a.ID, a.TenantID, a.UserID, a.Year, a.AbsenceTypeID, a.PoolID, a.Total.String(), a.Carried.String(),
			a.CarriedFromRun, a.Notes, a.UpdatedAt, a.UpdatedBy)
		if err != nil {
			return err
		}
		return affected(tag)
	})
}

// LockAllowances implements repo.Store.
func (d *DB) LockAllowances(ctx context.Context, tenantID string, ids []string) (out map[string]store.Allowance, err error) {
	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	out = map[string]store.Allowance{}
	err = d.run(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+allowanceCols+` FROM hr_allowances WHERE tenant_id=$1 AND id::text = ANY($2) ORDER BY id FOR UPDATE`,
			tenantID, sorted)
		if err != nil {
			return err
		}
		list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (store.Allowance, error) { return scanAllowance(r) })
		for _, a := range list {
			out[a.ID] = a
		}
		return err
	})
	return out, err
}

// AddUsed implements repo.Store.
func (d *DB) AddUsed(ctx context.Context, tenantID, id string, delta leavedays.Tenths, at time.Time) error {
	return d.run(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE hr_allowances SET used_days = used_days + $3::numeric, updated_at=$4 WHERE tenant_id=$1 AND id=$2`,
			tenantID, id, delta.String(), at)
		if err != nil {
			return err
		}
		return affected(tag)
	})
}

// AllowanceInUse implements repo.Store.
func (d *DB) AllowanceInUse(ctx context.Context, tenantID, id string) (used bool, err error) {
	err = d.run(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hr_request_charges WHERE tenant_id=$1 AND allowance_id=$2)`, tenantID, id).Scan(&used)
	})
	return used, err
}

// DeleteAllowance implements repo.Store.
func (d *DB) DeleteAllowance(ctx context.Context, tenantID, id string) error {
	return d.run(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM hr_allowances WHERE tenant_id=$1 AND id=$2`, tenantID, id)
		if err != nil {
			return err
		}
		return affected(tag)
	})
}

// --------------------------------------------------------------- requests

const requestCols = `id::text, tenant_id::text, user_id, absence_type_id::text, start_date, end_date, half_start, half_end, days::text,
	status, reason, notes, approver_ids, reviewed_by, reviewed_at, review_notes, COALESCE(submission_id::text, ''), signing_note,
	signing_started_at, signing_attempt, version, created_at, created_by, updated_at`

func scanRequest(sc scanner) (r store.Request, err error) {
	var d string
	if err = sc.Scan(&r.ID, &r.TenantID, &r.UserID, &r.AbsenceTypeID, &r.Start, &r.End, &r.HalfStart, &r.HalfEnd, &d, &r.Status, &r.Reason,
		&r.Notes, &r.ApproverIDs, &r.ReviewedBy, &r.ReviewedAt, &r.ReviewNotes, &r.SubmissionID, &r.SigningNote, &r.SigningStartedAt,
		&r.SigningAttempt, &r.Version, &r.CreatedAt, &r.CreatedBy, &r.UpdatedAt); err != nil {
		return r, err
	}
	r.Start, r.End = leavedays.Date(r.Start), leavedays.Date(r.End)
	r.Days, err = days(d)
	return r, err
}

func approvers(ids []string) []string {
	if ids == nil {
		return []string{}
	}
	return ids
}

// CreateRequest implements repo.Store.
func (d *DB) CreateRequest(ctx context.Context, r store.Request) error {
	if r.Version == 0 {
		r.Version = 1
	}
	return d.run(ctx, r.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO hr_requests (id, tenant_id, user_id, absence_type_id, start_date, end_date, half_start, half_end, days,
			status, reason, notes, approver_ids, reviewed_by, reviewed_at, review_notes, submission_id, signing_note, signing_started_at,
			signing_attempt, version, created_at, created_by, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10,$11,$12,$13,$14,$15,$16,NULLIF($17,'')::uuid,$18,$19,$20,$21,$22,$23,$24)`,
			r.ID, r.TenantID, r.UserID, r.AbsenceTypeID, r.Start, r.End, r.HalfStart, r.HalfEnd, r.Days.String(), r.Status, r.Reason, r.Notes,
			approvers(r.ApproverIDs), r.ReviewedBy, r.ReviewedAt, r.ReviewNotes, r.SubmissionID, r.SigningNote, r.SigningStartedAt,
			r.SigningAttempt, r.Version, r.CreatedAt, r.CreatedBy, r.UpdatedAt)
		return err
	})
}

func (d *DB) getRequest(ctx context.Context, tenantID, id, suffix string) (r store.Request, err error) {
	err = d.run(ctx, tenantID, func(tx pgx.Tx) error {
		r, err = scanRequest(tx.QueryRow(ctx, `SELECT `+requestCols+` FROM hr_requests WHERE tenant_id=$1 AND id=$2`+suffix, tenantID, id))
		return err
	})
	return r, err
}

// GetRequest implements repo.Store.
func (d *DB) GetRequest(ctx context.Context, tenantID, id string) (store.Request, error) {
	return d.getRequest(ctx, tenantID, id, "")
}

// LockRequest implements repo.Store.
func (d *DB) LockRequest(ctx context.Context, tenantID, id string) (store.Request, error) {
	return d.getRequest(ctx, tenantID, id, " FOR UPDATE")
}

// UpdateRequest implements repo.Store.
func (d *DB) UpdateRequest(ctx context.Context, r store.Request) (out store.Request, err error) {
	err = d.run(ctx, r.TenantID, func(tx pgx.Tx) error {
		out, err = scanRequest(tx.QueryRow(ctx, `UPDATE hr_requests SET start_date=$4, end_date=$5, half_start=$6, half_end=$7, days=$8::numeric,
			status=$9, reason=$10, notes=$11, approver_ids=$12, reviewed_by=$13, reviewed_at=$14, review_notes=$15,
			submission_id=NULLIF($16,'')::uuid, signing_note=$17, signing_started_at=$18, signing_attempt=$19, updated_at=$20, version=version+1
			WHERE id=$1 AND tenant_id=$2 AND version=$3 RETURNING `+requestCols,
			r.ID, r.TenantID, r.Version, r.Start, r.End, r.HalfStart, r.HalfEnd, r.Days.String(), r.Status, r.Reason, r.Notes,
			approvers(r.ApproverIDs), r.ReviewedBy, r.ReviewedAt, r.ReviewNotes, r.SubmissionID, r.SigningNote, r.SigningStartedAt,
			r.SigningAttempt, r.UpdatedAt))
		if errors.Is(err, pgx.ErrNoRows) {
			var exists bool
			if e := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hr_requests WHERE tenant_id=$1 AND id=$2)`, r.TenantID, r.ID).Scan(&exists); e != nil {
				return e
			}
			if exists {
				return repo.ErrConflict
			}
			return repo.ErrNotFound
		}
		return err
	})
	return out, err
}

// DeleteRequest implements repo.Store.
func (d *DB) DeleteRequest(ctx context.Context, tenantID, id string) error {
	return d.run(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM hr_requests WHERE tenant_id=$1 AND id=$2`, tenantID, id)
		if err != nil {
			return err
		}
		return affected(tag)
	})
}

// ListRequests implements repo.Store.
func (d *DB) ListRequests(ctx context.Context, tenantID string, f repo.RequestFilter) (out []store.Request, total int, err error) {
	where := `tenant_id=$1 AND ($2 = '' OR user_id=$2) AND ($3::text[] IS NULL OR user_id = ANY($3))
		AND ($4 = '' OR absence_type_id::text=$4) AND ($5::text[] IS NULL OR status = ANY($5))
		AND ($6::date IS NULL OR end_date >= $6) AND ($7::date IS NULL OR start_date <= $7) AND ($8 = '' OR $8 = ANY(approver_ids))`
	var ids, statuses any
	if f.UserIDs != nil {
		ids = f.UserIDs
	}
	if f.Statuses != nil {
		statuses = f.Statuses
	}
	args := []any{tenantID, f.UserID, ids, f.AbsenceTypeID, statuses, f.From, f.To, f.ApproverID}
	err = d.run(ctx, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM hr_requests WHERE `+where, args...).Scan(&total); err != nil {
			return err
		}
		q := `SELECT ` + requestCols + ` FROM hr_requests WHERE ` + where + ` ORDER BY start_date DESC, id DESC`
		if !f.All {
			lim, off := pageArgs(f.Page, f.PageSize)
			q += ` LIMIT $9 OFFSET $10`
			args = append(args, lim, off)
		}
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (store.Request, error) { return scanRequest(r) })
		return err
	})
	return out, total, err
}

// Overlaps implements repo.Store.
func (d *DB) Overlaps(ctx context.Context, tenantID, userID string, start, end time.Time, excludeID string) (found bool, err error) {
	err = d.run(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hr_requests WHERE tenant_id=$1 AND user_id=$2
			AND status IN ('pending','awaiting_signing','approved') AND start_date <= $4 AND end_date >= $3 AND id::text <> $5)`,
			tenantID, userID, start, end, excludeID).Scan(&found)
	})
	return found, err
}

// RequestBySubmission implements repo.Store.
func (d *DB) RequestBySubmission(ctx context.Context, tenantID, submissionID string) (r store.Request, err error) {
	err = d.run(ctx, tenantID, func(tx pgx.Tx) error {
		r, err = scanRequest(tx.QueryRow(ctx, `SELECT `+requestCols+` FROM hr_requests WHERE tenant_id=$1 AND submission_id=$2`, tenantID, submissionID))
		return err
	})
	return r, err
}

// AwaitingSigning implements repo.Store.
func (d *DB) AwaitingSigning(ctx context.Context, tenantID string, before time.Time, limit int) (out []store.Request, err error) {
	err = d.run(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+requestCols+` FROM hr_requests WHERE tenant_id=$1 AND status='awaiting_signing'
			AND signing_started_at < $2 ORDER BY signing_started_at LIMIT $3`, tenantID, before, limit)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (store.Request, error) { return scanRequest(r) })
		return err
	})
	return out, err
}

// ---------------------------------------------------------------- charges

// AddCharges implements repo.Store.
func (d *DB) AddCharges(ctx context.Context, charges []store.Charge) error {
	if len(charges) == 0 {
		return nil
	}
	return d.run(ctx, charges[0].TenantID, func(tx pgx.Tx) error {
		for _, c := range charges {
			if _, err := tx.Exec(ctx, `INSERT INTO hr_request_charges (request_id, tenant_id, allowance_id, year, days)
				VALUES ($1,$2,$3,$4,$5::numeric)`, c.RequestID, c.TenantID, c.AllowanceID, c.Year, c.Days.String()); err != nil {
				return err
			}
		}
		return nil
	})
}

// Charges implements repo.Store.
func (d *DB) Charges(ctx context.Context, tenantID, requestID string) (out []store.Charge, err error) {
	err = d.run(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT request_id::text, tenant_id::text, allowance_id::text, year, days::text FROM hr_request_charges
			WHERE tenant_id=$1 AND request_id=$2 ORDER BY allowance_id`, tenantID, requestID)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (c store.Charge, err error) {
			var dd string
			if err = r.Scan(&c.RequestID, &c.TenantID, &c.AllowanceID, &c.Year, &dd); err != nil {
				return c, err
			}
			c.Days, err = days(dd)
			return c, err
		})
		return err
	})
	return out, err
}

// DeleteCharges implements repo.Store.
func (d *DB) DeleteCharges(ctx context.Context, tenantID, requestID string) error {
	return d.run(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM hr_request_charges WHERE tenant_id=$1 AND request_id=$2`, tenantID, requestID)
		return err
	})
}

// ------------------------------------------------------------ departments

const deptCols = `id::text, tenant_id::text, COALESCE(parent_id::text, ''), name, manager_id, created_at, created_by, updated_at, updated_by`

func scanDept(sc scanner) (d store.Department, err error) {
	err = sc.Scan(&d.ID, &d.TenantID, &d.ParentID, &d.Name, &d.ManagerID, &d.CreatedAt, &d.CreatedBy, &d.UpdatedAt, &d.UpdatedBy)
	return d, err
}

// CreateDepartment implements repo.Store.
func (d *DB) CreateDepartment(ctx context.Context, dp store.Department) error {
	return d.run(ctx, dp.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO hr_departments (id, tenant_id, parent_id, name, manager_id, created_at, created_by, updated_at, updated_by)
			VALUES ($1,$2,NULLIF($3,'')::uuid,$4,$5,$6,$7,$8,$9)`, dp.ID, dp.TenantID, dp.ParentID, dp.Name, dp.ManagerID,
			dp.CreatedAt, dp.CreatedBy, dp.UpdatedAt, dp.UpdatedBy)
		return err
	})
}

// GetDepartment implements repo.Store.
func (d *DB) GetDepartment(ctx context.Context, tenantID, id string) (dp store.Department, err error) {
	err = d.run(ctx, tenantID, func(tx pgx.Tx) error {
		dp, err = scanDept(tx.QueryRow(ctx, `SELECT `+deptCols+` FROM hr_departments WHERE tenant_id=$1 AND id=$2`, tenantID, id))
		return err
	})
	return dp, err
}

// ListDepartments implements repo.Store.
func (d *DB) ListDepartments(ctx context.Context, tenantID string) (out []store.Department, err error) {
	err = d.run(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+deptCols+` FROM hr_departments WHERE tenant_id=$1 ORDER BY lower(name), id`, tenantID)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (store.Department, error) { return scanDept(r) })
		return err
	})
	return out, err
}

// UpdateDepartment implements repo.Store.
func (d *DB) UpdateDepartment(ctx context.Context, dp store.Department) error {
	return d.run(ctx, dp.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE hr_departments SET parent_id=NULLIF($3,'')::uuid, name=$4, manager_id=$5, updated_at=$6, updated_by=$7
			WHERE id=$1 AND tenant_id=$2`, dp.ID, dp.TenantID, dp.ParentID, dp.Name, dp.ManagerID, dp.UpdatedAt, dp.UpdatedBy)
		if err != nil {
			return err
		}
		return affected(tag)
	})
}

// DepartmentInUse implements repo.Store.
func (d *DB) DepartmentInUse(ctx context.Context, tenantID, id string) (used bool, err error) {
	err = d.run(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hr_departments WHERE tenant_id=$1 AND parent_id=$2)
			OR EXISTS (SELECT 1 FROM hr_members WHERE tenant_id=$1 AND department_id=$2)`, tenantID, id).Scan(&used)
	})
	return used, err
}

// DeleteDepartment implements repo.Store.
func (d *DB) DeleteDepartment(ctx context.Context, tenantID, id string) error {
	return d.run(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM hr_departments WHERE tenant_id=$1 AND id=$2`, tenantID, id)
		if err != nil {
			return err
		}
		return affected(tag)
	})
}

// ---------------------------------------------------------------- members

const memberCols = `tenant_id::text, user_id, COALESCE(department_id::text, ''), display_name, active, synced_at`

func scanMember(sc scanner) (m store.Member, err error) {
	err = sc.Scan(&m.TenantID, &m.UserID, &m.DepartmentID, &m.DisplayName, &m.Active, &m.SyncedAt)
	return m, err
}

// UpsertMember implements repo.Store.
func (d *DB) UpsertMember(ctx context.Context, m store.Member) error {
	return d.run(ctx, m.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO hr_members (tenant_id, user_id, department_id, display_name, active, synced_at)
			VALUES ($1,$2,NULLIF($3,'')::uuid,$4,$5,$6) ON CONFLICT (tenant_id, user_id) DO UPDATE SET department_id=EXCLUDED.department_id,
			display_name=EXCLUDED.display_name, active=EXCLUDED.active, synced_at=EXCLUDED.synced_at`,
			m.TenantID, m.UserID, m.DepartmentID, m.DisplayName, m.Active, m.SyncedAt)
		return err
	})
}

// GetMember implements repo.Store.
func (d *DB) GetMember(ctx context.Context, tenantID, userID string) (m store.Member, err error) {
	err = d.run(ctx, tenantID, func(tx pgx.Tx) error {
		m, err = scanMember(tx.QueryRow(ctx, `SELECT `+memberCols+` FROM hr_members WHERE tenant_id=$1 AND user_id=$2`, tenantID, userID))
		return err
	})
	return m, err
}

// ListMembers implements repo.Store.
func (d *DB) ListMembers(ctx context.Context, tenantID string) (out []store.Member, err error) {
	err = d.run(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+memberCols+` FROM hr_members WHERE tenant_id=$1 ORDER BY user_id`, tenantID)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (store.Member, error) { return scanMember(r) })
		return err
	})
	return out, err
}

// --------------------------------------------------------------- holidays

const holidayCols = `id::text, tenant_id::text, date, name, recurring, created_at, created_by`

func scanHoliday(sc scanner) (h store.Holiday, err error) {
	err = sc.Scan(&h.ID, &h.TenantID, &h.Date, &h.Name, &h.Recurring, &h.CreatedAt, &h.CreatedBy)
	h.Date = leavedays.Date(h.Date)
	return h, err
}

// CreateHoliday implements repo.Store.
func (d *DB) CreateHoliday(ctx context.Context, h store.Holiday) error {
	return d.run(ctx, h.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO hr_holidays (id, tenant_id, date, name, recurring, created_at, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			h.ID, h.TenantID, leavedays.Date(h.Date), h.Name, h.Recurring, h.CreatedAt, h.CreatedBy)
		return err
	})
}

// GetHoliday implements repo.Store.
func (d *DB) GetHoliday(ctx context.Context, tenantID, id string) (h store.Holiday, err error) {
	err = d.run(ctx, tenantID, func(tx pgx.Tx) error {
		h, err = scanHoliday(tx.QueryRow(ctx, `SELECT `+holidayCols+` FROM hr_holidays WHERE tenant_id=$1 AND id=$2`, tenantID, id))
		return err
	})
	return h, err
}

// ListHolidays implements repo.Store.
func (d *DB) ListHolidays(ctx context.Context, tenantID string) (out []store.Holiday, err error) {
	err = d.run(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+holidayCols+` FROM hr_holidays WHERE tenant_id=$1 ORDER BY date`, tenantID)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (store.Holiday, error) { return scanHoliday(r) })
		return err
	})
	return out, err
}

// UpdateHoliday implements repo.Store.
func (d *DB) UpdateHoliday(ctx context.Context, h store.Holiday) error {
	return d.run(ctx, h.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE hr_holidays SET date=$3, name=$4, recurring=$5 WHERE id=$1 AND tenant_id=$2`,
			h.ID, h.TenantID, leavedays.Date(h.Date), h.Name, h.Recurring)
		if err != nil {
			return err
		}
		return affected(tag)
	})
}

// DeleteHoliday implements repo.Store.
func (d *DB) DeleteHoliday(ctx context.Context, tenantID, id string) error {
	return d.run(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM hr_holidays WHERE tenant_id=$1 AND id=$2`, tenantID, id)
		if err != nil {
			return err
		}
		return affected(tag)
	})
}

// ------------------------------------------------------ outcomes and runs

// RecordOutcome implements repo.Store.
func (d *DB) RecordOutcome(ctx context.Context, o store.SigningOutcome) (inserted bool, err error) {
	err = d.run(ctx, o.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO hr_signing_outcomes (tenant_id, submission_id, outcome, source, event_id, request_id, result, received_at)
			VALUES ($1,$2,$3,$4,$5,NULLIF($6,'')::uuid,$7,$8) ON CONFLICT DO NOTHING`,
			o.TenantID, o.SubmissionID, o.Outcome, o.Source, o.EventID, o.RequestID, o.Result, o.ReceivedAt)
		inserted = tag.RowsAffected() == 1
		return err
	})
	return inserted, err
}

// CreateCarryOverRun implements repo.Store.
func (d *DB) CreateCarryOverRun(ctx context.Context, r store.CarryOverRun) error {
	return d.run(ctx, r.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO hr_carryover_runs (id, tenant_id, source_year, created_at, created_by, created, updated)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`, r.ID, r.TenantID, r.SourceYear, r.CreatedAt, r.CreatedBy, r.Created, r.Updated)
		return err
	})
}

// ------------------------------------------------------------------- mail

// EnqueueMail implements repo.Store.
func (d *DB) EnqueueMail(ctx context.Context, m store.Mail) error {
	vars := m.Vars
	if vars == nil {
		vars = map[string]string{}
	}
	return d.run(ctx, m.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO hr_mail_outbox (id, tenant_id, key, user_id, vars, attempts, next_at, last_error, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, m.ID, m.TenantID, m.Key, m.UserID, js(vars), m.Attempts, m.NextAt, m.LastError, m.CreatedAt)
		return err
	})
}

// DueMailSystem implements repo.Store.
func (d *DB) DueMailSystem(ctx context.Context, now time.Time, limit int) (out []store.Mail, err error) {
	err = d.system(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, tenant_id::text, key, user_id, vars, attempts, next_at, last_error, created_at
			FROM hr_mail_outbox WHERE next_at <= $1 ORDER BY next_at, id LIMIT $2`, now, limit)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (m store.Mail, err error) {
			var vars []byte
			if err = r.Scan(&m.ID, &m.TenantID, &m.Key, &m.UserID, &vars, &m.Attempts, &m.NextAt, &m.LastError, &m.CreatedAt); err != nil {
				return m, err
			}
			return m, unjs(vars, &m.Vars)
		})
		return err
	})
	return out, err
}

// DeleteMailSystem implements repo.Store.
func (d *DB) DeleteMailSystem(ctx context.Context, id string) error {
	return d.system(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM hr_mail_outbox WHERE id=$1`, id)
		return err
	})
}

// RetryMailSystem implements repo.Store.
func (d *DB) RetryMailSystem(ctx context.Context, id string, attempts int, next time.Time, lastError string) error {
	return d.system(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE hr_mail_outbox SET attempts=$2, next_at=$3, last_error=$4 WHERE id=$1`, id, attempts, next, lastError)
		if err != nil {
			return err
		}
		return affected(tag)
	})
}

// ------------------------------------------------------------------ audit

// AppendAudit implements repo.Store (system scope: the writer serves every tenant).
func (d *DB) AppendAudit(ctx context.Context, row store.AuditRow) error {
	return d.system(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO hr_audit_events (id, tenant_id, at, actor_kind, actor_id, action, subject_kind, subject_id, outcome, reason, detail)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, row.ID, row.TenantID, row.At, row.ActorKind, row.ActorID, row.Action,
			row.SubjectKind, row.SubjectID, row.Outcome, row.Reason, js(row.Detail))
		return err
	})
}
