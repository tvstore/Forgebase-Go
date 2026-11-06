package core

import (
	"slices"
	"testing"
)

func wfState(name string, docstatus DocStatus) WorkflowState {
	return WorkflowState{Name: name, DocStatus: docstatus, EditRoles: []string{}}
}

func wfTransition(action, from, to string) Transition {
	return Transition{Action: action, From: from, To: to, Roles: []string{"Clerk"}}
}

func testWorkflow() *Workflow {
	return &Workflow{
		States: []WorkflowState{
			wfState("Draft", Draft),
			wfState("Approved", Submitted),
			wfState("Cancelled", Cancelled),
		},
		Transitions: []Transition{
			wfTransition("Approve", "Draft", "Approved"),
			wfTransition("Cancel", "Approved", "Cancelled"),
			wfTransition("Reopen", "Draft", "Draft"),
		},
	}
}

// Rust: workflow.rs::lookups
func TestWorkflowLookups(t *testing.T) {
	w := testWorkflow()
	if s := w.Initial(); s == nil || s.Name != "Draft" {
		t.Errorf("initial = %v", s)
	}
	if s := w.State("Approved"); s == nil || s.DocStatus != Submitted {
		t.Errorf("state(Approved) = %v", s)
	}
	if w.State("approved") != nil {
		t.Error("names are case-sensitive")
	}
	if s := w.FirstStateWith(Cancelled); s == nil || s.Name != "Cancelled" {
		t.Errorf("first_state_with = %v", s)
	}
	if tr := w.Transition("Draft", "Approve"); tr == nil || tr.To != "Approved" {
		t.Errorf("transition = %v", tr)
	}
	if w.Transition("Approved", "Approve") != nil {
		t.Error("transition from the wrong state")
	}
	var fromDraft []string
	for _, tr := range w.TransitionsFrom("Draft") {
		fromDraft = append(fromDraft, tr.Action)
	}
	if !slices.Equal(fromDraft, []string{"Approve", "Reopen"}) {
		t.Errorf("from draft = %v", fromDraft)
	}
	if (&Workflow{}).Initial() != nil {
		t.Error("initial of an empty workflow")
	}
}

// Rust: workflow.rs::moves_and_their_rights
func TestMovesAndTheirRights(t *testing.T) {
	w := testWorkflow()
	var moves []Move
	for i := range w.Transitions {
		m, ok := w.MoveOf(&w.Transitions[i])
		if !ok {
			t.Fatalf("transition %d has no move", i)
		}
		moves = append(moves, m)
	}
	sameValue(t, moves, []Move{MoveSubmit, MoveCancel, MoveEdit}, "moves")
	if MoveEdit.Right() != RightWrite || MoveEditSubmitted.Right() != RightWrite ||
		MoveSubmit.Right() != RightSubmit || MoveCancel.Right() != RightCancel {
		t.Error("move rights")
	}
	// Everything else is not a move.
	for _, c := range [][2]DocStatus{{Draft, Cancelled}, {Submitted, Draft}, {Cancelled, Draft}, {Cancelled, Submitted}, {Cancelled, Cancelled}} {
		if _, ok := MoveOf(c[0], c[1]); ok {
			t.Errorf("%s to %s is a move", c[0], c[1])
		}
	}
	x := wfTransition("X", "Draft", "Nowhere")
	if _, ok := w.MoveOf(&x); ok {
		t.Error("a transition to an unknown state has a move")
	}
}

// Rust: workflow.rs::style_names_round_trip
func TestStyleNamesRoundTrip(t *testing.T) {
	for _, style := range allStyles {
		got, ok := StateStyleFromName(style.Name())
		if !ok || got != style {
			t.Errorf("%s", style)
		}
	}
	if _, ok := StateStyleFromName("red"); ok {
		t.Error("red is a style")
	}
	var zero StateStyle
	if zero != StyleNeutral {
		t.Error("default style")
	}
}

func TestDocStatusConversions(t *testing.T) {
	for _, c := range []struct {
		v    int16
		s    DocStatus
		name string
	}{{0, Draft, "draft"}, {1, Submitted, "submitted"}, {2, Cancelled, "cancelled"}} {
		got, ok := DocStatusFromInt16(c.v)
		if !ok || got != c.s || got.Int16() != c.v || got.String() != c.name {
			t.Errorf("%d: %v %v %s", c.v, got, ok, got)
		}
	}
	for _, v := range []int16{-1, 3, 100} {
		if _, ok := DocStatusFromInt16(v); ok {
			t.Errorf("%d accepted", v)
		}
	}
}
