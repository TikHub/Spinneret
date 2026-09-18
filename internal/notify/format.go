package notify

import (
	"encoding/json"
	"html"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Rendering limits for chat messages.
const (
	// chatBudgetBytes bounds rendered chat texts; it stays below the 4096
	// character/byte limits of Telegram and WeCom markdown messages.
	chatBudgetBytes     = 3800
	maxRenderedMessage  = 1500
	maxRenderedDetail   = 200
	maxRenderedDetailKV = 30
)

// message is the provider-independent content of one alert delivery.
type message struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind"`
	Severity  string         `json:"severity"`
	Title     string         `json:"title"`
	Message   string         `json:"message"`
	Tenant    string         `json:"tenant"`
	Namespace string         `json:"namespace"`
	Site      string         `json:"site"`
	Details   map[string]any `json:"details"`
	CreatedAt time.Time      `json:"created_at"`
}

// detailLine is one rendered key/value pair of the details.
type detailLine struct {
	Key   string
	Value string
}

// detailLines renders details sorted by key, values as strings (non-strings
// JSON-encoded) truncated to maxRenderedDetail bytes.
func (m message) detailLines() []detailLine {
	keys := make([]string, 0, len(m.Details))
	for k := range m.Details {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > maxRenderedDetailKV {
		keys = keys[:maxRenderedDetailKV]
	}
	out := make([]detailLine, 0, len(keys))
	for _, k := range keys {
		var v string
		switch t := m.Details[k].(type) {
		case string:
			v = t
		case nil:
			continue
		default:
			b, err := json.Marshal(t)
			if err != nil {
				continue
			}
			v = string(b)
		}
		out = append(out, detailLine{Key: oneLine(truncateBytes(k, 64)), Value: oneLine(ellipsis(v, maxRenderedDetail))})
	}
	return out
}

// headline returns "[SEVERITY] title".
func (m message) headline() string {
	return "[" + strings.ToUpper(m.Severity) + "] " + oneLine(m.Title)
}

// scope returns "Tenant: a · Namespace: b · Site: c" for the non-empty parts.
func (m message) scope(sep string) string {
	parts := make([]string, 0, 3)
	if m.Tenant != "" {
		parts = append(parts, "Tenant: "+oneLine(m.Tenant))
	}
	if m.Namespace != "" {
		parts = append(parts, "Namespace: "+oneLine(m.Namespace))
	}
	if m.Site != "" {
		parts = append(parts, "Site: "+oneLine(m.Site))
	}
	return strings.Join(parts, sep)
}

func (m message) footer() string {
	return m.ID + " · " + m.CreatedAt.UTC().Format(time.RFC3339)
}

// renderText renders a plain-text message (Feishu).
func renderText(m message) string {
	b := newBudget(chatBudgetBytes)
	b.add(m.headline()+"\n", nil)
	if m.Message != "" {
		b.add(ellipsis(m.Message, maxRenderedMessage)+"\n", nil)
	}
	if s := m.scope(" · "); s != "" {
		b.add(s+"\n", nil)
	}
	b.addDetails(m.detailLines(), func(d detailLine) (string, func(string) string) {
		return "- " + d.Key + ": " + d.Value + "\n", nil
	})
	b.add(m.footer(), nil)
	return b.String()
}

// renderMarkdown renders a markdown message (DingTalk, WeCom).
func renderMarkdown(m message) string {
	b := newBudget(chatBudgetBytes)
	b.add("### "+m.headline()+"\n\n", nil)
	if m.Message != "" {
		b.add(ellipsis(m.Message, maxRenderedMessage)+"\n\n", nil)
	}
	if s := m.scope(" | "); s != "" {
		b.add("> "+s+"\n\n", nil)
	}
	b.addDetails(m.detailLines(), func(d detailLine) (string, func(string) string) {
		return "- **" + d.Key + "**: " + d.Value + "\n", nil
	})
	b.add("\n###### "+m.footer(), nil)
	return b.String()
}

// renderTelegramHTML renders an HTML message for Telegram's parse_mode HTML.
// Every dynamic value is escaped; the budget counts visible text only, which
// is what Telegram's message length limit applies to.
func renderTelegramHTML(m message) string {
	tag := func(openTag, closeTag string) func(string) string {
		return func(s string) string { return openTag + html.EscapeString(s) + closeTag }
	}
	b := newBudget(chatBudgetBytes)
	b.add(m.headline(), tag("<b>", "</b>\n"))
	if m.Message != "" {
		b.add(ellipsis(m.Message, maxRenderedMessage), tag("", "\n"))
	}
	if s := m.scope(" · "); s != "" {
		b.add(s, tag("<i>", "</i>\n"))
	}
	b.addDetails(m.detailLines(), func(d detailLine) (string, func(string) string) {
		key := d.Key
		return key + ": " + d.Value, func(s string) string {
			value := strings.TrimPrefix(s, key+": ")
			return "<b>" + html.EscapeString(key) + "</b>: " + html.EscapeString(value) + "\n"
		}
	})
	b.add(m.footer(), tag("<code>", "</code>"))
	return b.String()
}

// budget accumulates message segments while their visible size fits a byte
// budget. Each segment is truncated (on a rune boundary) before it is
// rendered, so markup and escaping are never cut.
type budget struct {
	sb    strings.Builder
	used  int
	limit int
}

func newBudget(limit int) *budget { return &budget{limit: limit} }

// add appends raw, truncated to the remaining budget, rendered through wrap
// (nil = verbatim).
func (b *budget) add(raw string, wrap func(string) string) {
	remaining := b.limit - b.used
	if remaining <= 0 {
		return
	}
	raw = truncateBytes(raw, remaining)
	b.used += len(raw)
	if wrap != nil {
		raw = wrap(raw)
	}
	b.sb.WriteString(raw)
}

// addDetails appends detail lines while they fit, keeping room for the footer,
// and summarizes the rest as "… n more".
func (b *budget) addDetails(lines []detailLine, render func(detailLine) (string, func(string) string)) {
	const reserve = 160
	for i, d := range lines {
		raw, wrap := render(d)
		if b.used+len(raw)+reserve > b.limit {
			b.add("… "+strconv.Itoa(len(lines)-i)+" more\n", nil)
			return
		}
		b.add(raw, wrap)
	}
}

func (b *budget) String() string { return b.sb.String() }

// ellipsis truncates s to at most n bytes, marking truncation with "…".
func ellipsis(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return truncateBytes(s, n-len("…")) + "…"
}

// oneLine replaces line breaks so single-line fields cannot inject structure.
func oneLine(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	return strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
}

// validUTF8 replaces invalid UTF-8 sequences.
func validUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "�")
}
