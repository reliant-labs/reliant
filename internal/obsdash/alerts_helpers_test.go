package obsdash

import "github.com/reliant-labs/forge/pkg/alerting"

func validateExprForTest(expr string) error { return alerting.ValidateExpr(expr) }
