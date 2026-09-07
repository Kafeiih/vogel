package main

import (
	"context"

	"github.com/kafeiih/vogel/workflow"
)

// ApprovalDefinitionName is the registered name of the definition returned by
// ApprovalDefinition. Call sites reference this constant instead of the string
// literal so that renaming the process cannot silently desynchronize the
// definition from the handlers that open cases against it.
const ApprovalDefinitionName = "document-approval"

// ApprovalDefinition describes the four-node approval process this example
// runs every Document through:
//
//	draft --submit--> in_review --approve(guard: assigned)--> approved
//	                   in_review --reject--> rejected
//	                   in_review --return--> draft
func ApprovalDefinition() workflow.Definition {
	return workflow.Definition{
		Name:    ApprovalDefinitionName,
		Version: 1,
		Nodes: []workflow.Node{
			{ID: "draft", Start: true, Eligible: workflow.ByPosition("author")},
			{ID: "in_review", Eligible: workflow.ByPositionInUnit("reviewer", "")},
			{ID: "approved", Terminal: true},
			{ID: "rejected", Terminal: true},
		},
		Transitions: []workflow.Transition{
			{From: "draft", To: "in_review", Action: "submit"},
			{From: "in_review", To: "approved", Action: "approve", Guard: "assigned"},
			{From: "in_review", To: "rejected", Action: "reject"},
			{From: "in_review", To: "draft", Action: "return"},
		},
	}
}

// AssignedGuard is registered on the workflow engine under the name
// "assigned" (see the "approve" transition in ApprovalDefinition) and
// requires a case to have been claimed, via Engine.Claim, before it can be
// approved.
//
// A GuardFunc only ever sees a workflow.Case, which deliberately carries no
// domain data of its own -- only Domain and ExternalID, a bare reference to
// the object the case concerns (see the doc comment on workflow.Case). A
// guard that needs to read the Document itself -- for example, to reject an
// invoice above some amount -- must look it up through the consumer's own
// store using c.ExternalID, exactly the way DocumentHandler does; the
// workflow engine never reaches into DocumentStore on a guard's behalf, and
// this guard does not need to because AssignedTo already lives on Case.
func AssignedGuard(_ context.Context, c workflow.Case) (bool, error) {
	return c.AssignedTo != "", nil
}
