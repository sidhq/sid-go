package sid_test

import (
	"errors"
	"strings"
	"testing"

	sid "github.com/sidhq/sid-go"
)

func TestParseRenderedModelFacingID(t *testing.T) {
	t.Parallel()
	modelID, span, err := sid.ParseRenderedModelFacingID("004218")
	if err != nil || modelID != "004218" || span != nil {
		t.Fatalf("bare reference = %q, %v, %v", modelID, span, err)
	}

	modelID, span, err = sid.ParseRenderedModelFacingID("004218# -5: ١٠")
	if err != nil {
		t.Fatal(err)
	}
	if modelID != "004218" || span == nil || *span != (sid.CharacterRange{-5, 10}) {
		t.Fatalf("ranged reference = %q, %v", modelID, span)
	}

	for _, reference := range []string{
		"", "#1:2", "a#", "a#x:y", "a#1:2:3", "a#1:2#3:4",
		"a#2:2", "a#10:5", "a#+1:2", "a#1_0:20", "a#1.0:2",
	} {
		if _, _, err := sid.ParseRenderedModelFacingID(reference); err == nil {
			t.Errorf("malformed reference %q did not fail", reference)
		}
	}
}

func TestRangePoliciesUseCodePointOffsets(t *testing.T) {
	t.Parallel()
	cache, err := sid.NewDocumentCache()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cache.AddDocument("d", sid.Document{"content": "😀alpha e\u0301 tail"}); err != nil {
		t.Fatal(err)
	}
	resolved, err := cache.ResolveCharRange("d", "content", sid.CharacterRange{-3, 100})
	if err != nil {
		t.Fatal(err)
	}
	if resolved != (sid.CharacterRange{0, 14}) {
		t.Fatalf("resolved range = %v, want [0 14]", resolved)
	}

	view, err := cache.GetSingleSpanDocumentView("d", sid.SingleSpanOptions{
		SnippetField:       "content",
		SnippetDisplaySpan: sid.RangeValue(1, 4),
		DisplayFields:      []string{"content"},
	})
	if err != nil {
		t.Fatal(err)
	}
	xml, err := view.RenderXML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(xml, `#1:4" doc_length=14>`) ||
		!strings.Contains(xml, "\n... alp ...\n") {
		t.Fatalf("unexpected code-point rendering:\n%s", xml)
	}

	strict, err := sid.NewDocumentCache(sid.DocumentCacheOptions{RangeMode: sid.RangeModeStrict})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := strict.AddDocument("d", sid.Document{"content": "short"}); err != nil {
		t.Fatal(err)
	}
	for _, span := range []sid.CharacterRange{{-1, 3}, {1, 9}, {2, 2}, {99, 100}} {
		if _, err := strict.ResolveCharRange("d", "content", span); err == nil {
			t.Errorf("strict range %v did not fail", span)
		} else {
			var invalid *sid.InvalidCharacterRange
			if !errors.As(err, &invalid) {
				t.Errorf("strict range %v returned %T", span, err)
			}
		}
	}
}

func TestXMLAndMarkdownRendering(t *testing.T) {
	t.Parallel()
	document := sid.Document{
		"title":   `A "quote" <b> &`,
		"tags":    []string{"x", "y"},
		"count":   3,
		"zero":    0,
		"no":      false,
		"yes":     true,
		"empty":   []string{},
		"content": "x|y\nz",
	}
	view := sid.NewDocumentViewWithSnippet(
		"d",
		"004218",
		document,
		"content",
		nil,
		[]sid.CharacterRange{{0, 5}},
		[]string{"title", "tags", "count", "zero", "no", "yes", "empty", "content"},
	)

	xml, err := view.RenderXML()
	if err != nil {
		t.Fatal(err)
	}
	expectedXML := `<doc id="004218" doc_length=5 title="A "quote" &lt;b&gt; &amp;" tags="x, y" count=3 yes=True>` +
		"\nx|y\nz\n</doc>"
	if xml != expectedXML {
		t.Fatalf("xml mismatch\ngot:  %s\nwant: %s", xml, expectedXML)
	}

	table, err := sid.RenderMarkdownTable([]*sid.DocumentView{view})
	if err != nil {
		t.Fatal(err)
	}
	expectedTable := "| id | doc_length | title | tags | count | zero | no | yes | empty | content |\n" +
		"|----|------------|-------|------|-------|------|----|-----|-------|---------|\n" +
		`| 004218 | 5 | A "quote" <b> & | x, y | 3 | 0 | False | True |  | x\|y z |`
	if table != expectedTable {
		t.Fatalf("table mismatch\ngot:\n%s\nwant:\n%s", table, expectedTable)
	}
}

func TestPartialAndSeenRendering(t *testing.T) {
	t.Parallel()
	document := sid.Document{"title": "T", "content": "aaaa bbbb cccc dddd"}
	partial := sid.NewDocumentViewWithSnippet(
		"d", "123456", document, "content", nil,
		[]sid.CharacterRange{{5, 9}}, []string{"title", "content"},
	)
	xml, err := partial.RenderXML()
	if err != nil {
		t.Fatal(err)
	}
	expected := `<doc id="123456#5:9" doc_length=19 title="T">` +
		"\n... bbbb ...\n</doc>"
	if xml != expected {
		t.Fatalf("partial xml = %q, want %q", xml, expected)
	}

	seen := sid.NewDocumentViewWithSnippet(
		"d", "123456", document, "content",
		[]sid.CharacterRange{{0, 19}}, nil, []string{"title", "content"},
	)
	parts, err := seen.RenderParts()
	if err != nil {
		t.Fatal(err)
	}
	if parts.ID != "123456" || parts.Body == nil || *parts.Body != `[seen: "#0:19"]` {
		t.Fatalf("fully-seen parts = %#v", parts)
	}
}

func TestMetadataViewAndEmptyMarkdown(t *testing.T) {
	t.Parallel()
	view := sid.NewDocumentView("d", "abcde", sid.Document{"title": "T"}, []string{"title"})
	xml, err := view.RenderXML()
	if err != nil {
		t.Fatal(err)
	}
	if xml != `<doc id="abcde" title="T"></doc>` {
		t.Fatalf("metadata xml = %q", xml)
	}
	table, err := sid.RenderMarkdownTable(nil)
	if err != nil || table != "" {
		t.Fatalf("empty table = %q, %v", table, err)
	}
}
