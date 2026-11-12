package money

import (
	"errors"
	"math"
	"testing"
)

func TestFormat(t *testing.T) {
	cases := []struct {
		name string
		m    Money
		want string
	}{
		{"usd whole", FromCents(1200, USD), "$12.00"},
		{"usd cents", FromCents(1234, USD), "$12.34"},
		{"usd grouped", FromCents(123456, USD), "$1,234.56"},
		{"usd big", FromCents(123456789, USD), "$1,234,567.89"},
		{"usd negative", FromCents(-123456, USD), "-$1,234.56"},
		{"usd zero", FromCents(0, USD), "$0.00"},
		{"eur", FromCents(999, EUR), "€9.99"},
		{"gbp", FromCents(50, GBP), "£0.50"},
		{"jpy no fraction", FromCents(1234, JPY), "¥1,234"},
		{"jpy negative", FromCents(-500, JPY), "-¥500"},
		{"unknown currency code prefix", FromCents(500, "XyZ"), "XYZ 5.00"},
		{"min int64 usd", FromCents(math.MinInt64, USD), "-$92,233,720,368,547,758.08"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.m.Format(); got != tc.want {
				t.Errorf("Format() = %q, want %q", got, tc.want)
			}
			if got := tc.m.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFromMajor(t *testing.T) {
	m, err := FromMajor(12, USD)
	if err != nil {
		t.Fatalf("FromMajor: %v", err)
	}
	if m.Cents() != 1200 {
		t.Errorf("cents = %d, want 1200", m.Cents())
	}
	jpy, err := FromMajor(500, JPY)
	if err != nil {
		t.Fatalf("FromMajor jpy: %v", err)
	}
	if jpy.Cents() != 500 {
		t.Errorf("jpy cents = %d, want 500", jpy.Cents())
	}
	if _, err := FromMajor(math.MaxInt64, USD); !errors.Is(err, ErrOverflow) {
		t.Errorf("FromMajor overflow: got %v, want ErrOverflow", err)
	}
}

func TestAddSub(t *testing.T) {
	a := FromCents(1000, USD)
	b := FromCents(250, USD)

	sum, err := a.Add(b)
	if err != nil || sum.Cents() != 1250 {
		t.Errorf("Add = %v, %v; want 1250", sum.Cents(), err)
	}
	diff, err := a.Sub(b)
	if err != nil || diff.Cents() != 750 {
		t.Errorf("Sub = %v, %v; want 750", diff.Cents(), err)
	}

	// Currency mismatch.
	if _, err := a.Add(FromCents(1, EUR)); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("Add mismatch: got %v, want ErrCurrencyMismatch", err)
	}
	if _, err := a.Sub(FromCents(1, EUR)); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("Sub mismatch: got %v, want ErrCurrencyMismatch", err)
	}

	// Overflow.
	if _, err := FromCents(math.MaxInt64, USD).Add(FromCents(1, USD)); !errors.Is(err, ErrOverflow) {
		t.Errorf("Add overflow: got %v, want ErrOverflow", err)
	}
	if _, err := FromCents(math.MinInt64, USD).Sub(FromCents(1, USD)); !errors.Is(err, ErrOverflow) {
		t.Errorf("Sub overflow: got %v, want ErrOverflow", err)
	}
}

func TestMul(t *testing.T) {
	m := FromCents(199, USD)
	got, err := m.Mul(3)
	if err != nil || got.Cents() != 597 {
		t.Errorf("Mul = %d, %v; want 597", got.Cents(), err)
	}
	if _, err := FromCents(math.MaxInt64, USD).Mul(2); !errors.Is(err, ErrOverflow) {
		t.Errorf("Mul overflow: got %v, want ErrOverflow", err)
	}
	if _, err := FromCents(math.MinInt64, USD).Mul(-1); !errors.Is(err, ErrOverflow) {
		t.Errorf("Mul MinInt64*-1: got %v, want ErrOverflow", err)
	}
}

func TestMulRatio(t *testing.T) {
	cases := []struct {
		name     string
		cents    int64
		num, den int64
		want     int64
	}{
		{"7.5% tax rounds up", 1000, 75, 1000, 75},
		{"half rounds away up", 100, 1, 8, 13},     // 12.5 -> 13
		{"half rounds away down", -100, 1, 8, -13}, // -12.5 -> -13
		{"exact", 1000, 1, 2, 500},
		{"third rounds", 100, 1, 3, 33},      // 33.33 -> 33
		{"two thirds rounds", 100, 2, 3, 67}, // 66.66 -> 67
		{"percentage of large", 1000000, 15, 100, 150000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := FromCents(tc.cents, USD).MulRatio(tc.num, tc.den)
			if err != nil {
				t.Fatalf("MulRatio: %v", err)
			}
			if got.Cents() != tc.want {
				t.Errorf("MulRatio(%d,%d) of %d = %d, want %d", tc.num, tc.den, tc.cents, got.Cents(), tc.want)
			}
		})
	}
	if _, err := FromCents(100, USD).MulRatio(1, 0); !errors.Is(err, ErrDivideByZero) {
		t.Errorf("MulRatio div-by-zero: got %v, want ErrDivideByZero", err)
	}
	// Large product must NOT overflow mid-calc (big.Int intermediate).
	got, err := FromCents(math.MaxInt64, USD).MulRatio(1, 2)
	if err != nil {
		t.Fatalf("MulRatio big product: %v", err)
	}
	if got.Cents() != (math.MaxInt64+1)/2 {
		t.Errorf("MulRatio halve MaxInt64 = %d", got.Cents())
	}
	// Rounded result that no longer fits -> overflow.
	if _, err := FromCents(math.MaxInt64, USD).MulRatio(3, 1); !errors.Is(err, ErrOverflow) {
		t.Errorf("MulRatio result overflow: got %v, want ErrOverflow", err)
	}
}

func TestAllocate(t *testing.T) {
	// The classic: split 5 cents 3 ways with no loss.
	parts, err := FromCents(5, USD).Allocate([]int64{1, 1, 1})
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	got := []int64{parts[0].Cents(), parts[1].Cents(), parts[2].Cents()}
	want := []int64{2, 2, 1}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Allocate 5/3 = %v, want %v", got, want)
			break
		}
	}
	assertSums(t, FromCents(5, USD), parts)

	// Weighted split.
	parts, err = FromCents(1000, USD).Allocate([]int64{1, 3})
	if err != nil {
		t.Fatalf("Allocate weighted: %v", err)
	}
	if parts[0].Cents() != 250 || parts[1].Cents() != 750 {
		t.Errorf("Allocate 1000 by 1:3 = %d,%d want 250,750", parts[0].Cents(), parts[1].Cents())
	}
	assertSums(t, FromCents(1000, USD), parts)

	// Negative amount still sums exactly.
	parts, err = FromCents(-5, USD).Allocate([]int64{1, 1, 1})
	if err != nil {
		t.Fatalf("Allocate negative: %v", err)
	}
	assertSums(t, FromCents(-5, USD), parts)

	// Zero-ratio parts stay zero.
	parts, err = FromCents(100, USD).Allocate([]int64{0, 1})
	if err != nil {
		t.Fatalf("Allocate zero ratio: %v", err)
	}
	if parts[0].Cents() != 0 || parts[1].Cents() != 100 {
		t.Errorf("Allocate 100 by 0:1 = %d,%d want 0,100", parts[0].Cents(), parts[1].Cents())
	}

	// Errors.
	if _, err := FromCents(100, USD).Allocate(nil); !errors.Is(err, ErrDivideByZero) {
		t.Errorf("Allocate nil: got %v", err)
	}
	if _, err := FromCents(100, USD).Allocate([]int64{0, 0}); !errors.Is(err, ErrDivideByZero) {
		t.Errorf("Allocate all-zero: got %v", err)
	}
	if _, err := FromCents(100, USD).Allocate([]int64{-1, 2}); err == nil {
		t.Errorf("Allocate negative ratio: want error")
	}
}

func assertSums(t *testing.T, whole Money, parts []Money) {
	t.Helper()
	var total int64
	for _, p := range parts {
		if p.Currency() != whole.Currency() {
			t.Fatalf("part currency %s != %s", p.Currency(), whole.Currency())
		}
		total += p.Cents()
	}
	if total != whole.Cents() {
		t.Errorf("parts sum to %d, want %d", total, whole.Cents())
	}
}

func TestCmpEqual(t *testing.T) {
	a := FromCents(100, USD)
	b := FromCents(200, USD)
	if c, _ := a.Cmp(b); c != -1 {
		t.Errorf("Cmp a<b = %d, want -1", c)
	}
	if c, _ := b.Cmp(a); c != 1 {
		t.Errorf("Cmp b>a = %d, want 1", c)
	}
	if c, _ := a.Cmp(FromCents(100, USD)); c != 0 {
		t.Errorf("Cmp equal = %d, want 0", c)
	}
	if _, err := a.Cmp(FromCents(100, EUR)); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("Cmp mismatch: got %v", err)
	}
	if !a.Equal(FromCents(100, USD)) {
		t.Errorf("Equal same: want true")
	}
	if a.Equal(FromCents(100, EUR)) {
		t.Errorf("Equal cross-currency: want false")
	}
}

func TestNegAbs(t *testing.T) {
	if got := FromCents(100, USD).Neg(); got.Cents() != -100 {
		t.Errorf("Neg = %d, want -100", got.Cents())
	}
	if got := FromCents(-100, USD).Abs(); got.Cents() != 100 {
		t.Errorf("Abs = %d, want 100", got.Cents())
	}
	if got := FromCents(100, USD).Abs(); got.Cents() != 100 {
		t.Errorf("Abs positive = %d, want 100", got.Cents())
	}
	// MinInt64 has no positive form; must not panic/wrap.
	if got := FromCents(math.MinInt64, USD).Neg(); got.Cents() != math.MinInt64 {
		t.Errorf("Neg MinInt64 = %d, want unchanged", got.Cents())
	}
	if got := FromCents(math.MinInt64, USD).Abs(); got.Cents() != math.MinInt64 {
		t.Errorf("Abs MinInt64 = %d, want unchanged", got.Cents())
	}
}

func TestParse(t *testing.T) {
	cases := []struct {
		in       string
		currency Currency
		want     int64
	}{
		{"12.34", USD, 1234},
		{"12", USD, 1200},
		{"0.05", USD, 5},
		{".5", USD, 50},
		{"-1,234.56", USD, -123456},
		{"+7.00", USD, 700},
		{"  9.99 ", USD, 999},
		{"1000", JPY, 1000}, // no fraction for JPY
		{"5.", USD, 500},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			m, err := Parse(tc.in, tc.currency)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.in, err)
			}
			if m.Cents() != tc.want {
				t.Errorf("Parse(%q) = %d, want %d", tc.in, m.Cents(), tc.want)
			}
			if m.Currency() != tc.currency {
				t.Errorf("Parse currency = %s, want %s", m.Currency(), tc.currency)
			}
		})
	}

	bad := []struct {
		in       string
		currency Currency
	}{
		{"12.345", USD}, // too many fractional digits
		{"1.5", JPY},    // JPY takes no fraction
		{"abc", USD},    // junk
		{"", USD},       // empty
		{"1.2.3", USD},  // two dots
		{"12x", USD},    // trailing junk
	}
	for _, tc := range bad {
		t.Run("bad_"+tc.in, func(t *testing.T) {
			if _, err := Parse(tc.in, tc.currency); err == nil {
				t.Errorf("Parse(%q) = nil error, want failure", tc.in)
			}
		})
	}
}

func TestParseFormatRoundTrip(t *testing.T) {
	for _, cents := range []int64{0, 5, 100, 1234, -9999, 123456789} {
		m := FromCents(cents, USD)
		// Format includes symbol+commas; parse the plain value back.
		reparsed, err := Parse(stripUSD(m.Format()), USD)
		if err != nil {
			t.Fatalf("reparse %q: %v", m.Format(), err)
		}
		if reparsed.Cents() != cents {
			t.Errorf("round trip %d -> %q -> %d", cents, m.Format(), reparsed.Cents())
		}
	}
}

func stripUSD(s string) string {
	// Remove the "$" for reparsing; keep sign and commas (Parse tolerates them).
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '$' {
			continue
		}
		out = append(out, s[i])
	}
	return string(out)
}

func TestZeroValueUsable(t *testing.T) {
	var m Money // zero value
	if !m.IsZero() {
		t.Errorf("zero value IsZero = false")
	}
	other := Zero("")
	sum, err := m.Add(other)
	if err != nil || !sum.IsZero() {
		t.Errorf("zero + zero = %v, %v", sum, err)
	}
	// zero-currency vs USD is a mismatch.
	if _, err := m.Add(FromCents(1, USD)); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("zero-currency + USD: got %v, want mismatch", err)
	}
}

func TestFloat64(t *testing.T) {
	if got := FromCents(1234, USD).Float64(); got != 12.34 {
		t.Errorf("Float64 = %v, want 12.34", got)
	}
	if got := FromCents(500, JPY).Float64(); got != 500 {
		t.Errorf("Float64 jpy = %v, want 500", got)
	}
}
