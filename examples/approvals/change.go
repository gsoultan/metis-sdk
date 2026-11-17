package main

import (
	"context"
	"fmt"
	"log"
	"time"

	metis "github.com/gsoultan/metis-sdk"
)

// change is the scenario of an approval line that changes while requests are
// running on it: Budi, the supervisor, leaves, and his step goes to Tono.
//
//  1. The changed line is deployed as a new version. New requests follow it.
//  2. Requests already running keep the line they were submitted on, so the
//     ones waiting for Budi would wait for somebody who is gone.
//  3. Behind lists every request still on an older version; the sample
//     chooses, request by request, to move the ones waiting for the
//     supervisor and keep the rest on the line they started on.
//  4. Move puts the chosen requests on the new version, where the supervisor
//     step is Tono's. A request metis cannot move safely stays, with why.
func change(ctx context.Context, cfg config) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	admin := metis.NewClient(cfg.MetisURL)
	if err := admin.Login(ctx, cfg.AdminUser, cfg.AdminPassword); err != nil {
		return fmt.Errorf("sign in as %s: %w", cfg.AdminUser, err)
	}
	projectID, err := findProject(ctx, admin, cfg.Project)
	if err != nil {
		return err
	}
	approvals := admin.Project(projectID).Approvals()

	current, err := approvals.Line(ctx, lineKey)
	if err != nil {
		return fmt.Errorf("read the approval line (run seed first): %w", err)
	}
	if !sameLine(*current, changedLine) {
		defined, err := approvals.Define(ctx, changedLine)
		if err != nil {
			return fmt.Errorf("deploy the changed line: %w", err)
		}
		log.Printf("✓ line changed: version %d gives the supervisor step to Tono", defined.Version)
	} else {
		log.Printf("✓ the line already gives the supervisor step to Tono (version %d)", current.Version)
	}

	behind, err := approvals.Behind(ctx, lineKey)
	if err != nil {
		return fmt.Errorf("list the requests on older versions: %w", err)
	}
	var move []string
	for _, older := range behind.Behind {
		for i := range older.Requests {
			req := &older.Requests[i]
			step := req.Waiting()
			switch {
			case step != nil && step.ID == "supervisor":
				log.Printf("  version %d: %q waits for the supervisor → move it to Tono", older.Version, title(req))
				move = append(move, req.ID)
			case step != nil:
				log.Printf("  version %d: %q is at %s → keep it on its line", older.Version, title(req), step.Name)
			default:
				log.Printf("  version %d: %q is %s → keep it on its line", older.Version, title(req), req.Status)
			}
		}
		if !older.Plan.CanApply() {
			log.Printf("  version %d cannot be moved as a whole: %v", older.Version, older.Plan.Refusals)
		}
	}
	if len(move) == 0 {
		log.Printf("✓ nothing waits for the supervisor on an older line; nothing to move")
		return nil
	}
	res, err := approvals.Move(ctx, lineKey, move...)
	if err != nil {
		return fmt.Errorf("move the requests: %w", err)
	}
	log.Printf("✓ moved %d request(s) to version %d: Tono now has them", res.Moved, behind.LiveVersion)
	for _, left := range res.Left {
		log.Printf("  left %s where it was: %s", left.RequestID, left.Why)
	}
	return nil
}

func title(req *metis.ApprovalRequest) string {
	if t, ok := req.Variables["title"].(string); ok && t != "" {
		return t
	}
	return req.ID
}
