package incident

import "errors"

// dammTable is the standard order-10 weakly totally anti-symmetric quasigroup
// used by the Damm algorithm. Row = interim digit, column = next digit.
// It detects every single-digit error and every adjacent transposition, which
// is why the incident reference uses it (ADR-0007).
var dammTable = [10][10]byte{
	{0, 3, 1, 7, 5, 9, 8, 6, 4, 2},
	{7, 0, 9, 2, 1, 5, 4, 8, 6, 3},
	{4, 2, 0, 6, 8, 7, 1, 3, 5, 9},
	{1, 7, 5, 0, 9, 8, 3, 4, 2, 6},
	{6, 1, 2, 3, 0, 4, 5, 9, 7, 8},
	{3, 6, 7, 4, 2, 0, 9, 5, 8, 1},
	{5, 8, 6, 9, 7, 2, 0, 1, 3, 4},
	{8, 9, 4, 5, 3, 6, 2, 0, 1, 7},
	{9, 4, 3, 8, 6, 1, 7, 2, 0, 5},
	{2, 5, 8, 1, 4, 3, 6, 7, 9, 0},
}

var errNotDigits = errors.New("damm: input must be ASCII digits")

func dammInterim(digits string) (byte, error) {
	var interim byte
	for i := range len(digits) {
		c := digits[i]
		if c < '0' || c > '9' {
			return 0, errNotDigits
		}
		interim = dammTable[interim][c-'0']
	}
	return interim, nil
}

// DammCheckDigit returns the ASCII check digit for a string of ASCII digits.
func DammCheckDigit(digits string) (byte, error) {
	if digits == "" {
		return 0, errNotDigits
	}
	interim, err := dammInterim(digits)
	if err != nil {
		return 0, err
	}
	return '0' + interim, nil
}

// DammValid reports whether digits (body followed by its check digit) is valid.
func DammValid(digits string) bool {
	if len(digits) < 2 {
		return false
	}
	interim, err := dammInterim(digits)
	return err == nil && interim == 0
}
