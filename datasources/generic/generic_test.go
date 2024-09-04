package generic

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/timelinize/timelinize/timeline"
)

func TestClientWalk(t *testing.T) {
	client := &Client{}
	ctx := context.Background()
	opts := timeline.ListingOptions{DataSourceOptions: new(Options)}

	t.Run("import single txt file", func(t *testing.T) {
		itemChan := make(chan *timeline.Graph, 10)

		client.FileImport(ctx, []string{"testdata/fixtures/file.txt"}, itemChan, opts)
		close(itemChan)

		expectFiles(t, []string{"file.txt"}, itemChan)
	})

	t.Run("import single zip file", func(t *testing.T) {
		itemChan := make(chan *timeline.Graph, 10)

		client.FileImport(ctx, []string{"testdata/fixtures/file.zip"}, itemChan, opts)
		close(itemChan)

		expectFiles(t, []string{"file.txt"}, itemChan)
	})

	t.Run("Directory with single txt file", func(t *testing.T) {
		itemChan := make(chan *timeline.Graph, 10)

		client.FileImport(ctx, []string{"testdata/fixtures/dir-single-file"}, itemChan, opts)
		close(itemChan)

		expectFiles(t, []string{"file.txt"}, itemChan)
	})

	t.Run("Directory with multiple files", func(t *testing.T) {
		itemChan := make(chan *timeline.Graph, 10)
		client.FileImport(ctx, []string{"testdata/fixtures/dir-multiple-files"}, itemChan, opts)
		close(itemChan)

		expectFiles(t, []string{"file1", "file2"}, itemChan)
	})

	t.Run("Zip file within a zip file", func(t *testing.T) {
		itemChan := make(chan *timeline.Graph, 10)

		// zip-in-archive.zip contains file2.zip
		client.FileImport(ctx, []string{"testdata/fixtures/zip-in-archive.zip"}, itemChan, opts)
		close(itemChan)

		expectFiles(t, []string{"file2.zip"}, itemChan)
	})
}

func expectFiles(t *testing.T, files []string, itemChan chan *timeline.Graph) {
	graphs := []*timeline.Graph{}
	for graph := range itemChan {
		graphs = append(graphs, graph)
		fname := graph.Item.Content.Filename
		if !slices.Contains(files, graphs[0].Item.Content.Filename) {
			t.Errorf("Expected file %s to be in %s", fname, strings.Join(files, ","))
		}
	}

	if len(graphs) != len(files) {
		t.Errorf("Expected %d files, got %d", len(files), len(graphs))
	}
}
