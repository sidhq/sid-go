package sid

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"
)

// Document is a record stored and rendered by DocumentCache.
type Document map[string]any

// Attribute preserves a rendered field's name, value, and order.
type Attribute struct {
	Name  string
	Value any
}

// RenderParts is the format-independent representation of a document view.
// Body is nil for a metadata-only view.
type RenderParts struct {
	ID         string
	Attributes []Attribute
	Body       *string
}

// AttributeMap returns a copy of the attributes keyed by field name.
func (parts RenderParts) AttributeMap() map[string]any {
	result := make(map[string]any, len(parts.Attributes))
	for _, attribute := range parts.Attributes {
		result[attribute.Name] = attribute.Value
	}
	return result
}

// DocumentView is a model-facing view of one document. SnippetField is empty
// for a metadata-only view.
type DocumentView struct {
	DataID              string
	ModelFacingID       string
	Document            Document
	DisplayFields       []string
	SnippetField        string
	SnippetSeenSpans    []CharacterRange
	SnippetDisplaySpans []CharacterRange
}

// DocumentViewWithSnippet is a compatibility alias. Go uses one view type so
// metadata-only and snippet views can be handled uniformly.
type DocumentViewWithSnippet = DocumentView

// NewDocumentView constructs a metadata-only view.
func NewDocumentView(dataID, modelFacingID string, document Document, displayFields []string) *DocumentView {
	return &DocumentView{
		DataID:        dataID,
		ModelFacingID: modelFacingID,
		Document:      document,
		DisplayFields: slices.Clone(displayFields),
	}
}

// NewDocumentViewWithSnippet constructs a snippet view.
func NewDocumentViewWithSnippet(
	dataID, modelFacingID string,
	document Document,
	snippetField string,
	snippetSeenSpans, snippetDisplaySpans []CharacterRange,
	displayFields []string,
) *DocumentViewWithSnippet {
	return &DocumentView{
		DataID:              dataID,
		ModelFacingID:       modelFacingID,
		Document:            document,
		DisplayFields:       slices.Clone(displayFields),
		SnippetField:        snippetField,
		SnippetSeenSpans:    slices.Clone(snippetSeenSpans),
		SnippetDisplaySpans: slices.Clone(snippetDisplaySpans),
	}
}

// HasSnippet reports whether this view has a content body.
func (view *DocumentView) HasSnippet() bool {
	return view != nil && view.SnippetField != ""
}

// RenderParts returns the model-facing ID, ordered attributes, and optional
// body shared by XML and Markdown rendering.
func (view *DocumentView) RenderParts(displayFields ...[]string) (RenderParts, error) {
	if view == nil {
		return RenderParts{}, fmt.Errorf("cannot render a nil document view")
	}
	if len(displayFields) > 1 {
		return RenderParts{}, fmt.Errorf("RenderParts accepts at most one display-fields value")
	}
	fields := view.DisplayFields
	if len(displayFields) == 1 {
		fields = displayFields[0]
	}

	if view.SnippetField == "" {
		attributes := make([]Attribute, 0, len(fields))
		for _, name := range fields {
			value, err := documentField(view.Document, name)
			if err != nil {
				return RenderParts{}, err
			}
			attributes = append(attributes, Attribute{Name: name, Value: value})
		}
		return RenderParts{
			ID:         view.ModelFacingID,
			Attributes: attributes,
		}, nil
	}

	contentValue, err := documentField(view.Document, view.SnippetField)
	if err != nil {
		return RenderParts{}, err
	}
	content, ok := contentValue.(string)
	if !ok || content == "" {
		return RenderParts{}, fmt.Errorf("snippet field %q must be a non-empty string", view.SnippetField)
	}
	text, err := newCodePointText(content)
	if err != nil {
		return RenderParts{}, err
	}

	type segment struct {
		kind       string
		start, end int
	}
	segments := make([]segment, 0, len(view.SnippetDisplaySpans)+len(view.SnippetSeenSpans))
	for _, span := range view.SnippetDisplaySpans {
		segments = append(segments, segment{kind: "show", start: span[0], end: span[1]})
	}
	for _, span := range view.SnippetSeenSpans {
		segments = append(segments, segment{kind: "seen", start: span[0], end: span[1]})
	}
	slices.SortStableFunc(segments, func(left, right segment) int {
		return left.start - right.start
	})
	if len(segments) == 0 {
		return RenderParts{}, fmt.Errorf("snippet view requires at least one span")
	}

	start, end := segments[0].start, segments[len(segments)-1].end
	fullySeen := len(view.SnippetDisplaySpans) == 0
	var body strings.Builder
	if start > 0 && !fullySeen {
		body.WriteString("... ")
	}
	for _, segment := range segments {
		if segment.start < 0 || segment.start >= segment.end || segment.end > text.len() {
			return RenderParts{}, &InvalidCharacterRange{
				Message: fmt.Sprintf(
					"invalid character range %d:%d for document %q (%d characters)",
					segment.start, segment.end, view.DataID, text.len(),
				),
			}
		}
		if segment.kind == "seen" {
			fmt.Fprintf(&body, `[seen: "#%d:%d"]`, segment.start, segment.end)
		} else {
			body.WriteString(text.slice(segment.start, segment.end))
		}
	}
	if end < text.len() && !fullySeen {
		body.WriteString(" ...")
	}
	bodyText := body.String()

	id := view.ModelFacingID
	if !fullySeen && (start != 0 || end != text.len()) {
		id += fmt.Sprintf("#%d:%d", start, end)
	}
	attributes := []Attribute{{Name: "doc_length", Value: text.len()}}
	for _, name := range fields {
		if name == view.SnippetField {
			continue
		}
		value, err := documentField(view.Document, name)
		if err != nil {
			return RenderParts{}, err
		}
		attributes = append(attributes, Attribute{Name: name, Value: value})
	}
	return RenderParts{ID: id, Attributes: attributes, Body: &bodyText}, nil
}

// RenderXML renders the SID model-facing <doc> format.
func (view *DocumentView) RenderXML() (string, error) {
	parts, err := view.RenderParts()
	if err != nil {
		return "", err
	}

	attributes := []string{`id="` + escapeSID(parts.ID) + `"`}
	for _, attribute := range parts.Attributes {
		if !truthy(attribute.Value) {
			continue
		}
		value := stringify(attribute.Value)
		if rendersUnquoted(attribute.Value) {
			attributes = append(attributes, attribute.Name+"="+value)
		} else {
			attributes = append(attributes, attribute.Name+`="`+escapeSID(value)+`"`)
		}
	}

	if parts.Body == nil {
		return "<doc " + strings.Join(attributes, " ") + "></doc>", nil
	}
	return "<doc " + strings.Join(attributes, " ") + ">\n" +
		escapeSID(*parts.Body) + "\n</doc>", nil
}

// RenderXml is an alias for RenderXML.
func (view *DocumentView) RenderXml() (string, error) { return view.RenderXML() }

// RenderMarkdownTable renders one row per view. If displayFields is omitted,
// the first view's fields determine the columns.
func RenderMarkdownTable(views []*DocumentView, displayFields ...[]string) (string, error) {
	if len(views) == 0 {
		return "", nil
	}
	if len(displayFields) > 1 {
		return "", fmt.Errorf("RenderMarkdownTable accepts at most one display-fields value")
	}
	fields := views[0].DisplayFields
	if len(displayFields) == 1 {
		fields = displayFields[0]
	}

	rows := make([]map[string]any, 0, len(views))
	var firstAttributes []Attribute
	for i, view := range views {
		parts, err := view.RenderParts(fields)
		if err != nil {
			return "", err
		}
		if i == 0 {
			firstAttributes = parts.Attributes
		}
		row := map[string]any{"id": parts.ID}
		for _, attribute := range parts.Attributes {
			row[attribute.Name] = attribute.Value
		}
		if parts.Body != nil {
			row[view.SnippetField] = *parts.Body
		}
		rows = append(rows, row)
	}

	columns := []string{"id"}
	for _, attribute := range firstAttributes {
		if attribute.Name != "id" && !slices.Contains(fields, attribute.Name) {
			columns = append(columns, attribute.Name)
		}
	}
	columns = append(columns, fields...)

	var result strings.Builder
	result.WriteString("| ")
	result.WriteString(strings.Join(columns, " | "))
	result.WriteString(" |\n|")
	for i, column := range columns {
		if i > 0 {
			result.WriteByte('|')
		}
		result.WriteString(strings.Repeat("-", utf8.RuneCountInString(column)+2))
	}
	result.WriteByte('|')
	for _, row := range rows {
		result.WriteString("\n| ")
		for i, column := range columns {
			if i > 0 {
				result.WriteString(" | ")
			}
			value := any("")
			if present, ok := row[column]; ok {
				value = present
			}
			result.WriteString(markdownCell(value))
		}
		result.WriteString(" |")
	}
	return result.String(), nil
}

func documentField(document Document, name string) (any, error) {
	value, ok := document[name]
	if !ok {
		return nil, fmt.Errorf("document field %q not found", name)
	}
	return value, nil
}

func stringify(value any) string {
	if value == nil {
		return "None"
	}
	reflected := reflect.ValueOf(value)
	for reflected.Kind() == reflect.Interface || reflected.Kind() == reflect.Pointer {
		if reflected.IsNil() {
			return "None"
		}
		reflected = reflected.Elem()
	}
	if reflected.Kind() == reflect.Slice || reflected.Kind() == reflect.Array {
		parts := make([]string, reflected.Len())
		for i := 0; i < reflected.Len(); i++ {
			parts[i] = stringify(reflected.Index(i).Interface())
		}
		return strings.Join(parts, ", ")
	}

	switch reflected.Kind() {
	case reflect.Bool:
		if reflected.Bool() {
			return "True"
		}
		return "False"
	case reflect.String:
		return reflected.String()
	}
	value = reflected.Interface()
	if stringer, ok := value.(fmt.Stringer); ok {
		return stringer.String()
	}
	return fmt.Sprint(value)
}

func escapeSID(value any) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
	).Replace(stringify(value))
}

func markdownCell(value any) string {
	return strings.NewReplacer("|", `\|`, "\n", " ").Replace(stringify(value))
}

func truthy(value any) bool {
	if value == nil {
		return false
	}
	reflected := reflect.ValueOf(value)
	for reflected.Kind() == reflect.Interface || reflected.Kind() == reflect.Pointer {
		if reflected.IsNil() {
			return false
		}
		reflected = reflected.Elem()
	}
	switch reflected.Kind() {
	case reflect.Bool:
		return reflected.Bool()
	case reflect.String, reflect.Array:
		return reflected.Len() > 0
	case reflect.Slice, reflect.Map:
		return !reflected.IsNil() && reflected.Len() > 0
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflected.Int() != 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return reflected.Uint() != 0
	case reflect.Float32, reflect.Float64:
		return reflected.Float() != 0
	case reflect.Complex64, reflect.Complex128:
		return reflected.Complex() != 0
	default:
		return true
	}
}

func rendersUnquoted(value any) bool {
	if value == nil {
		return false
	}
	reflected := reflect.ValueOf(value)
	for reflected.Kind() == reflect.Interface || reflected.Kind() == reflect.Pointer {
		if reflected.IsNil() {
			return false
		}
		reflected = reflected.Elem()
	}
	kind := reflected.Kind()
	switch kind {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	default:
		return false
	}
}
