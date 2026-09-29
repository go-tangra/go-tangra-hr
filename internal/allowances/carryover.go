package allowances

import (
	"context"
	"sort"
	"time"

	"github.com/go-tangra/go-tangra-hr/v4/internal/apperr"
	"github.com/go-tangra/go-tangra-hr/v4/internal/audit"
	"github.com/go-tangra/go-tangra-hr/v4/internal/authz"
	"github.com/go-tangra/go-tangra-hr/v4/internal/leavedays"
	"github.com/go-tangra/go-tangra-hr/v4/internal/repo"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

// Carry-over actions.
const (
	ActionCreate = "create" // the next year's allowance is created
	ActionUpdate = "update" // its carried-over days change
	ActionNone   = "none"   // already carried (re-run)
)

// CarryItem is the carry-over of one allowance into the next year.
type CarryItem struct {
	UserID       string
	Kind         string // pool | type
	ID           string // pool or type id
	SourceID     string // source-year allowance
	TargetID     string // next-year allowance ("" when it will be created)
	Unused       leavedays.Tenths
	Cap          *leavedays.Tenths
	Carried      leavedays.Tenths // what the next year receives
	Action       string
	TotalCreated leavedays.Tenths // total of a created allowance (copied from the source)
}

// CarryPlan is a carry-over preview or run.
type CarryPlan struct {
	SourceYear int
	Items      []CarryItem
	Created    int
	Updated    int
	RunID      string // set when applied
}

// CarryOver computes (apply=false) or applies the carry-over of sourceYear
// into sourceYear+1 for the caller's tenant (FR-048, FR-049): per allowance,
// carried = min(max(unused, 0), cap) with the pool's cap for pool allowances
// and the type's cap for type allowances (no cap = everything unused).
// Re-running for the same year changes nothing.
func (s *Service) CarryOver(ctx context.Context, subj authz.Subjects, sourceYear int, apply bool) (plan CarryPlan, err error) {
	t := audit.CarryOverPreview
	if apply {
		t = audit.CarryOverRun
	}
	defer func() {
		s.record(ctx, subj, t, audit.SubjectTenant, subj.TenantID, err,
			map[string]any{"source_year": sourceYear, "created": plan.Created, "updated": plan.Updated})
	}()
	plan.SourceYear = sourceYear
	if err = s.require(ctx, subj, authz.Manage); err != nil {
		return plan, err
	}
	if sourceYear < 2000 || sourceYear > 2098 {
		return plan, apperr.Validation.WithField("source_year")
	}
	err = s.d.Store.Tx(ctx, subj.TenantID, func(tx repo.Store) error {
		p, err := computeCarry(ctx, tx, subj.TenantID, sourceYear)
		if err != nil {
			return err
		}
		plan = p
		if !apply {
			return nil
		}
		return applyCarry(ctx, tx, subj, &plan, s.d.Now)
	})
	return plan, err
}

func computeCarry(ctx context.Context, tx repo.Store, tenant string, year int) (CarryPlan, error) {
	plan := CarryPlan{SourceYear: year}
	src, _, err := tx.ListAllowances(ctx, tenant, repo.AllowanceFilter{Year: year, All: true})
	if err != nil {
		return plan, err
	}
	next, _, err := tx.ListAllowances(ctx, tenant, repo.AllowanceFilter{Year: year + 1, All: true})
	if err != nil {
		return plan, err
	}
	types, err := tx.ListAbsenceTypes(ctx, tenant)
	if err != nil {
		return plan, err
	}
	pools, err := tx.ListPools(ctx, tenant)
	if err != nil {
		return plan, err
	}
	typeCap, poolCap := map[string]*leavedays.Tenths{}, map[string]*leavedays.Tenths{}
	for _, t := range types {
		typeCap[t.ID] = t.CarryOverCap
	}
	for _, p := range pools {
		poolCap[p.ID] = p.CarryOverCap
	}
	target := map[[3]string]store.Allowance{}
	for _, a := range next {
		target[[3]string{a.UserID, a.AbsenceTypeID, a.PoolID}] = a
	}
	for _, a := range src {
		it := CarryItem{UserID: a.UserID, SourceID: a.ID, Unused: max(a.Remaining(), 0)}
		if a.PoolID != "" {
			it.Kind, it.ID, it.Cap = "pool", a.PoolID, poolCap[a.PoolID]
		} else {
			it.Kind, it.ID, it.Cap = "type", a.AbsenceTypeID, typeCap[a.AbsenceTypeID]
		}
		it.Carried = it.Unused
		if it.Cap != nil {
			it.Carried = min(it.Unused, *it.Cap)
		}
		switch n, ok := target[[3]string{a.UserID, a.AbsenceTypeID, a.PoolID}]; {
		case !ok:
			it.Action, it.TotalCreated = ActionCreate, a.Total
			plan.Created++
		case n.Carried == it.Carried:
			it.Action, it.TargetID = ActionNone, n.ID
		default:
			it.Action, it.TargetID = ActionUpdate, n.ID
			plan.Updated++
		}
		plan.Items = append(plan.Items, it)
	}
	sort.Slice(plan.Items, func(i, j int) bool {
		if plan.Items[i].UserID != plan.Items[j].UserID {
			return plan.Items[i].UserID < plan.Items[j].UserID
		}
		return plan.Items[i].ID < plan.Items[j].ID
	})
	return plan, nil
}

func applyCarry(ctx context.Context, tx repo.Store, subj authz.Subjects, plan *CarryPlan, now func() time.Time) error {
	at := now()
	run := store.CarryOverRun{ID: store.NewID(), TenantID: subj.TenantID, SourceYear: plan.SourceYear, CreatedAt: at,
		CreatedBy: subj.ActorID(), Created: plan.Created, Updated: plan.Updated}
	for _, it := range plan.Items {
		switch it.Action {
		case ActionCreate:
			a := store.Allowance{ID: store.NewID(), TenantID: subj.TenantID, UserID: it.UserID, Year: plan.SourceYear + 1,
				Total: it.TotalCreated, Carried: it.Carried, CarriedFromRun: run.ID,
				Audit: store.Audit{CreatedAt: at, CreatedBy: subj.ActorID(), UpdatedAt: at, UpdatedBy: subj.ActorID()}}
			if it.Kind == "pool" {
				a.PoolID = it.ID
			} else {
				a.AbsenceTypeID = it.ID
			}
			if err := tx.CreateAllowance(ctx, a); err != nil {
				return err
			}
		case ActionUpdate:
			a, err := tx.GetAllowance(ctx, subj.TenantID, it.TargetID)
			if err != nil {
				return err
			}
			a.Carried, a.CarriedFromRun, a.UpdatedAt, a.UpdatedBy = it.Carried, run.ID, at, subj.ActorID()
			if err := tx.UpdateAllowance(ctx, a); err != nil {
				return err
			}
		}
	}
	plan.RunID = run.ID
	return tx.CreateCarryOverRun(ctx, run)
}
