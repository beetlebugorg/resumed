package web

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Pay ranges in postings take several forms. Each one below is taken from a
// posting in the tracker:
//
//	$181,000 - $226,000 USD annually
//	$230,000 –$260,000
//	$300,000- $385,000 + Bonus
//	$185,000 to 215,000
//	$218K/yr - $256.5K/yr
//	US Tier 1 - T4$184K – $276K
//	$135k - $195k
//
// The second amount is allowed to drop its currency symbol, the separator may
// be a hyphen, either dash, or the word "to", and the gap before the separator
// may hold "/yr", "USD", or similar.
var payRe = regexp.MustCompile(`(?i)([$£€])\s?([0-9][0-9,]*(?:\.[0-9]+)?)\s*([km])?\b` +
	`[^0-9\n$£€]{0,16}?(?:-|–|—|\bto\b)\s{0,3}` +
	`[$£€]?\s?([0-9][0-9,]*(?:\.[0-9]+)?)\s*([km])?\b`)

// Postings quote money for other reasons: a $500 home office stipend, $60 a
// month toward fitness, $100M raised in a Series C. Only amounts inside this
// band are read as a salary.
const (
	payFloor   = 20_000
	payCeiling = 2_000_000
)

// payWords mark a range as compensation rather than some other pair of
// numbers. A posting quoting customer pricing in the same band reads as a
// salary without them.
var payWords = regexp.MustCompile(`(?i)salary|compensation|base pay|pay range|per year|a year|annually|/yr|\bOTE\b`)

// Some postings quote one figure rather than a range: "The salary for this
// role is $190k USD". A lone amount is far likelier to be a bonus or a
// stipend, so it counts only next to one of the words that names pay outright.
var loneAmountRe = regexp.MustCompile(`(?i)([$£€])\s?([0-9][0-9,]*(?:\.[0-9]+)?)\s*([km])?\b`)

var strictPayWords = regexp.MustCompile(`(?i)salary|compensation|base pay|pay range|\bOTE\b`)

// loneLookback is tighter than payLookback, for the same reason.
const loneLookback = 120

// payLookback is how far before a range the text is searched for those words.
const payLookback = 220

// salaryRange returns a posting's pay range formatted for the sidebar, or ""
// when the posting quotes none. A range near one of the pay words wins over an
// earlier range with none near it.
func salaryRange(description string) string {
	if description == "" {
		return ""
	}
	var fallback string
	// malformed records a range-shaped phrase the amounts failed. The posting
	// states pay as a range, so reading one figure out of it is worse than
	// reading none.
	var malformed bool
	for _, m := range payRe.FindAllStringSubmatchIndex(description, -1) {
		g := func(i int) string {
			if m[2*i] < 0 {
				return ""
			}
			return description[m[2*i]:m[2*i+1]]
		}
		low, lowOK := payAmount(g(2), g(3))
		high, highOK := payAmount(g(4), g(5))
		if lowOK && highOK && high < low {
			malformed = true
			continue
		}
		if !lowOK || !highOK {
			continue
		}
		text := formatPay(g(1), low, high)
		from := m[0] - payLookback
		if from < 0 {
			from = 0
		}
		if payWords.MatchString(description[from:m[1]]) {
			return text
		}
		if fallback == "" {
			fallback = text
		}
	}
	if fallback != "" {
		return fallback
	}
	if malformed {
		return ""
	}
	return loneSalary(description)
}

// loneSalary reads a single quoted figure, used when the posting states no
// range at all.
func loneSalary(description string) string {
	for _, m := range loneAmountRe.FindAllStringSubmatchIndex(description, -1) {
		g := func(i int) string {
			if m[2*i] < 0 {
				return ""
			}
			return description[m[2*i]:m[2*i+1]]
		}
		v, ok := payAmount(g(2), g(3))
		if !ok {
			continue
		}
		from := m[0] - loneLookback
		if from < 0 {
			from = 0
		}
		if strictPayWords.MatchString(description[from:m[1]]) {
			return g(1) + payShort(v)
		}
	}
	return ""
}

// payAmount reads one amount and reports whether it lands in the salary band.
func payAmount(num, suffix string) (int, bool) {
	v, err := strconv.ParseFloat(strings.ReplaceAll(num, ",", ""), 64)
	if err != nil {
		return 0, false
	}
	switch strings.ToLower(suffix) {
	case "k":
		v *= 1_000
	case "m":
		v *= 1_000_000
	}
	n := int(v)
	return n, n >= payFloor && n <= payCeiling
}

// formatPay writes the range at sidebar width, with the currency stated once.
func formatPay(currency string, low, high int) string {
	return fmt.Sprintf("%s%s–%s", currency, payShort(low), payShort(high))
}

func payShort(v int) string {
	switch {
	case v >= 1_000_000:
		return trimPoint(float64(v)/1_000_000) + "M"
	case v >= 1_000:
		return trimPoint(float64(v)/1_000) + "k"
	}
	return strconv.Itoa(v)
}

// trimPoint keeps one decimal where it matters, so 256500 reads as 256.5 and
// 135000 reads as 135.
func trimPoint(f float64) string {
	s := strconv.FormatFloat(f, 'f', 1, 64)
	return strings.TrimSuffix(s, ".0")
}
