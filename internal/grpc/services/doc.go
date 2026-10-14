// Copyright (c) 2025 Reliant Labs
//
// These are Connect RPC handlers: the exported methods are the proto-defined
// service methods, and the package embeds the generated
// reliantv1connect.*ServiceHandler. The contract is the .proto service, so a
// contract.go here would duplicate the proto boundary.
//
// The package's contract opt-out lives here, in one file no service owns, so
// moving a service onto forge's handler model (P3b) never moves it. It goes
// when the package does.
//
//forge:lint-disable-next-line forge-exclude-contract-outbound-io: the handlers publish chat events to JetStream (project.go) as part of serving RPCs; the RPC surface is the contract, and extracting a publisher adapter is deferred; tracked in H-RELIANT-CI-lint follow-ups
//forge:exclude-contract: Connect RPC handler implementations; their contract is the generated proto service interface
package services
