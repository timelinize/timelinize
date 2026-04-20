package timeline

import (
	"testing"
)

func TestUint64SliceHash_DoesNotMutateReceiver(t *testing.T) {
	// hash() must produce a canonical (sorted) key without mutating the
	// underlying slice, because the caller may still be appending to it.
	s := uint64Slice{3, 1, 2}
	h := s.hash()

	// The hash must be sorted regardless of insertion order.
	if h != "1,2,3," {
		t.Fatalf("expected hash %q, got %q", "1,2,3,", h)
	}

	// The original slice must keep its insertion order.
	if s[0] != 3 || s[1] != 1 || s[2] != 2 {
		t.Fatalf("hash() mutated the receiver: got %v, want [3 1 2]", []uint64(s))
	}
}

func TestUint64SliceHash_OrderIndependent(t *testing.T) {
	// Two slices with the same elements in different order must hash equally.
	a := uint64Slice{10, 20, 30}
	b := uint64Slice{30, 10, 20}

	if a.hash() != b.hash() {
		t.Fatalf("expected equal hashes for %v and %v, got %q vs %q", a, b, a.hash(), b.hash())
	}
}

func TestUint64SliceAppendIfUnique(t *testing.T) {
	var s uint64Slice

	s.appendIfUnique(1)
	s.appendIfUnique(2)
	s.appendIfUnique(1) // duplicate

	if len(s) != 2 {
		t.Fatalf("expected 2 elements, got %d: %v", len(s), s)
	}
}
