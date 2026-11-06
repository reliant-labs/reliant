// Package nocalls imports the models but calls no serializer.
package nocalls

import "github.com/reliant-labs/reliant/internal/db"

func Count(rows []*db.StepExecution) int { return len(rows) }
