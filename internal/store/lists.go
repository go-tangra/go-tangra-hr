package store

import "github.com/go-tangra/go-tangra/v4/listquery"

// List definitions of the hr data tables (go-tangra
// specs/032-server-side-tables, contracts/sortable-fields.md "hr"). Sort
// fields map to constant SQL expressions only; the "user" field orders by the
// member's display name (hr_members, LEFT JOINed as m; people without a name
// sort last).
var (
	// RequestList pages hr_requests (alias r) in SQL.
	RequestList = listquery.Spec{
		Fields: map[string]listquery.Field{
			"start_date": {Expr: "r.start_date", DefaultDir: listquery.Desc},
			"end_date":   {Expr: "r.end_date", DefaultDir: listquery.Desc},
			"status":     {Expr: "r.status", Text: true, DefaultDir: listquery.Asc},
			"days":       {Expr: "r.days", DefaultDir: listquery.Desc},
			"created_at": {Expr: "r.created_at", DefaultDir: listquery.Desc},
			"user":       {Expr: "NULLIF(m.display_name, '')", Text: true, DefaultDir: listquery.Asc},
		},
		Default: "start_date", TieBreak: "r.id",
	}
	// AllowanceList pages hr_allowances (alias a) in SQL; "type" is the name
	// of the absence type (t) or pool (p) the allowance is for.
	AllowanceList = listquery.Spec{
		Fields: map[string]listquery.Field{
			"year":      {Expr: "a.year", DefaultDir: listquery.Desc},
			"user":      {Expr: "NULLIF(m.display_name, '')", Text: true, DefaultDir: listquery.Asc},
			"type":      {Expr: "COALESCE(t.name, p.name)", Text: true, DefaultDir: listquery.Asc},
			"total":     {Expr: "a.total_days", DefaultDir: listquery.Desc},
			"remaining": {Expr: "(a.total_days + a.carried_over - a.used_days)", DefaultDir: listquery.Desc},
		},
		Default: "year", TieBreak: "a.id", DefaultSize: 50,
	}
	// HolidayList pages the (small) holiday list in memory.
	HolidayList = listquery.Spec{
		Fields: map[string]listquery.Field{
			"date": {Expr: "date", DefaultDir: listquery.Asc},
			"name": {Expr: "name", Text: true, DefaultDir: listquery.Asc},
		},
		Default: "date", TieBreak: "id",
	}
	// AbsenceTypeList pages the (small) absence type list in memory.
	AbsenceTypeList = listquery.Spec{
		Fields: map[string]listquery.Field{
			"sort_order": {Expr: "sort_order", DefaultDir: listquery.Asc},
			"name":       {Expr: "name", Text: true, DefaultDir: listquery.Asc},
		},
		Default: "sort_order", TieBreak: "id",
	}
)

// ListOrDefault fills req's zero values (page, size, sort, order) from the
// spec's defaults, for internal callers that build a Request by hand; a
// request the spec rejects becomes the default first page.
func ListOrDefault(req listquery.Request, s listquery.Spec) listquery.Request {
	out, err := listquery.New(req.Page, req.PageSize, req.Sort, req.Order, s)
	if err != nil {
		out, _ = listquery.New(0, 0, "", "", s)
	}
	return out
}
