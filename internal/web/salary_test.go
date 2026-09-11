package web

import "testing"

// Every input here is lifted from a posting in the tracker.
func TestSalaryRangeReadsRealPostings(t *testing.T) {
	for _, c := range []struct{ name, posting, want string }{
		{"full numbers", "Compensation: $181,000 - $226,000 USD annually", "$181k–226k"},
		{"en dash, no space", "The national pay range for this full-time position is base salary of $230,000 –$260,000.", "$230k–260k"},
		{"hyphen on the first", "Salary Range: Base Salary Range $300,000- $385,000 + Bonus + Stock Equity.", "$300k–385k"},
		{"the word to", "- Base Salary Hiring Range: $185,000 to 215,000", "$185k–215k"},
		{"per-year suffixes", "$218K/yr - $256.5K/yr", "$218k–256.5k"},
		{"tier prefix", "Compensation\n\nUS Tier 1 - T4$184K – $276K", "$184k–276k"},
		{"lowercase k", "Full-time • US - Remote • $135k - $195k", "$135k–195k"},
		{"a year", "Compensation & Benefits:\n$207,100 - $243,691 a year", "$207.1k–243.7k"},
		{"plain range", "The annual US base salary range for this role is $280,000 - $330,000.", "$280k–330k"},
	} {
		if got := salaryRange(c.posting); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// Postings quote money for many reasons. None of these is a salary.
func TestSalaryRangeIgnoresOtherMoney(t *testing.T) {
	for _, c := range []struct{ name, posting string }{
		{"stipend", "Competitive compensation and early equity. $500 stipend for home office setup."},
		{"monthly perk", "Wellness on your terms: get up to $60/month toward fitness or mental health."},
		{"tech allowance", "Technology stipend equivalent to $100 net/month"},
		{"funding round", "In February 2026, we raised an additional $100M in Series C funding."},
		{"development budget", "Base salary, equity, $1,500 annual professional development allowance."},
		{"no money at all", "We offer a competitive salary, equity, and benefits package."},
		{"empty", ""},
	} {
		if got := salaryRange(c.posting); got != "" {
			t.Errorf("%s: got %q, want none", c.name, got)
		}
	}
}

// A pay word near the range beats an earlier pair of numbers in the same band.
func TestSalaryRangePrefersTheCompensationSection(t *testing.T) {
	posting := "Our customers spend $40,000 - $90,000 a month on cloud.\n\n" +
		"Compensation: the base salary range is $190,000 - $240,000."
	if got := salaryRange(posting); got != "$190k–240k" {
		t.Errorf("got %q, want %q", got, "$190k–240k")
	}
}

// With no pay word anywhere, the first range inside the band is the best guess
// available.
func TestSalaryRangeFallsBackToTheFirstRange(t *testing.T) {
	if got := salaryRange("The band for this level is $150,000 - $180,000."); got != "$150k–180k" {
		t.Errorf("got %q, want %q", got, "$150k–180k")
	}
}

// A posting that quotes the same figure twice, once abbreviated and once in
// full, resolves to the same numbers either way.
func TestSalaryRangeAgreesWithItself(t *testing.T) {
	abbrev := salaryRange("$218K/yr - $256.5K/yr")
	full := salaryRange("Compensation: $218,025 – $256,500 annually")
	if abbrev != "$218k–256.5k" || full != "$218k–256.5k" {
		t.Errorf("abbrev %q, full %q", abbrev, full)
	}
}

// A backwards range is a posting whose pay phrase failed to parse. Reading one
// figure out of it is worse than reading none.
func TestSalaryRangeRejectsABackwardsRange(t *testing.T) {
	if got := salaryRange("Salary $300,000 - $200,000"); got != "" {
		t.Errorf("got %q, want none", got)
	}
}

func TestPayShort(t *testing.T) {
	for _, c := range []struct {
		v    int
		want string
	}{
		{135_000, "135k"},
		{256_500, "256.5k"},
		{243_691, "243.7k"},
		{1_200_000, "1.2M"},
		{950, "950"},
	} {
		if got := payShort(c.v); got != c.want {
			t.Errorf("payShort(%d) = %q, want %q", c.v, got, c.want)
		}
	}
}

// Fly.io states one figure because it does not negotiate.
func TestSalaryRangeReadsALoneFigure(t *testing.T) {
	posting := "Fly.io doesn't negotiate salaries. We have standardized salaries for " +
		"each employee level. The salary for this role is $190k USD, and we offer " +
		"competitive equity grants."
	if got := salaryRange(posting); got != "$190k" {
		t.Errorf("got %q, want %q", got, "$190k")
	}
}

// A lone figure needs a word that names pay outright. "annually" attaches to
// any amount, so it is not enough on its own.
func TestLoneFigureNeedsAPayWord(t *testing.T) {
	for _, posting := range []string{
		"We give every engineer a $60,000 hardware and travel budget annually.",
		"Customers on the enterprise plan pay $120,000 a year.",
	} {
		if got := salaryRange(posting); got != "" {
			t.Errorf("%q: got %q, want none", posting[:30], got)
		}
	}
}

// A range always wins over a lone figure elsewhere in the same posting.
func TestRangeBeatsALoneFigure(t *testing.T) {
	posting := "Base salary $220,000. Compensation: the range is $200,000 - $260,000."
	if got := salaryRange(posting); got != "$200k–260k" {
		t.Errorf("got %q, want %q", got, "$200k–260k")
	}
}
