// Package routing computes who approves a leave request over the department
// tree (spec FR-020, US5, research D3): the manager of the requester's
// department; when there is none, or it is the requester, or the manager is no
// longer an active member, the nearest ancestor department's manager; with no
// such manager, the HR administrators. Nobody approves their own request.
//
// The package is pure: callers load the tree and the member states.
package routing

import (
	"errors"
	"slices"
)

// Errors of tree changes.
var (
	ErrCycle   = errors.New("routing: department would become its own ancestor")
	ErrDepth   = errors.New("routing: department tree too deep")
	ErrUnknown = errors.New("routing: unknown department")
)

// Department is one node of the tree.
type Department struct {
	ID        string
	ParentID  string // "" = top level
	ManagerID string // "" = no manager
}

// Tree is a tenant's departments and member assignments.
type Tree struct {
	Departments map[string]Department
	MemberOf    map[string]string // user id → department id
	Active      func(userID string) bool
}

func (t Tree) active(userID string) bool { return t.Active == nil || t.Active(userID) }

// chain returns the department of id and its ancestors, nearest first,
// stopping on a missing parent or a cycle.
func (t Tree) chain(id string) []Department {
	var out []Department
	seen := map[string]bool{}
	for id != "" && !seen[id] {
		d, ok := t.Departments[id]
		if !ok {
			break
		}
		seen[id] = true
		out = append(out, d)
		id = d.ParentID
	}
	return out
}

// Approvers returns the routed approvers of a request by requester.
func (t Tree) Approvers(requester string, hrAdmins []string) []string {
	for _, d := range t.chain(t.MemberOf[requester]) {
		if d.ManagerID != "" && d.ManagerID != requester && t.active(d.ManagerID) {
			return []string{d.ManagerID}
		}
	}
	out := make([]string, 0, len(hrAdmins))
	for _, a := range hrAdmins {
		if a != requester && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	slices.Sort(out)
	return out
}

// Managers returns every manager above userID (their department and its
// ancestors), excluding userID: they may read the person's requests and
// balance (FR-043).
func (t Tree) Managers(userID string) []string {
	var out []string
	for _, d := range t.chain(t.MemberOf[userID]) {
		if d.ManagerID != "" && d.ManagerID != userID && !slices.Contains(out, d.ManagerID) {
			out = append(out, d.ManagerID)
		}
	}
	return out
}

// Managed returns the departments userID manages and all their descendants.
func (t Tree) Managed(userID string) []string {
	var out []string
	for id := range t.Departments {
		for _, d := range t.chain(id) {
			if d.ManagerID == userID {
				out = append(out, id)
				break
			}
		}
	}
	slices.Sort(out)
	return out
}

// Depth is the number of levels from id up to the top (a top-level
// department has depth 1).
func (t Tree) Depth(id string) int { return len(t.chain(id)) }

// height is the number of levels of the subtree rooted at id.
func (t Tree) height(id string) int {
	h := 1
	for cid, d := range t.Departments {
		if d.ParentID == id && cid != id {
			if ch := 1 + t.height(cid); ch > h {
				h = ch
			}
		}
	}
	return h
}

// CheckParent validates placing department id (new: id not yet in the tree)
// under parent: parent exists, no cycle, depth ≤ maxDepth.
func (t Tree) CheckParent(id, parent string, maxDepth int) error {
	if parent == "" {
		if _, ok := t.Departments[id]; ok && t.height(id) > maxDepth {
			return ErrDepth
		}
		return nil
	}
	if _, ok := t.Departments[parent]; !ok {
		return ErrUnknown
	}
	for _, d := range t.chain(parent) {
		if d.ID == id {
			return ErrCycle
		}
	}
	h := 1
	if _, ok := t.Departments[id]; ok {
		h = t.height(id)
	}
	if t.Depth(parent)+h > maxDepth {
		return ErrDepth
	}
	return nil
}
