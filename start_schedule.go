package metis

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"
)

// StartScheduleStatus is where a process's scheduled starts stand: whether
// its start step has a schedule, whether that is paused, and when the next
// run is due.
type StartScheduleStatus struct {
	ProcessKey string `json:"process_key"`
	// Scheduled says a version that is live, or arranged to become live,
	// starts on a schedule. False for a process that only starts another way.
	Scheduled bool `json:"scheduled"`
	// Paused says nothing starts on the schedule until it is resumed, and
	// PausedAt since when.
	Paused   bool       `json:"paused"`
	PausedAt *time.Time `json:"paused_at,omitempty"`
	// NextRunAt is when the next run starts, in UTC. Nil while paused and
	// when nothing is scheduled to come.
	NextRunAt *time.Time `json:"next_run_at,omitempty"`
	// Failures counts the attempts in a row that could not start a run, and
	// LastError says why the latest could not. The server keeps the run owed
	// and tries again, so a count above zero is a schedule to look at, not a
	// run that was lost.
	Failures  int    `json:"failures"`
	LastError string `json:"last_error,omitempty"`
}

// GetStartSchedule says whether a process starts on a schedule, whether that
// is paused, and when its next run is due.
//
// The schedule is the live version's, so the next run follows a promotion, a
// rollback or a cutover arranged with [Client.ScheduleVersion]. A process
// that does not exist is refused, which [IsNotFound] reports; one with no
// schedule answers with Scheduled false.
//
//	status, err := client.GetStartSchedule(ctx, projectID, "nightly-report")
//	if err == nil && status.NextRunAt != nil {
//		fmt.Println("next run at", status.NextRunAt.Local())
//	}
func (c *Client) GetStartSchedule(ctx context.Context, projectID, processKey string) (*StartScheduleStatus, error) {
	if projectID == "" || processKey == "" {
		return nil, errors.New("metis: GetStartSchedule needs a project ID and a process key")
	}
	var out struct {
		Schedule StartScheduleStatus `json:"schedule"`
	}
	path := "/api/v1/definitions/start-schedule?" + url.Values{"project_id": {projectID}, "key": {processKey}}.Encode()
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out.Schedule, nil
}

// PauseStartSchedule stops a process starting on its schedule until
// [Client.ResumeStartSchedule]. Starting it by hand, by message or by signal
// still works, and runs already started carry on.
//
// The pause belongs to the process, not to a version, so it holds across
// deploys, promotions and cutovers. Scheduled times that pass while paused
// start nothing, then or later. Pausing a process that is already paused
// keeps the first pause; pausing one with no schedule is refused. It needs
// the role that may promote a version.
func (c *Client) PauseStartSchedule(ctx context.Context, projectID, processKey string) (*StartScheduleStatus, error) {
	return c.setStartSchedulePaused(ctx, "PauseStartSchedule", "pause", projectID, processKey)
}

// ResumeStartSchedule lets a paused process start on its schedule again. The
// first run is the next scheduled time: nothing is started for the times that
// passed while it was paused. Resuming a process that is not paused changes
// nothing.
func (c *Client) ResumeStartSchedule(ctx context.Context, projectID, processKey string) (*StartScheduleStatus, error) {
	return c.setStartSchedulePaused(ctx, "ResumeStartSchedule", "resume", projectID, processKey)
}

func (c *Client) setStartSchedulePaused(ctx context.Context, call, action, projectID, processKey string) (*StartScheduleStatus, error) {
	if projectID == "" || processKey == "" {
		return nil, errors.New("metis: " + call + " needs a project ID and a process key")
	}
	var out struct {
		Schedule StartScheduleStatus `json:"schedule"`
	}
	err := c.do(ctx, http.MethodPost, "/api/v1/definitions/start-schedule/"+action, map[string]any{
		"project_id": projectID,
		"key":        processKey,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out.Schedule, nil
}
