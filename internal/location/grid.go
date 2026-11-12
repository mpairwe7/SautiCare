// Package location places incidents on Sauti's own grid (Build Prompt §12):
// 500 m cells in UTM zone 36N (EPSG:32636), indexed by column and row and
// computed in Go, with search rings measured in Chebyshev distance. No
// external GIS service is involved.
package location

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

// ErrOutsideGrid reports a coordinate outside the area the grid serves
// (which also catches swapped latitude and longitude).
var ErrOutsideGrid = errors.New("location: coordinate outside the UTM 36N grid area")

// CellSize is the grid spacing in metres.
const CellSize = 500.0

// WGS84 and UTM zone 36N. EPSG:32636 has no false northing, so points south
// of the equator have negative northings (and negative rows).
const (
	wgs84A  = 6378137.0
	wgs84F  = 1 / 298.257223563
	k0      = 0.9996
	falseE  = 500000.0
	lon0Deg = 33.0
	minLat  = -5.0
	maxLat  = 6.0
	minLon  = 28.0
	maxLon  = 38.0
)

// Krüger series coefficients to sixth order in n (Karney 2011), accurate to
// well under a millimetre across the zone.
var (
	tmN     = wgs84F / (2 - wgs84F)
	tmA     = wgs84A / (1 + tmN) * (1 + tmN*tmN/4 + math.Pow(tmN, 4)/64 + math.Pow(tmN, 6)/256)
	tmAlpha = func(n float64) [6]float64 {
		n2, n3, n4, n5, n6 := n*n, n*n*n, math.Pow(n, 4), math.Pow(n, 5), math.Pow(n, 6)
		return [6]float64{
			n/2 - 2*n2/3 + 5*n3/16 + 41*n4/180 - 127*n5/288 + 7891*n6/37800,
			13*n2/48 - 3*n3/5 + 557*n4/1440 + 281*n5/630 - 1983433*n6/1935360,
			61*n3/240 - 103*n4/140 + 15061*n5/26880 + 167603*n6/181440,
			49561*n4/161280 - 179*n5/168 + 6601661*n6/7257600,
			34729*n5/80640 - 3418889*n6/1995840,
			212378941 * n6 / 319334400,
		}
	}(tmN)
)

// ToUTM36N converts a WGS84 latitude and longitude (degrees) to UTM zone 36N
// easting and northing (metres).
func ToUTM36N(lat, lon float64) (easting, northing float64, err error) {
	if !(lat >= minLat && lat <= maxLat && lon >= minLon && lon <= maxLon) {
		return 0, 0, fmt.Errorf("%w: lat %v, lon %v", ErrOutsideGrid, lat, lon)
	}
	phi := lat * math.Pi / 180
	dl := (lon - lon0Deg) * math.Pi / 180
	c := 2 * math.Sqrt(tmN) / (1 + tmN)
	t := math.Sinh(math.Atanh(math.Sin(phi)) - c*math.Atanh(c*math.Sin(phi)))
	xi := math.Atan2(t, math.Cos(dl))
	eta := math.Atanh(math.Sin(dl) / math.Sqrt(1+t*t))
	e, n := eta, xi
	for j, a := range tmAlpha {
		k := 2 * float64(j+1)
		e += a * math.Cos(k*xi) * math.Sinh(k*eta)
		n += a * math.Sin(k*xi) * math.Cosh(k*eta)
	}
	return falseE + k0*tmA*e, k0 * tmA * n, nil
}

// Cell is one 500 m grid cell.
type Cell struct{ Col, Row int }

func cellOf(easting, northing float64) Cell {
	return Cell{Col: int(math.Floor(easting / CellSize)), Row: int(math.Floor(northing / CellSize))}
}

// CellAt returns the cell containing a WGS84 point.
func CellAt(lat, lon float64) (Cell, error) {
	e, n, err := ToUTM36N(lat, lon)
	if err != nil {
		return Cell{}, err
	}
	return cellOf(e, n), nil
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// Chebyshev is the ring distance between two cells: ring 1 is the eight
// adjacent cells, ring 5 about 2.5 km.
func (c Cell) Chebyshev(o Cell) int { return max(abs(c.Col-o.Col), abs(c.Row-o.Row)) }

// Within returns every cell at Chebyshev distance at most k from centre,
// nearest first (ties by row, then column), starting with centre itself.
func Within(centre Cell, k int) []Cell {
	if k < 0 {
		return nil
	}
	out := make([]Cell, 0, (2*k+1)*(2*k+1))
	for r := centre.Row - k; r <= centre.Row+k; r++ {
		for col := centre.Col - k; col <= centre.Col+k; col++ {
			out = append(out, Cell{Col: col, Row: r})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		di, dj := centre.Chebyshev(out[i]), centre.Chebyshev(out[j])
		if di != dj {
			return di < dj
		}
		if out[i].Row != out[j].Row {
			return out[i].Row < out[j].Row
		}
		return out[i].Col < out[j].Col
	})
	return out
}
