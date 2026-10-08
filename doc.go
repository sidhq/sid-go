// Package sid turns search results into compact, model-facing document views.
//
// It assigns stable short IDs, selects relevant Unicode-aware snippets with
// unigram and bigram BM25, tracks exact character ranges already shown, masks
// repeated text, and renders SID's <doc> and Markdown table formats.
//
// All character offsets are half-open Unicode code-point offsets rather than
// UTF-8 byte offsets.
package sid
