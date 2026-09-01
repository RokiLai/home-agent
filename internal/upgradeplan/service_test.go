package upgradeplan

import (
	"testing"

	"homeagent/internal/command"
)

func TestUpgradePlanServiceLifecycle(t *testing.T) {
	svc := NewService()

	// 1. Create plan
	req := CreatePlanRequest{
		DeviceID:       "mac-node-1",
		RequestedBy:    command.Actor{Type: "user", ID: "u-admin"},
		IdempotencyKey: "idem-key-1",
		TargetVersion:  "v0.7.0",
		Snapshot: PlanSnapshot{
			TargetVersion: "v0.7.0",
			RequestDigest: "digest-1",
		},
	}

	plan, created, err := svc.CreatePlan(req)
	if err != nil {
		t.Fatalf("CreatePlan failed: %v", err)
	}
	if !created || plan.Stage != StageTargetPending || plan.PlanID == "" {
		t.Fatalf("unexpected plan after create: %+v", plan)
	}

	// 2. Idempotent create
	plan2, created2, err := svc.CreatePlan(req)
	if err != nil {
		t.Fatalf("idempotent CreatePlan failed: %v", err)
	}
	if created2 || plan2.PlanID != plan.PlanID {
		t.Fatalf("expected existing plan returned, got created=%v id=%s", created2, plan2.PlanID)
	}

	// 3. Idempotency conflict with different request digest
	conflictReq := req
	conflictReq.Snapshot.RequestDigest = "different-digest"
	if _, _, err := svc.CreatePlan(conflictReq); err != ErrIdempotencyConflict {
		t.Fatalf("expected ErrIdempotencyConflict, got %v", err)
	}

	// 4. Concurrency conflict on same device
	otherReq := CreatePlanRequest{
		DeviceID:      "mac-node-1",
		RequestedBy:   command.Actor{Type: "user", ID: "u-admin"},
		TargetVersion: "v0.8.0",
	}
	if _, _, err := svc.CreatePlan(otherReq); err != ErrPlanInProgress {
		t.Fatalf("expected ErrPlanInProgress, got %v", err)
	}

	// 5. Transition to Succeeded
	updated, err := svc.TransitionStage(plan.PlanID, plan.Revision, StageSucceeded, "")
	if err != nil {
		t.Fatalf("TransitionStage failed: %v", err)
	}
	if updated.Stage != StageSucceeded || !updated.Stage.Terminal() {
		t.Fatalf("unexpected stage after transition: %s", updated.Stage)
	}

	// 6. Now another plan can be created for the same device
	plan3, created3, err := svc.CreatePlan(otherReq)
	if err != nil {
		t.Fatalf("CreatePlan after terminal failed: %v", err)
	}
	if !created3 || plan3.PlanID == plan.PlanID {
		t.Fatalf("expected new plan created, got %+v", plan3)
	}

	// 7. Query active plan by device
	active, err := svc.GetActivePlanByDevice("mac-node-1")
	if err != nil {
		t.Fatalf("GetActivePlanByDevice failed: %v", err)
	}
	if active.PlanID != plan3.PlanID {
		t.Fatalf("expected active plan %s, got %s", plan3.PlanID, active.PlanID)
	}

	// 8. List filter
	list, err := svc.ListPlans(Filter{DeviceID: "mac-node-1"})
	if err != nil {
		t.Fatalf("ListPlans failed: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 plans in list, got %d", len(list))
	}
}
