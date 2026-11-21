package requireorm

// LeadOutbox is a custom query on the repo_ext seam, as the db skill shows.
type LeadOutbox struct{}

func NewLeadOutbox() *LeadOutbox { return &LeadOutbox{} }

func (o *LeadOutbox) ClaimDue() ([]Lead, error) { return nil, nil }

func (o *LeadOutbox) MarkNotified(id string) error { return nil }
