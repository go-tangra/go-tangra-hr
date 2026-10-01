package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-tangra/go-tangra/v4/listquery"

	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

// ReasonValidationFailed is the list contract's refusal of a page, page_size,
// sort or order value (go-tangra specs/032-server-side-tables).
const ReasonValidationFailed = "validation_failed"

// listParams are the list contract's query parameters; the request validator
// names them in validation_failed refusals.
var listParams = map[string]bool{"page": true, "page_size": true, "sort": true, "order": true}

// parseList reads the list parameters against spec; an invalid one is
// answered with validation_failed naming the parameter (never its value).
func parseList(w http.ResponseWriter, r *http.Request, spec listquery.Spec) (listquery.Request, bool) {
	req, err := listquery.Parse(r.URL.Query(), spec)
	var le *listquery.Error
	if errors.As(err, &le) {
		failParam(w, le.Param)
		return req, false
	}
	return req, err == nil
}

func failParam(w http.ResponseWriter, param string) {
	WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"reason": ReasonValidationFailed, "detail": map[string]string{"param": param}})
}

// pageHolidays sorts and windows the (small) holiday list in memory.
func pageHolidays(list []store.Holiday, req listquery.Request) listquery.Page[holidayView] {
	listquery.SortSlice(list, req, func(h store.Holiday, field string) any {
		if field == "name" {
			return h.Name
		}
		return h.Date
	}, func(h store.Holiday) string { return h.ID })
	page, total, applied := listquery.Window(list, req)
	out := make([]holidayView, 0, len(page))
	for _, x := range page {
		out = append(out, viewHoliday(x))
	}
	return listquery.NewPage(out, total, applied)
}

// pageTypes sorts and windows the (small) absence type list in memory.
func pageTypes(list []store.AbsenceType, req listquery.Request) listquery.Page[absenceTypeView] {
	listquery.SortSlice(list, req, func(t store.AbsenceType, field string) any {
		if field == "name" {
			return t.Name
		}
		return t.SortOrder
	}, func(t store.AbsenceType) string { return t.ID })
	page, total, applied := listquery.Window(list, req)
	out := make([]absenceTypeView, 0, len(page))
	for _, t := range page {
		out = append(out, viewType(t))
	}
	return listquery.NewPage(out, total, applied)
}
