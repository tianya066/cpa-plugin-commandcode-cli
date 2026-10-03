package plugin

import (
	"errors"
	"strconv"
	"strings"
)

// A tool call the CLI route refuses to assemble arrives as a pair of events,
// captured verbatim from the route:
//
//	{"type":"tool-call","toolCallId":"call_00_KNu…","toolName":"Bash",
//	 "input":"{\"command\"","dynamic":true,"invalid":true,
//	 "error":{"name":"AI_InvalidToolInputError",
//	          "cause":{"name":"AI_JSONParseError","cause":{},"text":"{\"command\""},
//	          "toolInput":"{\"command\"","toolName":"Bash"}}
//	{"type":"tool-error","toolCallId":"call_00_KNu…","toolName":"Bash",
//	 "input":"{\"command\"",
//	 "error":"Invalid input for tool Bash: JSON parsing failed: Text: {\"command\".\nError message: Expected ':' after property name in JSON at position 10 (line 1 column 11)"}
//
// `input` is therefore the model's raw, unparsable text rather than an object.
// Forwarding that as a tool call tells the client to execute a tool with
// arguments it cannot parse, and ignoring the tool-error hides why, so a refused
// call is reported instead of being passed on.

// cliRejectedCall is one refused tool call awaiting its client-visible report.
type cliRejectedCall struct {
	ID     string
	Reason string
}

// rejectCall records a refused tool call without emitting anything yet: the
// paired tool-error carries the readable reason, and emitting both would report
// one failure twice.
func (s *cliStreamState) rejectCall(ev map[string]any) {
	id := stringField(ev, "toolCallId")
	if id == "" {
		id = "call_" + uuidV4()[:12]
	}
	for _, call := range s.rejected {
		if call.ID == id {
			return
		}
	}
	s.rejected = append(s.rejected, cliRejectedCall{ID: id, Reason: invalidToolInputReason(ev)})
}

// reportCallFailure emits why a refused tool call failed, replacing the
// placeholder from rejectCall with the route's own sentence.
func (s *cliStreamState) reportCallFailure(ev map[string]any) [][]byte {
	reason := cliErrorMessage(ev)
	if reason == "" {
		reason = invalidToolInputReason(ev)
	}
	return s.failToolCall(stringField(ev, "toolCallId"), stringField(ev, "toolName"), reason)
}

// flushRejected reports every refused call the route never followed with a
// tool-error, so a refused call can never be dropped in silence.
func (s *cliStreamState) flushRejected() [][]byte {
	if len(s.rejected) == 0 {
		return nil
	}
	pending := s.rejected
	s.rejected = nil
	out := make([][]byte, 0, len(pending))
	for _, call := range pending {
		out = append(out, s.failToolCall(call.ID, "", call.Reason)...)
	}
	return out
}

// failToolCall emits one client-visible failure per refused call and remembers
// it, so the aggregation path cannot answer with a silent empty success.
func (s *cliStreamState) failToolCall(id, name, reason string) [][]byte {
	if id != "" {
		if s.reported[id] {
			return nil
		}
		if s.reported == nil {
			s.reported = map[string]bool{}
		}
		s.reported[id] = true
		s.dropRejected(id)
	}
	if strings.TrimSpace(reason) == "" {
		reason = "the tool call arguments could not be parsed"
	}
	if strings.TrimSpace(name) == "" {
		name = "unknown"
	}
	msg := "commandcode cli: tool call " + name + " was rejected: " + reason
	s.noteStreamErr(msg)
	return [][]byte{errorChunk(msg)}
}

func (s *cliStreamState) dropRejected(id string) {
	for i, call := range s.rejected {
		if call.ID == id {
			s.rejected = append(s.rejected[:i], s.rejected[i+1:]...)
			return
		}
	}
}

// noteStreamErr keeps the first in-stream failure.
func (s *cliStreamState) noteStreamErr(msg string) {
	if s.streamErr == nil && strings.TrimSpace(msg) != "" {
		s.streamErr = errors.New(msg)
	}
}

// errorChunk renders the OpenAI-shaped error payload clients already handle; the
// upstream error event is surfaced the same way.
func errorChunk(message string) []byte {
	return []byte(`{"error":{"message":` + strconv.Quote(message) + `,"type":"upstream_error"}}`)
}

// cliErrorMessage reads the reason out of an upstream error event, which nests
// it: {"type":"error","error":{"type":"server_error","message":"…"}}.
func cliErrorMessage(ev map[string]any) string {
	if msg := stringField(ev, "message"); msg != "" {
		return msg
	}
	if nested, ok := ev["error"].(map[string]any); ok {
		if msg := stringField(nested, "message"); msg != "" {
			return msg
		}
		if kind := stringField(nested, "type"); kind != "" {
			return kind
		}
	}
	return strings.TrimSpace(stringField(ev, "error"))
}

// invalidToolInputReason reads the reason from an invalid tool-call event:
// error.cause.name plus the raw text the model produced.
func invalidToolInputReason(ev map[string]any) string {
	raw := strings.TrimSpace(stringField(ev, "toolInput"))
	if raw == "" {
		raw = strings.TrimSpace(stringField(ev, "input"))
	}
	reason := ""
	if nested, ok := ev["error"].(map[string]any); ok {
		if cause, ok := nested["cause"].(map[string]any); ok {
			reason = stringField(cause, "name")
		}
		if reason == "" {
			reason = stringField(nested, "name")
		}
	}
	if raw != "" {
		if reason == "" {
			reason = "unparsable tool input"
		}
		return reason + ": " + raw
	}
	return reason
}
