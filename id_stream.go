package sid

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"math"
	"math/bits"
	"sync"
)

const (
	defaultIDAlphabet = "0123456789"
	defaultIDLength   = 6
	idRounds          = 4
)

// IDStreamOptions configures an IDStream. Empty Alphabet and zero Length use
// the six-decimal-digit defaults. Seed nil selects a cryptographically random
// permutation.
type IDStreamOptions struct {
	Alphabet string
	Length   int
	Seed     *int64
}

// IDSeed returns a pointer suitable for IDStreamOptions.Seed.
func IDSeed(seed int64) *int64 { return &seed }

// IDSpaceExhausted is returned after every ID in a stream has been minted.
type IDSpaceExhausted struct {
	Space  uint64
	Length int
	Base   int
}

func (e *IDSpaceExhausted) Error() string {
	return fmt.Sprintf(
		"all %d ids of this stream are in use (alphabet of %d characters, length %d); construct the stream with a longer length",
		e.Space, e.Base, e.Length,
	)
}

// Compatibility aliases use the mixed-case spelling present in the Python
// and TypeScript SDKs.
type IdStream = IDStream
type IdSpaceExhausted = IDSpaceExhausted
type IDSpaceExhaustedError = IDSpaceExhausted

// IDStream produces distinct short IDs by walking a keyed pseudorandom
// permutation. It is safe for concurrent use.
type IDStream struct {
	Alphabet string
	Length   int
	Space    uint64

	chars    []rune
	key      uint64
	halfBits uint

	mu      sync.Mutex
	counter uint64
}

// NewIDStream creates an ID stream. With no options it emits every six-digit
// decimal string exactly once in pseudorandom order.
func NewIDStream(options ...IDStreamOptions) (*IDStream, error) {
	if len(options) > 1 {
		return nil, fmt.Errorf("NewIDStream accepts at most one options value")
	}
	opts := IDStreamOptions{}
	if len(options) == 1 {
		opts = options[0]
	}
	if opts.Alphabet == "" {
		opts.Alphabet = defaultIDAlphabet
	}
	if opts.Length == 0 {
		opts.Length = defaultIDLength
	}
	if opts.Length < 1 {
		return nil, fmt.Errorf("length must be at least 1, got %d", opts.Length)
	}

	chars := []rune(opts.Alphabet)
	if len(chars) == 0 {
		return nil, fmt.Errorf("alphabet must be nonempty")
	}
	seen := make(map[rune]struct{}, len(chars))
	for _, char := range chars {
		if char == '#' || char == ':' {
			return nil, fmt.Errorf("alphabet must exclude document-reference separators '#' and ':'")
		}
		if _, exists := seen[char]; exists {
			return nil, fmt.Errorf("alphabet contains duplicate character %q", char)
		}
		seen[char] = struct{}{}
	}

	space := uint64(1)
	base := uint64(len(chars))
	for range opts.Length {
		if space > math.MaxUint64/base {
			return nil, fmt.Errorf("id space exceeds the maximum supported size")
		}
		space *= base
	}

	var key uint64
	if opts.Seed == nil {
		var random [8]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, fmt.Errorf("generate id-stream key: %w", err)
		}
		key = binary.LittleEndian.Uint64(random[:])
	} else {
		key = mixID(uint64(*opts.Seed), 0, 0)
	}

	bitLength := bits.Len64(space - 1)
	halfBits := uint(max(1, (bitLength+1)/2))
	return &IDStream{
		Alphabet: opts.Alphabet,
		Length:   opts.Length,
		Space:    space,
		chars:    chars,
		key:      key,
		halfBits: halfBits,
	}, nil
}

// Mint returns an ID not previously returned by this stream.
func (stream *IDStream) Mint() (string, error) {
	stream.mu.Lock()
	if stream.counter >= stream.Space {
		stream.mu.Unlock()
		return "", &IDSpaceExhausted{
			Space: stream.Space, Length: stream.Length, Base: len(stream.chars),
		}
	}
	index := stream.counter
	stream.counter++
	stream.mu.Unlock()
	return stream.At(index)
}

// At returns the index-th ID without consuming it.
func (stream *IDStream) At(index uint64) (string, error) {
	if index >= stream.Space {
		return "", fmt.Errorf("index %d outside id space of %d", index, stream.Space)
	}

	value := index
	for {
		value = stream.permute(value)
		if value < stream.Space {
			break
		}
	}

	base := uint64(len(stream.chars))
	result := make([]rune, stream.Length)
	for i := stream.Length - 1; i >= 0; i-- {
		result[i] = stream.chars[value%base]
		value /= base
	}
	return string(result), nil
}

// Minted is the number of IDs handed out by this stream.
func (stream *IDStream) Minted() uint64 {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.counter
}

// Remaining is the number of IDs still available.
func (stream *IDStream) Remaining() uint64 {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.Space - stream.counter
}

func (stream *IDStream) permute(value uint64) uint64 {
	mask := uint64(1<<stream.halfBits) - 1
	lo, hi := value>>stream.halfBits, value&mask
	for round := uint64(0); round < idRounds; round++ {
		lo, hi = hi, lo^(mixID(hi, stream.key, round)&mask)
	}
	return (lo << stream.halfBits) | hi
}

func mixID(value, key, round uint64) uint64 {
	value += key + round*0x9e3779b97f4a7c15
	value ^= value >> 30
	value *= 0xbf58476d1ce4e5b9
	value ^= value >> 27
	value *= 0x94d049bb133111eb
	return value ^ (value >> 31)
}
