// SID-2 preview.
//
// Before using this template: implement the vectorSearch and keywordSearch
// backend functions, set idField and snippetField to your document's fields,
// and customize the instructions, question, and toolExamples to fit your data.
// You can also define additional tools, or extend existing tools with more
// parameters (e.g. a filter). Just make sure to always update the tool's
// description and usage example. The report_helpful_ids tool is fixed and
// cannot be changed or modified.
//
// Run:
//
//	cd examples && SID_API_KEY=your-api-key go run ./reference_implementation
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
	sid "github.com/sidhq/sid-go"
)

// ToolError is a mistake the *model* made (bad id, malformed ref, invalid
// arguments). These errors are recoverable and passed back to the model as a
// message.
type ToolError struct{ message string }

func (e *ToolError) Error() string { return e.message }

func toolErrorf(format string, args ...any) error {
	return &ToolError{fmt.Sprintf(format, args...)}
}

// ---------------------------------------------------------------------------
// The episode. TODO: your question. The model is served by the first-party
// SID API, under one SID_API_KEY.
// ---------------------------------------------------------------------------

const question = "What government position was held by the woman who portrayed Corliss Archer in the film Kiss and Tell?"

const (
	model           = "sid-2"
	maxTurns        = 10
	reward          = "f1"  // the trained scoring prompt: "f1" (few, precise ids) | "ndcg" (many, best first)
	reasoningEffort = "low" // "low" | "medium" | "high"

	defaultLimit       = 5      // results per search when the model does not ask for a number
	maxReadChars       = 10_000 // document text returned per read call
	maxParallelFetches = 8      // backend queries in flight at once, across a turn's search calls
)

func baseURL() string {
	if url := os.Getenv("SID_BASE_URL"); url != "" {
		return strings.TrimRight(url, "/")
	}
	return "https://api.sid-1.com/v1"
}

// ---------------------------------------------------------------------------
// TODO: your document backend. The tools below call these functions;
// implement them against your database. A document is a map with:
//
//	idField      : your document id (any stable string; never shown to the model)
//	snippetField : the document's canonical text. Return the identical string
//	               for the same id everywhere: the seen-ledger's character
//	               offsets index it.
//	extras       : (optional) any other fields, such as a "title", rendered as
//	               display attributes. Every cached field is shown to the
//	               model (idField is removed before caching), so leave
//	               anything private out of your documents.
// ---------------------------------------------------------------------------

const (
	idField      = "id"
	snippetField = "content"
)

// vectorSearch is your semantic (embedding) search: the limit most relevant
// documents for query, best first.
func vectorSearch(ctx context.Context, query string, limit int) ([]sid.Document, error) {
	return nil, errors.New("TODO: implement vectorSearch against your document store")
}

// keywordSearch is your full-text keyword (BM25) search: the limit best
// matching documents for query.
func keywordSearch(ctx context.Context, query string, limit int) ([]sid.Document, error) {
	return nil, errors.New("TODO: implement keywordSearch against your document store")
}

// ---------------------------------------------------------------------------
// The system prompt: one sentence naming your subject matter, then the
// search -> repeat -> report scaffolding. TODO: edit the first paragraph to
// describe your data and goal; keep the steps.
// ---------------------------------------------------------------------------

const instructions = `You are an expert research assistant that is given a question and must use the provided search tools to find all documents needed to answer the question.

Steps:
1. Reflect on what information is needed to answer the question and use the search tools to find documents. Each document has an id.
2. Repeat step 1 until all documents necessary and sufficient to answer the question have been found. Take as many turns and searches as needed – you can make multiple searches per turn! Most questions will require multiple turns. Many will need more.
3. Use the report_helpful_ids tool to report the most helpful document ids. List the most helpful document ids first (important!).

The episode ends once report_helpful_ids is called.`

// ---------------------------------------------------------------------------
// The tool schemas. Each tool should be demonstrated in toolExamples.
// ---------------------------------------------------------------------------

type object = map[string]any

var snippetSizeDescription = fmt.Sprintf(
	"Words shown per result snippet (default %d). Must be a positive integer. "+
		"Pass a larger value to see more of each document.",
	sid.SnippetSizeDefault,
)

var searchTool = object{
	"type": "function",
	"name": "search",
	"description": "Semantic (vector) search over the dataset. Best for conceptual or paraphrased " +
		"queries: a long, descriptive query of what you want beats a few keywords; use " +
		"text_search when you know the distinctive words a match would contain. Result " +
		"ids may be ranged references 'id#start:end' naming the exact character span of " +
		"the document shown; stretches you have already been shown are replaced by " +
		"'[seen: \"#start:end\"]' markers instead of repeating, and doc_length gives the " +
		"document's total character count.",
	"parameters": object{
		"type": "object",
		"properties": object{
			"query":        object{"type": "string", "description": "The query to search the dataset with"},
			"limit":        object{"type": "integer", "description": "The number of results to return"},
			"snippet_size": object{"type": "integer", "description": snippetSizeDescription},
		},
		"required": []string{"query"},
	},
}

var textSearchTool = object{
	"type": "function",
	"name": "text_search",
	"description": "Full-text keyword search over the dataset, ranked by BM25. Provide the terms you " +
		"want matched as plain words separated by spaces: the distinctive content words a " +
		"matching document would contain. Result ids may be ranged references 'id#start:end' naming the exact " +
		"character span of the document shown; stretches you have already been shown are " +
		"replaced by '[seen: \"#start:end\"]' markers instead of repeating, and doc_length " +
		"gives the document's total character count.",
	"parameters": object{
		"type": "object",
		"properties": object{
			"query":        object{"type": "string", "description": "Plain space-separated content words to match (BM25)."},
			"limit":        object{"type": "integer", "description": "The number of results to return"},
			"snippet_size": object{"type": "integer", "description": snippetSizeDescription},
		},
		"required": []string{"query"},
	},
}

var readTool = object{
	"type": "function",
	"name": "read",
	"description": "Search results show only short snippets. Read up to 10,000 characters of a document per call. " +
		"A bare ID starts at character 0; a ranged reference starts at its requested offset. Use further ranged reads to continue.",
	"parameters": object{
		"type": "object",
		"properties": object{
			"id": object{
				"type": "string",
				"description": "The id of the document to read. Also accepts a ranged reference " +
					"'id#start:end' (character offsets, e.g. '123456#800:1600') to read only " +
					"that part of the document's text; a range running past the end of the " +
					"document is trimmed to the part that overlaps it.",
			},
		},
		"required": []string{"id"},
	},
}

// reportTool is the fixed terminal tool: do not change its name, description
// or parameters.
var reportTool = object{
	"type": "function",
	"name": "report_helpful_ids",
	"description": "Report the most helpful document ids from the search results. This is required in " +
		"the last step. Each id may be reported at most once; repeated ids are penalized. " +
		"Ids may be whole documents ('123456') or ranged references ('123456#800:1600'); a " +
		"ranged reference counts as its whole document, so there is no need to strip the " +
		"'#start:end' part.",
	"parameters": object{
		"type": "object",
		"properties": object{
			"ids": object{
				"type":        "array",
				"description": "The ids of the most helpful documents from the search results.",
				"items":       object{"type": "string"},
			},
		},
		"required": []string{"ids"},
	},
}

var tools = []object{searchTool, textSearchTool, readTool, reportTool}

func toolProperties(name string) object {
	for _, tool := range tools {
		if tool["name"] == name {
			return tool["parameters"].(object)["properties"].(object)
		}
	}
	return nil
}

func toolNames() []string {
	names := make([]string, len(tools))
	for i, tool := range tools {
		names[i] = tool["name"].(string)
	}
	return names
}

// toolExamples are few-shot calls, sent on every request via the sid
// extension. TODO: write search examples that fit your data; keep the read and
// report examples as they are.
var toolExamples = []object{
	{
		"caption":   "Example: a semantic search with a long, descriptive query:",
		"name":      "search",
		"arguments": object{"query": "the actress who played Corliss Archer in the 1945 film, and the government post she later held", "limit": 5},
	},
	{
		"caption":   "Example: a keyword search:",
		"name":      "text_search",
		"arguments": object{"query": "Kiss and Tell 1945 Shirley Temple Corliss Archer", "limit": 5},
	},
	{
		"caption":   "Example: reading one character range:",
		"name":      "read",
		"arguments": object{"id": "placeholder_1#800:1600"},
	},
	{
		"caption":   "Example: ending the episode with a report:",
		"name":      "report_helpful_ids",
		"arguments": object{"ids": []string{"placeholder_1", "placeholder_2", "placeholder_3"}},
	},
}

// ---------------------------------------------------------------------------
// The tools, split in two: fetchResults is the slow backend call and runs in
// parallel; everything below it touches the cache and runs serially, in call
// order.
// ---------------------------------------------------------------------------

// fetchResults is the backend call behind one search tool call. read and the
// report have nothing to fetch (reads are served from the document cache), so
// they get nil.
func fetchResults(ctx context.Context, name string, arguments object) ([]sid.Document, error) {
	if name != "search" && name != "text_search" {
		return nil, nil
	}

	// Catch mistyped arguments as a ToolError that the model can recover from.
	properties := toolProperties(name)
	for key, value := range arguments {
		expected := properties[key].(object)["type"]
		_, isString := value.(string)
		if expected == "string" && !isString || expected == "integer" && !isInteger(value) {
			return nil, toolErrorf("Invalid %s arguments: %s must be of type %s, got %T", name, key, expected, value)
		}
	}

	query, _ := arguments["query"].(string) // toolArguments strips ""; treat missing as empty
	if query == "" {
		return nil, toolErrorf("search requires a non-empty `query` to rank documents against. " +
			"To fetch a specific document by id, use the `read` tool. " +
			"To rank candidates against the question, call `search` with a meaningful query.")
	}

	snippetSize := intArgument(arguments, "snippet_size", sid.SnippetSizeDefault)
	if snippetSize <= 0 {
		return nil, toolErrorf("Invalid %s arguments: snippet_size must be a positive integer, got %d", name, snippetSize)
	}

	limit := intArgument(arguments, "limit", defaultLimit)
	if name == "search" {
		return vectorSearch(ctx, query, limit)
	}
	return keywordSearch(ctx, query, limit)
}

// renderResults renders one <doc> per result, in backend order. Shared by
// both search tools; only their fetch differs.
func renderResults(cache *sid.DocumentCache, results []sid.Document, query string, snippetSize int) (string, error) {
	rendered := make([]string, 0, len(results))
	for _, result := range results {
		dataID, ok := result[idField].(string)
		if !ok || dataID == "" {
			return "", fmt.Errorf("backend document must carry a non-empty string %q: %v", idField, result)
		}
		// Keep backend results intact when searches reuse the same maps.
		document := maps.Clone(result)
		// Removed, not read: every cached field renders, and your database ids must not.
		delete(document, idField)
		if content, ok := document[snippetField].(string); !ok || content == "" {
			// Not a model mistake: the seen-ledger's offsets index this string.
			return "", fmt.Errorf("backend document %q must carry non-empty %q text", dataID, snippetField)
		}

		// Searches routinely return the same document, and the cache is add-once.
		if _, err := cache.AddDocument(dataID, document); err != nil {
			return "", err
		}

		// BM25 snippet, with already-shown stretches collapsed to [seen: ...].
		view, err := cache.ApplySnippet(dataID, sid.ApplySnippetOptions{
			SnippetField: snippetField,
			Query:        query,
			SnippetSize:  snippetSize,
		})
		if err != nil {
			return "", err
		}
		xml, err := view.RenderXML()
		if err != nil {
			return "", err
		}
		rendered = append(rendered, xml)
		if err := cache.UpdateSeen(view); err != nil { // later views of this span collapse to [seen: ...]
			return "", err
		}
	}
	if len(rendered) == 0 {
		return "No results found. Try a different query.", nil
	}
	return strings.Join(rendered, "\n"), nil
}

// parseReference splits the model's ref ('R' or 'R#start:end') into id and
// optional char range. The SDK parser already writes a descriptive message.
func parseReference(reference any) (string, *sid.CharacterRange, error) {
	value, ok := reference.(string)
	if !ok {
		return "", nil, toolErrorf("id must be a string, got %T", reference)
	}
	modelID, charRange, err := sid.ParseRenderedModelFacingID(value)
	if err != nil {
		return "", nil, &ToolError{err.Error()}
	}
	return modelID, charRange, nil
}

// read fetches one document (or a character range of it) by the id a search
// returned. Explicit reads are not masked: the model asked for this span, so
// it renders up to maxReadChars characters. Model mistakes (bad ref grammar,
// an unknown or hallucinated id, an unusable range) return a ToolError.
func read(cache *sid.DocumentCache, reference any) (string, error) {
	modelID, charRange, err := parseReference(reference) // ToolError on bad grammar
	if err != nil {
		return "", err
	}
	if !cache.ContainsModelFacingID(modelID) {
		return "", toolErrorf("Invalid document id '%s'. "+
			"You can only read documents returned by search. "+
			"Use 'search' or 'text_search' first.", modelID)
	}
	dataID, err := cache.ToDataID(modelID) // the one conversion back into your ids
	if err != nil {
		return "", err
	}

	if charRange != nil {
		// Lenient mode trims partially overlapping ranges to the document.
		// Invalid ranges become recoverable tool errors.
		resolved, err := cache.ResolveCharRange(dataID, snippetField, *charRange)
		var invalid *sid.InvalidCharacterRange
		if errors.As(err, &invalid) {
			return "", toolErrorf("%s. Re-read '%s' without a range to read from the "+
				"start, or pass a range inside 0:doc_length.", invalid, modelID)
		} else if err != nil {
			return "", err
		}
		charRange = &resolved
	}

	// Cap document text per read; further ranged reads can continue from here.
	// Count Unicode code points, matching the SDK's character offsets.
	if charRange != nil {
		charRange = sid.RangeValue(charRange.Start(), min(charRange.End(), charRange.Start()+maxReadChars))
	} else {
		document, err := cache.GetDocument(dataID)
		if err != nil {
			return "", err
		}
		if content, _ := document[snippetField].(string); utf8.RuneCountInString(content) > maxReadChars {
			charRange = sid.RangeValue(0, maxReadChars)
		}
	}

	view, err := cache.GetSingleSpanDocumentView(dataID, sid.SingleSpanOptions{
		SnippetField:       snippetField,
		SnippetDisplaySpan: charRange,
	})
	if err != nil {
		return "", err
	}
	rendered, err := view.RenderXML()
	if err != nil {
		return "", err
	}
	return rendered, cache.UpdateSeen(view)
}

// report is the terminal report: ids are doc-granular (a ranged ref denotes
// its document), deduplicated preserving order, and translated back to your
// data ids. IDs outside the model-facing ID map are dropped.
func report(cache *sid.DocumentCache, ids any) ([]string, error) {
	values, ok := ids.([]any)
	if !ok {
		return nil, toolErrorf("ids must be a list of strings")
	}
	if len(values) == 0 {
		return nil, toolErrorf("ids list cannot be empty")
	}
	var reported []string
	for _, reference := range values {
		if _, ok := reference.(string); !ok {
			return nil, toolErrorf("ids must be a list of strings")
		}
		modelID, _, err := parseReference(reference) // ToolError on bad grammar
		if err != nil {
			return nil, err
		}
		if !slices.Contains(reported, modelID) {
			reported = append(reported, modelID)
		}
	}

	dataIDs := make([]string, 0, len(reported))
	for _, modelID := range reported {
		if dataID, err := cache.ToDataID(modelID); err == nil {
			dataIDs = append(dataIDs, dataID)
		}
	}
	return dataIDs, nil
}

// ---------------------------------------------------------------------------
// The agent loop: one Responses API call per turn, tool outputs paired back
// by call_id. The episode ends when the model reports.
//
// Requests are stateless (store=false): nothing is retained server-side, so
// the full conversation is resent on every call. The model's reasoning comes
// back as an opaque encrypted_content blob on each reasoning item, and
// carries over by echoing the output items back verbatim in the next input.
//
// The prompt (instructions, tools, reasoning effort, tool_examples, reward) is
// fixed for the life of the conversation. Build the request once and send it
// unchanged on every turn; only input grows.
// ---------------------------------------------------------------------------

type fetched struct {
	results []sid.Document
	err     error
}

func main() {
	ctx := context.Background()
	cache, err := sid.NewDocumentCache(sid.DocumentCacheOptions{RangeMode: sid.RangeModeLenient}) // this episode's id map, document cache and seen-ledger
	if err != nil {
		log.Fatal(err)
	}
	apiKey := os.Getenv("SID_API_KEY")
	if apiKey == "" {
		log.Fatal("SID_API_KEY is not set")
	}
	client := openai.NewClient(option.WithBaseURL(baseURL()), option.WithAPIKey(apiKey))

	// One request shape, only update the history at each turn.
	request := responses.ResponseNewParams{
		Model:           model,
		MaxOutputTokens: openai.Int(4096),
		Store:           openai.Bool(false),
		Include:         []responses.ResponseIncludable{responses.ResponseIncludableReasoningEncryptedContent},
		Instructions:    openai.String(instructions),
		// Reasoning effort, tool_examples, and the target reward are managed server-side
		Reasoning: shared.ReasoningParam{Effort: reasoningEffort},
	}
	options := []option.RequestOption{
		// The tools and the sid extension are plain JSON, set on the request body.
		option.WithJSONSet("tools", tools),
		option.WithJSONSet("sid", object{"tool_examples": toolExamples, "reward": reward}),
		option.WithHeader("x-session-affinity", newSessionID()), // KV cache routing: one session per episode
	}

	// The whole conversation as Responses API input items, grown every turn.
	history := responses.ResponseInputParam{
		responses.ResponseInputItemParamOfMessage(question, responses.EasyInputMessageRoleUser),
	}
	create := func() (*responses.Response, error) {
		request.Input = responses.ResponseNewParamsInputUnion{OfInputItemList: history}
		return client.Responses.New(ctx, request, options...)
	}

	fmt.Printf("Q: %s\n", question)
	resp, err := create()
	if err != nil {
		log.Fatal(err)
	}

	for turn := 1; turn <= maxTurns; turn++ {
		printModelOutput(turn, resp)
		var calls []responses.ResponseFunctionToolCall
		for _, item := range resp.Output {
			if item.Type == "function_call" {
				calls = append(calls, item.AsFunctionCall())
			}
		}
		arguments := make([]object, len(calls))
		for i, call := range calls {
			arguments[i] = toolArguments(call)
		}

		// Execute the model's tool calls in parallel; maxParallelFetches caps
		// how many queries your backend sees at once (1 makes the turn serial).
		fetches := make([]fetched, len(calls))
		semaphore := make(chan struct{}, maxParallelFetches)
		var wg sync.WaitGroup
		for i, call := range calls {
			wg.Add(1)
			go func() {
				defer wg.Done()
				semaphore <- struct{}{}
				defer func() { <-semaphore }()
				results, err := fetchResults(ctx, call.Name, arguments[i])
				fetches[i] = fetched{results, err}
			}()
		}
		wg.Wait()

		// Compose observations serially, in call order: what earlier calls show, later calls mask.
		observations := make([]string, len(calls))
		var reported []string
		for i, call := range calls {
			observation, err := runTool(cache, turn, len(calls), call.Name, arguments[i], fetches[i], &reported)
			var toolError *ToolError
			if errors.As(err, &toolError) {
				// A recoverable mistake made by the model.
				observation = fmt.Sprintf("Error executing %s: %s", call.Name, toolError)
			} else if err != nil {
				log.Fatal(err)
			}
			observations[i] = observation
			fmt.Printf("  [%s] -> %s\n", call.Name, observation) // verbatim: what you see is what the model receives
		}

		// Termination conditions
		if reported != nil {
			printReport(cache, reported)
			return
		}
		if turn == maxTurns {
			fmt.Printf("\n[max turns reached: %d: episode ends without a report]\n", maxTurns)
			return
		}
		if resp.Status == responses.ResponseStatusIncomplete {
			fmt.Println("\n[generation hit max_output_tokens; ending the episode]")
			return
		}

		// Non-terminal turn: append the model's output verbatim (the reasoning
		// items carry their encrypted_content), then feed back the tool outputs
		// (paired by call_id) plus the turn budget indicator as a user message.
		for _, item := range resp.Output {
			history = append(history, param.Override[responses.ResponseInputItemUnionParam](json.RawMessage(item.RawJSON())))
		}
		for i, call := range calls {
			output := responses.ResponseInputItemParamOfFunctionCallOutput(observations[i])
			output.OfFunctionCallOutput.CallID = openai.String(call.CallID)
			history = append(history, output)
		}

		remaining := maxTurns - turn
		unit := "turns"
		if remaining == 1 {
			unit = "turn"
		}
		messages := []string{fmt.Sprintf("You have %d %s remaining out of %d total.", remaining, unit, maxTurns)}
		if turn == maxTurns-1 {
			messages = append(messages, fmt.Sprintf(
				"This is your last turn: you must call %s now. "+
					"Anything you have found but not reported is lost when this turn ends.", reportTool["name"]))
		}
		for _, message := range messages {
			history = append(history, responses.ResponseInputItemParamOfMessage(message, responses.EasyInputMessageRoleUser))
			fmt.Printf("[user] %s\n", message)
		}

		if resp, err = create(); err != nil {
			log.Fatal(err)
		}
	}
}

// runTool dispatches one call. A ToolError is a recoverable model mistake;
// any other error is a bug or a backend failure and ends the episode.
func runTool(
	cache *sid.DocumentCache,
	turn, callCount int,
	name string,
	arguments object,
	fetch fetched,
	reported *[]string,
) (string, error) {
	if fetch.err != nil {
		return "", fetch.err
	}
	switch name {
	case "search", "text_search":
		return renderResults(cache, fetch.results, arguments["query"].(string),
			intArgument(arguments, "snippet_size", sid.SnippetSizeDefault))
	case "read":
		return read(cache, arguments["id"])
	case "report_helpful_ids":
		if callCount > 1 {
			return "", toolErrorf("Combining report tool call with other tool calls is not allowed.")
		}
		if turn == 1 {
			return "", toolErrorf("Cannot report in the first turn. Must make a search first.")
		}
		ids, err := report(cache, arguments["ids"])
		if err != nil {
			return "", err
		}
		*reported = ids
		return "reported ids submitted: " + strings.Join(ids, ","), nil
	default:
		return fmt.Sprintf("Tool '%s' is not a valid tool. Available tools: %v", name, toolNames()), nil
	}
}

// toolArguments parses a call's JSON arguments: keep only the parameters the
// tool's schema declares, and drop the ""/null "defaults" LLMs like to send.
// Anything else the model got wrong comes back as a ToolError from the fetch
// or the tool.
func toolArguments(call responses.ResponseFunctionToolCall) object {
	var arguments object
	properties := toolProperties(call.Name)
	if json.Unmarshal([]byte(call.Arguments), &arguments) != nil || properties == nil {
		return object{}
	}
	for key, value := range arguments {
		if _, declared := properties[key]; !declared || value == nil || value == "" {
			delete(arguments, key)
		}
	}
	return arguments
}

// isInteger reports whether a decoded JSON value is a whole number.
func isInteger(value any) bool {
	number, ok := value.(float64)
	return ok && number == float64(int(number))
}

func intArgument(arguments object, key string, fallback int) int {
	if value, ok := arguments[key].(float64); ok {
		return int(value)
	}
	return fallback
}

func newSessionID() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		log.Fatal(err)
	}
	return hex.EncodeToString(id[:])
}

// printModelOutput prints the model's side of one turn: its thinking, any
// prose, and its calls.
func printModelOutput(turn int, resp *responses.Response) {
	fmt.Printf("\n--- turn %d (status=%s) %s\n", turn, resp.Status, strings.Repeat("-", 40))
	for _, item := range resp.Output {
		switch item.Type {
		case "reasoning":
			for _, part := range item.AsReasoning().Content {
				fmt.Printf("[thinking] %s\n", part.Text)
			}
		case "message":
			fmt.Printf("[assistant] %s\n", resp.OutputText())
		case "function_call":
			fmt.Printf(">>> %s(%s)\n", item.Name, item.AsFunctionCall().Arguments)
		}
	}
}

// printReport prints the episode's answer: your database ids, most helpful
// first.
func printReport(cache *sid.DocumentCache, reported []string) {
	fmt.Printf("\nReported for: %s\n", question)
	for _, dataID := range reported {
		document, _ := cache.GetDocument(dataID)
		title, _ := document["title"].(string)
		fmt.Printf("   %s  %s\n", dataID, title)
	}
}
