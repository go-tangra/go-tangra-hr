package httpapi

import (
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-tangra/go-tangra-hr/v4/internal/allowances"
	"github.com/go-tangra/go-tangra-hr/v4/internal/apperr"
	"github.com/go-tangra/go-tangra-hr/v4/internal/authz"
	"github.com/go-tangra/go-tangra-hr/v4/internal/catalog"
	"github.com/go-tangra/go-tangra-hr/v4/internal/departments"
	"github.com/go-tangra/go-tangra-hr/v4/internal/leavedays"
	"github.com/go-tangra/go-tangra-hr/v4/internal/repo"
	"github.com/go-tangra/go-tangra-hr/v4/internal/requests"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

func tenths(f *float64, field string) (*leavedays.Tenths, error) {
	if f == nil {
		return nil, nil
	}
	t, err := leavedays.FromFloat(*f)
	if err != nil {
		return nil, apperr.Validation.WithField(field)
	}
	return &t, nil
}

func tenthsOf(f float64, field string) (leavedays.Tenths, error) {
	t, err := leavedays.FromFloat(f)
	if err != nil {
		return 0, apperr.Validation.WithField(field)
	}
	return t, nil
}

// ------------------------------------------------------------------ people

func (h *handlers) registerPeople() {
	s := h.s
	s.withSubject("GET", Prefix+"/me", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		perms := []string{}
		for _, p := range authz.Permissions {
			if authz.Allowed(r.Context(), h.d.Checker, subj, p) {
				perms = append(perms, p)
			}
		}
		tree, err := h.d.Tree(r.Context(), subj.TenantID)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		manages := tree.Managed(subj.UserID)
		if manages == nil {
			manages = []string{}
		}
		WriteJSON(w, http.StatusOK, map[string]any{"user_id": subj.UserID, "permissions": perms, "manages": manages,
			"department_id": tree.MemberOf[subj.UserID]})
	})
	s.withSubject("GET", Prefix+"/people", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		list, err := h.people(r, subj, r.URL.Query().Get("department"), strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q"))))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": list})
	})
}

func (h *handlers) people(r *http.Request, subj authz.Subjects, department, q string) ([]personView, error) {
	members, err := h.d.People.Members(r.Context(), subj.TenantID)
	if err != nil {
		return nil, apperr.TemporarilyUnavailable
	}
	tree, err := h.d.Tree(r.Context(), subj.TenantID)
	if err != nil {
		return nil, err
	}
	out := make([]personView, 0, len(members))
	for _, m := range members {
		dept := tree.MemberOf[m.UserID]
		if (department != "" && dept != department) || (q != "" && !strings.Contains(strings.ToLower(m.DisplayName), q)) {
			continue
		}
		out = append(out, personView{UserID: m.UserID, Name: m.DisplayName, DepartmentID: dept})
	}
	return out, nil
}

// ----------------------------------------------------------------- catalog

type typeBody struct {
	Name             string                 `json:"name"`
	Description      string                 `json:"description"`
	Color            string                 `json:"color"`
	Icon             string                 `json:"icon"`
	SortOrder        int                    `json:"sort_order"`
	Active           *bool                  `json:"active"`
	Metadata         map[string]any         `json:"metadata"`
	Deducts          bool                   `json:"deducts"`
	RequiresApproval *bool                  `json:"requires_approval"`
	PoolID           string                 `json:"pool_id"`
	CarryOverCap     *float64               `json:"carry_over_cap"`
	RequiresSigning  bool                   `json:"requires_signing"`
	Signing          *store.SigningSettings `json:"signing"`
}

func (b typeBody) input() (catalog.TypeInput, error) {
	cap, err := tenths(b.CarryOverCap, "carry_over_cap")
	if err != nil {
		return catalog.TypeInput{}, err
	}
	active, approval := true, true
	if b.Active != nil {
		active = *b.Active
	}
	if b.RequiresApproval != nil {
		approval = *b.RequiresApproval
	}
	return catalog.TypeInput{Name: b.Name, Description: b.Description, Color: b.Color, Icon: b.Icon, SortOrder: b.SortOrder, Active: active,
		Metadata: b.Metadata, Deducts: b.Deducts, RequiresApproval: approval, PoolID: b.PoolID, CarryOverCap: cap,
		RequiresSigning: b.RequiresSigning, Signing: b.Signing}, nil
}

type poolBody struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Color        string   `json:"color"`
	Icon         string   `json:"icon"`
	CarryOverCap *float64 `json:"carry_over_cap"`
}

func (b poolBody) input() (catalog.PoolInput, error) {
	cap, err := tenths(b.CarryOverCap, "carry_over_cap")
	return catalog.PoolInput{Name: b.Name, Description: b.Description, Color: b.Color, Icon: b.Icon, CarryOverCap: cap}, err
}

type holidayBody struct {
	Date      string `json:"date"`
	Name      string `json:"name"`
	Recurring bool   `json:"recurring"`
}

func (b holidayBody) input() (catalog.HolidayInput, error) {
	d, err := time.Parse("2006-01-02", b.Date)
	if err != nil {
		return catalog.HolidayInput{}, apperr.Validation.WithField("date")
	}
	return catalog.HolidayInput{Date: d, Name: b.Name, Recurring: b.Recurring}, nil
}

func (h *handlers) registerCatalog() {
	s, c := h.s, h.d.Catalog
	s.withSubject("GET", Prefix+"/absence-types", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		list, err := c.ListTypes(r.Context(), subj, queryBool(r, "all"))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		out := make([]absenceTypeView, 0, len(list))
		for _, t := range list {
			out = append(out, viewType(t))
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	})
	s.withSubject("POST", Prefix+"/absence-types", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var b typeBody
		if err := DecodeJSON(r, &b, 0); err != nil {
			s.fail(w, r, err)
			return
		}
		in, err := b.input()
		if err == nil {
			var t store.AbsenceType
			if t, err = c.CreateType(r.Context(), subj, in); err == nil {
				WriteJSON(w, http.StatusCreated, viewType(t))
				return
			}
		}
		s.fail(w, r, err)
	})
	s.withSubject("GET", Prefix+"/absence-types/{id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		t, err := c.GetType(r.Context(), subj, r.PathValue("id"))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, viewType(t))
	})
	s.withSubject("PUT", Prefix+"/absence-types/{id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var b typeBody
		if err := DecodeJSON(r, &b, 0); err != nil {
			s.fail(w, r, err)
			return
		}
		in, err := b.input()
		if err == nil {
			var t store.AbsenceType
			if t, err = c.UpdateType(r.Context(), subj, r.PathValue("id"), in); err == nil {
				WriteJSON(w, http.StatusOK, viewType(t))
				return
			}
		}
		s.fail(w, r, err)
	})
	s.withSubject("DELETE", Prefix+"/absence-types/{id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		if err := c.DeleteType(r.Context(), subj, r.PathValue("id")); err != nil {
			s.fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if h.d.Signing != nil {
		s.withSubject("GET", Prefix+"/absence-types/{id}/signing-check", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
			t, err := c.GetType(r.Context(), subj, r.PathValue("id"))
			if err != nil {
				s.fail(w, r, err)
				return
			}
			if t.Signing == nil {
				WriteJSON(w, http.StatusOK, map[string]any{"ok": false, "reason": "no_signing"})
				return
			}
			if _, err := h.d.Signing.CheckSettings(r.Context(), subj.TenantID, *t.Signing); err != nil {
				reason := "signing_unavailable"
				if e, ok := apperr.As(err); ok {
					reason = e.Reason
					if d, ok := e.Detail["reason"].(string); ok {
						reason = d
					}
				}
				WriteJSON(w, http.StatusOK, map[string]any{"ok": false, "reason": reason})
				return
			}
			WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
		})
		s.withSubject("GET", Prefix+"/signing/templates", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
			list, err := h.d.Signing.Templates(r.Context(), subj.TenantID)
			if err != nil {
				s.fail(w, r, err)
				return
			}
			type party struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			type field struct {
				ID         string `json:"id"`
				Name       string `json:"name"`
				Type       string `json:"type"`
				Party      string `json:"party"`
				TextValued bool   `json:"text_valued"`
			}
			type tpl struct {
				ID      string  `json:"id"`
				Name    string  `json:"name"`
				Parties []party `json:"parties"`
				Fields  []field `json:"fields"`
			}
			out := make([]tpl, 0, len(list))
			for _, t := range list {
				v := tpl{ID: t.ID, Name: t.Name, Parties: []party{}, Fields: []field{}}
				for _, p := range t.Parties {
					v.Parties = append(v.Parties, party{p.ID, p.Name})
				}
				for _, f := range t.Fields {
					v.Fields = append(v.Fields, field{f.ID, f.Name, f.Type, f.Party, f.TextValued})
				}
				out = append(out, v)
			}
			WriteJSON(w, http.StatusOK, map[string]any{"items": out})
		})
	}
	s.withSubject("GET", Prefix+"/pools", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		list, err := c.ListPools(r.Context(), subj)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		out := make([]poolView, 0, len(list))
		for _, p := range list {
			out = append(out, viewPool(p))
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	})
	s.withSubject("POST", Prefix+"/pools", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var b poolBody
		if err := DecodeJSON(r, &b, 0); err != nil {
			s.fail(w, r, err)
			return
		}
		in, err := b.input()
		if err == nil {
			var p store.Pool
			if p, err = c.CreatePool(r.Context(), subj, in); err == nil {
				WriteJSON(w, http.StatusCreated, viewPool(p))
				return
			}
		}
		s.fail(w, r, err)
	})
	s.withSubject("GET", Prefix+"/pools/{id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		p, err := c.GetPool(r.Context(), subj, r.PathValue("id"))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, viewPool(p))
	})
	s.withSubject("PUT", Prefix+"/pools/{id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var b poolBody
		if err := DecodeJSON(r, &b, 0); err != nil {
			s.fail(w, r, err)
			return
		}
		in, err := b.input()
		if err == nil {
			var p store.Pool
			if p, err = c.UpdatePool(r.Context(), subj, r.PathValue("id"), in); err == nil {
				WriteJSON(w, http.StatusOK, viewPool(p))
				return
			}
		}
		s.fail(w, r, err)
	})
	s.withSubject("DELETE", Prefix+"/pools/{id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		if err := c.DeletePool(r.Context(), subj, r.PathValue("id")); err != nil {
			s.fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	s.withSubject("GET", Prefix+"/holidays", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		list, err := c.ListHolidays(r.Context(), subj, queryInt(r, "year"))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		out := make([]holidayView, 0, len(list))
		for _, x := range list {
			out = append(out, viewHoliday(x))
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	})
	s.withSubject("POST", Prefix+"/holidays", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var b holidayBody
		if err := DecodeJSON(r, &b, 0); err != nil {
			s.fail(w, r, err)
			return
		}
		in, err := b.input()
		if err == nil {
			var x store.Holiday
			if x, err = c.CreateHoliday(r.Context(), subj, in); err == nil {
				WriteJSON(w, http.StatusCreated, viewHoliday(x))
				return
			}
		}
		s.fail(w, r, err)
	})
	s.withSubject("PUT", Prefix+"/holidays/{id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var b holidayBody
		if err := DecodeJSON(r, &b, 0); err != nil {
			s.fail(w, r, err)
			return
		}
		in, err := b.input()
		if err == nil {
			var x store.Holiday
			if x, err = c.UpdateHoliday(r.Context(), subj, r.PathValue("id"), in); err == nil {
				WriteJSON(w, http.StatusOK, viewHoliday(x))
				return
			}
		}
		s.fail(w, r, err)
	})
	s.withSubject("DELETE", Prefix+"/holidays/{id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		if err := c.DeleteHoliday(r.Context(), subj, r.PathValue("id")); err != nil {
			s.fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	s.withSubject("POST", Prefix+"/holidays/import", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		limit := h.d.MaxImport
		if limit <= 0 {
			limit = 64 << 10
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
		if err != nil {
			s.fail(w, r, ErrBodyTooLarge)
			return
		}
		res, err := c.ImportHolidays(r.Context(), subj, data, queryBool(r, "dry_run"))
		if err != nil {
			if e, ok := apperr.As(err); ok && e.Status == http.StatusUnprocessableEntity {
				if res.Errors == nil {
					res.Errors = []catalog.LineError{}
				}
				WriteJSON(w, e.Status, map[string]any{"reason": e.Reason, "field": e.Field, "detail": map[string]any{"result": res}})
				return
			}
			s.fail(w, r, err)
			return
		}
		if res.Errors == nil {
			res.Errors = []catalog.LineError{}
		}
		WriteJSON(w, http.StatusOK, res)
	})
}

// -------------------------------------------------------------- allowances

type allowanceBody struct {
	UserID        string  `json:"user_id"`
	Year          int     `json:"year"`
	AbsenceTypeID string  `json:"absence_type_id"`
	PoolID        string  `json:"pool_id"`
	Total         float64 `json:"total_days"`
	Carried       float64 `json:"carried_over"`
	Notes         string  `json:"notes"`
}

func (b allowanceBody) input() (allowances.Input, error) {
	total, err := tenthsOf(b.Total, "total_days")
	if err != nil {
		return allowances.Input{}, err
	}
	carried, err := tenthsOf(b.Carried, "carried_over")
	return allowances.Input{UserID: b.UserID, Year: b.Year, AbsenceTypeID: b.AbsenceTypeID, PoolID: b.PoolID, Total: total,
		Carried: carried, Notes: b.Notes}, err
}

type carryItemView struct {
	UserID  string   `json:"user_id"`
	Kind    string   `json:"kind"`
	ID      string   `json:"id"`
	Unused  float64  `json:"unused"`
	Cap     *float64 `json:"cap"`
	Carried float64  `json:"carried"`
	Action  string   `json:"action"`
}

func viewPlan(p allowances.CarryPlan) map[string]any {
	items := make([]carryItemView, 0, len(p.Items))
	for _, it := range p.Items {
		items = append(items, carryItemView{UserID: it.UserID, Kind: it.Kind, ID: it.ID, Unused: days(it.Unused), Cap: capView(it.Cap),
			Carried: days(it.Carried), Action: it.Action})
	}
	return map[string]any{"source_year": p.SourceYear, "run_id": p.RunID, "created": p.Created, "updated": p.Updated, "items": items}
}

func (h *handlers) registerAllowances() {
	s, a := h.s, h.d.Allowances
	s.withSubject("GET", Prefix+"/allowances", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		q := r.URL.Query()
		list, total, err := a.List(r.Context(), subj, allowances.Filter{UserID: q.Get("user"), Year: queryInt(r, "year"),
			AbsenceTypeID: q.Get("type"), PoolID: q.Get("pool"), Page: queryInt(r, "page"), PageSize: queryInt(r, "page_size")})
		if err != nil {
			s.fail(w, r, err)
			return
		}
		out := make([]allowanceView, 0, len(list))
		for _, x := range list {
			out = append(out, viewAllowance(x))
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out, "total": total})
	})
	s.withSubject("POST", Prefix+"/allowances", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var b allowanceBody
		if err := DecodeJSON(r, &b, 0); err != nil {
			s.fail(w, r, err)
			return
		}
		in, err := b.input()
		if err == nil {
			var x store.Allowance
			if x, err = a.Create(r.Context(), subj, in); err == nil {
				WriteJSON(w, http.StatusCreated, viewAllowance(x))
				return
			}
		}
		s.fail(w, r, err)
	})
	s.withSubject("GET", Prefix+"/allowances/{id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		x, err := a.Get(r.Context(), subj, r.PathValue("id"))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, viewAllowance(x))
	})
	s.withSubject("PUT", Prefix+"/allowances/{id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var b allowanceBody
		if err := DecodeJSON(r, &b, 0); err != nil {
			s.fail(w, r, err)
			return
		}
		in, err := b.input()
		if err == nil {
			var x store.Allowance
			if x, err = a.Update(r.Context(), subj, r.PathValue("id"), in); err == nil {
				WriteJSON(w, http.StatusOK, viewAllowance(x))
				return
			}
		}
		s.fail(w, r, err)
	})
	s.withSubject("DELETE", Prefix+"/allowances/{id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		if err := a.Delete(r.Context(), subj, r.PathValue("id")); err != nil {
			s.fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	s.withSubject("GET", Prefix+"/balance/{user_id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		year := queryInt(r, "year")
		if year == 0 {
			year = h.d.Now().Year()
		}
		user := r.PathValue("user_id")
		lines, err := a.Balance(r.Context(), subj, user, year)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		out := make([]balanceLineView, 0, len(lines))
		for _, l := range lines {
			out = append(out, viewLine(l))
		}
		WriteJSON(w, http.StatusOK, map[string]any{"user_id": user, "year": year, "lines": out})
	})
	carry := func(apply bool) func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		return func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
			var b struct {
				SourceYear int `json:"source_year"`
			}
			if err := DecodeJSON(r, &b, 0); err != nil {
				s.fail(w, r, err)
				return
			}
			plan, err := a.CarryOver(r.Context(), subj, b.SourceYear, apply)
			if err != nil {
				s.fail(w, r, err)
				return
			}
			WriteJSON(w, http.StatusOK, viewPlan(plan))
		}
	}
	s.withSubject("POST", Prefix+"/carry-over/preview", carry(false))
	s.withSubject("POST", Prefix+"/carry-over", carry(true))
}

// ------------------------------------------------------------- departments

type departmentBody struct {
	Name      string `json:"name"`
	ParentID  string `json:"parent_id"`
	ManagerID string `json:"manager_id"`
}

func (h *handlers) registerDepartments() {
	s, dp := h.s, h.d.Departments
	view := func(r *http.Request, subj authz.Subjects, d store.Department, members []store.Member) departmentView {
		v := departmentView{ID: d.ID, Name: d.Name, ParentID: d.ParentID, ManagerID: d.ManagerID, MemberIDs: []string{}}
		for _, m := range members {
			if m.DepartmentID == d.ID && m.Active {
				v.MemberIDs = append(v.MemberIDs, m.UserID)
			}
		}
		if d.ManagerID != "" && h.d.People != nil {
			if names, err := h.d.People.Names(r.Context(), subj.TenantID, []string{d.ManagerID}); err == nil {
				v.ManagerName = names[d.ManagerID]
			}
		}
		return v
	}
	s.withSubject("GET", Prefix+"/departments", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		list, err := dp.List(r.Context(), subj)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		members, err := dp.Members(r.Context(), subj)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		out := make([]departmentView, 0, len(list))
		for _, d := range list {
			out = append(out, view(r, subj, d, members))
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	})
	s.withSubject("POST", Prefix+"/departments", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var b departmentBody
		if err := DecodeJSON(r, &b, 0); err != nil {
			s.fail(w, r, err)
			return
		}
		d, err := dp.Create(r.Context(), subj, departments.Input(b))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusCreated, view(r, subj, d, nil))
	})
	s.withSubject("PUT", Prefix+"/departments/{id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var b departmentBody
		if err := DecodeJSON(r, &b, 0); err != nil {
			s.fail(w, r, err)
			return
		}
		d, err := dp.Update(r.Context(), subj, r.PathValue("id"), departments.Input(b))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, view(r, subj, d, nil))
	})
	s.withSubject("DELETE", Prefix+"/departments/{id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		if err := dp.Delete(r.Context(), subj, r.PathValue("id")); err != nil {
			s.fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	s.withSubject("PUT", Prefix+"/departments/{id}/members", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var b struct {
			Add    []string `json:"add"`
			Remove []string `json:"remove"`
		}
		if err := DecodeJSON(r, &b, 0); err != nil {
			s.fail(w, r, err)
			return
		}
		if err := dp.SetMembers(r.Context(), subj, r.PathValue("id"), b.Add, b.Remove); err != nil {
			s.fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// ---------------------------------------------------------------- requests

type requestBody struct {
	UserID        string `json:"user_id"`
	AbsenceTypeID string `json:"absence_type_id"`
	StartDate     string `json:"start_date"`
	EndDate       string `json:"end_date"`
	HalfStart     bool   `json:"half_start"`
	HalfEnd       bool   `json:"half_end"`
	Reason        string `json:"reason"`
	Notes         string `json:"notes"`
}

func (b requestBody) input() (requests.Input, error) {
	start, err := time.Parse("2006-01-02", b.StartDate)
	if err != nil {
		return requests.Input{}, apperr.Validation.WithField("start_date")
	}
	end, err := time.Parse("2006-01-02", b.EndDate)
	if err != nil {
		return requests.Input{}, apperr.Validation.WithField("end_date")
	}
	return requests.Input{UserID: b.UserID, AbsenceTypeID: b.AbsenceTypeID, Start: start, End: end, HalfStart: b.HalfStart,
		HalfEnd: b.HalfEnd, Reason: b.Reason, Notes: b.Notes}, nil
}

// views renders requests with names and the caller's allowed actions.
func (h *handlers) views(r *http.Request, subj authz.Subjects, list []store.Request) ([]requestView, error) {
	ids := []string{}
	for _, x := range list {
		ids = append(ids, x.UserID, x.ReviewedBy)
		ids = append(ids, x.ApproverIDs...)
	}
	names, err := h.d.People.Names(r.Context(), subj.TenantID, ids)
	if err != nil {
		names = map[string]string{}
	}
	manage := authz.Allowed(r.Context(), h.d.Checker, subj, authz.Manage)
	today := leavedays.Date(h.d.Now())
	out := make([]requestView, 0, len(list))
	for _, x := range list {
		v := requestView{ID: x.ID, UserID: x.UserID, UserName: names[x.UserID], AbsenceTypeID: x.AbsenceTypeID, StartDate: dateString(x.Start),
			EndDate: dateString(x.End), HalfStart: x.HalfStart, HalfEnd: x.HalfEnd, Days: days(x.Days), Status: x.Status, Reason: x.Reason,
			Notes: x.Notes, ApproverIDs: x.ApproverIDs, ApproverNames: []string{}, ReviewedBy: x.ReviewedBy, ReviewerName: names[x.ReviewedBy],
			ReviewedAt: x.ReviewedAt, ReviewNotes: x.ReviewNotes, Signing: x.SubmissionID != "", SigningNote: x.SigningNote, CreatedAt: x.CreatedAt}
		if v.ApproverIDs == nil {
			v.ApproverIDs = []string{}
		}
		for _, a := range x.ApproverIDs {
			v.ApproverNames = append(v.ApproverNames, names[a])
		}
		own := authz.Is(subj, x.UserID)
		open := x.Status == store.StatusPending || x.Status == store.StatusAwaitingSigning
		v.CanReview = !own && ((open && (slices.Contains(x.ApproverIDs, subj.UserID) || manage)) ||
			(x.Status == store.StatusApproved && (slices.Contains(x.ApproverIDs, subj.UserID) || manage)))
		v.CanEdit = own && x.Status == store.StatusPending
		v.CanCancel = (own || manage) && (open || (x.Status == store.StatusApproved && x.Start.After(today)))
		out = append(out, v)
	}
	return out, nil
}

func (h *handlers) writeRequest(w http.ResponseWriter, r *http.Request, subj authz.Subjects, status int, x store.Request) {
	v, err := h.views(r, subj, []store.Request{x})
	if err != nil {
		h.s.fail(w, r, err)
		return
	}
	WriteJSON(w, status, v[0])
}

func (h *handlers) registerRequests() {
	s, rq := h.s, h.d.Requests
	s.withSubject("GET", Prefix+"/requests", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		q := r.URL.Query()
		from, err := queryDate(r, "from")
		if err != nil {
			s.fail(w, r, err)
			return
		}
		to, err := queryDate(r, "to")
		if err != nil {
			s.fail(w, r, err)
			return
		}
		list, total, err := rq.List(r.Context(), subj, requests.ListFilter{View: q.Get("view"), UserID: q.Get("user"),
			DepartmentID: q.Get("department"), AbsenceTypeID: q.Get("type"), Status: q.Get("status"), From: from, To: to,
			Page: queryInt(r, "page"), PageSize: queryInt(r, "page_size")})
		if err != nil {
			s.fail(w, r, err)
			return
		}
		items, err := h.views(r, subj, list)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
	})
	s.withSubject("POST", Prefix+"/requests", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var b requestBody
		if err := DecodeJSON(r, &b, 0); err != nil {
			s.fail(w, r, err)
			return
		}
		in, err := b.input()
		if err == nil {
			var x store.Request
			if x, err = rq.Create(r.Context(), subj, in); err == nil {
				h.writeRequest(w, r, subj, http.StatusCreated, x)
				return
			}
		}
		s.fail(w, r, err)
	})
	s.withSubject("POST", Prefix+"/requests/preview", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var b requestBody
		if err := DecodeJSON(r, &b, 0); err != nil {
			s.fail(w, r, err)
			return
		}
		in, err := b.input()
		if err != nil {
			s.fail(w, r, err)
			return
		}
		p, err := rq.Preview(r.Context(), subj, in)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		per, remaining := map[string]float64{}, map[string]float64{}
		for y, v := range p.PerYear {
			per[itoa(y)] = days(v)
		}
		for y, v := range p.Remaining {
			remaining[itoa(y)] = days(v)
		}
		approvers := p.Approvers
		if approvers == nil {
			approvers = []string{}
		}
		names, _ := h.d.People.Names(r.Context(), subj.TenantID, approvers)
		approverNames := make([]string, 0, len(approvers))
		for _, a := range approvers {
			approverNames = append(approverNames, names[a])
		}
		WriteJSON(w, http.StatusOK, map[string]any{"days": days(p.Days), "per_year": per, "approvers": approvers,
			"approver_names": approverNames, "overlaps": p.Overlaps, "remaining": remaining, "status": p.Status})
	})
	s.withSubject("GET", Prefix+"/requests/{id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		x, err := rq.Get(r.Context(), subj, r.PathValue("id"))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		h.writeRequest(w, r, subj, http.StatusOK, x)
	})
	s.withSubject("PUT", Prefix+"/requests/{id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var b struct {
			Reason string `json:"reason"`
			Notes  string `json:"notes"`
		}
		if err := DecodeJSON(r, &b, 0); err != nil {
			s.fail(w, r, err)
			return
		}
		x, err := rq.Update(r.Context(), subj, r.PathValue("id"), b.Reason, b.Notes)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		h.writeRequest(w, r, subj, http.StatusOK, x)
	})
	s.withSubject("DELETE", Prefix+"/requests/{id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		if err := rq.Delete(r.Context(), subj, r.PathValue("id")); err != nil {
			s.fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	decide := func(fn func(r *http.Request, subj authz.Subjects, id, notes string) (store.Request, error)) func(http.ResponseWriter, *http.Request, authz.Subjects) {
		return func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
			var b struct {
				Notes string `json:"notes"`
			}
			if r.ContentLength != 0 {
				if err := DecodeJSON(r, &b, 0); err != nil {
					s.fail(w, r, err)
					return
				}
			}
			x, err := fn(r, subj, r.PathValue("id"), b.Notes)
			if err != nil {
				s.fail(w, r, err)
				return
			}
			h.writeRequest(w, r, subj, http.StatusOK, x)
		}
	}
	s.withSubject("POST", Prefix+"/requests/{id}/approve", decide(func(r *http.Request, subj authz.Subjects, id, n string) (store.Request, error) {
		return rq.Approve(r.Context(), subj, id, n)
	}))
	s.withSubject("POST", Prefix+"/requests/{id}/reject", decide(func(r *http.Request, subj authz.Subjects, id, n string) (store.Request, error) {
		return rq.Reject(r.Context(), subj, id, n)
	}))
	s.withSubject("POST", Prefix+"/requests/{id}/revoke", decide(func(r *http.Request, subj authz.Subjects, id, n string) (store.Request, error) {
		return rq.Revoke(r.Context(), subj, id, n)
	}))
	s.withSubject("POST", Prefix+"/requests/{id}/cancel", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		x, err := rq.Cancel(r.Context(), subj, r.PathValue("id"))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		h.writeRequest(w, r, subj, http.StatusOK, x)
	})
	s.withSubject("GET", Prefix+"/requests/{id}/signed-document", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		rc, name, err := rq.SignedDocument(r.Context(), subj, r.PathValue("id"))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		defer func() { _ = rc.Close() }()
		hd := w.Header()
		hd.Set("Content-Type", "application/pdf")
		hd.Set("Content-Disposition", disposition(name))
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, rc)
	})
	s.withSubject("GET", Prefix+"/calendar", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		from, err := queryDate(r, "from")
		if err == nil && from == nil {
			err = apperr.Validation.WithField("from")
		}
		to, err2 := queryDate(r, "to")
		if err == nil && (err2 != nil || to == nil) {
			err = apperr.Validation.WithField("to")
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		members, err := h.d.People.Members(r.Context(), subj.TenantID)
		if err != nil {
			s.fail(w, r, apperr.TemporarilyUnavailable)
			return
		}
		people := make([]requests.CalendarPerson, 0, len(members))
		for _, m := range members {
			people = append(people, requests.CalendarPerson{UserID: m.UserID, Name: m.DisplayName})
		}
		q := r.URL.Query()
		c, err := rq.Calendar(r.Context(), subj, *from, *to, q.Get("department"), q.Get("user"), people)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		type ev struct {
			ID            string  `json:"id"`
			UserID        string  `json:"user_id"`
			AbsenceTypeID string  `json:"absence_type_id"`
			StartDate     string  `json:"start_date"`
			EndDate       string  `json:"end_date"`
			HalfStart     bool    `json:"half_start"`
			HalfEnd       bool    `json:"half_end"`
			Days          float64 `json:"days"`
			Status        string  `json:"status"`
		}
		type hol struct {
			Date string `json:"date"`
			Name string `json:"name"`
		}
		ps := make([]personView, 0, len(c.People))
		for _, p := range c.People {
			ps = append(ps, personView{UserID: p.UserID, Name: p.Name, DepartmentID: p.DepartmentID})
		}
		evs := make([]ev, 0, len(c.Events))
		for _, e := range c.Events {
			evs = append(evs, ev{e.ID, e.UserID, e.AbsenceTypeID, dateString(e.Start), dateString(e.End), e.HalfStart, e.HalfEnd, days(e.Days), e.Status})
		}
		hs := make([]hol, 0, len(c.Holidays))
		for _, x := range c.Holidays {
			hs = append(hs, hol{dateString(x.Date), x.Name})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"people": ps, "events": evs, "holidays": hs})
	})
}

func itoa(n int) string { return time.Date(n, 1, 1, 0, 0, 0, 0, time.UTC).Format("2006") }

// disposition is an attachment Content-Disposition with an ASCII fallback and
// the UTF-8 name (RFC 6266).
func disposition(name string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' || r == '/' {
			return '_'
		}
		return r
	}, name)
	if ascii == "" {
		ascii = "document.pdf"
	}
	return `attachment; filename="` + ascii + `"; filename*=UTF-8''` + urlEscape(name)
}

func urlEscape(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for _, c := range []byte(s) {
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}

// ------------------------------------------------------------------- stats

func (h *handlers) registerStats() {
	s := h.s
	s.withSubject("GET", Prefix+"/stats", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		if err := authz.Require(r.Context(), h.d.Checker, subj, authz.Read); err != nil {
			s.fail(w, r, apperr.Forbidden)
			return
		}
		ctx, t := r.Context(), subj.TenantID
		types, err := h.d.Store.ListAbsenceTypes(ctx, t)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		out := map[string]any{"absence_types": len(types)}
		for key, status := range map[string]string{"pending": store.StatusPending, "awaiting_signing": store.StatusAwaitingSigning,
			"approved": store.StatusApproved, "rejected": store.StatusRejected} {
			_, n, err := h.d.Store.ListRequests(ctx, t, repo.RequestFilter{Statuses: []string{status}, PageSize: 1})
			if err != nil {
				s.fail(w, r, err)
				return
			}
			out[key] = n
		}
		today := leavedays.Date(h.d.Now())
		absent, _, err := h.d.Store.ListRequests(ctx, t, repo.RequestFilter{Statuses: []string{store.StatusApproved, store.StatusAwaitingSigning},
			From: &today, To: &today, All: true})
		if err != nil {
			s.fail(w, r, err)
			return
		}
		ids := make([]string, 0, len(absent))
		for _, x := range absent {
			ids = append(ids, x.UserID)
		}
		names, _ := h.d.People.Names(ctx, t, ids)
		people := make([]personView, 0, len(ids))
		for _, id := range slices.Compact(slices.Sorted(slices.Values(ids))) {
			people = append(people, personView{UserID: id, Name: names[id]})
		}
		out["absent_today"] = people
		WriteJSON(w, http.StatusOK, out)
	})
}

// ------------------------------------------------------------------ backup

func (h *handlers) registerBackup() {
	s, b := h.s, h.d.Backup
	s.withSubject("POST", Prefix+"/backup/export", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		if err := authz.Require(r.Context(), h.d.Checker, subj, authz.Manage); err != nil {
			s.fail(w, r, apperr.Forbidden)
			return
		}
		hd := w.Header()
		hd.Set("Content-Type", "application/gzip")
		hd.Set("Content-Disposition", disposition("hr-"+subj.TenantID+"-"+h.d.Now().UTC().Format("20060102-150405")+".json.gz"))
		w.WriteHeader(http.StatusOK)
		// Past this point a failure can only cut the stream short (the
		// archive then fails its gzip check on import).
		_ = b.Export(r.Context(), subj, w)
	})
	s.withSubject("POST", Prefix+"/backup/import", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		limit := h.d.MaxBackup
		if limit <= 0 {
			limit = 256 << 20
		}
		mode := r.URL.Query().Get("mode")
		if mode == "" {
			mode = "skip"
		}
		res, err := b.Import(r.Context(), subj, mode, http.MaxBytesReader(w, r.Body, limit))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, res)
	})
}
