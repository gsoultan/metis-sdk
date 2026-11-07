package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"time"

	metis "github.com/gsoultan/metis-sdk"
)

// seed sets the sample up, all through the SDK: the four people and their
// groups, and the approval line. Running it again changes nothing, unless the
// line changed, which deploys it as a new version.
func seed(ctx context.Context, cfg config) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	client := metis.NewClient(cfg.MetisURL)
	if err := client.Login(ctx, cfg.AdminUser, cfg.AdminPassword); err != nil {
		return fmt.Errorf("sign in as %s: %w", cfg.AdminUser, err)
	}
	orgs, err := client.ListOrganizations(ctx)
	if err != nil {
		return fmt.Errorf("list organizations: %w", err)
	}
	if len(orgs) == 0 {
		return errors.New("the administrator belongs to no organization")
	}
	org := orgs[0].ID
	projectID, err := findProject(ctx, client, cfg.Project)
	if err != nil {
		return err
	}

	for _, p := range people {
		account := metis.NewAccount{Username: p.Username, Name: p.Name, Email: p.Username + "@example.invalid", Password: cfg.PeoplePassword}
		if p.Group != "" {
			account.Groups = []string{p.Group}
		}
		if _, err := client.EnsureAccount(ctx, org, account); err != nil {
			return fmt.Errorf("set up %s: %w", p.Username, err)
		}
	}
	log.Printf("✓ people: %d accounts, in groups requesters, supervisors, unit-heads and finance", len(people))

	approvals := client.Approvals(projectID)
	current, err := approvals.Line(ctx, lineKey)
	if err != nil && !metis.IsNotFound(err) {
		return fmt.Errorf("read the approval line: %w", err)
	}
	if current != nil && sameLine(*current, expenseLine) {
		log.Printf("✓ approval line %s is up to date (version %d)", lineKey, current.Version)
		return nil
	}
	defined, err := approvals.Define(ctx, expenseLine)
	if err != nil {
		return fmt.Errorf("define the approval line: %w", err)
	}
	log.Printf("✓ approval line %s deployed as version %d", lineKey, defined.Version)
	return nil
}

// sameLine reports whether the deployed line is the one this sample defines.
func sameLine(deployed, want metis.ApprovalLine) bool {
	if deployed.Name != want.Name || deployed.AllowRevise != want.AllowRevise || len(deployed.Steps) != len(want.Steps) || len(deployed.Fields) != len(want.Fields) {
		return false
	}
	for i, s := range want.Steps {
		d := deployed.Steps[i]
		if d.ID != s.ID || d.Name != s.Name || !slices.Equal(d.Users, s.Users) || !slices.Equal(d.Groups, s.Groups) {
			return false
		}
	}
	for i, f := range want.Fields {
		d := deployed.Fields[i]
		if d.ID != f.ID || d.Label != f.Label || (d.Type != f.Type && !(d.Type == "text" && f.Type == "")) {
			return false
		}
	}
	return true
}

// findProject is the project named, or the first one when none is.
func findProject(ctx context.Context, client *metis.Client, name string) (string, error) {
	projects, err := client.ListProjects(ctx)
	if err != nil {
		return "", fmt.Errorf("list projects: %w", err)
	}
	for _, p := range projects {
		if p.Name == name {
			return p.ID, nil
		}
	}
	// No name asked for: a fresh metis has one project, made at its setup.
	if name == "" && len(projects) > 0 {
		return projects[0].ID, nil
	}
	return "", fmt.Errorf("no project named %q; set METIS_PROJECT to one of the %d there are", name, len(projects))
}
