package chat_completions

import "github.com/tidwall/gjson"

type streamedMessage struct {
	id      string
	hadText bool
	done    bool
}

type messageTextStream struct {
	byID        map[string]*streamedMessage
	byIndex     map[int64]*streamedMessage
	open        map[*streamedMessage]struct{}
	unkeyedText bool
}

// find resolves the known identity first and retains both aliases when the
// event supplies them. An anonymous event can use the sole open message.
func (s *messageTextStream) find(event, item gjson.Result) *streamedMessage {
	id := item.Get("id").String()
	if id == "" {
		id = event.Get("item_id").String()
	}
	index := event.Get("output_index")
	hasIndex := index.Type == gjson.Number
	if id == "" && !hasIndex {
		if len(s.open) == 1 {
			for state := range s.open {
				return state
			}
		}
		return nil
	}
	state := s.byID[id]
	if candidate := s.byIndex[index.Int()]; state == nil && hasIndex && candidate != nil && (id == "" || candidate.id == "" || candidate.id == id) {
		state = candidate
	}
	if state == nil {
		state = &streamedMessage{}
		if s.open == nil {
			s.open = make(map[*streamedMessage]struct{})
		}
		s.open[state] = struct{}{}
	}
	if id != "" {
		state.id = id
		if s.byID == nil {
			s.byID = make(map[string]*streamedMessage)
		}
		s.byID[id] = state
	}
	if hasIndex {
		if s.byIndex == nil {
			s.byIndex = make(map[int64]*streamedMessage)
		}
		s.byIndex[index.Int()] = state
	}
	return state
}

func (s *messageTextStream) mark(event gjson.Result) {
	if state := s.find(event, gjson.Result{}); state != nil {
		state.hadText = true
	} else {
		s.unkeyedText = true
	}
}

func (s *messageTextStream) complete(event, item gjson.Result) bool {
	state := s.find(event, item)
	if state != nil {
		if state.done {
			return false
		}
		state.done = true
		delete(s.open, state)
		if state.hadText {
			return false
		}
	}
	hadText := s.unkeyedText
	s.unkeyedText = false
	return !hadText
}
