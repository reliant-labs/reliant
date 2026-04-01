package model

import (
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
)

// --- CelString accessors ---

// CelStringIsSet returns true if the CelString has any value (literal or expr).
func CelStringIsSet(c *reliantv1.CelString) bool {
	return c != nil && c.GetValue() != nil
}

// CelStringIsExpr returns true if the CelString holds a CEL expression.
func CelStringIsExpr(c *reliantv1.CelString) bool {
	if c == nil {
		return false
	}
	_, ok := c.GetValue().(*reliantv1.CelString_Expr)
	return ok
}

// CelStringValue returns the literal string value, or "" if not set or is an expr.
func CelStringValue(c *reliantv1.CelString) string {
	if c == nil {
		return ""
	}
	return c.GetLiteral()
}

// CelStringExpr returns the CEL expression string, or "" if not set or is a literal.
func CelStringExpr(c *reliantv1.CelString) string {
	if c == nil {
		return ""
	}
	return c.GetExpr()
}

// CelStringRaw returns the expression if IsExpr, otherwise the literal value.
// This is useful when you need the raw string regardless of whether it's a CEL expression.
func CelStringRaw(c *reliantv1.CelString) string {
	if c == nil {
		return ""
	}
	if CelStringIsExpr(c) {
		return c.GetExpr()
	}
	return c.GetLiteral()
}

// CelStringField returns (value_or_expr, isExpr, isSet) for a CelString.
// Useful for save_message patterns where you need all three pieces of info.
func CelStringField(c *reliantv1.CelString) (string, bool, bool) {
	if !CelStringIsSet(c) {
		return "", false, false
	}
	if CelStringIsExpr(c) {
		return c.GetExpr(), true, true
	}
	return c.GetLiteral(), false, true
}

// --- CelBool accessors ---

// CelBoolIsSet returns true if the CelBool has any value (literal or expr).
func CelBoolIsSet(c *reliantv1.CelBool) bool {
	return c != nil && c.GetValue() != nil
}

// CelBoolIsExpr returns true if the CelBool holds a CEL expression.
func CelBoolIsExpr(c *reliantv1.CelBool) bool {
	if c == nil {
		return false
	}
	_, ok := c.GetValue().(*reliantv1.CelBool_Expr)
	return ok
}

// CelBoolValue returns the literal bool value, or false if not set or is an expr.
func CelBoolValue(c *reliantv1.CelBool) bool {
	if c == nil {
		return false
	}
	return c.GetLiteral()
}

// CelBoolExpr returns the CEL expression string, or "" if not set or is a literal.
func CelBoolExpr(c *reliantv1.CelBool) string {
	if c == nil {
		return ""
	}
	return c.GetExpr()
}

// --- CelDouble accessors ---

// CelDoubleIsSet returns true if the CelDouble has any value (literal or expr).
func CelDoubleIsSet(c *reliantv1.CelDouble) bool {
	return c != nil && c.GetValue() != nil
}

// CelDoubleIsExpr returns true if the CelDouble holds a CEL expression.
func CelDoubleIsExpr(c *reliantv1.CelDouble) bool {
	if c == nil {
		return false
	}
	_, ok := c.GetValue().(*reliantv1.CelDouble_Expr)
	return ok
}

// CelDoubleValue returns the literal float64 value, or 0 if not set or is an expr.
func CelDoubleValue(c *reliantv1.CelDouble) float64 {
	if c == nil {
		return 0
	}
	return c.GetLiteral()
}

// CelDoubleExpr returns the CEL expression string, or "" if not set or is a literal.
func CelDoubleExpr(c *reliantv1.CelDouble) string {
	if c == nil {
		return ""
	}
	return c.GetExpr()
}

// --- CelInt accessors ---

// CelIntIsSet returns true if the CelInt has any value (literal or expr).
func CelIntIsSet(c *reliantv1.CelInt) bool {
	return c != nil && c.GetValue() != nil
}

// CelIntIsExpr returns true if the CelInt holds a CEL expression.
func CelIntIsExpr(c *reliantv1.CelInt) bool {
	if c == nil {
		return false
	}
	_, ok := c.GetValue().(*reliantv1.CelInt_Expr)
	return ok
}

// CelIntValue returns the literal int64 value, or 0 if not set or is an expr.
func CelIntValue(c *reliantv1.CelInt) int64 {
	if c == nil {
		return 0
	}
	return c.GetLiteral()
}

// CelIntExpr returns the CEL expression string, or "" if not set or is a literal.
func CelIntExpr(c *reliantv1.CelInt) string {
	if c == nil {
		return ""
	}
	return c.GetExpr()
}

// --- CelStringList accessors ---

// CelStringListIsSet returns true if the CelStringList has any value (literal or expr).
func CelStringListIsSet(c *reliantv1.CelStringList) bool {
	return c != nil && c.GetValue() != nil
}

// CelStringListIsExpr returns true if the CelStringList holds a CEL expression.
func CelStringListIsExpr(c *reliantv1.CelStringList) bool {
	if c == nil {
		return false
	}
	_, ok := c.GetValue().(*reliantv1.CelStringList_Expr)
	return ok
}

// CelStringListValue returns the literal []string value, or nil if not set or is an expr.
func CelStringListValue(c *reliantv1.CelStringList) []string {
	if c == nil {
		return nil
	}
	literal := c.GetLiteral()
	if literal == nil {
		return nil
	}
	return literal.GetValues()
}

// CelStringListExpr returns the CEL expression string, or "" if not set or is a literal.
func CelStringListExpr(c *reliantv1.CelStringList) string {
	if c == nil {
		return ""
	}
	return c.GetExpr()
}

// --- CelModelSelector accessors ---

// CelModelSelectorIsSet returns true if the CelModelSelector has any value.
func CelModelSelectorIsSet(c *reliantv1.CelModelSelector) bool {
	return c != nil && c.GetValue() != nil
}

// CelModelSelectorIsExpr returns true if the CelModelSelector holds a CEL expression.
func CelModelSelectorIsExpr(c *reliantv1.CelModelSelector) bool {
	if c == nil {
		return false
	}
	_, ok := c.GetValue().(*reliantv1.CelModelSelector_Expr)
	return ok
}

// CelModelSelectorValue returns the literal V2ModelSelector, or nil if not set or is an expr.
func CelModelSelectorValue(c *reliantv1.CelModelSelector) *reliantv1.ModelSelector {
	if c == nil {
		return nil
	}
	return c.GetLiteral()
}

// CelModelSelectorExpr returns the CEL expression string, or "" if not set or is a literal.
func CelModelSelectorExpr(c *reliantv1.CelModelSelector) string {
	if c == nil {
		return ""
	}
	return c.GetExpr()
}

// --- DirectCelBool accessors ---

// DirectCelExpr returns the expression string from a DirectCelBool, or "" if nil.
func DirectCelExpr(c *reliantv1.DirectCelBool) string {
	if c == nil {
		return ""
	}
	return c.GetExpr()
}

// DirectCelIsSet returns true if the DirectCelBool has a non-empty expression.
func DirectCelIsSet(c *reliantv1.DirectCelBool) bool {
	return c != nil && c.GetExpr() != ""
}
