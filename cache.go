package sid

import (
	"fmt"
	"math"
	"reflect"
	"slices"
	"sort"
	"sync"
	"unicode/utf8"
)

const (
	// SnippetSizeDefault is the default number of UAX #29 source tokens.
	SnippetSizeDefault = 50
	// MinSeenOverlapDefault is the default number of repeated characters
	// required before text is replaced with a seen marker.
	MinSeenOverlapDefault = 100

	// Compatibility spellings from the TypeScript SDK.
	SNIPPET_SIZE_DEFAULT     = SnippetSizeDefault
	MIN_SEEN_OVERLAP_DEFAULT = MinSeenOverlapDefault
)

// SnippetSelector is an optional replacement for the built-in BM25 selector.
// Returned ranges are always strictly validated by DocumentCache.
type SnippetSelector func(
	query, content string,
	options SnippetOptions,
) (CharacterRange, error)

// DocumentCacheOptions configures a new cache.
type DocumentCacheOptions struct {
	Language        Language
	RangeMode       RangeMode
	IDStream        *IDStream
	SnippetSelector SnippetSelector
}

// ApplySnippetOptions configures one snippet selection.
type ApplySnippetOptions struct {
	SnippetField string
	Query        string
	SnippetSize  int

	// MinSeenOverlap is nil for the default. Point to zero to disable the
	// minimum and mask every non-empty overlap.
	MinSeenOverlap *int
	DisplayFields  []string
	Language       Language
}

// IntValue is a convenience for pointer-valued integer options.
func IntValue(value int) *int { return &value }

// SingleSpanOptions configures an exact-range or metadata-only view.
type SingleSpanOptions struct {
	// Empty SnippetField creates a metadata-only view.
	SnippetField string
	// Nil SnippetDisplaySpan displays the whole non-empty snippet field.
	SnippetDisplaySpan *CharacterRange
	// Nil DisplayFields means every field for snippet views. Metadata-only
	// views require a non-nil value; an empty slice is allowed.
	DisplayFields []string
}

// RangeValue is a convenience for SingleSpanOptions.SnippetDisplaySpan.
func RangeValue(start, end int) *CharacterRange {
	value := CharacterRange{start, end}
	return &value
}

type cacheFamily struct {
	mu sync.RWMutex

	documents map[string]Document
	toData    map[string]string
	toModel   map[string]string
	ids       *IDStream
}

// DocumentCache stores documents and stable model-facing IDs. Forks share a
// cache family while retaining independent seen ledgers.
type DocumentCache struct {
	Language  Language
	RangeMode RangeMode

	family   *cacheFamily
	selector SnippetSelector

	seenMu     sync.RWMutex
	seenLedger map[string][]CharacterRange
}

// NewDocumentCache creates a cache. Defaults are English analysis, lenient
// ranges, and six-digit decimal model-facing IDs.
func NewDocumentCache(options ...DocumentCacheOptions) (*DocumentCache, error) {
	if len(options) > 1 {
		return nil, fmt.Errorf("NewDocumentCache accepts at most one options value")
	}
	opts := DocumentCacheOptions{}
	if len(options) == 1 {
		opts = options[0]
	}
	if opts.Language == "" {
		opts.Language = LanguageEnglish
	}
	if opts.RangeMode == "" {
		opts.RangeMode = RangeModeLenient
	}
	if _, err := validateLanguage(opts.Language); err != nil {
		return nil, err
	}
	if _, err := validateRangeMode(opts.RangeMode); err != nil {
		return nil, err
	}
	if opts.IDStream == nil {
		var err error
		opts.IDStream, err = NewIDStream()
		if err != nil {
			return nil, err
		}
	}
	if opts.SnippetSelector == nil {
		opts.SnippetSelector = func(
			query, content string,
			options SnippetOptions,
		) (CharacterRange, error) {
			return BM25SnippetWithStride(query, content, options)
		}
	}

	return &DocumentCache{
		Language:  opts.Language,
		RangeMode: opts.RangeMode,
		family: &cacheFamily{
			documents: make(map[string]Document),
			toData:    make(map[string]string),
			toModel:   make(map[string]string),
			ids:       opts.IDStream,
		},
		selector:   opts.SnippetSelector,
		seenLedger: make(map[string][]CharacterRange),
	}, nil
}

// AddDocument stores one deep copy on first insertion and returns its stable
// model-facing ID. Re-adding dataID is an idempotent lookup: document is not
// inspected or replaced.
func (cache *DocumentCache) AddDocument(dataID string, document Document) (string, error) {
	if cache == nil {
		return "", fmt.Errorf("cannot add a document to a nil cache")
	}

	cache.family.mu.Lock()
	modelID, exists := cache.family.toModel[dataID]
	if !exists {
		stored, err := cloneDocument(document)
		if err != nil {
			cache.family.mu.Unlock()
			return "", err
		}
		modelID, err = cache.family.ids.Mint()
		if err != nil {
			cache.family.mu.Unlock()
			return "", err
		}
		cache.family.documents[dataID] = stored
		cache.family.toModel[dataID] = modelID
		cache.family.toData[modelID] = dataID
	}
	cache.family.mu.Unlock()

	cache.seenMu.Lock()
	if _, known := cache.seenLedger[dataID]; !known {
		cache.seenLedger[dataID] = nil
	}
	cache.seenMu.Unlock()
	return modelID, nil
}

// Contains reports whether this fork family contains dataID.
func (cache *DocumentCache) Contains(dataID string) bool {
	if cache == nil {
		return false
	}
	cache.family.mu.RLock()
	defer cache.family.mu.RUnlock()
	_, ok := cache.family.documents[dataID]
	return ok
}

// ContainsModelFacingID reports whether this family minted modelID.
func (cache *DocumentCache) ContainsModelFacingID(modelID string) bool {
	if cache == nil {
		return false
	}
	cache.family.mu.RLock()
	defer cache.family.mu.RUnlock()
	_, ok := cache.family.toData[modelID]
	return ok
}

// ToDataID resolves a model-facing ID across the fork family.
func (cache *DocumentCache) ToDataID(modelID string) (string, error) {
	cache.family.mu.RLock()
	defer cache.family.mu.RUnlock()
	dataID, ok := cache.family.toData[modelID]
	if !ok {
		return "", fmt.Errorf("model-facing id %q not found in cache", modelID)
	}
	return dataID, nil
}

// ToModelFacingID returns the stable model-facing ID for dataID.
func (cache *DocumentCache) ToModelFacingID(dataID string) (string, error) {
	cache.family.mu.RLock()
	defer cache.family.mu.RUnlock()
	modelID, ok := cache.family.toModel[dataID]
	if !ok {
		return "", fmt.Errorf("data id %q not found in cache", dataID)
	}
	return modelID, nil
}

// GetDocument returns the shared stored document. Treat the returned record as
// read-only; mutating it can invalidate previously recorded ranges.
func (cache *DocumentCache) GetDocument(dataID string) (Document, error) {
	cache.family.mu.RLock()
	defer cache.family.mu.RUnlock()
	document, ok := cache.family.documents[dataID]
	if !ok {
		return nil, fmt.Errorf("data id %q not found in the document cache", dataID)
	}
	return document, nil
}

// GetDocumentFromModelFacingID resolves modelID and returns its document.
func (cache *DocumentCache) GetDocumentFromModelFacingID(modelID string) (Document, error) {
	dataID, err := cache.ToDataID(modelID)
	if err != nil {
		return nil, err
	}
	return cache.GetDocument(dataID)
}

// ResolveCharRange applies this cache's range policy to an exact Unicode
// code-point range.
func (cache *DocumentCache) ResolveCharRange(
	dataID, snippetField string,
	charRange CharacterRange,
) (CharacterRange, error) {
	content, err := cache.content(dataID, snippetField)
	if err != nil {
		return CharacterRange{}, err
	}
	return resolveRange(
		charRange,
		utf8.RuneCountInString(content),
		cache.RangeMode,
		dataID,
	)
}

// ApplySnippet selects the most relevant snippet and masks sufficiently long
// overlaps with this cache's seen ledger.
func (cache *DocumentCache) ApplySnippet(
	dataID string,
	options ApplySnippetOptions,
) (*DocumentViewWithSnippet, error) {
	document, err := cache.GetDocument(dataID)
	if err != nil {
		return nil, err
	}
	content, err := cache.content(dataID, options.SnippetField)
	if err != nil {
		return nil, err
	}

	fields := options.DisplayFields
	if fields == nil {
		fields = documentFields(document)
	} else {
		fields = slices.Clone(fields)
	}
	if !slices.Contains(fields, options.SnippetField) {
		return nil, fmt.Errorf(
			"snippet field %q must be in display fields for document %q",
			options.SnippetField, dataID,
		)
	}

	snippetSize := options.SnippetSize
	if snippetSize == 0 {
		snippetSize = SnippetSizeDefault
	}
	if snippetSize < 1 {
		return nil, fmt.Errorf("snippet size must be greater than zero")
	}
	if uint64(snippetSize) > math.MaxUint32 {
		return nil, fmt.Errorf("snippet size must fit an unsigned 32-bit integer")
	}
	minSeenOverlap := MinSeenOverlapDefault
	if options.MinSeenOverlap != nil {
		minSeenOverlap = *options.MinSeenOverlap
	}
	if minSeenOverlap < 0 {
		return nil, fmt.Errorf("minimum seen overlap must be nonnegative")
	}
	language := options.Language
	if language == "" {
		language = cache.Language
	}
	if _, err := validateLanguage(language); err != nil {
		return nil, err
	}

	span, err := cache.selector(options.Query, content, SnippetOptions{
		WindowSize: snippetSize,
		Stride:     max(1, snippetSize/5),
		Language:   language,
	})
	if err != nil {
		return nil, err
	}
	span, err = resolveRange(
		span,
		utf8.RuneCountInString(content),
		RangeModeStrict,
		dataID,
	)
	if err != nil {
		return nil, err
	}

	cache.seenMu.RLock()
	seen := slices.Clone(cache.seenLedger[dataID])
	cache.seenMu.RUnlock()

	intersections := Overlaps(span, seen)
	fullySeen := len(intersections) == 1 && intersections[0] == span
	var display, masked []CharacterRange
	if fullySeen {
		masked = []CharacterRange{span}
	} else {
		display, masked = PlanSegments(span, seen, minSeenOverlap)
	}
	modelID, err := cache.ToModelFacingID(dataID)
	if err != nil {
		return nil, err
	}
	return NewDocumentViewWithSnippet(
		dataID,
		modelID,
		document,
		options.SnippetField,
		masked,
		display,
		fields,
	), nil
}

// GetSingleSpanDocumentView returns a metadata-only view when SnippetField is
// empty. Otherwise it returns an exact range (or the complete field when the
// range is nil), ignoring the seen ledger.
func (cache *DocumentCache) GetSingleSpanDocumentView(
	dataID string,
	options ...SingleSpanOptions,
) (*DocumentView, error) {
	if len(options) > 1 {
		return nil, fmt.Errorf("GetSingleSpanDocumentView accepts at most one options value")
	}
	opts := SingleSpanOptions{}
	if len(options) == 1 {
		opts = options[0]
	}

	document, err := cache.GetDocument(dataID)
	if err != nil {
		return nil, err
	}
	modelID, err := cache.ToModelFacingID(dataID)
	if err != nil {
		return nil, err
	}

	if opts.SnippetField == "" {
		if opts.DisplayFields == nil {
			return nil, fmt.Errorf(
				"display fields are required when snippet field is omitted for document %q",
				dataID,
			)
		}
		return NewDocumentView(dataID, modelID, document, opts.DisplayFields), nil
	}

	content, err := cache.content(dataID, opts.SnippetField)
	if err != nil {
		return nil, err
	}
	span := CharacterRange{0, utf8.RuneCountInString(content)}
	if opts.SnippetDisplaySpan != nil {
		span, err = cache.ResolveCharRange(dataID, opts.SnippetField, *opts.SnippetDisplaySpan)
		if err != nil {
			return nil, err
		}
	}
	fields := opts.DisplayFields
	if fields == nil {
		fields = documentFields(document)
	}
	return NewDocumentViewWithSnippet(
		dataID,
		modelID,
		document,
		opts.SnippetField,
		nil,
		[]CharacterRange{span},
		fields,
	), nil
}

// UpdateSeen records only the ranges a view displayed. Metadata-only views are
// a no-op.
func (cache *DocumentCache) UpdateSeen(view *DocumentView) error {
	if view == nil {
		return fmt.Errorf("cannot update seen state from a nil document view")
	}
	if _, err := cache.GetDocument(view.DataID); err != nil {
		return err
	}

	cache.seenMu.Lock()
	defer cache.seenMu.Unlock()
	seen := cache.seenLedger[view.DataID]
	for _, span := range view.SnippetDisplaySpans {
		var err error
		seen, err = InsertInterval(seen, span)
		if err != nil {
			return err
		}
	}
	cache.seenLedger[view.DataID] = seen
	return nil
}

// SeenLedger returns a deep copy of this cache's current seen ledger.
func (cache *DocumentCache) SeenLedger() map[string][]CharacterRange {
	cache.seenMu.RLock()
	defer cache.seenMu.RUnlock()
	result := make(map[string][]CharacterRange, len(cache.seenLedger))
	for dataID, spans := range cache.seenLedger {
		result[dataID] = slices.Clone(spans)
	}
	return result
}

// Fork creates caches sharing documents, mappings, and the ID stream, with
// independent copies of the parent's current seen ledger.
func (cache *DocumentCache) Fork(count int) ([]*DocumentCache, error) {
	if count < 0 {
		return nil, fmt.Errorf("fork count must be nonnegative")
	}
	cache.seenMu.RLock()
	snapshot := make(map[string][]CharacterRange, len(cache.seenLedger))
	for dataID, spans := range cache.seenLedger {
		snapshot[dataID] = slices.Clone(spans)
	}
	cache.seenMu.RUnlock()

	forks := make([]*DocumentCache, count)
	for i := range forks {
		ledger := make(map[string][]CharacterRange, len(snapshot))
		for dataID, spans := range snapshot {
			ledger[dataID] = slices.Clone(spans)
		}
		forks[i] = &DocumentCache{
			Language:   cache.Language,
			RangeMode:  cache.RangeMode,
			family:     cache.family,
			selector:   cache.selector,
			seenLedger: ledger,
		}
	}
	return forks, nil
}

// Validate checks that the shared document store and the two ID maps are exact
// inverses. It is intended as a debugging aid.
func (cache *DocumentCache) Validate() error {
	cache.family.mu.RLock()
	defer cache.family.mu.RUnlock()
	if len(cache.family.documents) != len(cache.family.toModel) ||
		len(cache.family.toModel) != len(cache.family.toData) {
		return fmt.Errorf("document store and id mappings have different sizes")
	}
	for dataID := range cache.family.documents {
		modelID, ok := cache.family.toModel[dataID]
		if !ok {
			return fmt.Errorf("document %q has no model-facing id", dataID)
		}
		if cache.family.toData[modelID] != dataID {
			return fmt.Errorf("id mappings disagree for document %q", dataID)
		}
	}
	return nil
}

func (cache *DocumentCache) content(dataID, snippetField string) (string, error) {
	document, err := cache.GetDocument(dataID)
	if err != nil {
		return "", err
	}
	value, err := documentField(document, snippetField)
	if err != nil {
		return "", err
	}
	content, ok := value.(string)
	if !ok || content == "" {
		return "", fmt.Errorf(
			"snippet field %q of document %q must be a non-empty string, got %v",
			snippetField, dataID, value,
		)
	}
	if !utf8.ValidString(content) {
		return "", fmt.Errorf(
			"snippet field %q of document %q must contain well-formed UTF-8",
			snippetField, dataID,
		)
	}
	return content, nil
}

func documentFields(document Document) []string {
	fields := make([]string, 0, len(document))
	for field := range document {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	return fields
}

func cloneDocument(document Document) (Document, error) {
	if document == nil {
		return nil, fmt.Errorf("document must be a non-nil record")
	}
	cloned, err := cloneValue(reflect.ValueOf(document), 0)
	if err != nil {
		return nil, fmt.Errorf("clone document: %w", err)
	}
	return cloned.Interface().(Document), nil
}

func cloneValue(value reflect.Value, depth int) (reflect.Value, error) {
	if depth > 100 {
		return reflect.Value{}, fmt.Errorf("document is cyclic or nested too deeply")
	}
	if !value.IsValid() {
		return value, nil
	}

	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		cloned, err := cloneValue(value.Elem(), depth+1)
		if err != nil {
			return reflect.Value{}, err
		}
		result := reflect.New(value.Type()).Elem()
		result.Set(cloned)
		return result, nil
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		cloned, err := cloneValue(value.Elem(), depth+1)
		if err != nil {
			return reflect.Value{}, err
		}
		result := reflect.New(value.Type().Elem())
		result.Elem().Set(cloned)
		return result, nil
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		result := reflect.MakeMapWithSize(value.Type(), value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			cloned, err := cloneValue(iterator.Value(), depth+1)
			if err != nil {
				return reflect.Value{}, err
			}
			result.SetMapIndex(iterator.Key(), cloned)
		}
		return result, nil
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		result := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := range value.Len() {
			cloned, err := cloneValue(value.Index(i), depth+1)
			if err != nil {
				return reflect.Value{}, err
			}
			result.Index(i).Set(cloned)
		}
		return result, nil
	case reflect.Array:
		result := reflect.New(value.Type()).Elem()
		for i := range value.Len() {
			cloned, err := cloneValue(value.Index(i), depth+1)
			if err != nil {
				return reflect.Value{}, err
			}
			result.Index(i).Set(cloned)
		}
		return result, nil
	case reflect.Struct:
		result := reflect.New(value.Type()).Elem()
		result.Set(value)
		for i := range value.NumField() {
			if !result.Field(i).CanSet() || !value.Field(i).CanInterface() {
				continue
			}
			cloned, err := cloneValue(value.Field(i), depth+1)
			if err != nil {
				return reflect.Value{}, err
			}
			result.Field(i).Set(cloned)
		}
		return result, nil
	case reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return reflect.Value{}, fmt.Errorf("unsupported value of type %s", value.Type())
	default:
		return value, nil
	}
}
