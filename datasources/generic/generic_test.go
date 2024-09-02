package generic

import (
	"context"
	"testing"

	"github.com/timelinize/timelinize/timeline"
)

func TestClientWalk(t *testing.T) {
	client := &Client{}
	itemChan := make(chan *timeline.Graph, 1)
	defer close(itemChan)

	ctx := context.Background()
	opts := timeline.ListingOptions{DataSourceOptions: new(Options)}

	client.walk(ctx, "testdata/fixtures/file.txt", ".", itemChan, opts)
	graph := <-itemChan
	item := graph.Item
	if item.Content.Filename != "file.txt" {
		t.Errorf("Expected filepath to be file.txt, got %s", item.Content.Filename)
	}

	client.walk(ctx, "testdata/fixtures", ".", itemChan, opts)
	graph = <-itemChan
	item = graph.Item
	if item.Content.Filename != "file.txt" {
		t.Errorf("Expected filepath to be file.txt, got %s", item.Content.Filename)
	}
}
