package helps

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestResponsesItemIDsPreserveCallsReasoningAndDistinctItems(t *testing.T) {
	for _, terminal := range []string{"response.completed", "response.incomplete"} {
		t.Run(terminal, func(t *testing.T) {
			var state ResponsesItemIDs
			events := []struct{ data, path, want string }{
				{`{"type":"response.output_item.added","output_index":0,"item":{"id":"reason-first","type":"reasoning","encrypted_content":"opaque"}}`, "item.id", "reason-first"},
				{`{"type":"response.reasoning_summary_text.delta","output_index":0,"item_id":"reason-new","delta":"thinking"}`, "item_id", "reason-first"},
				{`{"type":"response.output_item.done","output_index":0,"item":{"id":"reason-done","type":"reasoning","encrypted_content":"opaque"}}`, "item.id", "reason-first"},
				{`{"type":"response.output_item.added","output_index":1,"item":{"id":"call-first","type":"function_call","call_id":"call-123","name":"shell","arguments":""}}`, "item.id", "call-first"},
				{`{"type":"response.function_call_arguments.delta","output_index":1,"item_id":"call-new","delta":"ha ha"}`, "item_id", "call-first"},
				{`{"type":"response.output_item.done","output_index":1,"item":{"id":"call-done","type":"function_call","call_id":"call-123","name":"shell","arguments":"ha ha"}}`, "item.id", "call-first"},
				{`{"type":"response.output_item.added","output_index":2,"item":{"id":"message-first","type":"message","content":[]}}`, "item.id", "message-first"},
				{`{"type":"response.content_part.done","output_index":2,"item_id":"message-new","part":{"type":"output_text","text":"ha ha"}}`, "item_id", "message-first"},
			}
			for _, event := range events {
				got := state.Normalize([]byte(event.data))
				if gjson.GetBytes(got, event.path).String() != event.want {
					t.Fatalf("identity changed: %s", got)
				}
				// Only the selected ID field may change, including for tool/reasoning events.
				before := gjson.Parse(event.data).Map()
				after := gjson.ParseBytes(got).Map()
				for key, value := range before {
					if key != "item_id" && key != "item" && value.Raw != after[key].Raw {
						t.Fatalf("changed %s: %s", key, got)
					}
				}
				for _, key := range []string{"item.call_id", "item.arguments", "item.encrypted_content"} {
					if gjson.Get(event.data, key).Raw != gjson.GetBytes(got, key).Raw {
						t.Fatalf("changed %s: %s", key, got)
					}
				}
			}
			final := []byte(`{"type":"` + terminal + `","response":{"id":"response-1","usage":{"total_tokens":5},"output":[{"id":"r-final","type":"reasoning","encrypted_content":"opaque"},{"id":"f-final","type":"function_call","call_id":"call-123","arguments":"ha ha"},{"id":"m-final","type":"message","content":[{"type":"output_text","text":"ha ha"}]}]}}`)
			got := state.Normalize(final)
			for index, want := range []string{"reason-first", "call-first", "message-first"} {
				if gjson.GetBytes(got, "response.output").Array()[index].Get("id").String() != want {
					t.Fatalf("terminal identity changed: %s", got)
				}
			}
			for _, key := range []string{"response.id", "response.usage", "response.output.0.encrypted_content", "response.output.1.call_id", "response.output.1.arguments", "response.output.2.content"} {
				if gjson.GetBytes(final, key).Raw != gjson.GetBytes(got, key).Raw {
					t.Fatalf("changed %s: %s", key, got)
				}
			}
			var other ResponsesItemIDs
			input := []byte(events[0].data)
			if string(other.Normalize(input)) != string(input) {
				t.Fatal("state leaked between streams")
			}
		})
	}
}

func TestResponsesItemIDsIgnoreEventsWithoutOutputIndex(t *testing.T) {
	var state ResponsesItemIDs
	for _, data := range []string{
		`{"type":"response.output_text.delta","item_id":"orphan","delta":"ha"}`,
		`{"type":"response.output_item.added","output_index":-1,"item":{"id":"negative"}}`,
		`{"type":"response.output_item.added","output_index":0.5,"item":{"id":"fraction"}}`,
		`{"type":"message_start","message":{"id":"claude"}}`,
		`{"type":"response.created","response":{"id":"response","output":[]}}`,
	} {
		if got := state.Normalize([]byte(data)); string(got) != data {
			t.Fatalf("unexpected rewrite: %s", got)
		}
	}
	if len(state.ids) != 0 {
		t.Fatalf("invalid indexes stored: %v", state.ids)
	}
}
