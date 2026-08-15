package notify

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Escapes, never literal characters. A literal U+202E or U+009B in Go source is
// invisible in review and can be lost in transit — which would silently reduce
// the leak assertions below to strings.Contains(xml, ""), always true. Writing
// the first draft of this file with literals did exactly that.
const (
	bidiRLO = "‮" // right-to-left override: PRINTABLE, so no control filter catches it
	c1CSI   = "" // C1 CSI introducer, the reason internal/tui/oscfilter.go exists
)

func TestBuildToastXML_ContainsFields(t *testing.T) {
	t.Parallel()
	xml := BuildToastXML(Notification{
		Title:  "quil · claude-code",
		Body:   "▲ Waiting for your input",
		Tag:    "pane-0a1b2c3d",
		Launch: "quil://activate?pane=pane-0a1b2c3d&pid=1",
	})
	for _, want := range []string{
		`activationType="protocol"`,
		`launch="quil://activate?pane=pane-0a1b2c3d&amp;pid=1"`,
		"quil · claude-code",
		"▲ Waiting for your input",
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("BuildToastXML() missing %q\ngot:\n%s", want, xml)
		}
	}
}

func TestBuildToastXML_EscapesXMLMetacharacters(t *testing.T) {
	t.Parallel()
	xml := BuildToastXML(Notification{
		Title: `a & b <script> "q"`,
		Body:  `x > y & z`,
	})
	if strings.Contains(xml, "<script>") {
		t.Errorf("unescaped element leaked into XML:\n%s", xml)
	}
	if !strings.Contains(xml, "&amp;") {
		t.Errorf("ampersand not escaped:\n%s", xml)
	}
}

// A project or pane name arrives from a daemon the user may not control.
// Bidi overrides are PRINTABLE, so XML escaping passes them through untouched
// and the toast renders a reversed pane name. This is the OSC 52 defect shape
// in a new surface: an escaping pass is not a sanitising pass.
func TestBuildToastXML_StripsBidiAndControls(t *testing.T) {
	t.Parallel()
	xml := BuildToastXML(Notification{
		Title: "safe" + bidiRLO + "reversed",
		Body:  "before" + c1CSI + "after\x07\x00end",
	})
	for _, bad := range []string{bidiRLO, c1CSI, "\x07", "\x00"} {
		if bad == "" {
			t.Fatal("empty needle in the leak table would make this assertion vacuous")
		}
		if strings.Contains(xml, bad) {
			t.Errorf("BuildToastXML() leaked %q\ngot:\n%s", bad, xml)
		}
	}
	for _, keep := range []string{"safe", "reversed", "before", "after", "end"} {
		if !strings.Contains(xml, keep) {
			t.Errorf("printable text %q was destroyed:\n%s", keep, xml)
		}
	}
}

// Sanitising does not shorten anything — a megabyte of ordinary printable text
// survives it whole — so bounding is a third, separate concern.
func TestBuildToastXML_BoundsFieldLength(t *testing.T) {
	t.Parallel()
	xml := BuildToastXML(Notification{
		Title: strings.Repeat("a", 10_000),
		Body:  strings.Repeat("b", 10_000),
	})
	if len(xml) > 4096 {
		t.Errorf("toast XML = %d bytes, want bounded well under 4096", len(xml))
	}
}

// Pins the actual cut point, not just "the result is bounded".
// TestBuildToastXML_BoundsFieldLength only asserts the whole document stays
// under 4 KiB, which a MaxFieldRunes of 1000 would also satisfy — so without
// this the constant could be changed by an order of magnitude unnoticed.
func TestBuildToastXML_TruncatesAtMaxFieldRunes(t *testing.T) {
	t.Parallel()
	// A filler the surrounding template cannot contain. Counting "a" instead
	// was off by six, because <toast>, visual, template and ToastGeneric all
	// contribute one — a reminder that counting a character in whole-document
	// output only works when the document cannot supply it.
	const filler = "ß"
	xml := BuildToastXML(Notification{Title: strings.Repeat(filler, MaxFieldRunes+80)})

	kept := strings.Count(xml, filler)
	if kept != MaxFieldRunes {
		t.Errorf("kept %d runes, want exactly MaxFieldRunes (%d)", kept, MaxFieldRunes)
	}
	if !strings.Contains(xml, "…") {
		t.Error("a truncated field must be marked with an ellipsis")
	}

	// Exactly at the cap must survive whole and unmarked.
	exact := BuildToastXML(Notification{Title: strings.Repeat(filler, MaxFieldRunes)})
	if strings.Contains(exact, "…") {
		t.Error("a field exactly at the cap must not be truncated")
	}
	if got := strings.Count(exact, filler); got != MaxFieldRunes {
		t.Errorf("kept %d runes at the cap, want %d", got, MaxFieldRunes)
	}
}

func TestBuildToastXML_OmitsLaunchWhenEmpty(t *testing.T) {
	t.Parallel()
	xml := BuildToastXML(Notification{Title: "t", Body: "b"})
	if strings.Contains(xml, "activationType") {
		t.Errorf("launch attributes emitted for a toast with no launch URI:\n%s", xml)
	}
}

// Non-ASCII must survive byte-identically: this is a control filter, not a
// transliteration, and the sidebar's glyphs plus any non-Latin project name
// depend on it.
func TestBuildToastXML_PreservesPrintableNonASCII(t *testing.T) {
	t.Parallel()
	xml := BuildToastXML(Notification{Title: "проект · 日本語 · ▲◐✓◆"})
	for _, want := range []string{"проект", "日本語", "▲◐✓◆"} {
		if !strings.Contains(xml, want) {
			t.Errorf("BuildToastXML() dropped %q\ngot:\n%s", want, xml)
		}
	}
}

// Truncation must not split a multi-byte rune — the result goes into an XML
// document Windows parses, and a half rune is invalid UTF-8.
func TestBuildToastXML_TruncatesOnRuneBoundary(t *testing.T) {
	t.Parallel()
	xml := BuildToastXML(Notification{Title: strings.Repeat("日", 500)})
	if !utf8.ValidString(xml) {
		t.Errorf("truncation produced invalid UTF-8:\n%s", xml)
	}
	if strings.Contains(xml, "�") {
		t.Errorf("truncation split a rune (replacement char present):\n%s", xml)
	}
}

// Tab must become a space rather than being dropped: a rendered width that
// disagrees with a measured one is the trap the sidebar cutters document.
func TestBuildToastXML_MapsTabToSpace(t *testing.T) {
	t.Parallel()
	xml := BuildToastXML(Notification{Title: "a\tb"})
	if strings.Contains(xml, "\t") || strings.Contains(xml, "&#x9;") {
		t.Errorf("tab survived into the toast XML:\n%s", xml)
	}
	if !strings.Contains(xml, "a b") {
		t.Errorf("tab was dropped instead of mapped to a space:\n%s", xml)
	}
}
