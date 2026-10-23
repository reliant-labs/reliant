// Package money provides a small, allocation-free value type for representing
// monetary amounts as an integer number of minor units ("cents") plus a
// currency, together with overflow-checked arithmetic and locale-neutral
// formatting.
//
// # Why a type instead of a bare int64
//
// Backend pricing that passes raw int64 cents around is a bug farm: it is easy
// to add two amounts in different currencies, to lose pennies when splitting a
// charge, or to silently overflow an int64 on a large multiply. Money makes
// those mistakes either impossible or loud:
//
//   - Currency is carried with the amount, so Add/Sub/Cmp refuse to combine
//     mismatched currencies (ErrCurrencyMismatch) instead of producing a
//     nonsense number.
//   - Arithmetic is overflow-checked and returns ErrOverflow rather than
//     wrapping around.
//   - Allocate splits an amount across ratios WITHOUT losing or inventing
//     minor units — the classic "split the bill" problem.
//
// # Representation
//
// A Money holds an int64 count of the currency's smallest unit and the ISO
// 4217 currency code. For USD/EUR/GBP that unit is the cent (2 decimal
// digits); for JPY it is the yen itself (0 decimal digits). The stored count
// is called "cents" throughout because that is the common case and the name
// the frontend's formatMoneyCents helper uses — but Format renders using the
// currency's actual decimal digits, so JPY formats without a fractional part.
//
// The zero Money{} is a valid amount of zero in the empty currency (""). Two
// zero-currency amounts combine fine; combining "" with "USD" is a mismatch.
package money

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
)

// Currency is an ISO 4217 alphabetic currency code (e.g. "USD"). It is a bare
// string so callers may name currencies the registry does not know about;
// unknown currencies format with a code prefix and default to 2 decimal
// digits.
type Currency string

// Common currencies. This is not an exhaustive ISO 4217 list — it is the set
// with registered display metadata (symbol + decimal digits). Any other code
// still works as a Currency value.
const (
	USD Currency = "USD"
	EUR Currency = "EUR"
	GBP Currency = "GBP"
	JPY Currency = "JPY"
	CAD Currency = "CAD"
	AUD Currency = "AUD"
	CHF Currency = "CHF"
	CNY Currency = "CNY"
	INR Currency = "INR"
	BRL Currency = "BRL"
)

// Sentinel errors returned by the arithmetic and parsing helpers. Callers can
// match them with errors.Is.
var (
	// ErrCurrencyMismatch is returned when an operation combines two amounts
	// whose currencies differ.
	ErrCurrencyMismatch = errors.New("money: currency mismatch")
	// ErrOverflow is returned when a result does not fit in int64.
	ErrOverflow = errors.New("money: int64 overflow")
	// ErrDivideByZero is returned by Allocate/MulRatio for a zero denominator.
	ErrDivideByZero = errors.New("money: division by zero")
	// ErrParse is returned when a decimal string cannot be parsed.
	ErrParse = errors.New("money: invalid decimal string")
)

// currencyInfo carries the display metadata for a known currency.
type currencyInfo struct {
	digits int    // number of minor-unit decimal digits (2 for USD, 0 for JPY)
	symbol string // display symbol; empty means "prefix the code instead"
}

// currencies maps known codes to their display metadata. Unknown codes fall
// back to defaultInfo.
var currencies = map[Currency]currencyInfo{
	USD: {2, "$"},
	CAD: {2, "$"},
	AUD: {2, "$"},
	EUR: {2, "€"},
	GBP: {2, "£"},
	JPY: {0, "¥"},
	CNY: {2, "¥"},
	CHF: {2, "CHF "},
	INR: {2, "₹"},
	BRL: {2, "R$"},
}

// defaultInfo is used for currencies not in the registry: 2 decimal digits and
// a "CODE " prefix (empty symbol signals code-prefix formatting).
var defaultInfo = currencyInfo{digits: 2, symbol: ""}

func infoFor(c Currency) currencyInfo {
	if ci, ok := currencies[c]; ok {
		return ci
	}
	return defaultInfo
}

// Money is a monetary amount: an int64 count of the currency's minor unit plus
// the currency itself. The zero value is a valid zero amount in currency "".
type Money struct {
	cents    int64
	currency Currency
}

// FromCents constructs a Money from a raw minor-unit count and currency. This
// is the primary constructor and the direct counterpart to the frontend's
// formatMoneyCents(cents, currency): both take the same integer minor-unit
// amount.
func FromCents(cents int64, currency Currency) Money {
	return Money{cents: cents, currency: currency}
}

// FromMajor constructs a Money from a whole-unit amount (e.g. FromMajor(12,
// USD) == $12.00). It returns ErrOverflow if the scaled value does not fit in
// int64.
func FromMajor(major int64, currency Currency) (Money, error) {
	scale := pow10(infoFor(currency).digits)
	c, ok := mulOverflow(major, scale)
	if !ok {
		return Money{}, fmt.Errorf("%w: %d major units of %s", ErrOverflow, major, currency)
	}
	return Money{cents: c, currency: currency}, nil
}

// Zero returns the zero amount in the given currency.
func Zero(currency Currency) Money { return Money{currency: currency} }

// Cents returns the raw minor-unit count. It is the value to hand to the
// frontend formatMoneyCents helper or to persist.
func (m Money) Cents() int64 { return m.cents }

// Currency returns the amount's currency.
func (m Money) Currency() Currency { return m.currency }

// IsZero reports whether the amount is exactly zero (currency-independent).
func (m Money) IsZero() bool { return m.cents == 0 }

// IsNegative reports whether the amount is below zero.
func (m Money) IsNegative() bool { return m.cents < 0 }

// sameCurrency validates that two amounts share a currency.
func (m Money) sameCurrency(o Money) error {
	if m.currency != o.currency {
		return fmt.Errorf("%w: %s vs %s", ErrCurrencyMismatch, m.currency, o.currency)
	}
	return nil
}

// Add returns m+o, or ErrCurrencyMismatch / ErrOverflow.
func (m Money) Add(o Money) (Money, error) {
	if err := m.sameCurrency(o); err != nil {
		return Money{}, err
	}
	sum, ok := addOverflow(m.cents, o.cents)
	if !ok {
		return Money{}, fmt.Errorf("%w: %d + %d", ErrOverflow, m.cents, o.cents)
	}
	return Money{cents: sum, currency: m.currency}, nil
}

// Sub returns m-o, or ErrCurrencyMismatch / ErrOverflow.
func (m Money) Sub(o Money) (Money, error) {
	if err := m.sameCurrency(o); err != nil {
		return Money{}, err
	}
	neg, ok := negOverflow(o.cents)
	if !ok {
		return Money{}, fmt.Errorf("%w: -%d", ErrOverflow, o.cents)
	}
	diff, ok := addOverflow(m.cents, neg)
	if !ok {
		return Money{}, fmt.Errorf("%w: %d - %d", ErrOverflow, m.cents, o.cents)
	}
	return Money{cents: diff, currency: m.currency}, nil
}

// Mul multiplies the amount by an integer factor (e.g. quantity), overflow-
// checked. The currency is preserved.
func (m Money) Mul(factor int64) (Money, error) {
	p, ok := mulOverflow(m.cents, factor)
	if !ok {
		return Money{}, fmt.Errorf("%w: %d * %d", ErrOverflow, m.cents, factor)
	}
	return Money{cents: p, currency: m.currency}, nil
}

// MulRatio scales the amount by the rational num/den, rounding the result to
// the nearest minor unit (ties rounded away from zero — the common financial
// rounding). It is the building block for percentages, tax, and discounts:
// a 7.5% tax is MulRatio(75, 1000); an 8th of a charge is MulRatio(1, 8).
//
// The intermediate product is computed in arbitrary precision, so it never
// overflows mid-calculation; ErrOverflow is only returned if the ROUNDED
// result does not fit in int64. A zero denominator yields ErrDivideByZero.
func (m Money) MulRatio(num, den int64) (Money, error) {
	if den == 0 {
		return Money{}, ErrDivideByZero
	}
	// result = round(cents * num / den), ties away from zero.
	prod := new(big.Int).Mul(big.NewInt(m.cents), big.NewInt(num))
	denom := big.NewInt(den)
	// Normalize sign onto the numerator so rounding is symmetric about zero.
	if denom.Sign() < 0 {
		prod.Neg(prod)
		denom.Neg(denom)
	}
	q := new(big.Int)
	r := new(big.Int)
	q.QuoRem(prod, denom, r) // truncated toward zero; r has sign of prod
	// Round half away from zero: if 2*|r| >= den, step the quotient away from 0.
	absR := new(big.Int).Abs(r)
	twiceR := new(big.Int).Lsh(absR, 1)
	if twiceR.Cmp(denom) >= 0 {
		if prod.Sign() < 0 {
			q.Sub(q, big.NewInt(1))
		} else {
			q.Add(q, big.NewInt(1))
		}
	}
	if !q.IsInt64() {
		return Money{}, fmt.Errorf("%w: %d * %d / %d", ErrOverflow, m.cents, num, den)
	}
	return Money{cents: q.Int64(), currency: m.currency}, nil
}

// Neg returns -m. math.MinInt64 has no positive counterpart, so it is clamped
// to itself; use Sub from Zero if you must detect that edge.
func (m Money) Neg() Money {
	if m.cents == math.MinInt64 {
		return m
	}
	return Money{cents: -m.cents, currency: m.currency}
}

// Abs returns the absolute value. math.MinInt64 is returned unchanged (it has
// no positive representation in int64).
func (m Money) Abs() Money {
	if m.cents >= 0 {
		return m
	}
	return m.Neg()
}

// Cmp compares two amounts of the same currency, returning -1, 0, or +1. It
// returns ErrCurrencyMismatch if the currencies differ.
func (m Money) Cmp(o Money) (int, error) {
	if err := m.sameCurrency(o); err != nil {
		return 0, err
	}
	switch {
	case m.cents < o.cents:
		return -1, nil
	case m.cents > o.cents:
		return 1, nil
	default:
		return 0, nil
	}
}

// Equal reports whether the two amounts have the same currency AND value. It
// never errors: mismatched currencies are simply not equal.
func (m Money) Equal(o Money) bool {
	return m.currency == o.currency && m.cents == o.cents
}

// Allocate splits the amount across len(ratios) parts in proportion to the
// given ratios, distributing every last minor unit so the parts sum EXACTLY
// back to the original (no pennies lost or invented). Remainder units left by
// integer division are handed out one at a time to the earliest parts, which
// makes the split deterministic and largest-remainder-fair enough for billing.
//
// Ratios must be non-negative and not all zero; a negative ratio or an
// all-zero set returns an error. The sign of the amount is preserved.
func (m Money) Allocate(ratios []int64) ([]Money, error) {
	if len(ratios) == 0 {
		return nil, fmt.Errorf("%w: no ratios", ErrDivideByZero)
	}
	var total int64
	for _, r := range ratios {
		if r < 0 {
			return nil, fmt.Errorf("money: negative allocation ratio %d", r)
		}
		var ok bool
		total, ok = addOverflow(total, r)
		if !ok {
			return nil, fmt.Errorf("%w: ratio sum", ErrOverflow)
		}
	}
	if total == 0 {
		return nil, fmt.Errorf("%w: ratios sum to zero", ErrDivideByZero)
	}

	out := make([]Money, len(ratios))
	// Assign the floor share to each part, tracking what's left to distribute.
	remainder := m.cents
	for i, r := range ratios {
		// share = trunc(cents * r / total), computed in big.Int to avoid
		// overflow in the cents*r product.
		share := new(big.Int).Mul(big.NewInt(m.cents), big.NewInt(r))
		share.Quo(share, big.NewInt(total))
		s := share.Int64()
		out[i] = Money{cents: s, currency: m.currency}
		remainder -= s
	}
	// Distribute the leftover units (can be negative for negative amounts) one
	// per part, in order, skipping zero-ratio parts so they stay at zero.
	step := int64(1)
	if remainder < 0 {
		step = -1
	}
	for i := 0; remainder != 0; i = (i + 1) % len(ratios) {
		if ratios[i] == 0 {
			continue
		}
		out[i].cents += step
		remainder -= step
	}
	return out, nil
}

// Float64 returns the amount as a float64 in major units (e.g. 12.34 for
// $12.34). It is a lossy convenience for display or coarse math only — never
// round-trip money through float64.
func (m Money) Float64() float64 {
	return float64(m.cents) / float64(pow10(infoFor(m.currency).digits))
}

// String implements fmt.Stringer, returning the same rendering as Format.
func (m Money) String() string { return m.Format() }

// Format renders the amount for display using the currency's symbol (or code)
// and decimal digits, with comma thousands-grouping on the integer part:
// FromCents(123456, USD).Format() == "$1,234.56"; a negative amount is
// "-$1,234.56"; JPY (0 digits) renders "¥1,234". Unknown currencies use a code
// prefix: FromCents(500, "XyZ").Format() == "XYZ 5.00".
func (m Money) Format() string {
	ci := infoFor(m.currency)
	neg := m.cents < 0

	// Work in absolute minor units, guarding math.MinInt64 (whose negation
	// overflows) by promoting to uint64 magnitude.
	var mag uint64
	if neg {
		mag = uint64(-(m.cents + 1)) + 1
	} else {
		mag = uint64(m.cents)
	}

	scale := uint64(pow10(ci.digits))
	intPart := mag / scale
	fracPart := mag % scale

	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	if ci.symbol != "" {
		b.WriteString(ci.symbol)
	} else {
		b.WriteString(strings.ToUpper(string(m.currency)))
		b.WriteByte(' ')
	}
	b.WriteString(groupThousands(intPart))
	if ci.digits > 0 {
		b.WriteByte('.')
		// Zero-pad the fractional part to the currency's digit count.
		frac := fmt.Sprintf("%0*d", ci.digits, fracPart)
		b.WriteString(frac)
	}
	return b.String()
}

// Parse reads a decimal string like "12.34", "-1,234.56", or "1000" into a
// Money of the given currency, using that currency's decimal digits. Thousands
// separators (commas) and surrounding whitespace are tolerated; more fractional
// digits than the currency allows, or any non-numeric junk, is an ErrParse.
func Parse(s string, currency Currency) (Money, error) {
	ci := infoFor(currency)
	orig := s
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, ",", "")
	if s == "" {
		return Money{}, fmt.Errorf("%w: empty", ErrParse)
	}

	neg := false
	switch s[0] {
	case '-':
		neg = true
		s = s[1:]
	case '+':
		s = s[1:]
	}

	intStr, fracStr, hasDot := strings.Cut(s, ".")
	if hasDot && strings.Contains(fracStr, ".") {
		return Money{}, fmt.Errorf("%w: %q", ErrParse, orig)
	}
	if len(fracStr) > ci.digits {
		return Money{}, fmt.Errorf("%w: %q has more than %d fractional digits", ErrParse, orig, ci.digits)
	}
	// Right-pad the fractional part to the currency's digit count.
	for len(fracStr) < ci.digits {
		fracStr += "0"
	}
	digits := intStr + fracStr
	if digits == "" {
		return Money{}, fmt.Errorf("%w: %q", ErrParse, orig)
	}

	var cents int64
	for i := 0; i < len(digits); i++ {
		d := digits[i]
		if d < '0' || d > '9' {
			return Money{}, fmt.Errorf("%w: %q", ErrParse, orig)
		}
		next, ok := mulOverflow(cents, 10)
		if !ok {
			return Money{}, fmt.Errorf("%w: %q", ErrOverflow, orig)
		}
		next, ok = addOverflow(next, int64(d-'0'))
		if !ok {
			return Money{}, fmt.Errorf("%w: %q", ErrOverflow, orig)
		}
		cents = next
	}
	if neg {
		cents = -cents
	}
	return Money{cents: cents, currency: currency}, nil
}

// --- integer helpers -------------------------------------------------------

// pow10 returns 10^n for the small n used by currency digit counts.
func pow10(n int) int64 {
	p := int64(1)
	for i := 0; i < n; i++ {
		p *= 10
	}
	return p
}

// addOverflow returns a+b and whether it stayed within int64.
func addOverflow(a, b int64) (int64, bool) {
	s := a + b
	// Overflow iff a and b share a sign that differs from the sum's sign.
	if (a > 0 && b > 0 && s < 0) || (a < 0 && b < 0 && s >= 0) {
		return 0, false
	}
	return s, true
}

// negOverflow returns -a and whether it fit (only math.MinInt64 fails).
func negOverflow(a int64) (int64, bool) {
	if a == math.MinInt64 {
		return 0, false
	}
	return -a, true
}

// mulOverflow returns a*b and whether it stayed within int64.
func mulOverflow(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	p := a * b
	// Divide back out to detect wraparound; guard the one irreversible case.
	if a == math.MinInt64 && b == -1 || b == math.MinInt64 && a == -1 {
		return 0, false
	}
	if p/b != a {
		return 0, false
	}
	return p, true
}

// groupThousands renders a non-negative integer with comma separators.
func groupThousands(n uint64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	// Insert commas from the right every three digits.
	lead := len(s) % 3
	if lead == 0 {
		lead = 3
	}
	var b strings.Builder
	b.WriteString(s[:lead])
	for i := lead; i < len(s); i += 3 {
		b.WriteByte(',')
		b.WriteString(s[i : i+3])
	}
	return b.String()
}
