package generic

import (
	"context"
	"testing"

	"github.com/timelinize/timelinize/timeline"
)

func TestClientWalk(t *testing.T) {
	client := &Client{}
	ctx := context.Background()
	opts := timeline.ListingOptions{DataSourceOptions: new(Options)}

	t.Run("import single txt file", func(t *testing.T) {
		itemChan := make(chan *timeline.Graph, 10)
		defer close(itemChan)

		client.walk(ctx, "testdata/fixtures/file.txt", ".", itemChan, opts)
		graph := <-itemChan
		item := graph.Item
		if item.Content.Filename != "file.txt" {
			t.Errorf("Expected filepath to be file.txt, got %s", item.Content.Filename)
		}
	})

	t.Run("import single zip file", func(t *testing.T) {
		itemChan := make(chan *timeline.Graph, 10)
		defer close(itemChan)

		client.walk(ctx, "testdata/fixtures/file.zip", ".", itemChan, opts)
		graph := <-itemChan
		item := graph.Item
		if item.Content.Filename != "file.txt" {
			t.Errorf("Expected filepath to be file.txt, got %s", item.Content.Filename)
		}
	})

	t.Run("Directory with single txt file", func(t *testing.T) {
		itemChan := make(chan *timeline.Graph, 10)
		defer close(itemChan)

		client.walk(ctx, "testdata/fixtures/dir-single-file", ".", itemChan, opts)
		graph := <-itemChan
		item := graph.Item
		if item.Content.Filename != "file.txt" {
			t.Errorf("Expected filepath to be file.txt, got %s", item.Content.Filename)
		}
	})

	t.Run("Directory with multiple files", func(t *testing.T) {
		itemChan := make(chan *timeline.Graph, 10)
		defer close(itemChan)
		client.walk(ctx, "testdata/fixtures/dir-multiple-files", ".", itemChan, opts)

		graph := <-itemChan
		fname := graph.Item.Content.Filename
		if fname != "file1" {
			t.Errorf("Expected filepath to be file1, got %s", fname)
		}

		graph = <-itemChan
		fname = graph.Item.Content.Filename
		if fname != "file2" {
			t.Errorf("Expected filepath to be file2, got %s", fname)
		}
	})
}
