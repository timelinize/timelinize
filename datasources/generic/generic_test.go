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

	client.walk(context.Background(), "testdata/fixtures/file.txt", ".", itemChan, timeline.ListingOptions{DataSourceOptions: new(Options)})
	graph := <-itemChan
	if graph.Item.Content.Filename != "file.txt" {
		t.Errorf("Expected filepath to be file.txt, got %s", graph.Item.Content.Filename)
	}

	client.walk(context.Background(), "testdata/fixtures", ".", itemChan, timeline.ListingOptions{DataSourceOptions: new(Options)})
	graph = <-itemChan
	if graph.Item.Content.Filename != "file.txt" {
		t.Errorf("Expected filepath to be file.txt, got %s", graph.Item.Content.Filename)
	}
}
