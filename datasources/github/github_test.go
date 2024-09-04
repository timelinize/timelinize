package github

import (
	"context"
	"testing"

	"github.com/timelinize/timelinize/timeline"
)

func TestClientWalk(t *testing.T) {
	client := &GitHub{}
	ctx := context.WithValue(context.Background(), "TLZ_TEST", true)
	opts := timeline.ListingOptions{}

	t.Run("ghstars.json with one starred repo", func(t *testing.T) {
		itemChan := make(chan *timeline.Graph, 10)

		err := client.FileImport(ctx, []string{"testdata/fixtures/ghstars.json"}, itemChan, opts)
		mustCount(t, itemChan, 1)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})

	t.Run("ghstars.json with multiple starred repos", func(t *testing.T) {
		itemChan := make(chan *timeline.Graph, 10)

		err := client.FileImport(ctx, []string{"testdata/fixtures/ghstars-multi.json"}, itemChan, opts)
		mustCount(t, itemChan, 2)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})

	t.Run("ghstars.json with empty list", func(t *testing.T) {
		itemChan := make(chan *timeline.Graph, 10)

		err := client.FileImport(ctx, []string{"testdata/fixtures/ghstars-empty.json"}, itemChan, opts)
		mustCount(t, itemChan, 0)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})

	t.Run("ghstars.json malformed", func(t *testing.T) {
		itemChan := make(chan *timeline.Graph, 10)

		err := client.FileImport(ctx, []string{"testdata/fixtures/ghstars-malformed.json"}, itemChan, opts)
		mustCount(t, itemChan, 0)
		if err == nil {
			t.Fatal("expected error")
		}

		if err.Error() != "processing testdata/fixtures/ghstars-malformed.json: malformed JSON: json: cannot unmarshal string into Go value of type github.Repository" {
			t.Fatalf("expected a different error than: %s", err)
		}
	})
}

func mustCount(t *testing.T, itemChan chan *timeline.Graph, count int) {
	graphs := []*timeline.Graph{}
	for graph := range itemChan {
		graphs = append(graphs, graph)
	}

	if len(graphs) != count {
		t.Fatalf("expected %d graph, got %d", count, len(graphs))
	}
}
