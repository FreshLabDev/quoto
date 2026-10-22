// SPDX-License-Identifier: Apache-2.0

package ai

import (
	"encoding/json"
	"strings"

	"github.com/FreshLabDev/quoto/internal/claude"
	"github.com/FreshLabDev/quoto/internal/i18n"
)

// The response schemas are raw JSON, not maps: the model writes its fields in
// the order the schema lists them, and Python dicts kept that order. OpenRouter
// gets these as they are; Claude gets a copy without the keywords its
// structured outputs reject (numeric and length bounds, maxItems), and the
// parser clamps the values instead.
const (
	daySchemaJSON = `{"type":"object","additionalProperties":false,"properties":{` +
		`"should_publish":{"type":"boolean"},` +
		`"reason_code":{"type":"string"},` +
		`"reason_text":{"type":"string","maxLength":200}},` +
		`"required":["should_publish","reason_code","reason_text"]}`

	quoteSchemaJSON = `{"type":"object","additionalProperties":false,"properties":{` +
		`"primary_id":{"type":["integer","null"]},` +
		`"context_ids":{"type":"array","items":{"type":"integer"}},` +
		`"context_needed":{"type":"boolean"}},` +
		`"required":["primary_id","context_ids","context_needed"]}`

	messagesSchemaJSON = `{"type":"array","maxItems":10,"items":{"type":"object","additionalProperties":false,"properties":{` +
		`"id":{"type":"integer"},` +
		`"score":{"type":"number","minimum":0,"maximum":10}},` +
		`"required":["id","score"]}}`
)

// responseSchema is one request's schema in the three forms it is used in.
type responseSchema struct {
	name   string          // json_schema.name for OpenRouter
	format json.RawMessage // the whole response_format object for OpenRouter
	claude json.RawMessage // output_config.format.schema for Claude
}

var (
	daySchema         = buildSchema(false)
	dayLanguageSchema = buildSchema(true)
)

func buildSchema(withLanguage bool) responseSchema {
	var b strings.Builder
	b.WriteString(`{"type":"object","additionalProperties":false,"properties":{"day":`)
	b.WriteString(daySchemaJSON)
	b.WriteString(`,"quote":`)
	b.WriteString(quoteSchemaJSON)
	b.WriteString(`,"messages":`)
	b.WriteString(messagesSchemaJSON)
	required := `["day","quote","messages"]`
	name := "quoto_day_verdict"
	if withLanguage {
		enum, _ := json.Marshal(i18n.PromptCodes())
		b.WriteString(`,"language":{"type":"object","additionalProperties":false,"properties":{` +
			`"chat_language":{"type":"string","maxLength":80},` +
			`"interface_language":{"type":"string","enum":` + string(enum) + `}},` +
			`"required":["chat_language","interface_language"]}`)
		required = `["day","quote","messages","language"]`
		name = "quoto_day_verdict_language"
	}
	b.WriteString(`},"required":` + required + `}`)
	full := b.String()

	stripped, err := claude.StripUnsupported([]byte(full))
	if err != nil {
		panic("ai: the response schema is not valid JSON: " + err.Error()) // static data; a test covers it
	}
	return responseSchema{
		name:   name,
		format: json.RawMessage(`{"type":"json_schema","json_schema":{"name":"` + name + `","strict":true,"schema":` + full + `}}`),
		claude: stripped,
	}
}

// schemaFor picks the schema of a request: the day verdict, with the language
// block when the chat has no stored language yet.
func schemaFor(detectLanguage bool) responseSchema {
	if detectLanguage {
		return dayLanguageSchema
	}
	return daySchema
}
