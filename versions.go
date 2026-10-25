package metis

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"
)

// ListLiveVersions returns, for each process key in the project, the version
// that new instances start on. A key whose newest deployment has not been
// promoted keeps starting on the older one, so this, not the newest
// [Definition], is what [Client.StartProcess] runs.
//
//	live, err := client.ListLiveVersions(ctx, projectID)
//	fmt.Println("orders start on version", live["order"])
func (c *Client) ListLiveVersions(ctx context.Context, projectID string) (map[string]int, error) {
	if projectID == "" {
		return nil, errors.New("metis: ListLiveVersions needs a project ID")
	}
	var out struct {
		Live map[string]int `json:"live"`
	}
	path := "/api/v1/definitions/live-versions?" + url.Values{"project_id": {projectID}}.Encode()
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	if out.Live == nil {
		out.Live = map[string]int{}
	}
	return out.Live, nil
}

// PromoteVersion makes one deployed version of a process the one new
// instances start on. Instances already running keep the version they started
// with; moving them is a migration, not a promotion.
//
// Promoting the version that is already live succeeds, so a deploy pipeline
// can call it on every run. Promoting an older version is a rollback, and the
// server then also cancels every cutover arranged with [Client.ScheduleVersion]:
// they were arranged from the version being rolled back from. A version that
// was never deployed is refused.
func (c *Client) PromoteVersion(ctx context.Context, projectID, processKey string, version int) error {
	if projectID == "" || processKey == "" || version < 1 {
		return errors.New("metis: PromoteVersion needs a project ID, a process key and a version from 1")
	}
	return c.do(ctx, http.MethodPost, "/api/v1/definitions/versions/promote", map[string]any{
		"project_id": projectID,
		"key":        processKey,
		"version":    version,
	}, nil)
}

// VersionStatus is one deployed version of a process and the work it holds.
type VersionStatus struct {
	// ID is the version's [Definition.ID].
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Name      string    `json:"name"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at,omitzero"`
	// Live is whether new instances start on this version.
	Live bool `json:"live"`
	// RunningInstances still execute this version; TotalInstances ever did.
	RunningInstances int64 `json:"running_instances"`
	TotalInstances   int64 `json:"total_instances"`
	// ScheduledFor is when a cutover arranged for later makes this version
	// live, and ScheduledRelease names that cutover, to cancel it. Both are
	// zero when none is arranged.
	ScheduledFor     time.Time  `json:"scheduled_for,omitzero"`
	ScheduledRelease ReleaseRef `json:"-"`
}

// UnmarshalJSON reads the cutover from scheduled_release_id; [Client.ListVersions]
// gives it its project.
func (v *VersionStatus) UnmarshalJSON(data []byte) error {
	type plain VersionStatus
	var wire struct {
		plain
		ScheduledReleaseID string `json:"scheduled_release_id"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*v = VersionStatus(wire.plain)
	v.ScheduledRelease.ID = wire.ScheduledReleaseID
	return nil
}

// Draining reports whether the version is no longer live but still has
// instances running on it — the versions a migration would move work off.
func (v VersionStatus) Draining() bool { return !v.Live && v.RunningInstances > 0 }

// ListVersions returns every deployed version of one process, with which is
// live, how many instances each still runs, and any cutover arranged.
func (c *Client) ListVersions(ctx context.Context, projectID, processKey string) ([]VersionStatus, error) {
	if projectID == "" || processKey == "" {
		return nil, errors.New("metis: ListVersions needs a project ID and a process key")
	}
	var out struct {
		Versions []VersionStatus `json:"versions"`
	}
	path := "/api/v1/definitions/versions?" + url.Values{"project_id": {projectID}, "key": {processKey}}.Encode()
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	for i := range out.Versions {
		if !out.Versions[i].ScheduledRelease.IsZero() {
			out.Versions[i].ScheduledRelease.Project = ProjectRef{ID: projectID}
		}
	}
	return out.Versions, nil
}

// ScheduleVersion arranges for a deployed version to become live at a later
// time — a cutover at midnight, or when a regulation takes effect. Nothing on
// the server has to run at that moment: the live version is worked out from
// the clock on every start, so a cutover cannot be missed by a restart.
//
// A time that is not in the future is refused; use [Client.PromoteVersion] to
// change the live version now.
func (c *Client) ScheduleVersion(ctx context.Context, projectID, processKey string, version int, at time.Time) error {
	if projectID == "" || processKey == "" || version < 1 {
		return errors.New("metis: ScheduleVersion needs a project ID, a process key and a version from 1")
	}
	if at.IsZero() {
		return errors.New("metis: ScheduleVersion needs a time for the cutover")
	}
	return c.do(ctx, http.MethodPost, "/api/v1/definitions/versions/schedule", map[string]any{
		"project_id":  projectID,
		"key":         processKey,
		"version":     version,
		"activate_at": at.UTC().Format(time.RFC3339),
	}, nil)
}

// CancelScheduledVersion drops a cutover that has not happened yet, named by
// [VersionStatus.ScheduledRelease] — the cutover, not the version, since one
// version can be scheduled more than once.
func (c *Client) CancelScheduledVersion(ctx context.Context, projectID, releaseID string) error {
	if projectID == "" || releaseID == "" {
		return errors.New("metis: CancelScheduledVersion needs a project ID and the cutover's release ID")
	}
	return c.do(ctx, http.MethodPost, "/api/v1/definitions/versions/schedule/cancel", map[string]any{
		"project_id": projectID,
		"release_id": releaseID,
	}, nil)
}
