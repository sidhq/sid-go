package sid

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestCacheInsertionLookupsAndIdempotence(t *testing.T) {
	t.Parallel()
	cache, err := NewDocumentCache()
	if err != nil {
		t.Fatal(err)
	}
	input := Document{
		"content": "some text",
		"nested":  map[string]any{"value": 1},
	}
	modelID, err := cache.AddDocument("data-1", input)
	if err != nil {
		t.Fatal(err)
	}
	input["nested"].(map[string]any)["value"] = 2
	stored, err := cache.GetDocument("data-1")
	if err != nil {
		t.Fatal(err)
	}
	if stored["nested"].(map[string]any)["value"] != 1 {
		t.Fatal("AddDocument did not deep-copy its input")
	}

	again, err := cache.AddDocument("data-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if again != modelID {
		t.Fatalf("idempotent add returned %q, want %q", again, modelID)
	}
	if dataID, err := cache.ToDataID(modelID); err != nil || dataID != "data-1" {
		t.Fatalf("ToDataID = %q, %v", dataID, err)
	}
	if id, err := cache.ToModelFacingID("data-1"); err != nil || id != modelID {
		t.Fatalf("ToModelFacingID = %q, %v", id, err)
	}
	if !cache.Contains("data-1") || !cache.ContainsModelFacingID(modelID) {
		t.Fatal("cache membership lookup failed")
	}
	if err := cache.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCacheSnippetSeenMaskingAndRendering(t *testing.T) {
	t.Parallel()
	cache, err := NewDocumentCache()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cache.AddDocument("short", Document{
		"title": "T", "content": "just a few words",
	}); err != nil {
		t.Fatal(err)
	}

	first, err := cache.ApplySnippet("short", ApplySnippetOptions{
		SnippetField:  "content",
		Query:         "words",
		DisplayFields: []string{"title", "content"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.SnippetDisplaySpans) != 1 ||
		first.SnippetDisplaySpans[0] != (CharacterRange{0, 16}) {
		t.Fatalf("first spans = %v", first.SnippetDisplaySpans)
	}
	if err := cache.UpdateSeen(first); err != nil {
		t.Fatal(err)
	}

	repeat, err := cache.ApplySnippet("short", ApplySnippetOptions{
		SnippetField:  "content",
		Query:         "words",
		DisplayFields: []string{"title", "content"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(repeat.SnippetDisplaySpans) != 0 {
		t.Fatalf("repeat displayed spans %v", repeat.SnippetDisplaySpans)
	}
	xml, err := repeat.RenderXML()
	if err != nil {
		t.Fatal(err)
	}
	if want := `[seen: "#0:16"]`; !contains(xml, want) {
		t.Fatalf("repeat xml %q does not contain %q", xml, want)
	}
}

func TestCacheShortSeenRunsAreRedisplayed(t *testing.T) {
	t.Parallel()
	cache := mustCache(t)
	content := wordsForTest(100)
	if _, err := cache.AddDocument("d", Document{"content": content}); err != nil {
		t.Fatal(err)
	}
	seen, err := cache.GetSingleSpanDocumentView("d", SingleSpanOptions{
		SnippetField:       "content",
		SnippetDisplaySpan: RangeValue(0, 50),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.UpdateSeen(seen); err != nil {
		t.Fatal(err)
	}

	view, err := cache.ApplySnippet("d", ApplySnippetOptions{
		SnippetField: "content",
		Query:        "w0090",
		SnippetSize:  200,
	})
	if err != nil {
		t.Fatal(err)
	}
	parts, err := view.RenderParts()
	if err != nil {
		t.Fatal(err)
	}
	if parts.Body == nil || contains(*parts.Body, "[seen:") {
		t.Fatalf("short overlap was masked: %#v", parts.Body)
	}
}

func TestForkSharesFamilyAndIsolatesSeen(t *testing.T) {
	cache := mustCache(t)
	if _, err := cache.AddDocument("a", testDocument("a")); err != nil {
		t.Fatal(err)
	}
	markRange(t, cache, "a", CharacterRange{0, 200})

	forks, err := cache.Fork(2)
	if err != nil {
		t.Fatal(err)
	}
	left, right := forks[0], forks[1]
	newID, err := left.AddDocument("b", testDocument("b"))
	if err != nil {
		t.Fatal(err)
	}
	if !cache.Contains("b") || !right.ContainsModelFacingID(newID) {
		t.Fatal("fork addition was not visible across the family")
	}
	parentDocument, _ := cache.GetDocument("b")
	rightDocument, _ := right.GetDocument("b")
	if fmt.Sprintf("%p", parentDocument) != fmt.Sprintf("%p", rightDocument) {
		t.Fatal("fork family does not share the stored document")
	}

	markRange(t, left, "a", CharacterRange{300, 500})
	if got := left.SeenLedger()["a"]; len(got) != 2 {
		t.Fatalf("left ledger = %v", got)
	}
	for name, other := range map[string]*DocumentCache{"parent": cache, "right": right} {
		if got := other.SeenLedger()["a"]; len(got) != 1 || got[0] != (CharacterRange{0, 200}) {
			t.Fatalf("%s ledger = %v", name, got)
		}
	}
}

func TestSingleSpanRangeModesAndMetadataViews(t *testing.T) {
	t.Parallel()
	cache := mustCache(t)
	if _, err := cache.AddDocument("d", Document{
		"title": "T", "content": "alpha bravo charlie delta",
	}); err != nil {
		t.Fatal(err)
	}
	view, err := cache.GetSingleSpanDocumentView("d", SingleSpanOptions{
		SnippetField:       "content",
		SnippetDisplaySpan: RangeValue(-5, 11),
		DisplayFields:      []string{"title", "content"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.SnippetDisplaySpans[0] != (CharacterRange{0, 11}) {
		t.Fatalf("clamped span = %v", view.SnippetDisplaySpans)
	}
	if _, err := cache.GetSingleSpanDocumentView("d"); err == nil {
		t.Fatal("metadata-only view accepted omitted display fields")
	}
	metadata, err := cache.GetSingleSpanDocumentView("d", SingleSpanOptions{
		DisplayFields: []string{"title"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.UpdateSeen(metadata); err != nil {
		t.Fatal(err)
	}
	if got := cache.SeenLedger()["d"]; len(got) != 0 {
		t.Fatalf("metadata-only view changed seen ledger: %v", got)
	}
}

func TestCacheSelectorOutputIsStrictlyValidated(t *testing.T) {
	t.Parallel()
	cache, err := NewDocumentCache(DocumentCacheOptions{
		SnippetSelector: func(string, string, SnippetOptions) (CharacterRange, error) {
			return CharacterRange{0, 999}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cache.AddDocument("d", Document{"content": "short text"}); err != nil {
		t.Fatal(err)
	}
	_, err = cache.ApplySnippet("d", ApplySnippetOptions{
		SnippetField: "content",
		Query:        "short",
	})
	var invalid *InvalidCharacterRange
	if !errors.As(err, &invalid) {
		t.Fatalf("selector error = %T %v, want InvalidCharacterRange", err, err)
	}
}

func TestCacheConcurrentFamilyAddsAndSeenUpdates(t *testing.T) {
	parent := mustCache(t)
	forks, err := parent.Fork(7)
	if err != nil {
		t.Fatal(err)
	}
	caches := append([]*DocumentCache{parent}, forks...)

	const documentsPerCache = 100
	var wait sync.WaitGroup
	errorsChannel := make(chan error, len(caches))
	for worker, cache := range caches {
		wait.Add(1)
		go func(worker int, cache *DocumentCache) {
			defer wait.Done()
			for i := range documentsPerCache {
				dataID := fmt.Sprintf("doc-%d-%d", worker, i)
				if _, err := cache.AddDocument(dataID, testDocument(dataID)); err != nil {
					errorsChannel <- err
					return
				}
				view, err := cache.GetSingleSpanDocumentView(dataID, SingleSpanOptions{
					SnippetField:       "content",
					SnippetDisplaySpan: RangeValue(0, 100),
				})
				if err != nil {
					errorsChannel <- err
					return
				}
				if err := cache.UpdateSeen(view); err != nil {
					errorsChannel <- err
					return
				}
			}
		}(worker, cache)
	}
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		t.Fatal(err)
	}

	if err := parent.Validate(); err != nil {
		t.Fatal(err)
	}
	for worker, cache := range caches {
		for i := range documentsPerCache {
			dataID := fmt.Sprintf("doc-%d-%d", worker, i)
			if !parent.Contains(dataID) {
				t.Fatalf("family lost %s", dataID)
			}
			if got := cache.SeenLedger()[dataID]; len(got) != 1 || got[0] != (CharacterRange{0, 100}) {
				t.Fatalf("%s ledger = %v", dataID, got)
			}
		}
	}
}

func mustCache(t *testing.T) *DocumentCache {
	t.Helper()
	cache, err := NewDocumentCache()
	if err != nil {
		t.Fatal(err)
	}
	return cache
}

func testDocument(tag string) Document {
	return Document{
		"title":   "doc " + tag,
		"content": wordsForTest(500),
		"meta":    map[string]any{"tag": tag},
	}
}

func markRange(t *testing.T, cache *DocumentCache, dataID string, span CharacterRange) {
	t.Helper()
	view, err := cache.GetSingleSpanDocumentView(dataID, SingleSpanOptions{
		SnippetField:       "content",
		SnippetDisplaySpan: &span,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.UpdateSeen(view); err != nil {
		t.Fatal(err)
	}
}

func contains(value, substring string) bool {
	for i := 0; i+len(substring) <= len(value); i++ {
		if value[i:i+len(substring)] == substring {
			return true
		}
	}
	return false
}
