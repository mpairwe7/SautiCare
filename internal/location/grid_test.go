package location

import (
	"errors"
	"math"
	"testing"
)

// Reference values from pyproj 3.7.2 / PROJ 9.5.1, EPSG:4326 → EPSG:32636
// (UTM zone 36N: no false northing, so south of the equator is negative).
var vectors = []struct {
	name     string
	lat, lon float64
	e, n     float64
}{
	{"Kawempe", 0.38, 32.56, 451039.6015, 42002.6705},
	{"Kampala centre", 0.3136, 32.5811, 453387.1935, 34663.1586},
	{"Mulago", 0.3383, 32.5767, 452897.6924, 37393.3463},
	{"Equator 33E", 0.0, 33.0, 500000.0000, 0.0000},
	{"South of equator", -0.25, 32.7, 466617.6732, -27632.8946},
	{"Zone west edge", 0.5, 30.0001, 166045.2407, 55341.3831},
	{"Zone east edge", -1.0, 35.9999, 833916.7959, -110682.8276},
	{"North", 3.0, 34.5, 666704.2923, 331707.4045},
}

func TestToUTM36NMatchesReferenceVectors(t *testing.T) {
	for _, v := range vectors {
		e, n, err := ToUTM36N(v.lat, v.lon)
		if err != nil {
			t.Fatalf("%s: %v", v.name, err)
		}
		if math.Abs(e-v.e) > 0.001 || math.Abs(n-v.n) > 0.001 {
			t.Errorf("%s: got (%.4f, %.4f), want (%.4f, %.4f)", v.name, e, n, v.e, v.n)
		}
	}
}

// Swapped coordinates or a point far outside Uganda's grid are errors, never
// a silently wrong zone.
func TestToUTM36NRejectsOutsideZone(t *testing.T) {
	for _, p := range [][2]float64{{32.56, 0.38}, {0.38, 40}, {0.38, 20}, {12, 33}, {-9, 33}, {math.NaN(), 33}} {
		if _, _, err := ToUTM36N(p[0], p[1]); !errors.Is(err, ErrOutsideGrid) {
			t.Errorf("ToUTM36N(%v, %v) err = %v, want ErrOutsideGrid", p[0], p[1], err)
		}
	}
}

func TestCellAtBoundaries(t *testing.T) {
	c, err := CellAt(0.38, 32.56) // E 451039.6, N 42002.7
	if err != nil {
		t.Fatal(err)
	}
	if c != (Cell{Col: 902, Row: 84}) {
		t.Fatalf("Kawempe cell = %+v", c)
	}
	s, err := CellAt(-0.25, 32.7) // N −27632.9 → row −56 (floor, not truncation)
	if err != nil {
		t.Fatal(err)
	}
	if s != (Cell{Col: 933, Row: -56}) {
		t.Fatalf("south cell = %+v", s)
	}
	if got := cellOf(500, 999.999); got != (Cell{Col: 1, Row: 1}) {
		t.Fatalf("cellOf boundary = %+v", got)
	}
	if got := cellOf(499.999, -0.001); got != (Cell{Col: 0, Row: -1}) {
		t.Fatalf("cellOf negative boundary = %+v", got)
	}
}

func TestChebyshev(t *testing.T) {
	a := Cell{Col: 10, Row: 10}
	for _, tc := range []struct {
		b    Cell
		want int
	}{{a, 0}, {Cell{11, 11}, 1}, {Cell{9, 10}, 1}, {Cell{13, 8}, 3}, {Cell{10, 15}, 5}, {Cell{5, -2}, 12}} {
		if got := a.Chebyshev(tc.b); got != tc.want || tc.b.Chebyshev(a) != tc.want {
			t.Errorf("Chebyshev(%v, %v) = %d, want %d", a, tc.b, got, tc.want)
		}
	}
}

// Ring 1 = adjacent cells; ring 5 ≈ 2.5 km (Build Prompt §12).
func TestWithinCountsAndOrder(t *testing.T) {
	c := Cell{Col: 902, Row: 84}
	for k, want := range map[int]int{0: 1, 1: 9, 5: 121} {
		cells := Within(c, k)
		if len(cells) != want {
			t.Fatalf("Within(k=%d) = %d cells, want %d", k, len(cells), want)
		}
		prev := -1
		for _, x := range cells {
			d := c.Chebyshev(x)
			if d > k || d < prev {
				t.Fatalf("Within(k=%d): %v at distance %d after %d", k, x, d, prev)
			}
			prev = d
		}
		if cells[0] != c {
			t.Fatalf("Within(k=%d) does not start at the centre", k)
		}
	}
	if Within(c, -1) != nil {
		t.Fatal("negative k must give no cells")
	}
}
