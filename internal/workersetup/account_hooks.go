// Copyright (c) 2025 Reliant Labs
package workersetup

import (
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
)

// accountDriverFor builds a driver that talks as one account. With it set,
// account routing is live: a thread is assigned an account of its provider and
// served by that account's credential. A test may replace it with a fake driver.
var accountDriverFor handlers.DriverForAccountFunc = drivers.GetDriverForAccount
