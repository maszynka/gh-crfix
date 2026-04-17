// Package input parses PR number specifications (URLs, ranges, lists).
package input

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	urlRe       = regexp.MustCompile(`github\.com/([^/]+/[^/]+)/pull/(.*)`)
	rangeRe     = regexp.MustCompile(`^\d+-\d+$`)
	commaListRe = regexp.MustCompile(`^\d+(,\d+)+$`)
	singleRe    = regexp.MustCompile(`^\d+$`)
)

// ParseURL parses a GitHub PR URL and returns the owner/repo and PR numbers.
// It handles single numbers, ranges (e.g. 1-3), comma-separated lists, and
// bracket-enclosed lists. Trailing slashes are stripped.
func ParseURL(rawURL string) (ownerRepo string, prNumbers []int, err error) {
	m := urlRe.FindStringSubmatch(rawURL)
	if m == nil {
		return "", nil, fmt.Errorf("cannot extract owner/repo from URL: %q", rawURL)
	}
	ownerRepo = m[1]
	prPart := strings.TrimRight(m[2], "/")
	prPart = strings.TrimPrefix(prPart, "[")
	prPart = strings.TrimSuffix(prPart, "]")
	prNumbers, err = parsePRPart(prPart)
	return ownerRepo, prNumbers, err
}

// ParseBare parses a bare PR number specification (no URL, no gh CLI calls).
// It handles single numbers, ranges, comma-separated lists, and bracket-enclosed lists.
func ParseBare(input string) (prNumbers []int, err error) {
	s := strings.TrimPrefix(input, "[")
	s = strings.TrimSuffix(s, "]")
	return parsePRPart(s)
}

func parsePRPart(s string) ([]int, error) {
	switch {
	case rangeRe.MatchString(s):
		parts := strings.SplitN(s, "-", 2)
		start, err1 := strconv.Atoi(parts[0])
		end, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("cannot parse PR number(s) from: %q", s)
		}
		if start > end {
			return nil, fmt.Errorf("invalid PR range %d-%d: start must be <= end", start, end)
		}
		var result []int
		for i := start; i <= end; i++ {
			result = append(result, i)
		}
		return result, nil
	case commaListRe.MatchString(s):
		parts := strings.Split(s, ",")
		var result []int
		for _, p := range parts {
			n, err := strconv.Atoi(p)
			if err != nil {
				return nil, fmt.Errorf("cannot parse PR number(s) from: %q", s)
			}
			result = append(result, n)
		}
		return result, nil
	case singleRe.MatchString(s):
		n, _ := strconv.Atoi(s)
		return []int{n}, nil
	default:
		return nil, fmt.Errorf("cannot parse PR number(s) from: %q", s)
	}
}
