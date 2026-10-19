// Copyright (c) 2025 Reliant Labs
package workersetup

import "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"

// accountDriverFor builds a driver that talks as one account
// (drivers.GetDriverForAccount). Until it is set, account routing is off:
// an account is never assigned that cannot then be used, so every request
// keeps using its provider's default account.
var accountDriverFor handlers.DriverForAccountFunc
