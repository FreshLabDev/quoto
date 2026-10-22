// SPDX-License-Identifier: Apache-2.0

package claude

// Counts is the token counts of one model call.
type Counts struct {
	Input         int
	Output        int
	CacheCreation int // cache writes, five-minute TTL
	CacheRead     int
}

// Prompt is every token that went in: fresh input plus cache writes and reads.
func (c Counts) Prompt() int { return c.Input + c.CacheCreation + c.CacheRead }

// Iteration is one model run inside a call. With a server-side fallback the
// declined attempt and the answering one are two iterations, each with the
// model that ran it.
type Iteration struct {
	Type  string
	Model string
	Counts
}

// Usage is what a call consumed. Counts covers the attempt that answered (the
// top level of the API's usage object); Iterations, when present, lists every
// attempt, the declined one included.
type Usage struct {
	Counts
	// ThinkingTokens is output_tokens_details.thinking_tokens; nil when the API
	// did not say.
	ThinkingTokens *int
	Iterations     []Iteration
}

// The API reports tokens, not dollars, so cost is computed from list prices, in
// USD per million tokens. A model missing from the table has no cost.
type price struct{ in, out float64 }

var prices = map[string]price{
	"claude-opus-5-5":   {4, 20},
	"claude-sonnet-5-5": {2, 10},
	// Where the server-side fallback can send a declined request.
	"claude-opus-5":   {5, 25},
	"claude-opus-4-8": {5, 25},
	"claude-sonnet-5": {2, 10},
}

const (
	cacheWriteMultiplier = 1.25
	cacheReadMultiplier  = 0.1
)

// cacheReadMultipliers: Opus 5.5 reads its cache at half the usual share.
var cacheReadMultipliers = map[string]float64{"claude-opus-5-5": 0.05}

// Cost is the price of one set of counts on a model, in USD.
func Cost(model string, c Counts) (cost float64, ok bool) {
	p, ok := prices[model]
	if !ok {
		return 0, false
	}
	read, ok := cacheReadMultipliers[model]
	if !ok {
		read = cacheReadMultiplier
	}
	// Same grouping as the Python expression, so the floats come out the same.
	return (float64(c.Input)*p.in +
		float64(c.CacheCreation)*p.in*cacheWriteMultiplier +
		float64(c.CacheRead)*p.in*read +
		float64(c.Output)*p.out) / 1_000_000, true
}

// Cost is the price of the whole call. With iterations each is priced by its
// own model, so the declined attempt is paid for as well as the answer; one
// unpriced model makes the whole cost unknown. Without iterations the top-level
// counts are priced on requested, the model the caller asked for.
func (u Usage) Cost(requested string) (cost float64, ok bool) {
	if len(u.Iterations) == 0 {
		return Cost(requested, u.Counts)
	}
	for _, it := range u.Iterations {
		model := it.Model
		if model == "" {
			model = requested
		}
		c, ok := Cost(model, it.Counts)
		if !ok {
			return 0, false
		}
		cost += c
	}
	return cost, true
}
