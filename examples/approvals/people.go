package main

// person is one of the sample's people. Each is a real metis user, made
// by seed, in the group its approval step is offered to.
type person struct {
	Username string `json:"username"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	// Group is the metis group the person is in, and so the approval step
	// they are offered. The requester's group is offered the request back when an approver
	// asks for it to be revised.
	Group string `json:"group,omitempty"`
}

var people = []person{
	{Username: "rina", Name: "Rina", Role: "requester", Group: "requesters"},
	{Username: "budi", Name: "Budi", Role: "supervisor", Group: "supervisors"},
	{Username: "sari", Name: "Sari", Role: "unit head", Group: "unit-heads"},
	{Username: "dewi", Name: "Dewi", Role: "finance", Group: "finance"},
	// Tono takes over as supervisor when Budi leaves: see change.go.
	{Username: "tono", Name: "Tono", Role: "next supervisor"},
}

func personNamed(username string) (person, bool) {
	for _, p := range people {
		if p.Username == username {
			return p, true
		}
	}
	return person{}, false
}
