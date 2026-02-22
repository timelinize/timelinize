package timeline

import "testing"

func TestMetadataMerge(t *testing.T) {
	var m Metadata // it should not panic with a nil map
	m.Merge(Metadata{"a": 1}, MetaMergeAppend)
	if m["a"] != 1 {
		t.Errorf("expected metadata to be merged; actual=%v", m)
	}
}
