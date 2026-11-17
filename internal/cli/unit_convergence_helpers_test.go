package cli

// allHubUnits is the split of an env the hub converges whole: the shape the old
// env-level HubConverged boolean named.
func allHubUnits() unitSplit {
	return unitSplit{Hub: []string{"base"}, Source: unitSplitInferred}
}
