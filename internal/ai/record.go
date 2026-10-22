// SPDX-License-Identifier: Apache-2.0

package ai

import (
	"encoding/json"
	"errors"

	"github.com/FreshLabDev/quoto/internal/claude"
	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/openrouter"
)

// The audit records. The keys are the Python ones (doc section 6, "Audit log");
// audit.Log adds created_at in front. They are structs because the order of the
// keys is part of what an operator reading the file expects: the identifying
// keys, then the request, then what came back.

type requestRec struct {
	URL  string          `json:"url"`
	Body json.RawMessage `json:"body"`
}

type usageRec struct {
	PromptTokens     *int     `json:"prompt_tokens"`
	CompletionTokens *int     `json:"completion_tokens"`
	ReasoningTokens  *int     `json:"reasoning_tokens"`
	TotalTokens      *int     `json:"total_tokens"`
	CostUSD          *float64 `json:"cost_usd"`
}

func usageOf(u domain.TokenUsage) *usageRec {
	r := usageRec(u)
	return &r
}

// claudeRecord is one Claude attempt. Python wrote usage, response and result
// on success and only error on failure, and so does this.
type claudeRecord struct {
	RequestID               string             `json:"request_id"`
	Attempt                 int                `json:"attempt"`
	Provider                string             `json:"provider"`
	IncludeDayVerdict       bool               `json:"include_day_verdict"`
	DetectInterfaceLanguage bool               `json:"detect_interface_language"`
	MessageCount            int                `json:"message_count"`
	Request                 requestRec         `json:"request"`
	Usage                   *usageRec          `json:"usage,omitempty"`
	Response                *claudeResponseRec `json:"response,omitempty"`
	Result                  any                `json:"result,omitempty"`
	Error                   any                `json:"error,omitempty"`
}

type claudeResponseRec struct {
	StatusCode  int             `json:"status_code"` // the stream's own, always 200 here
	ID          string          `json:"id"`
	Model       string          `json:"model"`
	StopReason  string          `json:"stop_reason"`
	StopDetails *stopDetailsRec `json:"stop_details"`
	Content     string          `json:"content"`
}

type stopDetailsRec struct {
	Category    *string `json:"category"`
	Explanation *string `json:"explanation"`
}

// openRouterRecord is one OpenRouter attempt: the response comes before the
// usage, as in Python.
type openRouterRecord struct {
	RequestID               string     `json:"request_id"`
	Attempt                 int        `json:"attempt"`
	Provider                string     `json:"provider"`
	IncludeDayVerdict       bool       `json:"include_day_verdict"`
	DetectInterfaceLanguage bool       `json:"detect_interface_language"`
	MessageCount            int        `json:"message_count"`
	Request                 requestRec `json:"request"`
	Response                any        `json:"response,omitempty"`
	Usage                   *usageRec  `json:"usage,omitempty"`
	Result                  any        `json:"result,omitempty"`
	Error                   any        `json:"error,omitempty"`
}

// httpResponseRec is a response that was not usable: its status and the start of
// its body (nothing for a 404, whose body is not read).
type httpResponseRec struct {
	StatusCode int    `json:"status_code"`
	Text       string `json:"text"`
}

// openRouterResponseRec is a 2xx response: the raw text, and what was read out
// of it.
type openRouterResponseRec struct {
	StatusCode   int     `json:"status_code"`
	Text         string  `json:"text"`
	Model        string  `json:"model"`
	Content      string  `json:"content"`
	FinishReason *string `json:"finish_reason"`
}

type errorRec struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type httpErrorRec struct {
	Type       string `json:"type"`
	Message    string `json:"message"`
	StatusCode int    `json:"status_code"`
}

// claudeErrorRec has all four keys for an API or connection error, with nulls
// where the error has no status or type.
type claudeErrorRec struct {
	Type       string  `json:"type"`
	Message    string  `json:"message"`
	StatusCode *int    `json:"status_code"`
	ErrorType  *string `json:"error_type"`
}

type parsedResultRec struct {
	Status          string       `json:"status"`
	ActualModel     string       `json:"actual_model"`
	ScoreCount      int          `json:"score_count"`
	QuoteChoice     *quoteRec    `json:"quote_choice"`
	DayVerdict      *verdictRec  `json:"day_verdict"`
	LanguageChoice  *languageRec `json:"language_choice"`
	DayVerdictError *string      `json:"day_verdict_error"`
}

type quoteRec struct {
	PrimaryID     *int64  `json:"primary_id"`
	ContextIDs    []int64 `json:"context_ids"`
	ContextNeeded bool    `json:"context_needed"`
}

type verdictRec struct {
	ShouldPublish bool   `json:"should_publish"`
	ReasonCode    string `json:"reason_code"`
	ReasonText    string `json:"reason_text"`
}

type languageRec struct {
	ChatLanguage      string `json:"chat_language"`
	InterfaceLanguage string `json:"interface_language"`
}

type refusedResultRec struct {
	Status      string  `json:"status"`
	ActualModel string  `json:"actual_model"`
	Category    *string `json:"category"`
}

type emptyResultRec struct {
	Status      string `json:"status"`
	ActualModel string `json:"actual_model"`
}

func parsedResult(actual string, ev evaluation) *parsedResultRec {
	r := &parsedResultRec{Status: domain.RunParsed, ActualModel: actual, ScoreCount: len(ev.scores)}
	if ev.quote != nil {
		q := &quoteRec{ContextIDs: ev.quote.ContextIDs, ContextNeeded: ev.quote.ContextNeeded}
		if q.ContextIDs == nil {
			q.ContextIDs = []int64{}
		}
		if ev.quote.PrimaryID != 0 {
			id := ev.quote.PrimaryID
			q.PrimaryID = &id
		}
		r.QuoteChoice = q
	}
	v := ev.verdict
	r.DayVerdict = &verdictRec{ShouldPublish: v.ShouldPublish, ReasonCode: v.ReasonCode, ReasonText: v.ReasonText}
	if ev.language != nil {
		r.LanguageChoice = &languageRec{ChatLanguage: ev.language.ChatLanguage, InterfaceLanguage: ev.language.InterfaceLanguage}
	}
	return r
}

// claudeError describes a failed Claude call for the audit log. The text is
// redacted by the caller's redactor.
func (s *Service) claudeError(err error) claudeErrorRec {
	rec := claudeErrorRec{Message: s.redact(err.Error())}
	var api *claude.APIError
	var conn *claude.ConnError
	switch {
	case errors.As(err, &api):
		rec.Type = "APIError"
		rec.StatusCode = &api.Status
		if api.Type != "" {
			rec.ErrorType = &api.Type
		}
	case errors.As(err, &conn):
		rec.Type = "ConnError"
	default:
		rec.Type = "StreamError"
	}
	return rec
}

// openRouterError describes a failed OpenRouter call for the audit log and
// returns the response record that belongs with it, if the provider answered.
func (s *Service) openRouterError(err error) (resp any, rec any) {
	var se *openrouter.StatusError
	if errors.As(err, &se) {
		return httpResponseRec{StatusCode: se.Status, Text: se.Snippet},
			httpErrorRec{Type: "HTTPStatusError", Message: s.redact(se.Error()), StatusCode: se.Status}
	}
	var re *openrouter.RequestError
	if errors.As(err, &re) {
		return nil, errorRec{Type: "RequestError", Message: s.redact(re.Error())}
	}
	return nil, errorRec{Type: "Error", Message: s.redact(err.Error())}
}
