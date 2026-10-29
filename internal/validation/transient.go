package validation

import (
	"strings"

	"omnicatalog/internal/store"
)

// isTransientError reports whether an error is likely temporary.
func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	transientIndicators := []string{
		"rate_limit", "rate limit", "cooling down", "cooldown", "retry_after", "reset after",
		"timeout", "deadline exceeded", "context canceled",
		"502", "503", "504", "429", "408", "524",
		"connection reset", "connection refused", "eof", "temporarily unavailable", "server error",
	}
	for _, ind := range transientIndicators {
		if strings.Contains(msg, ind) {
			return true
		}
	}
	return false
}

// groupByProvider groups models by ProviderID.
func groupByProvider(models []store.Model) map[string][]store.Model {
	out := make(map[string][]store.Model)
	for _, m := range models {
		out[m.ProviderID] = append(out[m.ProviderID], m)
	}
	return out
}
