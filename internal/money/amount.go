// Package money provides an exact monetary amount for budgets and charges.
package money

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Amount is an exact USD value in nano-dollars, enough for per-token provider prices.
// It is stored as NUMERIC in PostgreSQL and never passes through float64.
type Amount int64

const (
	scale     = 1_000_000_000
	maxDigits = 9
)

var errRange = errors.New("money: amount out of range")

// Parse reads a non-negative plain decimal such as "0.50" or "0.000001234",
// rejecting signs, exponents and more than nine fractional digits.
func Parse(s string) (Amount, error) {
	whole, frac, hasDot := strings.Cut(s, ".")
	if !isDigits(whole) || (hasDot && !isDigits(frac)) {
		return 0, fmt.Errorf("money: invalid amount %q", s)
	}
	if len(frac) > maxDigits {
		return 0, fmt.Errorf("money: amount %q has more than %d fractional digits", s, maxDigits)
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	f, _ := strconv.ParseInt(frac+strings.Repeat("0", maxDigits-len(frac)), 10, 64)
	if err != nil || w > (math.MaxInt64-f)/scale {
		return 0, errRange
	}
	return Amount(w*scale + f), nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// ParseCost converts a provider-reported JSON cost, which may use exponent notation,
// rounding half up to the nearest nano-dollar. Negative costs are rejected.
func ParseCost(n json.Number) (Amount, error) {
	return scaledAmount(string(n), 1)
}

// ParsePerMillion converts a per-token price such as "0.0000003" (exponent notation allowed) into the exact
// price of one million tokens, rounding half up to the nearest nano-dollar. Negative prices are rejected.
func ParsePerMillion(s string) (Amount, error) {
	return scaledAmount(s, 1_000_000)
}

// scaledAmount reads a non-negative decimal and returns s*factor in nano-dollars, computed with big.Rat.
func scaledAmount(s string, factor int64) (Amount, error) {
	r, ok := new(big.Rat).SetString(s)
	if !ok || r.Sign() < 0 {
		return 0, fmt.Errorf("money: invalid cost %q", s)
	}
	r.Mul(r, big.NewRat(scale*factor, 1))
	q, m := new(big.Int).QuoRem(r.Num(), r.Denom(), new(big.Int))
	if m.Lsh(m, 1).Cmp(r.Denom()) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return 0, errRange
	}
	return Amount(q.Int64()), nil
}

// String formats the amount as a decimal USD string without trailing zeros.
func (a Amount) String() string {
	sign, v := "", uint64(a) //nolint:gosec // negative values are handled below; -MinInt64 converts to its magnitude
	if a < 0 {
		sign, v = "-", uint64(-a)
	}
	whole, frac := v/scale, v%scale
	if frac == 0 {
		return sign + strconv.FormatUint(whole, 10)
	}
	f := strings.TrimRight(fmt.Sprintf("%09d", frac), "0")
	return sign + strconv.FormatUint(whole, 10) + "." + f
}

// MarshalText encodes the amount as its decimal string, so JSON carries it without float rounding.
func (a Amount) MarshalText() ([]byte, error) {
	return []byte(a.String()), nil
}

// UnmarshalYAML decodes a YAML scalar through its literal text rather than a float.
func (a *Amount) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: money amount must be a scalar", value.Line)
	}
	v, err := Parse(value.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", value.Line, err)
	}
	*a = v
	return nil
}

// UnmarshalText is the inverse of MarshalText; unlike Parse it accepts a leading minus,
// because derived values such as remaining budget may be negative.
func (a *Amount) UnmarshalText(b []byte) error {
	s, neg := strings.CutPrefix(string(b), "-")
	v, err := Parse(s)
	if err != nil {
		return err
	}
	if neg {
		v = -v
	}
	*a = v
	return nil
}
