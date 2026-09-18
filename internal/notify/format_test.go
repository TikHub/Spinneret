package notify

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func sampleMessage() message {
	return message{
		ID: "alt_1", Kind: KindBreakerOpened, Severity: SeverityCritical,
		Title: "Breaker opened: shop/web/search", Message: "Circuit breaker opened <fast> & loud",
		Tenant: "acme", Namespace: "prod", Site: "shop",
		Details:   map[string]any{"endpoint_group": "search", "risk_ratio": 0.5, "nested": map[string]any{"a": 1}, "skip": nil},
		CreatedAt: time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC),
	}
}

func TestRenderText(t *testing.T) {
	t.Parallel()
	got := renderText(sampleMessage())
	require.Equal(t, "[CRITICAL] Breaker opened: shop/web/search\n"+
		"Circuit breaker opened <fast> & loud\n"+
		"Tenant: acme · Namespace: prod · Site: shop\n"+
		"- endpoint_group: search\n"+
		"- nested: {\"a\":1}\n"+
		"- risk_ratio: 0.5\n"+
		"alt_1 · 2026-09-17T10:00:00Z", got)
}

func TestRenderMarkdown(t *testing.T) {
	t.Parallel()
	got := renderMarkdown(sampleMessage())
	require.True(t, strings.HasPrefix(got, "### [CRITICAL] Breaker opened: shop/web/search\n\n"))
	require.Contains(t, got, "> Tenant: acme | Namespace: prod | Site: shop\n\n")
	require.Contains(t, got, "- **endpoint_group**: search\n")
	require.True(t, strings.HasSuffix(got, "\n###### alt_1 · 2026-09-17T10:00:00Z"))
}

func TestRenderTelegramHTMLEscapes(t *testing.T) {
	t.Parallel()
	m := sampleMessage()
	m.Title = "<script>alert(1)</script>"
	m.Details = map[string]any{"k<": "v&>"}
	got := renderTelegramHTML(m)
	require.Equal(t, "<b>[CRITICAL] &lt;script&gt;alert(1)&lt;/script&gt;</b>\n"+
		"Circuit breaker opened &lt;fast&gt; &amp; loud\n"+
		"<i>Tenant: acme · Namespace: prod · Site: shop</i>\n"+
		"<b>k&lt;</b>: v&amp;&gt;\n"+
		"<code>alt_1 · 2026-09-17T10:00:00Z</code>", got)
}

func TestRenderBudget(t *testing.T) {
	t.Parallel()
	m := sampleMessage()
	m.Message = strings.Repeat("é", 5000)
	m.Title = "multi\nline\r\ntitle"
	m.Details = map[string]any{}
	for i := range 40 {
		m.Details[fmt.Sprintf("key%02d", i)] = strings.Repeat("<", 500)
	}
	for name, render := range map[string]func(message) string{
		"text": renderText, "markdown": renderMarkdown, "telegram": renderTelegramHTML,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := render(m)
			require.True(t, utf8.ValidString(got))
			require.Contains(t, got, "multi line title")
			require.Contains(t, got, "… ")
			require.Contains(t, got, "more")
			require.Contains(t, got, "alt_1")
			if name != "telegram" {
				require.LessOrEqual(t, len(got), chatBudgetBytes)
			} else {
				// Telegram counts visible text: strip tags and entities.
				visible := strings.NewReplacer("<b>", "", "</b>", "", "<i>", "", "</i>", "", "<code>", "", "</code>", "",
					"&lt;", "<", "&gt;", ">", "&amp;", "&").Replace(got)
				require.LessOrEqual(t, len(visible), chatBudgetBytes)
			}
		})
	}
}

func TestBudgetStopsAtLimit(t *testing.T) {
	t.Parallel()
	b := newBudget(5)
	b.add("abcdef", nil)
	b.add("x", nil)
	require.Equal(t, "abcde", b.String())
	require.Equal(t, "ab…", ellipsis("abcdef", 5))
	require.Equal(t, "abc", ellipsis("abc", 5))
	require.Equal(t, "a b", oneLine("a\nb"))
	require.Equal(t, "a�b", validUTF8("a\xffb"))
}
