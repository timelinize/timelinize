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
		fname := (<-itemChan).Item.Content.Filename
		if fname != "file.txt" { //nolint:goconst
			t.Errorf("Expected filepath to be file.txt, got %s", fname)
		}
	})

	t.Run("import single zip file", func(t *testing.T) {
		itemChan := make(chan *timeline.Graph, 10)
		defer close(itemChan)

		client.walk(ctx, "testdata/fixtures/file.zip", ".", itemChan, opts)
		fname := (<-itemChan).Item.Content.Filename
		if fname != "file.txt" {
			t.Errorf("Expected filepath to be file.txt, got %s", fname)
		}
	})

	t.Run("Directory with single txt file", func(t *testing.T) {
		itemChan := make(chan *timeline.Graph, 10)
		defer close(itemChan)

		client.walk(ctx, "testdata/fixtures/dir-single-file", ".", itemChan, opts)
		fname := (<-itemChan).Item.Content.Filename
		if fname != "file.txt" {
			t.Errorf("Expected filepath to be file.txt, got %s", fname)
		}
	})

	t.Run("Directory with multiple files", func(t *testing.T) {
		itemChan := make(chan *timeline.Graph, 10)
		defer close(itemChan)
		client.walk(ctx, "testdata/fixtures/dir-multiple-files", ".", itemChan, opts)

		fname := (<-itemChan).Item.Content.Filename
		if fname != "file1" {
			t.Errorf("Expected filepath to be file1, got %s", fname)
		}

		fname = (<-itemChan).Item.Content.Filename
		if fname != "file2" {
			t.Errorf("Expected filepath to be file2, got %s", fname)
		}
	})

	t.Run("Zip file with a zip file", func(t *testing.T) {
		itemChan := make(chan *timeline.Graph, 10)
		defer close(itemChan)
		// zip-in-archive.zip contains file2.zip
		client.walk(ctx, "testdata/fixtures/zip-in-archive.zip", ".", itemChan, opts)

		fname := (<-itemChan).Item.Content.Filename
		if fname != "file2.zip" {
			t.Errorf("Expected filepath to be file2.zip, got %s", fname)
		}
	})
}
