// SPDX-License-Identifier: Apache-2.0

package ai

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/FreshLabDev/quoto/internal/claude"
	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/openrouter"
)

// responseError is a model's answer that cannot be used: empty, declined, not a
// chat completion. It is worth another try later, as in Python.
type responseError struct{ msg string }

func (e *responseError) Error() string { return e.msg }

// modelFailure is one model's failure inside a media chain.
type modelFailure struct {
	model string
	err   error
}

// modelsExhaustedError says every media model failed in one attempt. Its text
// names each model and what went wrong, with secrets scrubbed, because it ends
// up in the database.
type modelsExhaustedError struct {
	msg      string
	failures []modelFailure
}

func (e *modelsExhaustedError) Error() string { return e.msg }

// DescribeMedia describes one file. Photos, images and stickers in JPEG go to
// Claude first when it is configured and the file fits the API's limit; every
// kind can go to OpenRouter, and video and audio only there. The caller has
// already checked the group's agreement: if this runs, Claude is allowed.
//
// An error is final unless it is a *domain.MediaRetryableError. A cooldown after
// OpenRouter's billing error comes back as one with CountsAsAttempt false, since
// nothing was sent.
func (s *Service) DescribeMedia(ctx context.Context, req domain.MediaDescribeRequest) (domain.MediaDescription, error) {
	info, err := os.Stat(req.Path)
	if err != nil {
		return domain.MediaDescription{}, fmt.Errorf("media file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return domain.MediaDescription{}, fmt.Errorf("media file: %s is not a regular file", req.Path)
	}
	claudeModel := s.claudeMediaModel(req, info.Size())
	if claudeModel == "" && s.cfg.OpenRouterKey == "" {
		return domain.MediaDescription{}, errors.New("no AI provider key is configured for media analysis")
	}

	var failures []modelFailure
	if claudeModel != "" {
		desc, err := s.describeWithClaude(ctx, claudeModel, req)
		if err == nil {
			return desc, nil
		}
		if ctx.Err() != nil {
			return domain.MediaDescription{}, ctx.Err()
		}
		kept, ok := claudeMediaFailure(err)
		if !ok {
			return domain.MediaDescription{}, err // the file could not be read: final
		}
		failures = append(failures, modelFailure{claudeModel, kept})
		if s.cfg.OpenRouterKey == "" {
			return domain.MediaDescription{}, s.exhausted(failures, mediaRetryable(kept), 0)
		}
		s.log.Warn("media model failed, falling back to OpenRouter", "model", claudeModel, "err", s.redact(clipText(describeError(kept), 200)))
	}

	// The cooldown is OpenRouter's. It opens on OpenRouter's 402 and never stood
	// in Claude's way above.
	if left := s.mediaPauseRemaining(); left > 0 {
		if len(failures) > 0 {
			return domain.MediaDescription{}, s.exhausted(failures, true, left)
		}
		return domain.MediaDescription{}, &domain.MediaRetryableError{
			Err:        fmt.Errorf("media provider cooldown is active for %.0fs", left.Seconds()),
			RetryAfter: left,
			// Nothing was sent, so the attempt budget stays whole.
			CountsAsAttempt: false,
		}
	}

	part, err := mediaPart(req)
	if err != nil {
		return domain.MediaDescription{}, err
	}
	models := modelChain(s.cfg.OpenRouterMediaModel, s.cfg.OpenRouterMediaFallback)
	if len(models) == 0 {
		return domain.MediaDescription{}, errors.New("no OpenRouter media model is configured")
	}

	var orFailures []modelFailure
	for i, model := range models {
		desc, err := s.describeWithOpenRouter(ctx, model, req, part)
		if err == nil {
			s.mediaPauseReset()
			return desc, nil
		}
		if ctx.Err() != nil {
			return domain.MediaDescription{}, ctx.Err()
		}
		var be *openrouter.BodyError
		if errors.As(err, &be) {
			return domain.MediaDescription{}, fmt.Errorf("media file: %w", be.Err)
		}
		failures = append(failures, modelFailure{model, err})
		orFailures = append(orFailures, modelFailure{model, err})
		if i < len(models)-1 {
			s.log.Warn("media model failed, falling back", "model", model, "err", s.redact(clipText(describeError(err), 200)), "next", models[i+1])
			continue
		}

		// Every OpenRouter model said "payment required": the account is empty, and
		// every file would hit the same wall. Stop sending for a while.
		var retryAfter time.Duration
		allPayment := true
		for _, f := range orFailures {
			var se *openrouter.StatusError
			allPayment = allPayment && errors.As(f.err, &se) && se.Status == 402
		}
		if allPayment {
			retryAfter = s.mediaPauseStart()
		}
		retryable := false
		for _, f := range failures {
			retryable = retryable || mediaRetryable(f.err)
		}
		return domain.MediaDescription{}, s.exhausted(failures, retryable, retryAfter)
	}
	return domain.MediaDescription{}, errors.New("unreachable")
}

// claudeMediaModel is the Claude model for this file, or "" when Claude does not
// take it: no key or model, a kind other than an image, a type other than JPEG
// (all the normaliser produces), or a file over the API's limit.
func (s *Service) claudeMediaModel(req domain.MediaDescribeRequest, size int64) string {
	if s.claude == nil || s.cfg.AnthropicKey == "" || s.cfg.AnthropicMediaModel == "" {
		return ""
	}
	switch req.Kind {
	case domain.ContentPhoto, domain.ContentImage, domain.ContentSticker:
	default:
		return ""
	}
	if req.MimeType != "image/jpeg" || size > claudeMediaMaxBytes {
		return ""
	}
	return s.cfg.AnthropicMediaModel
}

// mediaPart is the part that carries the file in an OpenRouter request.
func mediaPart(req domain.MediaDescribeRequest) (openrouter.Part, error) {
	switch req.Kind {
	case domain.ContentPhoto, domain.ContentImage, domain.ContentSticker:
		return openrouter.ImagePart(req.MimeType), nil
	case domain.ContentVideo, domain.ContentAnimation, domain.ContentVideoNote:
		return openrouter.VideoPart(req.MimeType), nil
	case domain.ContentVoice, domain.ContentAudio:
		// The normaliser makes mp3 and nothing else.
		return openrouter.AudioPart("mp3"), nil
	}
	return openrouter.Part{}, fmt.Errorf("unsupported media kind: %q", req.Kind)
}

// mediaFileError is a file that could not be read for Claude. Retrying cannot
// help.
type mediaFileError struct{ err error }

func (e *mediaFileError) Error() string { return "media file: " + e.err.Error() }
func (e *mediaFileError) Unwrap() error { return e.err }

func (s *Service) describeWithClaude(ctx context.Context, model string, req domain.MediaDescribeRequest) (domain.MediaDescription, error) {
	data, err := os.ReadFile(req.Path)
	if err != nil {
		return domain.MediaDescription{}, &mediaFileError{err}
	}
	cr := claude.Request{
		Model:     model,
		MaxTokens: claudeMediaMaxTokens,
		Effort:    s.cfg.AnthropicMediaEffort,
		Messages: []claude.Message{{Role: "user", Blocks: []claude.Block{
			claude.ImageBlock(req.MimeType, data),
			claude.TextBlock(mediaPrompt(req.Kind)),
		}}},
	}
	// No audit record and no attempts of our own: the client's retries are all
	// a description gets.
	resp, err := s.claude.Message(ctx, cr)
	if err != nil {
		return domain.MediaDescription{}, err
	}
	if resp.StopReason == "refusal" {
		label := "no category"
		if resp.StopDetails != nil && resp.StopDetails.Category != nil && *resp.StopDetails.Category != "" {
			label = *resp.StopDetails.Category
		}
		return domain.MediaDescription{}, &responseError{fmt.Sprintf("media model %s declined the image (%s)", model, label)}
	}
	text := strings.TrimSpace(resp.Text)
	if text == "" {
		return domain.MediaDescription{}, &responseError{fmt.Sprintf("media model %s returned an empty description", model)}
	}
	u := claudeTokenUsage(model, resp.Usage)
	actual := resp.Model
	if actual == "" {
		actual = model
	}
	return domain.MediaDescription{
		Text:             text,
		ActualModel:      actual,
		PromptTokens:     *u.PromptTokens,
		CompletionTokens: *u.CompletionTokens,
		TotalTokens:      *u.TotalTokens,
	}, nil
}

// describeWithOpenRouter sends the file to one model, at most orMediaAttempts
// times. A rate limit, a 5xx or a broken connection is tried again after
// Retry-After or the usual backoff; anything else is the model's final answer.
func (s *Service) describeWithOpenRouter(ctx context.Context, model string, req domain.MediaDescribeRequest, part openrouter.Part) (domain.MediaDescription, error) {
	body, err := openrouter.Request{
		Model: model,
		Messages: []openrouter.Message{{Role: "user", Parts: []openrouter.Part{
			openrouter.TextPart(mediaPrompt(req.Kind)),
			part,
		}}},
		ReasoningEffort: s.cfg.OpenRouterMediaEffort,
		File:            req.Path,
	}.Body()
	if err != nil {
		return domain.MediaDescription{}, &openrouter.BodyError{Err: err}
	}

	for attempt := range orMediaAttempts {
		last := attempt == orMediaAttempts-1
		resp, err := s.router.Do(ctx, body, orMediaTimeout)
		if err != nil {
			if ctx.Err() != nil || last {
				return domain.MediaDescription{}, err
			}
			var delay time.Duration
			var se *openrouter.StatusError
			var re *openrouter.RequestError
			switch {
			case errors.As(err, &se) && openrouter.RetryableStatus(se.Status):
				if delay = se.RetryAfter; delay == 0 {
					delay = s.backoff(attempt)
				}
			case errors.As(err, &re):
				delay = s.backoff(attempt)
			default:
				return domain.MediaDescription{}, err
			}
			if serr := s.sleep(ctx, delay); serr != nil {
				return domain.MediaDescription{}, serr
			}
			continue
		}

		answer, err := decodeCompletion(resp.Body)
		if err != nil {
			return domain.MediaDescription{}, &responseError{fmt.Sprintf("media model %s returned an unusable response: %v", model, err)}
		}
		if answer.content == "" {
			return domain.MediaDescription{}, &responseError{fmt.Sprintf("media model %s returned an empty description", model)}
		}
		actual := answer.model
		if actual == "" {
			actual = model
		}
		return domain.MediaDescription{
			Text:             answer.content,
			ActualModel:      actual,
			PromptTokens:     derefInt(answer.usage.PromptTokens),
			CompletionTokens: derefInt(answer.usage.CompletionTokens),
			TotalTokens:      derefInt(answer.usage.TotalTokens),
		}, nil
	}
	return domain.MediaDescription{}, errors.New("unreachable")
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// claudeMediaFailure keeps the failures of a Claude media call that fall back to
// OpenRouter: the API's errors, a broken connection or stream, an unusable
// answer. Anything else (an unreadable file) is final.
func claudeMediaFailure(err error) (error, bool) {
	var (
		api  *claude.APIError
		conn *claude.ConnError
		resp *responseError
		strm *claude.StreamError
	)
	switch {
	case errors.As(err, &api), errors.As(err, &conn), errors.As(err, &resp):
		return err, true
	case errors.As(err, &strm):
		// A stream that is not the documented format is an answer we cannot use.
		return &responseError{"media model sent an unreadable stream: " + strm.Error()}, true
	}
	return err, false
}

// mediaRetryable reports whether a media failure may pass later: an empty
// balance (402) waits for a top-up, as do rate limits, timeouts, 5xx, broken
// connections and unusable answers. A bad request does not.
func mediaRetryable(err error) bool {
	var (
		api  *claude.APIError
		conn *claude.ConnError
		se   *openrouter.StatusError
		re   *openrouter.RequestError
		resp *responseError
	)
	switch {
	case errors.As(err, &api):
		return api.Status == 402 || claude.IsRetryable(api)
	case errors.As(err, &conn), errors.As(err, &re), errors.As(err, &resp):
		return true
	case errors.As(err, &se):
		switch se.Status {
		case 402, 408, 409, 425, 429:
			return true
		}
		return se.Status >= 500
	}
	return false
}

// describeError is an error as one line for a log or for the exhausted error.
func describeError(err error) string {
	var se *openrouter.StatusError
	if errors.As(err, &se) && se.Snippet != "" {
		return se.Error() + ": " + clip(strings.TrimSpace(se.Snippet), 200)
	}
	return err.Error()
}

// exhausted is the error for a chain that failed as a whole. When the failure
// may pass, it is a *domain.MediaRetryableError, which keeps the file pending.
func (s *Service) exhausted(failures []modelFailure, retryable bool, retryAfter time.Duration) error {
	parts := make([]string, len(failures))
	for i, f := range failures {
		parts[i] = f.model + ": " + clip(describeError(f.err), 300)
	}
	err := &modelsExhaustedError{msg: s.redact("all media models failed: " + strings.Join(parts, "; ")), failures: failures}
	if !retryable {
		return err
	}
	return &domain.MediaRetryableError{Err: err, RetryAfter: retryAfter, CountsAsAttempt: true}
}

// ---- the OpenRouter cooldown ----

func (s *Service) mediaPauseRemaining() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return max(0, s.mediaPausedUntil.Sub(s.now()))
}

// mediaPauseStart stops OpenRouter media for mediaCooldown and returns how long.
func (s *Service) mediaPauseStart() time.Duration {
	s.mu.Lock()
	s.mediaPausedUntil = s.now().Add(mediaCooldown)
	s.mu.Unlock()
	s.log.Warn("openrouter media paused after HTTP 402", "for", mediaCooldown.String())
	return mediaCooldown
}

// mediaPauseReset ends the pause: OpenRouter just answered.
func (s *Service) mediaPauseReset() {
	s.mu.Lock()
	s.mediaPausedUntil = time.Time{}
	s.mu.Unlock()
}
