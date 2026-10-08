# SID SDK for Go

`sid-go` turns search results into compact, model-facing document views. It
assigns stable short IDs, selects relevant snippets, tracks the exact character
ranges already shown, masks repeated text, and renders SID's `<doc>` format.

## Install

```sh
go get github.com/sidhq/sid-go
```

The module supports Go 1.23 and newer.

## Basic use

```go
package main

import (
	"fmt"
	"log"

	sid "github.com/sidhq/sid-go"
)

func main() {
	cache, err := sid.NewDocumentCache()
	if err != nil {
		log.Fatal(err)
	}
	_, err = cache.AddDocument("database-id", sid.Document{
		"title":   "Example",
		"content": "The complete document text ...",
	})
	if err != nil {
		log.Fatal(err)
	}

	view, err := cache.ApplySnippet("database-id", sid.ApplySnippetOptions{
		SnippetField: "content",
		Query:        "complete document",
	})
	if err != nil {
		log.Fatal(err)
	}
	rendered, err := view.RenderXML()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(rendered)
	if err := cache.UpdateSeen(view); err != nil {
		log.Fatal(err)
	}
}
```

## API

- `NewDocumentCache`: defaults to English analysis, lenient ranges, and
  six-digit decimal model IDs.
- `AddDocument`: deep-copies a document on first insertion and returns its
  stable model-facing ID. Re-adding a data ID is an idempotent lookup.
- `ApplySnippet`: selects a BM25 snippet and masks text already recorded in
  that cache's seen ledger.
- `GetSingleSpanDocumentView`: displays an exact range, a whole content field,
  or explicit metadata fields.
- `ResolveCharRange`: validates or clamps a model-provided range.
- `UpdateSeen`: records only displayed spans. Rendering and updating are
  separate operations.
- `Fork`: shares documents, mappings, and the ID stream while copying the
  parent's current seen ledger.
- `ParseRenderedModelFacingID`: parses bare and ranged references such as
  `004218#10:70`.
- `RenderMarkdownTable`: renders multiple views in a compact table.
- `BM25SnippetWithStride`: exposes the snippet selector directly.
- `NewIDStream`: creates a collision-free, seeded or random ID permutation.

Unknown IDs and invalid input return errors. Use `errors.As` with
`*sid.InvalidCharacterRange` or `*sid.IDSpaceExhausted` when error type matters.

## Character ranges

Ranges are half-open Unicode code-point offsets. They are not UTF-8 byte
offsets. Combining marks count separately.

Lenient mode intersects a partially overlapping range with the document.
Strict mode requires `0 <= start < end <= document length`. Empty, inverted,
and wholly disjoint ranges always fail.

```go
cache, _ := sid.NewDocumentCache(sid.DocumentCacheOptions{
	RangeMode: sid.RangeModeStrict,
})
```

## Languages and snippets

Named analyzers are available for Danish, Dutch, English, Finnish, French,
German, Hungarian, Italian, Norwegian, Portuguese, Russian, Spanish, and
Swedish. `LanguageGeneric` performs lowercase Unicode UAX #29 tokenization
without stopword removal. Named analyzers use the same stopword sets as the
Python and TypeScript SDKs.

Snippet windows use Unicode UAX #29 source tokens. Separate unigram and bigram
BM25 scores rank each window. A stopword-only or unmatched query selects the
earliest window.

## Rendering

XML escapes `&`, `<`, and `>` while preserving SID's trained observation
format. Quotes remain unchanged, falsy attributes are omitted, and integers
and booleans are unquoted. Lists and arrays join with commas.

Document values should be treated as read-only after insertion. Default display
fields are sorted by name because Go maps do not preserve insertion order; pass
`DisplayFields` for an explicit order.

## Forks and concurrency

Fork-family document additions and ID allocation are safe across goroutines.
Each fork has an independently locked seen ledger. `ApplySnippet` and
`UpdateSeen` remain separate operations, so callers should serialize that pair
when multiple goroutines use the same fork as one agent.

## Development

```sh
go test ./...
go test -race ./...
go vet ./...
```

## License

MIT. See [LICENSE](LICENSE) and
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
