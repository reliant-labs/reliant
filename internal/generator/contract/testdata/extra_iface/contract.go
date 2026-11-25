package extra_iface

import (
	"context"

	"example.com/project/billing"
)

// Service has a method returning billing.MeterClient — a project-local
// cross-package interface type that is NOT in the built-in
// crossPackageInterfaces allow-list. The mock generator only knows to
// emit "nil" for this return type when the project declares
// "billing.MeterClient" in forge.yaml's contracts.interface_types.
//
// This is the cp-forge v2 svcbilling repro: a contract method returning
// billing.MeterClient otherwise generates "billing.MeterClient{}" (a
// composite literal of an interface — illegal).
type Service interface {
	Meter(ctx context.Context) (billing.MeterClient, error)
}
