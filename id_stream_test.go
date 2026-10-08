package sid

import (
	"errors"
	"regexp"
	"sync"
	"testing"
)

func TestIDStreamCoversSpaceAndExhausts(t *testing.T) {
	t.Parallel()
	stream, err := NewIDStream(IDStreamOptions{
		Alphabet: "ab",
		Length:   3,
		Seed:     IDSeed(7),
	})
	if err != nil {
		t.Fatal(err)
	}

	indexed := make([]string, stream.Space)
	for i := range indexed {
		indexed[i], err = stream.At(uint64(i))
		if err != nil {
			t.Fatal(err)
		}
	}
	if stream.Minted() != 0 {
		t.Fatalf("At consumed ids: minted=%d", stream.Minted())
	}

	seen := make(map[string]struct{}, stream.Space)
	for i := range stream.Space {
		id, err := stream.Mint()
		if err != nil {
			t.Fatal(err)
		}
		if id != indexed[i] {
			t.Fatalf("Mint and At differ at %d: %q != %q", i, id, indexed[i])
		}
		seen[id] = struct{}{}
	}
	if len(seen) != int(stream.Space) {
		t.Fatalf("got %d distinct ids, want %d", len(seen), stream.Space)
	}
	if _, err := stream.Mint(); err == nil {
		t.Fatal("exhausted stream did not fail")
	} else {
		var exhausted *IDSpaceExhausted
		if !errors.As(err, &exhausted) {
			t.Fatalf("got %T, want IDSpaceExhausted", err)
		}
	}
}

func TestIDStreamDefaultsAndSeedReproducibility(t *testing.T) {
	t.Parallel()
	left, err := NewIDStream(IDStreamOptions{Seed: IDSeed(42)})
	if err != nil {
		t.Fatal(err)
	}
	right, err := NewIDStream(IDStreamOptions{Seed: IDSeed(42)})
	if err != nil {
		t.Fatal(err)
	}
	if left.Space != 1_000_000 {
		t.Fatalf("space=%d, want 1,000,000", left.Space)
	}
	pattern := regexp.MustCompile(`^[0-9]{6}$`)
	for range 100 {
		a, err := left.Mint()
		if err != nil {
			t.Fatal(err)
		}
		b, err := right.Mint()
		if err != nil {
			t.Fatal(err)
		}
		if a != b {
			t.Fatalf("seeded streams diverged: %q != %q", a, b)
		}
		if !pattern.MatchString(a) {
			t.Fatalf("default id %q is not six digits", a)
		}
	}
}

func TestIDStreamValidation(t *testing.T) {
	t.Parallel()
	for _, alphabet := range []string{"abca", "abc#", "abc:"} {
		if _, err := NewIDStream(IDStreamOptions{Alphabet: alphabet, Length: 2}); err == nil {
			t.Fatalf("alphabet %q did not fail", alphabet)
		}
	}
	if _, err := NewIDStream(IDStreamOptions{Alphabet: "ab", Length: -1}); err == nil {
		t.Fatal("negative length did not fail")
	}
	stream, err := NewIDStream(IDStreamOptions{Alphabet: "a", Length: 1})
	if err != nil {
		t.Fatal(err)
	}
	if id, err := stream.Mint(); err != nil || id != "a" {
		t.Fatalf("singleton mint = %q, %v", id, err)
	}
	if _, err := stream.Mint(); err == nil {
		t.Fatal("singleton stream did not exhaust")
	}
}

func TestIDStreamConcurrentMint(t *testing.T) {
	stream, err := NewIDStream(IDStreamOptions{Seed: IDSeed(0)})
	if err != nil {
		t.Fatal(err)
	}

	const workers = 8
	const perWorker = 500
	var wait sync.WaitGroup
	ids := make(chan string, workers*perWorker)
	errs := make(chan error, workers)
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range perWorker {
				id, err := stream.Mint()
				if err != nil {
					errs <- err
					return
				}
				ids <- id
			}
		}()
	}
	wait.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	seen := make(map[string]struct{}, workers*perWorker)
	for id := range ids {
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = struct{}{}
	}
	if len(seen) != workers*perWorker {
		t.Fatalf("got %d ids, want %d", len(seen), workers*perWorker)
	}
}
