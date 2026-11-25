package experimentstats

import (
	"math"
	"testing"
)

// The reference values below were computed with scipy 1.18.1 and statsmodels
// 0.15.0 (scipy.stats.norm / chi2 / chisquare, statsmodels'
// proportion_confint(method="wilson"), confint_proportions_2indep(
// method="newcomb", compare="diff") and proportions_ztest), and are
// hard-coded so the test needs neither.

func near(t *testing.T, what string, got, want, tol float64) {
	t.Helper()
	if math.IsNaN(got) || math.Abs(got-want) > tol*math.Max(1, math.Abs(want)) {
		t.Errorf("%s = %.17g, want %.17g", what, got, want)
	}
}

// nearRel compares tiny p-values relatively: an absolute tolerance would pass
// any two values below it.
func nearRel(t *testing.T, what string, got, want, rel float64) {
	t.Helper()
	if math.IsNaN(got) || math.Abs(got-want) > rel*math.Abs(want) {
		t.Errorf("%s = %.17g, want %.17g", what, got, want)
	}
}

func TestZCritical(t *testing.T) {
	for _, c := range []struct{ alpha, want float64 }{
		{0.05, 1.9599639845400545},
		{0.025, 2.241402727604945},
		{0.01, 2.575829303548901},
		{0.001, 3.2905267314918945},
	} {
		near(t, "ZCritical", ZCritical(c.alpha), c.want, 1e-12)
	}
}

func TestNormalTwoSidedP(t *testing.T) {
	for _, c := range []struct{ z, want float64 }{
		{0.1, 0.920344325445942},
		{1.0, 0.31731050786291415},
		{-1.0, 0.31731050786291415},
		{1.959963984540054, 0.05},
		{2.5758293035489004, 0.010000000000000007},
		{3.2905267314919255, 0.0009999999999998903},
		{5.0, 5.733031437583866e-07},
	} {
		nearRel(t, "NormalTwoSidedP", NormalTwoSidedP(c.z), c.want, 1e-10)
	}
}

func TestChiSquareSF(t *testing.T) {
	for _, c := range []struct {
		df      int
		x, want float64
	}{
		{1, 0.0001, 0.9920212873707368},
		{1, 0.5, 0.47950012218695337},
		{1, 3.841458820694124, 0.04999999999999989},
		{1, 10.827566170662733, 0.0010000000000000007},
		{1, 25.0, 5.733031437583875e-07},
		{2, 1.0, 0.6065306597126334},
		{2, 5.991464547107979, 0.05000000000000007},
		{2, 13.815510557964274, 0.0010000000000000002},
		{3, 0.35, 0.950366117368476},
		{3, 7.814727903251178, 0.05},
		{3, 16.26623619623813, 0.0010000000000000007},
		{3, 40.0, 1.065509033425585e-08},
		{4, 9.487729036781154, 0.05000000000000006},
		{5, 200.0, 2.8406228986415534e-41},
	} {
		nearRel(t, "ChiSquareSF", ChiSquareSF(c.x, c.df), c.want, 1e-9)
	}
	// The closed forms: df=1 is erfc(sqrt(x/2)), df=2 is exp(-x/2).
	for _, x := range []float64{0.01, 0.7, 2, 9, 30} {
		nearRel(t, "ChiSquareSF df=1", ChiSquareSF(x, 1), math.Erfc(math.Sqrt(x/2)), 1e-12)
		nearRel(t, "ChiSquareSF df=2", ChiSquareSF(x, 2), math.Exp(-x/2), 1e-12)
	}
	if ChiSquareSF(0, 3) != 1 {
		t.Error("ChiSquareSF(0) != 1")
	}
	if !math.IsNaN(ChiSquareSF(1, 0)) {
		t.Error("ChiSquareSF with df 0 is a number")
	}
}

func TestWilson(t *testing.T) {
	z := ZCritical(0.05)
	for _, c := range []struct {
		x, n   int64
		lo, hi float64
	}{
		{0, 10, 0, 0.27753279986288926},
		{10, 10, 0.7224672001371106, 1},
		{5, 10, 0.23659309051256394, 0.7634069094874361},
		{81, 263, 0.2552885198782742, 0.36620957698280004},
		{100, 1000, 0.08290944359309571, 0.1201519631953484},
		{1, 1, 0.2065493143772374, 1},
	} {
		got := Wilson(c.x, c.n, z)
		near(t, "Wilson low", got.Low, c.lo, 1e-12)
		near(t, "Wilson high", got.High, c.hi, 1e-12)
	}
	got := Wilson(100, 1000, ZCritical(0.01))
	near(t, "Wilson 99% low", got.Low, 0.07813838860676123, 1e-12)
	near(t, "Wilson 99% high", got.High, 0.12713454331596424, 1e-12)
	if got := Wilson(0, 0, z); got != (Interval{0, 1}) {
		t.Errorf("Wilson(0, 0) = %v, want [0, 1]", got)
	}
}

func TestNewcombeDiff(t *testing.T) {
	z := ZCritical(0.05)
	for _, c := range []struct {
		x1, n1, x2, n2 int64
		lo, hi         float64
	}{
		{100, 1000, 130, 1000, 0.0020023487152830075, 0.05807048502220285},
		{56, 70, 48, 80, -0.33387265403690614, -0.05243147240236498},
		{9, 10, 3, 10, -0.809017973535488, -0.1705227239345029},
		{0, 10, 0, 20, -0.27753279986288926, 0.1611251580528194},
		{10, 10, 0, 20, -1.0, -0.6790860371419145},
	} {
		got := NewcombeDiff(c.x1, c.n1, c.x2, c.n2, z)
		near(t, "Newcombe low", got.Low, c.lo, 1e-12)
		near(t, "Newcombe high", got.High, c.hi, 1e-12)
	}
	// At a Bonferroni level for two treatments.
	got := NewcombeDiff(100, 1000, 130, 1000, ZCritical(0.025))
	near(t, "Newcombe 97.5% low", got.Low, -0.002047869310340629, 1e-12)
	near(t, "Newcombe 97.5% high", got.High, 0.062142045084441606, 1e-12)
	if got := NewcombeDiff(0, 0, 1, 2, z); got != (Interval{-1, 1}) {
		t.Errorf("NewcombeDiff with an empty arm = %v, want [-1, 1]", got)
	}
}

func TestTwoProportionZ(t *testing.T) {
	for _, c := range []struct {
		x1, n1, x2, n2 int64
		z, p           float64
	}{
		{100, 1000, 130, 1000, 2.102740605622114, 0.03548845046647471},
		{56, 70, 48, 80, -2.6501719512585336, 0.00804508136819656},
		{200, 10000, 260, 10000, 2.830251652793251, 0.004651140450960617},
	} {
		z, p := TwoProportionZ(c.x1, c.n1, c.x2, c.n2)
		near(t, "z", z, c.z, 1e-12)
		nearRel(t, "p", p, c.p, 1e-10)
	}
	for _, c := range [][4]int64{{0, 10, 0, 20}, {10, 10, 20, 20}, {0, 0, 1, 2}} {
		if z, p := TwoProportionZ(c[0], c[1], c[2], c[3]); z != 0 || p != 1 {
			t.Errorf("TwoProportionZ%v = %v, %v; want 0, 1", c, z, p)
		}
	}
}
