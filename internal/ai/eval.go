// SPDX-License-Identifier: Apache-2.0

package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/FreshLabDev/quoto/internal/claude"
	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/openrouter"
)

// The texts the scheduler shows an operator when a run fails. They are the
// Python ones.
const (
	errNoKeys        = "No AI provider key is configured for automatic day verdict evaluation."
	errBreakerOpen   = "AI providers temporarily unavailable (circuit breaker open)."
	errEmptyResponse = "AI returned an empty response for automatic day verdict evaluation."
	errEvalFailed    = "AI evaluation failed before a valid automatic day verdict was produced."
)

type candidate struct{ provider, model string }

// evalCandidates is the chain, in order: Claude, then OpenRouter's primary and
// its fallback when that is a different model. A provider without a key is not
// in it, and neither is Claude with an empty model.
func (s *Service) evalCandidates() []candidate {
	var out []candidate
	if s.cfg.AnthropicKey != "" && s.cfg.AnthropicEvalModel != "" {
		out = append(out, candidate{providerAnthropic, s.cfg.AnthropicEvalModel})
	}
	if s.cfg.OpenRouterKey != "" {
		for _, m := range modelChain(s.cfg.OpenRouterEvalModel, s.cfg.OpenRouterEvalFallback) {
			out = append(out, candidate{providerOpenRouter, m})
		}
	}
	return out
}

// modelChain is the models in order, without empty names or repeats.
func modelChain(models ...string) []string {
	var out []string
	for _, m := range models {
		if m == "" {
			continue
		}
		dup := false
		for _, have := range out {
			dup = dup || have == m
		}
		if !dup {
			out = append(out, m)
		}
	}
	return out
}

// evalRun is one Evaluate call: the request as it is built once and sent to
// every candidate in turn.
type evalRun struct {
	s              *Service
	msgs           []domain.EvalMessage
	detectLanguage bool
	requestID      string
	system         string
	payload        string
	schema         responseSchema
	requested      string
}

// Evaluate asks the models for scores, the day verdict and the quote pick. It
// always returns a result: a run that failed has every message scored 0.5 in
// input order and DayVerdictError set, so the caller can tell a technical
// failure from a boring day.
func (s *Service) Evaluate(ctx context.Context, msgs []domain.EvalMessage, detectLanguage bool) domain.EvalResult {
	cands := s.evalCandidates()
	requested := s.cfg.OpenRouterEvalModel
	if len(cands) > 0 {
		requested = cands[0].model
	}
	if len(msgs) == 0 {
		return domain.EvalResult{ActualModel: requested, RequestedModel: requested, Status: domain.RunEmpty, Usage: &domain.TokenUsage{}}
	}
	run := &evalRun{s: s, msgs: msgs, detectLanguage: detectLanguage, requested: requested}

	if len(cands) == 0 {
		s.log.Warn("no AI provider key is configured: the day is not evaluated")
		return run.failure(domain.RunAIFailed, requested, nil, errNoKeys)
	}
	if s.breakerOpen() {
		s.log.Warn("ai circuit breaker is open: the day is not evaluated")
		return run.failure(domain.RunAIFailed, requested, nil, errBreakerOpen)
	}

	run.requestID = s.newID()
	run.payload = userPayload(msgs)
	run.system = dayPrompt + injectionGuard
	if detectLanguage {
		run.system += languageDetectionPrompt
	}
	run.schema = schemaFor(detectLanguage)

	var res domain.EvalResult
	for i, c := range cands {
		if c.provider == providerAnthropic {
			res = run.claude(ctx, c.model)
		} else {
			res = run.openRouter(ctx, c.model)
		}
		if res.Status == domain.RunParsed {
			s.breakerSuccess()
			return res
		}
		if ctx.Err() != nil {
			// The process is stopping; the providers did nothing wrong, so the
			// breaker is left alone and the rest of the chain is not tried.
			return res
		}
		if i < len(cands)-1 {
			s.log.Warn("eval model failed, trying the next", "model", c.model, "status", res.Status, "next", cands[i+1].model)
		}
	}
	s.breakerFailure()
	return res
}

// failure is a result with every message at 0.5.
func (r *evalRun) failure(status, actual string, usage *domain.TokenUsage, text string) domain.EvalResult {
	u := domain.TokenUsage{}
	if usage != nil {
		u = *usage
	}
	scores := make([]domain.Score, len(r.msgs))
	for i, m := range r.msgs {
		scores[i] = domain.Score{ID: m.ID, Value: 0.5}
	}
	return domain.EvalResult{
		Scores:          scores,
		ActualModel:     actual,
		RequestedModel:  r.requested,
		Status:          status,
		RequestID:       r.requestID,
		DayVerdictError: text,
		Usage:           &u,
	}
}

// success turns a parsed answer into the result.
func (r *evalRun) success(actual string, usage domain.TokenUsage, ev evaluation) domain.EvalResult {
	v := ev.verdict
	return domain.EvalResult{
		Scores:         ev.scores,
		ActualModel:    actual,
		RequestedModel: r.requested,
		Status:         domain.RunParsed,
		RequestID:      r.requestID,
		DayVerdict:     &v,
		QuoteChoice:    ev.quote,
		LanguageChoice: ev.language,
		Usage:          &usage,
	}
}

// ---- Claude ----

// claude sends the day to Claude, at most claudeAttempts times.
func (r *evalRun) claude(ctx context.Context, model string) domain.EvalResult {
	s := r.s
	req := claude.Request{
		Model:     model,
		MaxTokens: claudeEvalMaxTokens,
		System:    r.system,
		Messages:  []claude.Message{{Role: "user", Text: r.payload}},
		Effort:    s.cfg.AnthropicEvalEffort,
		Schema:    r.schema.claude,
	}
	body, err := req.Body()
	if err != nil {
		s.log.Error("claude eval request not built", "err", s.redact(err.Error()))
		return r.failure(domain.RunAIFailed, model, nil, errEvalFailed)
	}
	record := func(attempt int) claudeRecord {
		return claudeRecord{
			RequestID: r.requestID, Attempt: attempt + 1, Provider: providerAnthropic,
			IncludeDayVerdict: true, DetectInterfaceLanguage: r.detectLanguage, MessageCount: len(r.msgs),
			Request: requestRec{URL: claude.MessagesURL, Body: body},
		}
	}

	var usage domain.TokenUsage
	for attempt := range claudeAttempts {
		rec := record(attempt)
		resp, err := s.claude.Message(ctx, req)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			rec.Error = s.claudeError(err)
			s.writeAudit(rec)
			if claude.IsRetryable(err) && attempt < claudeAttempts-1 {
				delay := s.backoff(attempt)
				s.log.Warn("anthropic call failed, retrying", "model", model, "err", s.redact(err.Error()),
					"in", delay.String(), "attempt", attempt+1, "of", claudeAttempts)
				if s.sleep(ctx, delay) != nil {
					break
				}
				continue
			}
			s.log.Error("anthropic call failed", "model", model, "err", s.redact(clipText(err.Error(), 300)))
			break
		}

		usage = claudeTokenUsage(model, resp.Usage)
		actual := resp.Model
		if actual == "" {
			actual = model
		}
		content := strings.TrimSpace(resp.Text)
		var details *stopDetailsRec
		var category *string
		if resp.StopDetails != nil {
			details = &stopDetailsRec{Category: resp.StopDetails.Category, Explanation: resp.StopDetails.Explanation}
			category = resp.StopDetails.Category
		}
		if actual != model && resp.StopReason != "refusal" {
			s.log.Info("claude declined the day and a fallback model answered", "requested", model, "answered", actual)
		}
		rec.Usage = usageOf(usage)
		rec.Response = &claudeResponseRec{
			StatusCode: 200, ID: resp.ID, Model: actual, StopReason: resp.StopReason, StopDetails: details, Content: content,
		}

		if resp.StopReason == "refusal" {
			rec.Result = refusedResultRec{Status: "refused", ActualModel: actual, Category: category}
			s.writeAudit(rec)
			// The same day would be refused again, so it goes down the chain.
			label := "no category"
			if category != nil && *category != "" {
				label = *category
			}
			s.log.Warn("claude declined to evaluate the day", "model", actual, "category", label)
			return r.failure(domain.RunAIFailed, actual, &usage, fmt.Sprintf("The model declined to evaluate the day (%s).", label))
		}
		if resp.StopReason == "max_tokens" {
			s.log.Warn("claude answer was cut off (stop_reason=max_tokens)", "model", actual)
		}
		if content == "" {
			rec.Result = emptyResultRec{Status: "empty_response", ActualModel: actual}
			s.writeAudit(rec)
			s.log.Warn("claude returned an empty answer", "model", actual)
			return r.failure(domain.RunAIFailed, actual, &usage, errEmptyResponse)
		}

		ev, err := parseEvaluation(content, r.msgs, r.detectLanguage)
		if err != nil {
			rec.Error = errorRec{Type: "ParseError", Message: err.Error()}
			s.writeAudit(rec)
			s.log.Error("claude answer not understood", "model", actual, "err", err.Error())
			return r.failure(domain.RunParseFailed, actual, &usage, errEvalFailed)
		}
		rec.Result = parsedResult(actual, ev)
		s.writeAudit(rec)
		s.log.Debug("claude evaluated the day", "model", actual, "scores", len(ev.scores))
		return r.success(actual, usage, ev)
	}
	return r.failure(domain.RunAIFailed, model, &usage, errEvalFailed)
}

// claudeTokenUsage is what the run cost. Tokens are the answering attempt's (the
// top level of the API's usage); money covers every attempt, the declined one
// included (usage.iterations).
func claudeTokenUsage(requested string, u claude.Usage) domain.TokenUsage {
	prompt, out := u.Prompt(), u.Output
	total := prompt + out
	t := domain.TokenUsage{PromptTokens: &prompt, CompletionTokens: &out, TotalTokens: &total, ReasoningTokens: u.ThinkingTokens}
	if c, ok := u.Cost(requested); ok {
		t.CostUSD = &c
	}
	return t
}

// ---- OpenRouter ----

// openRouter sends the day to one OpenRouter model, at most orEvalAttempts times.
func (r *evalRun) openRouter(ctx context.Context, model string) domain.EvalResult {
	s := r.s
	req := openrouter.Request{
		Model: model,
		Messages: []openrouter.Message{
			{Role: "system", Text: r.system},
			{Role: "user", Text: r.payload},
		},
		ResponseFormat: r.schema.format,
		IncludeUsage:   true, // makes OpenRouter return the token counts and the dollar cost
		MaxTokens:      orEvalMaxTokens,
		// Empty sends no reasoning block.
		ReasoningEffort: s.cfg.OpenRouterEvalEffort,
	}
	body, err := req.Body()
	if err != nil {
		s.log.Error("openrouter eval request not built", "err", s.redact(err.Error()))
		return r.failure(domain.RunAIFailed, model, nil, errEvalFailed)
	}
	doc, _ := body.JSON()

	var usage domain.TokenUsage
	for attempt := range orEvalAttempts {
		rec := openRouterRecord{
			RequestID: r.requestID, Attempt: attempt + 1, Provider: providerOpenRouter,
			IncludeDayVerdict: true, DetectInterfaceLanguage: r.detectLanguage, MessageCount: len(r.msgs),
			Request: requestRec{URL: s.orURL, Body: doc},
		}
		last := attempt == orEvalAttempts-1
		resp, err := s.router.Do(ctx, body, orEvalTimeout)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			rec.Response, rec.Error = s.openRouterError(err)
			s.writeAudit(rec)

			var se *openrouter.StatusError
			var re *openrouter.RequestError
			switch {
			case errors.As(err, &se):
				if openrouter.RetryableStatus(se.Status) && !last {
					delay := se.RetryAfter
					if delay == 0 {
						delay = s.backoff(attempt)
					}
					s.log.Warn("openrouter eval call failed, retrying", "model", model, "status", se.Status,
						"in", delay.String(), "attempt", attempt+1, "of", orEvalAttempts)
					if s.sleep(ctx, delay) != nil {
						return r.failure(domain.RunAIFailed, model, &usage, errEvalFailed)
					}
					continue
				}
				s.log.Error("openrouter eval call failed", "model", model, "status", se.Status, "body", s.redact(clipText(se.Snippet, 200)))
			case errors.As(err, &re) && !last:
				delay := s.backoff(attempt)
				s.log.Warn("openrouter eval call failed, retrying", "model", model, "err", s.redact(err.Error()),
					"in", delay.String(), "attempt", attempt+1, "of", orEvalAttempts)
				if s.sleep(ctx, delay) != nil {
					return r.failure(domain.RunAIFailed, model, &usage, errEvalFailed)
				}
				continue
			default:
				s.log.Error("openrouter eval call failed", "model", model, "err", s.redact(err.Error()))
			}
			break
		}

		answer, err := decodeCompletion(resp.Body)
		if err != nil {
			// The body arrived but is not a chat completion. A second try would get
			// the same answer, so this is final for the model.
			rec.Response = httpResponseRec{StatusCode: resp.Status, Text: string(resp.Body)}
			rec.Error = errorRec{Type: "ParseError", Message: err.Error()}
			s.writeAudit(rec)
			s.log.Error("openrouter answer not understood", "model", model, "err", err.Error())
			return r.failure(domain.RunParseFailed, model, &usage, errEvalFailed)
		}
		usage = answer.usage
		actual := answer.model
		if actual == "" {
			actual = model
		}
		rec.Response = openRouterResponseRec{
			StatusCode: resp.Status, Text: string(resp.Body), Model: actual, Content: answer.content, FinishReason: answer.finishReason,
		}
		rec.Usage = usageOf(usage)
		if answer.finishReason != nil && *answer.finishReason == "length" {
			s.log.Warn("openrouter answer was cut off (finish_reason=length)", "model", actual)
		}

		if answer.content == "" {
			rec.Result = emptyResultRec{Status: "empty_response", ActualModel: actual}
			s.writeAudit(rec)
			if !last {
				delay := s.backoff(attempt)
				s.log.Warn("openrouter returned an empty answer, retrying", "model", model, "in", delay.String(),
					"attempt", attempt+1, "of", orEvalAttempts)
				if s.sleep(ctx, delay) != nil {
					return r.failure(domain.RunAIFailed, actual, &usage, errEmptyResponse)
				}
				continue
			}
			s.log.Warn("openrouter returned an empty answer on every try", "model", actual)
			return r.failure(domain.RunAIFailed, actual, &usage, errEmptyResponse)
		}

		ev, err := parseEvaluation(answer.content, r.msgs, r.detectLanguage)
		if err != nil {
			rec.Error = errorRec{Type: "ParseError", Message: err.Error()}
			s.writeAudit(rec)
			s.log.Error("openrouter answer not understood", "model", actual, "err", err.Error())
			return r.failure(domain.RunParseFailed, actual, &usage, errEvalFailed)
		}
		rec.Result = parsedResult(actual, ev)
		s.writeAudit(rec)
		s.log.Debug("openrouter evaluated the day", "model", actual, "scores", len(ev.scores))
		return r.success(actual, usage, ev)
	}
	return r.failure(domain.RunAIFailed, model, &usage, errEvalFailed)
}

// completion is what is read out of a chat completion.
type completion struct {
	model        string
	content      string // trimmed
	finishReason *string
	usage        domain.TokenUsage
}

// decodeCompletion reads a chat completion. A reply without choices has empty
// content, as in Python, so the caller retries it; content that is neither
// text nor null is an error.
func decodeCompletion(body []byte) (completion, error) {
	var data struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return completion{}, fmt.Errorf("response is not a chat completion: %w", err)
	}
	c := completion{model: data.Model, usage: extractUsage(data.Usage)}
	if len(data.Choices) == 0 {
		return c, nil
	}
	choice := data.Choices[0]
	c.finishReason = choice.FinishReason
	switch raw := strings.TrimSpace(string(choice.Message.Content)); {
	case raw == "" || raw == "null":
	case raw[0] == '"':
		s, ok := asString(choice.Message.Content)
		if !ok {
			return completion{}, errors.New("message content is not a valid string")
		}
		c.content = strings.TrimSpace(s)
	default:
		return completion{}, errors.New("message content is neither text nor null")
	}
	return c, nil
}

// extractUsage reads OpenRouter's usage object. Anything that is not a number
// is left out, and a usage that is not an object is empty.
func extractUsage(raw json.RawMessage) domain.TokenUsage {
	obj, ok := asObject(raw)
	if !ok {
		return domain.TokenUsage{}
	}
	u := domain.TokenUsage{
		PromptTokens:     optionalInt(obj["prompt_tokens"]),
		CompletionTokens: optionalInt(obj["completion_tokens"]),
		TotalTokens:      optionalInt(obj["total_tokens"]),
	}
	if details, ok := asObject(obj["completion_tokens_details"]); ok {
		u.ReasoningTokens = optionalInt(details["reasoning_tokens"])
	}
	if cost := strings.TrimSpace(string(obj["cost"])); cost != "" && (cost[0] == '-' || cost[0] >= '0' && cost[0] <= '9') {
		if f, err := strconv.ParseFloat(cost, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			u.CostUSD = &f
		}
	}
	return u
}

// optionalInt is Python's _optional_int: int(value), or nothing.
func optionalInt(raw json.RawMessage) *int {
	v, kind := parseIntLike(raw)
	if kind != intOK {
		return nil
	}
	n := int(v)
	return &n
}

// clipText cuts s to n runes for a log line.
func clipText(s string, n int) string { return clip(s, n) }
