package github

import (
	"context"
	"testing"

	"github.com/timelinize/timelinize/timeline"
)

func TestClientWalk(t *testing.T) {
	client := &GitHub{}
	ctx := context.WithValue(context.Background(), TLZTest, true)
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

	t.Run("ghstars.json missing starred_at", func(t *testing.T) {
		itemChan := make(chan *timeline.Graph, 10)

		err := client.FileImport(ctx, []string{"testdata/fixtures/ghstars-missing-starred-at.json"}, itemChan, opts)
		mustCount(t, itemChan, 0)
		mustError(
			t,
			err,
			"processing testdata/fixtures/ghstars-missing-starred-at.json: missing starred_at field for repo mojombo/grit",
		)
	})

	t.Run("ghstars.json missing HTML URL", func(t *testing.T) {
		itemChan := make(chan *timeline.Graph, 10)

		err := client.FileImport(ctx, []string{"testdata/fixtures/ghstars-missing-htmlurl.json"}, itemChan, opts)
		mustCount(t, itemChan, 0)
		mustError(
			t,
			err,
			"processing testdata/fixtures/ghstars-missing-htmlurl.json: missing HTMLURL field for repo mojombo/grit",
		)
	})
}

func mustError(t *testing.T, got error, expected string) {
	if got == nil {
		t.Fatal("expected error")
	}

	if got.Error() != expected {
		t.Fatalf("expected a different error than: %s", got)
	}
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
