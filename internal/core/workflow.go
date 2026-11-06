package core

// Workflows (M8.1, ADR 0015): the states a document of a DocType moves through and the
// transitions between them, written by the app in the DocType file ([workflow]).
//
// A workflow is a state machine over the docstatus: every state has a docstatus (0 draft, 1
// submitted, 2 cancelled), and a transition is the only way to change the state of a document of a
// DocType that has a workflow. This file is data and pure functions: the loader builds the types
// and DocType.Validate checks them; whether a caller may take a transition is decided by
// TransitionAccess, and internal/db runs it.
//
// The first state is the initial state of every new document. Conditions on a transition are
// written in Go (the BeforeTransition hook, see hooks.go), not in the file.

// MaxNameLen is the longest name of a state or an action, in characters.
const MaxNameLen = 64

// Workflow is the workflow of a DocType ([workflow]): its states, the first of which is the
// initial state, and the transitions between them.
type Workflow struct {
	// States are in declaration order. The first is the initial state.
	States []WorkflowState
	// Transitions are in declaration order.
	Transitions []Transition
}

// WorkflowState is one state of a Workflow.
type WorkflowState struct {
	// Name is unique in the workflow, 1 to MaxNameLen characters. It is stored in the
	// workflow_state column of the documents.
	Name string
	// DocStatus of a document in this state. 1 and 2 only on a submittable DocType.
	DocStatus DocStatus
	// Style is the tone of the state's badge in the desk.
	Style StateStyle
	// EditRoles may edit (update) a draft in this state, in addition to the right to write.
	// Empty: the DocType's write rule alone decides. Only on a state with docstatus 0.
	EditRoles []string
}

// StateStyle is the tone of a state's badge. Metadata for the desk only. The zero value is
// StyleNeutral.
type StateStyle int

const (
	// StyleNeutral has no particular meaning (the default).
	StyleNeutral StateStyle = iota
	// StyleSuccess is done or approved.
	StyleSuccess
	// StyleWarning is waiting or needs attention.
	StyleWarning
	// StyleDanger is rejected or cancelled.
	StyleDanger
	// StyleInfo is informational.
	StyleInfo
)

var allStyles = [...]StateStyle{StyleNeutral, StyleSuccess, StyleWarning, StyleDanger, StyleInfo}

// Name is the key used in the DocType file and in the API: neutral, success, warning, danger or
// info.
func (s StateStyle) Name() string {
	switch s {
	case StyleSuccess:
		return "success"
	case StyleWarning:
		return "warning"
	case StyleDanger:
		return "danger"
	case StyleInfo:
		return "info"
	}
	return "neutral"
}

func (s StateStyle) String() string { return s.Name() }

// StateStyleFromName is the style for a key of the DocType file; false for an unknown one.
func StateStyleFromName(name string) (StateStyle, bool) {
	for _, s := range allStyles {
		if s.Name() == name {
			return s, true
		}
	}
	return StyleNeutral, false
}

// Transition is one transition of a Workflow: an action that moves a document from one state to
// another.
type Transition struct {
	// Action is the label of the button: 1 to MaxNameLen characters, unique per From state.
	Action string
	// From is the name of the state the document must be in.
	From string
	// To is the name of the state the document is in afterwards.
	To string
	// Roles may take it: at least one. The caller also needs the underlying right (see
	// Move.Right); the roles never grant more than the DocType's rules do.
	Roles []string
}

// Move is what a transition does to the docstatus, which decides the pipeline it runs and the
// right it needs. The other moves (anything from a cancelled state, or a drop back from submitted
// to draft) are refused when the DocType is loaded.
type Move int

const (
	// MoveEdit is draft to draft (0 to 0): an update of the whole document. Needs the write right.
	MoveEdit Move = iota + 1
	// MoveSubmit is draft to submitted (0 to 1): the submit pipeline. Needs the submit right.
	MoveSubmit
	// MoveEditSubmitted is submitted to submitted (1 to 1): only allow_on_submit fields may
	// change. Needs the write right.
	MoveEditSubmitted
	// MoveCancel is submitted to cancelled (1 to 2): the cancel pipeline. Needs the cancel right.
	MoveCancel
)

// MoveOf is the move from docstatus from to docstatus to; false for one a workflow does not allow.
func MoveOf(from, to DocStatus) (Move, bool) {
	switch {
	case from == Draft && to == Draft:
		return MoveEdit, true
	case from == Draft && to == Submitted:
		return MoveSubmit, true
	case from == Submitted && to == Submitted:
		return MoveEditSubmitted, true
	case from == Submitted && to == Cancelled:
		return MoveCancel, true
	}
	return 0, false
}

// Right is the right the underlying operation needs: write, submit or cancel.
func (m Move) Right() Right {
	switch m {
	case MoveSubmit:
		return RightSubmit
	case MoveCancel:
		return RightCancel
	}
	return RightWrite
}

// Initial is the initial state: the first one. nil only for a workflow without states, which
// loading refuses.
func (w *Workflow) Initial() *WorkflowState {
	if len(w.States) == 0 {
		return nil
	}
	return &w.States[0]
}

// State is the state with this name, or nil.
func (w *Workflow) State(name string) *WorkflowState {
	for i := range w.States {
		if w.States[i].Name == name {
			return &w.States[i]
		}
	}
	return nil
}

// FirstStateWith is the first state with this docstatus, or nil. Documents that exist when a
// workflow is added (migrate) get it.
func (w *Workflow) FirstStateWith(docstatus DocStatus) *WorkflowState {
	for i := range w.States {
		if w.States[i].DocStatus == docstatus {
			return &w.States[i]
		}
	}
	return nil
}

// Transition is the transition that action names from the state from, or nil.
func (w *Workflow) Transition(from, action string) *Transition {
	for i := range w.Transitions {
		if w.Transitions[i].From == from && w.Transitions[i].Action == action {
			return &w.Transitions[i]
		}
	}
	return nil
}

// TransitionsFrom are the transitions that leave the state from, in declaration order.
func (w *Workflow) TransitionsFrom(from string) []*Transition {
	var out []*Transition
	for i := range w.Transitions {
		if w.Transitions[i].From == from {
			out = append(out, &w.Transitions[i])
		}
	}
	return out
}

// MoveOf is what transition does to the docstatus; false when it names a state that does not
// exist or makes a move a workflow does not allow (loading refuses both).
func (w *Workflow) MoveOf(t *Transition) (Move, bool) {
	from := w.State(t.From)
	to := w.State(t.To)
	if from == nil || to == nil {
		return 0, false
	}
	return MoveOf(from.DocStatus, to.DocStatus)
}
