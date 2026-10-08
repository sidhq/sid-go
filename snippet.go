package sid

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/clipperhouse/uax29/v2/words"
)

const (
	bm25K1            = 1.2
	bm25B             = 0.75
	unigramWeight     = 1.0
	bigramWeight      = 1.0
	defaultWindowSize = 50
	defaultStride     = 10
)

// Language names one of the SDK's text analyzers.
type Language string

const (
	LanguageDanish     Language = "danish"
	LanguageDutch      Language = "dutch"
	LanguageEnglish    Language = "english"
	LanguageFinnish    Language = "finnish"
	LanguageFrench     Language = "french"
	LanguageGerman     Language = "german"
	LanguageGeneric    Language = "generic"
	LanguageHungarian  Language = "hungarian"
	LanguageItalian    Language = "italian"
	LanguageNorwegian  Language = "norwegian"
	LanguagePortuguese Language = "portuguese"
	LanguageRussian    Language = "russian"
	LanguageSpanish    Language = "spanish"
	LanguageSwedish    Language = "swedish"
)

// SupportedLanguages lists all accepted analyzers. Generic lowercases tokens
// without removing stopwords.
var SupportedLanguages = []Language{
	LanguageDanish,
	LanguageDutch,
	LanguageEnglish,
	LanguageFinnish,
	LanguageFrench,
	LanguageGerman,
	LanguageGeneric,
	LanguageHungarian,
	LanguageItalian,
	LanguageNorwegian,
	LanguagePortuguese,
	LanguageRussian,
	LanguageSpanish,
	LanguageSwedish,
}

// SnippetOptions configures BM25SnippetWithStride. Zero values select the
// defaults: a 50-token window, stride 10, and English.
type SnippetOptions struct {
	WindowSize int
	Stride     int
	Language   Language
}

type sourceToken struct {
	text       string
	start, end int
	position   int
}

type bigram struct {
	left, right string
}

type bigramEvent struct {
	left, right int
}

// BM25SnippetWithStride selects the most relevant fixed-size source-token
// window and returns exact Unicode code-point offsets into content.
//
// Scores combine independent unigram and bigram BM25 streams. Ties choose the
// earliest window, and the final possible window is always considered.
func BM25SnippetWithStride(query, content string, options ...SnippetOptions) (CharacterRange, error) {
	if len(options) > 1 {
		return CharacterRange{}, fmt.Errorf("BM25SnippetWithStride accepts at most one options value")
	}
	opts := SnippetOptions{}
	if len(options) == 1 {
		opts = options[0]
	}
	if opts.WindowSize == 0 {
		opts.WindowSize = defaultWindowSize
	}
	if opts.Stride == 0 {
		opts.Stride = defaultStride
	}
	if opts.Language == "" {
		opts.Language = LanguageEnglish
	}
	if opts.WindowSize < 1 {
		return CharacterRange{}, fmt.Errorf("window size must be greater than zero")
	}
	if opts.Stride < 1 {
		return CharacterRange{}, fmt.Errorf("stride must be greater than zero")
	}
	if uint64(opts.WindowSize) > math.MaxUint32 || uint64(opts.Stride) > math.MaxUint32 {
		return CharacterRange{}, fmt.Errorf("window size and stride must fit unsigned 32-bit integers")
	}
	if _, err := validateLanguage(opts.Language); err != nil {
		return CharacterRange{}, err
	}
	if !utf8.ValidString(query) || !utf8.ValidString(content) {
		return CharacterRange{}, fmt.Errorf("query and content must contain well-formed UTF-8")
	}

	source := tokenizeSource(content)
	if len(source) <= opts.WindowSize {
		return CharacterRange{0, utf8.RuneCountInString(content)}, nil
	}

	queryTokens := analyzeTokens(query, opts.Language)
	unigrams := uniqueUnigrams(queryTokens)
	bigrams := uniqueBigrams(queryTokens)
	contentTokens := analyzeTokens(content, opts.Language)

	filteredPositions := make([]int, len(contentTokens))
	unigramEvents := make(map[string][]int, len(unigrams))
	bigramEvents := make(map[bigram][]bigramEvent, len(bigrams))
	var previous *sourceToken
	for i := range contentTokens {
		token := &contentTokens[i]
		filteredPositions[i] = token.position
		if _, wanted := unigrams[token.text]; wanted {
			unigramEvents[token.text] = append(unigramEvents[token.text], token.position)
		}
		if previous != nil {
			feature := bigram{previous.text, token.text}
			if _, wanted := bigrams[feature]; wanted {
				bigramEvents[feature] = append(
					bigramEvents[feature],
					bigramEvent{previous.position, token.position},
				)
			}
		}
		previous = token
	}

	starts := windowStarts(len(source), opts.WindowSize, opts.Stride)
	unigramLengths := make([]int, len(starts))
	bigramLengths := make([]int, len(starts))
	for i, start := range starts {
		count := countPositions(filteredPositions, start, start+opts.WindowSize)
		unigramLengths[i] = count
		bigramLengths[i] = max(0, count-1)
	}
	averageUnigramLength := average(unigramLengths)
	averageBigramLength := average(bigramLengths)
	scores := make([]float64, len(starts))

	for _, feature := range orderedUnigrams(queryTokens) {
		events := unigramEvents[feature]
		documentFrequency := 0
		for _, start := range starts {
			if countPositions(events, start, start+opts.WindowSize) > 0 {
				documentFrequency++
			}
		}
		if documentFrequency == 0 {
			continue
		}
		featureIDF := inverseDocumentFrequency(len(starts), documentFrequency)
		for i, start := range starts {
			tf := countPositions(events, start, start+opts.WindowSize)
			scores[i] += unigramWeight * bm25TermScore(
				tf, unigramLengths[i], averageUnigramLength, featureIDF,
			)
		}
	}

	for _, feature := range orderedBigrams(queryTokens) {
		events := bigramEvents[feature]
		documentFrequency := 0
		for _, start := range starts {
			if countBigramEvents(events, start, start+opts.WindowSize) > 0 {
				documentFrequency++
			}
		}
		if documentFrequency == 0 {
			continue
		}
		featureIDF := inverseDocumentFrequency(len(starts), documentFrequency)
		for i, start := range starts {
			tf := countBigramEvents(events, start, start+opts.WindowSize)
			scores[i] += bigramWeight * bm25TermScore(
				tf, bigramLengths[i], averageBigramLength, featureIDF,
			)
		}
	}

	best := 0
	for i := 1; i < len(scores); i++ {
		if scores[i] > scores[best] {
			best = i
		}
	}
	start := starts[best]
	return CharacterRange{
		source[start].start,
		source[start+opts.WindowSize-1].end,
	}, nil
}

func validateLanguage(language Language) (Language, error) {
	for _, supported := range SupportedLanguages {
		if language == supported {
			return language, nil
		}
	}
	values := make([]string, len(SupportedLanguages))
	for i, supported := range SupportedLanguages {
		values[i] = string(supported)
	}
	return "", fmt.Errorf(
		"unsupported language %q; supported values: %s",
		language, strings.Join(values, ", "),
	)
}

func tokenizeSource(text string) []sourceToken {
	byteToCodePoint := make(map[int]int, utf8.RuneCountInString(text)+1)
	position := 0
	for byteOffset := range text {
		byteToCodePoint[byteOffset] = position
		position++
	}
	byteToCodePoint[len(text)] = position

	result := make([]sourceToken, 0)
	iterator := words.FromString(text)
	for iterator.Next() {
		value := iterator.Value()
		if !containsWordRune(value) {
			continue
		}
		result = append(result, sourceToken{
			text:     value,
			start:    byteToCodePoint[iterator.Start()],
			end:      byteToCodePoint[iterator.End()],
			position: len(result),
		})
	}
	return result
}

func containsWordRune(value string) bool {
	for _, char := range value {
		if unicode.IsLetter(char) || unicode.IsNumber(char) {
			return true
		}
	}
	return false
}

func analyzeTokens(text string, language Language) []sourceToken {
	source := tokenizeSource(text)
	result := make([]sourceToken, 0, len(source))
	for _, token := range source {
		token.text = strings.ToLower(token.text)
		if language != LanguageGeneric {
			if _, stopword := stopwordSets[language][token.text]; stopword {
				continue
			}
		}
		result = append(result, token)
	}
	return result
}

func uniqueUnigrams(tokens []sourceToken) map[string]struct{} {
	result := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		result[token.text] = struct{}{}
	}
	return result
}

func orderedUnigrams(tokens []sourceToken) []string {
	seen := make(map[string]struct{}, len(tokens))
	result := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if _, exists := seen[token.text]; exists {
			continue
		}
		seen[token.text] = struct{}{}
		result = append(result, token.text)
	}
	return result
}

func uniqueBigrams(tokens []sourceToken) map[bigram]struct{} {
	result := make(map[bigram]struct{}, max(0, len(tokens)-1))
	for i := 1; i < len(tokens); i++ {
		result[bigram{tokens[i-1].text, tokens[i].text}] = struct{}{}
	}
	return result
}

func orderedBigrams(tokens []sourceToken) []bigram {
	seen := make(map[bigram]struct{}, max(0, len(tokens)-1))
	result := make([]bigram, 0, max(0, len(tokens)-1))
	for i := 1; i < len(tokens); i++ {
		feature := bigram{tokens[i-1].text, tokens[i].text}
		if _, exists := seen[feature]; exists {
			continue
		}
		seen[feature] = struct{}{}
		result = append(result, feature)
	}
	return result
}

func windowStarts(sourceTokenCount, windowSize, stride int) []int {
	finalStart := sourceTokenCount - windowSize
	starts := make([]int, 0, finalStart/stride+2)
	for start := 0; start <= finalStart; start += stride {
		starts = append(starts, start)
	}
	if starts[len(starts)-1] != finalStart {
		starts = append(starts, finalStart)
	}
	return starts
}

func countPositions(positions []int, start, end int) int {
	left := sort.SearchInts(positions, start)
	right := sort.SearchInts(positions, end)
	return right - left
}

func countBigramEvents(events []bigramEvent, start, end int) int {
	left := sort.Search(len(events), func(i int) bool {
		return events[i].left >= start
	})
	right := sort.Search(len(events), func(i int) bool {
		return events[i].right >= end
	})
	return max(0, right-left)
}

func average(values []int) float64 {
	total := 0
	for _, value := range values {
		total += value
	}
	return float64(total) / float64(len(values))
}

func bm25TermScore(tf, documentLength int, averageLength, idf float64) float64 {
	if tf == 0 || averageLength == 0 {
		return 0
	}
	frequency := float64(tf)
	lengthRatio := float64(documentLength) / averageLength
	return idf * (frequency * (bm25K1 + 1)) /
		(frequency + bm25K1*(1-bm25B+bm25B*lengthRatio))
}

func inverseDocumentFrequency(documentCount, documentFrequency int) float64 {
	return math.Log(
		1 + (float64(documentCount)-float64(documentFrequency)+0.5)/
			(float64(documentFrequency)+0.5),
	)
}
