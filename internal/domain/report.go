// SPDX-License-Identifier: Apache-2.0

package domain

// EvaluationReport is what the scheduler hands to the report store after the AI
// has judged a day: the inputs and the verdict, enough to write one
// ai_evaluation_runs row and one message_ai_scores row per message the model
// scored. It carries the same data Python's save_evaluation_report took.
type EvaluationReport struct {
	// GroupID is the group's chat id. The tables keep it twice (group_id and
	// chat_id) because they were once told apart; the values are equal.
	GroupID int64
	Window  Window
	// SourceCount is how many messages the window held before the #quoto filter.
	SourceCount int
	// Scored are the messages that went to the model, oldest first: after the
	// #quoto filter and the cap. Its length is the run's message_count.
	Scored []WindowMessage
	Result EvalResult
	// SelectedID is the messages.id of the quote the scheduler picked, 0 for none.
	SelectedID int64
	// AuthorNames maps user ids to the names the model saw. A user who is not in
	// it was shown as "Unknown".
	AuthorNames map[int64]string
}
