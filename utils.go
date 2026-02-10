package rely

import (
	"strings"

	"github.com/nbd-wtf/go-nostr"
)

// normalizeURL removes the protocol scheme (e.g., "https://") if present,
// returning only the host and path (e.g., "example.com/abc").
func normalizeURL(url string) string {
	url = strings.TrimSpace(url)
	url = strings.TrimSuffix(url, "/")
	index := strings.Index(url, "://")
	if index != -1 {
		return url[index+3:]
	}
	return url
}

// ApplyBudget adjusts the Limit of each filter in-place so that the total does not exceed the given budget.
// Filters with limits <= budget / len(filters) are preserved, while larger ones are scaled down proportionally.
// It panics if budget is negative.
func ApplyBudget(budget int, filters ...nostr.Filter) {
	if budget < 0 {
		panic("rely.ApplyBudget: budget should not be negative")
	}

	if len(filters) == 0 {
		return
	}

	if budget < len(filters) {
		// give 1 to as many filters as possible, set the rest to zero
		for i := range filters {
			if i < budget {
				filters[i].Limit = 1
			} else {
				filters[i].Limit = 0
				filters[i].LimitZero = true
			}
		}
		return
	}

	used := 0
	for i := range filters {
		if filters[i].LimitZero {
			// ensure consistency
			filters[i].Limit = 0
		}

		if !filters[i].LimitZero && filters[i].Limit < 1 {
			// limit is unspecified (or negative), so we set it equal to the budget
			filters[i].Limit = budget
		}

		used += filters[i].Limit
	}

	if used > budget {
		// modify filters based on whether they have a limit lower or higher than budget / len(filters).
		// 	- lowers: do nothing
		//	- highers: linearly scale their limit

		fair := budget / len(filters)
		var sumHighers int
		var highers []int

		for i := range filters {
			limit := filters[i].Limit
			if limit > fair {
				highers = append(highers, i)
				sumHighers += limit
			} else {
				budget -= limit
			}
		}

		scalingFactor := float64(budget) / float64(sumHighers)
		for _, idx := range highers {
			limit := float64(filters[idx].Limit)
			newLimit := int(scalingFactor * limit)

			if newLimit == 0 {
				filters[idx].Limit = 0
				filters[idx].LimitZero = true
			} else {
				filters[idx].Limit = newLimit
			}
		}
	}
}
