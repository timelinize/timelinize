package github

import (
	"context"
	"os"
	"testing"

	"github.com/timelinize/timelinize/timeline"
)

func TestClientWalk(t *testing.T) {
	client := &GitHub{}
	ctx := context.Background()
	params := timeline.ImportParams{}

	t.Run("ghstars.json with one starred repo", func(t *testing.T) {
		itemChan := make(chan *timeline.Graph, 10)
		params.Pipeline = itemChan
		dirEntry := timeline.DirEntry{FS: os.DirFS("testdata/fixtures"), Filename: "ghstars.json"}

		err := client.FileImport(ctx, dirEntry, params)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		close(params.Pipeline)
		mustCount(t, itemChan, 1)
	})

	t.Run("ghstars.json with multiple starred repos", func(t *testing.T) {
		itemChan := make(chan *timeline.Graph, 10)
		params.Pipeline = itemChan
		dirEntry := timeline.DirEntry{FS: os.DirFS("testdata/fixtures"), Filename: "ghstars-multi.json"}

		err := client.FileImport(ctx, dirEntry, params)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		close(params.Pipeline)
		mustCount(t, itemChan, 2)
	})

	t.Run("ghstars.json with empty list", func(t *testing.T) {
		itemChan := make(chan *timeline.Graph, 10)
		params.Pipeline = itemChan
		dirEntry := timeline.DirEntry{FS: os.DirFS("testdata/fixtures"), Filename: "ghstars-empty.json"}

		err := client.FileImport(ctx, dirEntry, params)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		close(params.Pipeline)
		mustCount(t, itemChan, 0)
	})

	t.Run("ghstars.json malformed", func(t *testing.T) {
		itemChan := make(chan *timeline.Graph, 10)
		params.Pipeline = itemChan
		dirEntry := timeline.DirEntry{FS: os.DirFS("testdata/fixtures"), Filename: "ghstars-malformed.json"}

		err := client.FileImport(ctx, dirEntry, params)
		mustError(
			t,
			err,
			"malformed JSON: json: cannot unmarshal string into Go value of type github.Repository",
		)
		close(params.Pipeline)
		mustCount(t, itemChan, 0)
	})

	t.Run("ghstars.json missing starred_at", func(t *testing.T) {
		itemChan := make(chan *timeline.Graph, 10)
		params.Pipeline = itemChan
		dirEntry := timeline.DirEntry{FS: os.DirFS("testdata/fixtures"), Filename: "ghstars-missing-starred-at.json"}

		err := client.FileImport(ctx, dirEntry, params)
		mustError(
			t,
			err,
			"missing starred_at field for repo mojombo/grit",
		)
		close(params.Pipeline)
		mustCount(t, itemChan, 0)
	})

	t.Run("ghstars.json missing HTML URL", func(t *testing.T) {
		itemChan := make(chan *timeline.Graph, 10)
		params.Pipeline = itemChan
		dirEntry := timeline.DirEntry{FS: os.DirFS("testdata/fixtures"), Filename: "ghstars-missing-htmlurl.json"}

		err := client.FileImport(ctx, dirEntry, params)
		mustError(
			t,
			err,
			"missing HTMLURL field for repo mojombo/grit",
		)
		close(params.Pipeline)
		mustCount(t, itemChan, 0)
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
