package experimentstats

import "math"

// Interval is a closed interval [Low, High].
type Interval struct {
	Low, High float64
}

// ZCritical is the two-sided standard normal critical value for level alpha:
// the z with P(|Z| > z) = alpha (1.959964 for 0.05).
func ZCritical(alpha float64) float64 {
	return math.Sqrt2 * math.Erfcinv(alpha)
}

// NormalTwoSidedP is P(|Z| >= |z|) for a standard normal Z.
func NormalTwoSidedP(z float64) float64 {
	return math.Erfc(math.Abs(z) / math.Sqrt2)
}

// Wilson is the Wilson score interval for successes out of trials at the
// two-sided critical value z (ZCritical). Zero trials know nothing: [0, 1].
func Wilson(successes, trials int64, z float64) Interval {
	if trials <= 0 {
		return Interval{0, 1}
	}
	n := float64(trials)
	p := float64(successes) / n
	z2 := z * z
	denom := 1 + z2/n
	center := (p + z2/(2*n)) / denom
	half := z / denom * math.Sqrt(p*(1-p)/n+z2/(4*n*n))
	lo, hi := center-half, center+half
	// Exact at the edges, where rounding would leave a hair outside [0, 1].
	if successes <= 0 {
		lo = 0
	}
	if successes >= trials {
		hi = 1
	}
	return Interval{math.Max(0, lo), math.Min(1, hi)}
}

// NewcombeDiff is Newcombe's hybrid score interval (method 10) for the
// difference of two proportions p2 - p1, from each one's Wilson interval at
// z. Either side with zero trials yields the widest difference, [-1, 1].
func NewcombeDiff(x1, n1, x2, n2 int64, z float64) Interval {
	if n1 <= 0 || n2 <= 0 {
		return Interval{-1, 1}
	}
	p1, p2 := float64(x1)/float64(n1), float64(x2)/float64(n2)
	w1, w2 := Wilson(x1, n1, z), Wilson(x2, n2, z)
	d := p2 - p1
	lo := d - math.Sqrt((p2-w2.Low)*(p2-w2.Low)+(w1.High-p1)*(w1.High-p1))
	hi := d + math.Sqrt((w2.High-p2)*(w2.High-p2)+(p1-w1.Low)*(p1-w1.Low))
	return Interval{math.Max(-1, lo), math.Min(1, hi)}
}

// TwoProportionZ is the pooled two-proportion z-test of p2 against p1: the
// statistic (positive when p2 > p1) and its two-sided p-value. When the pooled
// rate is 0 or 1 the arms cannot differ: z = 0, p = 1.
func TwoProportionZ(x1, n1, x2, n2 int64) (z, p float64) {
	if n1 <= 0 || n2 <= 0 {
		return 0, 1
	}
	a, b := float64(n1), float64(n2)
	pool := float64(x1+x2) / (a + b)
	se := math.Sqrt(pool * (1 - pool) * (1/a + 1/b))
	if se == 0 {
		return 0, 1
	}
	z = (float64(x2)/b - float64(x1)/a) / se
	return z, NormalTwoSidedP(z)
}

// ChiSquareSF is the chi-square survival function P(X >= x) with df degrees
// of freedom: the p-value of a chi-square statistic x. It is the regularized
// upper incomplete gamma function Q(df/2, x/2).
func ChiSquareSF(x float64, df int) float64 {
	if df <= 0 {
		return math.NaN()
	}
	if x <= 0 {
		return 1
	}
	return gammaQ(float64(df)/2, x/2)
}

const (
	gammaEps     = 1e-15
	gammaFPMin   = 1e-300
	gammaMaxIter = 1000
)

// gammaQ is the regularized upper incomplete gamma function Q(a, x) for
// a > 0, x > 0: the series for P when x < a+1 (where it converges fast), else
// the continued fraction for Q by the modified Lentz method (Numerical Recipes
// 6.2), each to full double precision.
func gammaQ(a, x float64) float64 {
	lga, _ := math.Lgamma(a)
	prefix := math.Exp(-x + a*math.Log(x) - lga)
	if x < a+1 {
		sum, del, ap := 1/a, 1/a, a
		for range gammaMaxIter {
			ap++
			del *= x / ap
			sum += del
			if math.Abs(del) < math.Abs(sum)*gammaEps {
				break
			}
		}
		return math.Max(0, 1-sum*prefix)
	}
	b := x + 1 - a
	c := 1 / gammaFPMin
	d := 1 / b
	h := d
	for i := 1; i <= gammaMaxIter; i++ {
		an := -float64(i) * (float64(i) - a)
		b += 2
		d = an*d + b
		if math.Abs(d) < gammaFPMin {
			d = gammaFPMin
		}
		c = b + an/c
		if math.Abs(c) < gammaFPMin {
			c = gammaFPMin
		}
		d = 1 / d
		del := d * c
		h *= del
		if math.Abs(del-1) < gammaEps {
			break
		}
	}
	return prefix * h
}
