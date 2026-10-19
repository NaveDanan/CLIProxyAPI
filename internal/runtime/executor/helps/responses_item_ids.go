package helps

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ResponsesItemIDs keeps the first upstream ID for each output index in one
// response stream. Copilot can otherwise change an item's ID on every event.
// Text, function call IDs, and reasoning payloads remain untouched.
type ResponsesItemIDs struct {
	ids map[int64]string
}

func (s *ResponsesItemIDs) Normalize(payload []byte) []byte {
	root := gjson.ParseBytes(payload)
	event := root.Get("type").String()
	if !strings.HasPrefix(event, "response.") {
		return payload
	}
	stabilize := func(path string, index int64) {
		observed := gjson.GetBytes(payload, path).String()
		if observed == "" {
			return
		}
		if s.ids == nil {
			s.ids = make(map[int64]string)
		}
		id, exists := s.ids[index]
		if !exists {
			s.ids[index] = observed
			return
		}
		if observed != id {
			if updated, err := sjson.SetBytes(payload, path, id); err == nil {
				payload = updated
			}
		}
	}
	if index := root.Get("output_index"); index.Type == gjson.Number && index.Int() >= 0 && index.Float() == float64(index.Int()) {
		stabilize("item.id", index.Int())
		stabilize("item_id", index.Int())
	}
	switch event {
	case "response.completed", "response.incomplete":
		for index := range root.Get("response.output").Array() {
			stabilize("response.output."+strconv.Itoa(index)+".id", int64(index))
		}
	}
	return payload
}
