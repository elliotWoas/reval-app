// Package keyboards builds Telegram reply_markup JSON.
package keyboards

import "encoding/json"

// Btn is one inline button: callback data or a URL.
type Btn struct {
	Text string
	Data string
	URL  string
}

// B makes a callback button.
func B(text, data string) Btn { return Btn{Text: text, Data: data} }

// U makes a URL button.
func U(text, url string) Btn { return Btn{Text: text, URL: url} }

type inlineBtn struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
}

// Inline returns an inline keyboard. Empty rows are dropped.
func Inline(rows ...[]Btn) string {
	var kb [][]inlineBtn
	for _, r := range rows {
		if len(r) == 0 {
			continue
		}
		row := make([]inlineBtn, 0, len(r))
		for _, b := range r {
			row = append(row, inlineBtn{Text: b.Text, CallbackData: b.Data, URL: b.URL})
		}
		kb = append(kb, row)
	}
	if len(kb) == 0 {
		return ""
	}
	raw, _ := json.Marshal(map[string]any{"inline_keyboard": kb})
	return string(raw)
}

// Grid splits buttons into rows of n.
func Grid(btns []Btn, n int) [][]Btn {
	var rows [][]Btn
	for i := 0; i < len(btns); i += n {
		end := min(i+n, len(btns))
		rows = append(rows, btns[i:end])
	}
	return rows
}

type replyBtn struct {
	Text string `json:"text"`
}

// Reply returns a reply keyboard (buttons at the bottom of the screen).
func Reply(rows [][]string, persistent bool, placeholder string) string {
	var kb [][]replyBtn
	for _, r := range rows {
		if len(r) == 0 {
			continue
		}
		row := make([]replyBtn, 0, len(r))
		for _, t := range r {
			row = append(row, replyBtn{Text: t})
		}
		kb = append(kb, row)
	}
	m := map[string]any{"keyboard": kb, "resize_keyboard": true}
	if persistent {
		m["is_persistent"] = true
	} else {
		m["one_time_keyboard"] = false
	}
	if placeholder != "" {
		m["input_field_placeholder"] = placeholder
	}
	raw, _ := json.Marshal(m)
	return string(raw)
}

// ReplyGrid splits labels into rows of n.
func ReplyGrid(labels []string, n int) [][]string {
	var rows [][]string
	for i := 0; i < len(labels); i += n {
		end := min(i+n, len(labels))
		rows = append(rows, labels[i:end])
	}
	return rows
}

// Remove hides the reply keyboard.
func Remove() string { return `{"remove_keyboard":true}` }
