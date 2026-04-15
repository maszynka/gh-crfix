// Package score provides scoring utilities (sum, normalize, validate).
package score

import (
	"fmt"
	"regexp"
)

var validScoreRe = regexp.MustCompile(`^(0(\.[0-9]+)?|1(\.0+)?|\.[0-9]+)$`)

// IsValidScore reports whether s is a valid score in the range [0, 1].
func IsValidScore(s string) bool {
	return validScoreRe.MatchString(s)
}

// Sum returns the sum of all provided scores.
func Sum(scores ...float64) float64 {
	var total float64
	for _, v := range scores {
		total += v
	}
	return total
}

// Normalize formats v as a "%.3f" string.
func Normalize(v float64) string {
	return fmt.Sprintf("%.3f", v)
}

// AtLeastOne reports whether total >= 1.0.
func AtLeastOne(total float64) bool {
	return total >= 1.0
}
