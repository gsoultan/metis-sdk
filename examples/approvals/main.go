// Command approvals is a web sample: an expense approval run by metis, with a
// Go backend that talks to metis only through metis-sdk and a React UI (in
// web/) that shows each request's approval line.
//
// The flow is: someone submits a request, a supervisor approves it, then a
// unit head, then finance, and it is paid and completed. On the way it passes
// one of each kind of task metis runs — a script task, a service task that
// calls an API, a business rule task, user tasks, and a service task a worker
// serves. process.bpmn is the whole definition.
//
//	go run ./examples/approvals seed     # once: people, groups, decision, process
//	go run ./examples/approvals          # the web app on http://localhost:8090
//
// run.sh does both, and builds the UI first. README.md has the details.
package main

import (
	"cmp"
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// config is everything the sample reads from its environment.
type config struct {
	// MetisURL is where metis answers, as scripts/dev.sh starts it.
	MetisURL string
	// AdminUser and AdminPassword are an administrator's sign-in. The sample
	// uses it to set itself up and to read requests back; each approval is
	// made by the person it was offered to, signed in as them.
	AdminUser, AdminPassword string
	// Project is the name of the metis project the process lives in.
	Project string
	// PeoplePassword is the password seed gives the four sample people.
	PeoplePassword string
	// Addr is where this app listens.
	Addr string
	// PublicURL is this app's address as metis reaches it. The budget check
	// is a call from metis to this app, so metis has to be able to reach it.
	PublicURL string
	// WebDir holds the built React app.
	WebDir string
}

func loadConfig() config {
	return config{
		MetisURL:       cmp.Or(os.Getenv("METIS_URL"), "http://localhost:8273"),
		AdminUser:      cmp.Or(os.Getenv("METIS_USERNAME"), "admin"),
		AdminPassword:  os.Getenv("METIS_PASSWORD"),
		Project:        cmp.Or(os.Getenv("METIS_PROJECT"), "Default Project"),
		PeoplePassword: cmp.Or(os.Getenv("APPROVALS_PEOPLE_PASSWORD"), "sample-password"),
		Addr:           cmp.Or(os.Getenv("APPROVALS_ADDR"), ":8090"),
		PublicURL:      strings.TrimRight(cmp.Or(os.Getenv("APPROVALS_PUBLIC_URL"), "http://localhost:8090"), "/"),
		WebDir:         cmp.Or(os.Getenv("APPROVALS_WEB_DIR"), "examples/approvals/web/dist"),
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := loadConfig()
	if cfg.AdminPassword == "" {
		log.Fatal("approvals: set METIS_PASSWORD to the metis administrator's password")
	}

	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	var err error
	switch command {
	case "seed":
		err = seed(ctx, cfg)
	case "serve":
		err = serve(ctx, cfg)
	default:
		err = fmt.Errorf("unknown command %q: use seed or serve", command)
	}
	if err != nil {
		log.Fatal("approvals: ", err)
	}
}
