package discovery

import (
	"strings"

	"omnicatalog/internal/omniroute"
)

// Free-model classification (OmniRoute v3.8.53).
//
// OmniRoute publishes a documented free-model catalog at
// GET /api/free-tier/summary (see FreeTierModel) with a freeType per model:
//
//	recurring-daily, recurring-monthly, recurring-uncapped, recurring-credit,
//	keyless                   -> admitted as "documented" free
//	one-time-initial           -> cataloged, not admitted (signup credit only)
//	discontinued               -> cataloged, not admitted
//
// omnicatalog classifies each discovered model deterministically at sync
// time — no request is sent to the provider to guess whether it is free.
// Precedence: documented catalog > provider connection flag
// (importFreeModelsOnly) > inferred (zero pricing or free id suffix) > not free.
// Only "documented" and "connection" are admitted into probes and combos;
// "inferred" models are cataloged, labeled "(free, inferred)" and stay out
// of combos unless free_validation.inferred_in_combos is set.

const (
	FreeSourceDocumented = "documented"
	FreeSourceConnection = "connection"
	FreeSourceInferred   = "inferred"
)

// AdmittedFreeTypes are the catalog freeTypes that grant recurring or
// keyless free access. one-time-initial and discontinued are excluded.
var admittedFreeTypes = map[string]bool{
	"recurring-daily":    true,
	"recurring-monthly":  true,
	"recurring-uncapped": true,
	"recurring-credit":   true,
	"keyless":            true,
}

// freeInfo carries the classification outcome for one model.
type freeInfo struct {
	IsFree   bool
	Source   string
	Type     string // catalog freeType when the model matches a catalog entry (informational)
	PoolKey  string
	Admitted bool // source is documented or connection (probes + combos)
}

// freeCatalog is a lookup structure built once per sync from the free-tier
// catalog and the provider alias map (/api/pricing/models).
type freeCatalog struct {
	// provider slug (canonical or alias) -> catalog entries, expanded so a
	// model-list prefix like "kc" can find entries filed under "kilocode".
	byProvider map[string][]omniroute.FreeTierModel
	// alias -> canonical slug (from /api/pricing/models)
	alias2canon map[string]string
	// canonical slug -> aliases
	canon2aliases map[string][]string

	CatalogUpdatedAt string
	CatalogSource    string
	ModelCount       int
}

// buildFreeCatalog expands catalog keys over the alias map: entries filed
// under a canonical slug are also reachable through every known alias, and
// vice-versa (model lists expose either prefix depending on the connection).
func buildFreeCatalog(summary *omniroute.FreeTierSummary, pricingModels map[string]omniroute.PricingModelsProvider) *freeCatalog {
	fc := &freeCatalog{
		byProvider:    map[string][]omniroute.FreeTierModel{},
		alias2canon:   map[string]string{},
		canon2aliases: map[string][]string{},
	}
	if summary != nil {
		fc.CatalogUpdatedAt = summary.CatalogUpdatedAt
		fc.CatalogSource = summary.CatalogSource
		fc.ModelCount = summary.ModelCount
		for _, e := range summary.PerModel {
			fc.byProvider[e.Provider] = append(fc.byProvider[e.Provider], e)
		}
	}
	for alias, slot := range pricingModels {
		canonical := slot.ID
		if canonical == "" {
			canonical = alias
		}
		fc.alias2canon[alias] = canonical
		fc.canon2aliases[canonical] = append(fc.canon2aliases[canonical], alias)
	}
	// Expand keys: for each catalog provider, also register its entries
	// under the aliases of that provider so prefix matching works.
	for provider, entries := range fc.byProvider {
		c := fc.canonicalFor(provider)
		for _, alias := range fc.canon2aliases[c] {
			if alias == provider {
				continue
			}
			fc.byProvider[alias] = append(fc.byProvider[alias], entries...)
		}
	}
	return fc
}

func (f *freeCatalog) canonicalFor(prefix string) string {
	if c, ok := f.alias2canon[prefix]; ok {
		return c
	}
	return prefix
}

// prefixCandidates returns every provider slug a model-list prefix may map
// to: itself, its canonical slug and every alias of that canonical slug.
func (f *freeCatalog) prefixCandidates(p0 string) []string {
	out := []string{p0}
	c := f.canonicalFor(p0)
	if c != p0 {
		out = append(out, c)
	}
	for _, a := range f.canon2aliases[c] {
		if a != p0 && a != c {
			out = append(out, a)
		}
	}
	return out
}

// match finds the catalog entry for a model id of the form "<prefix>/<rest>".
// Matching is boundary-aware: the catalog modelId may carry its own path
// segments (e.g. opencode-zen files "opencode/big-pickle" while the model
// list calls it "opencode-zen/big-pickle").
func (f *freeCatalog) match(modelID string) (omniroute.FreeTierModel, bool) {
	if f == nil {
		return omniroute.FreeTierModel{}, false
	}
	prefix, rest, found := strings.Cut(modelID, "/")
	if !found {
		return omniroute.FreeTierModel{}, false
	}
	for _, p := range f.prefixCandidates(prefix) {
		for _, e := range f.byProvider[p] {
			mid := e.ModelID
			if rest == mid || strings.HasSuffix(mid, "/"+rest) || strings.HasSuffix(rest, "/"+mid) {
				return e, true
			}
		}
	}
	return omniroute.FreeTierModel{}, false
}

// classifyModel decides whether a discovered model is free and how it may be
// admitted. pricingZero comes from the client's normalized pricing (explicit
// zero in either upstream shape); suffixFree covers ids ending in :free/-free
// or providers ending in -free.
func classifyModel(modelID string, providerFreeTier, pricingZero, suffixFree bool, cat *freeCatalog) freeInfo {
	if entry, ok := cat.match(modelID); ok {
		if admittedFreeTypes[entry.FreeType] {
			return freeInfo{IsFree: true, Source: FreeSourceDocumented, Type: entry.FreeType, PoolKey: entry.PoolKey, Admitted: true}
		}
		// Matched but not admitted (one-time-initial / discontinued): record
		// the evidence, stay out as not-free.
		return freeInfo{Type: entry.FreeType, PoolKey: entry.PoolKey}
	}
	if providerFreeTier {
		return freeInfo{IsFree: true, Source: FreeSourceConnection, Type: "connection", Admitted: true}
	}
	if pricingZero || suffixFree {
		return freeInfo{IsFree: true, Source: FreeSourceInferred, Type: "inferred"}
	}
	return freeInfo{}
}

// hasFreeSuffix reports whether the model id (or its provider prefix) ends
// with an explicit free marker (:free, -free, *-free).
func hasFreeSuffix(modelID string) bool {
	prefix, _, _ := strings.Cut(modelID, "/")
	if strings.HasSuffix(prefix, "-free") {
		return true
	}
	last := modelID
	if i := strings.LastIndex(modelID, "/"); i >= 0 {
		last = modelID[i+1:]
	}
	return strings.HasSuffix(last, ":free") || strings.HasSuffix(last, "-free")
}
