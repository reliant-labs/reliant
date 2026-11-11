package store

// AssignUnassignedOrgInBatches is AssignUnassignedOrg with the batch size a
// test chooses, so one can span several batches with a handful of rows.
var AssignUnassignedOrgInBatches = assignUnassignedOrg
