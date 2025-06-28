package archivebox_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/timelinize/timelinize/datasources/archivebox"
	"github.com/timelinize/timelinize/internal/testhelpers"
	"github.com/timelinize/timelinize/timeline"
)

func TestFileImport(t *testing.T) {
	// Setup
	fixtures := os.DirFS("testdata/fixtures")
	dirEntry := timeline.DirEntry{
		FS:       fixtures,
		Filename: "1750395680.340654",
	}

	pipeline := make(chan *timeline.Graph, 10)
	params := timeline.ImportParams{Pipeline: pipeline}

	// Run the import
	runErr := new(archivebox.Importer).FileImport(context.Background(), dirEntry, params)
	if runErr != nil {
		t.Errorf("unable to import: %v", runErr)
	}
	close(params.Pipeline)

	if len(pipeline) != 1 {
		t.Fatalf("received %d messages instead of one", len(pipeline))
	}

	item := <-pipeline

	testhelpers.ValidateItemData(t, "", []byte("Archive of [web page at _slow.fyi_](https://slow.fyi)"), item.Item.Content, "ArchiveBox description is incorrect")

	expectedAttachments := []struct {
		name           string
		filename       string
		mediaType      string
		classification timeline.Classification
		start          time.Time
		end            time.Time
	}{
		{
			name:           "PDF snapshot",
			filename:       "output.pdf",
			mediaType:      "application/pdf",
			classification: timeline.ClassDocument,
			start:          time.UnixMicro(1750395686980717),
			end:            time.UnixMicro(1750395687954766),
		},
		{
			name:           "Screenshot",
			filename:       "screenshot.png",
			mediaType:      "", // The mediatype isn't provided, so it can be auto-guessed
			classification: timeline.ClassScreen,
			start:          time.UnixMicro(1750395687964614),
			end:            time.UnixMicro(1750395689093718),
		},
		{
			name:           "SingleFile snapshot",
			filename:       "singlefile.html",
			mediaType:      "text/html",
			classification: timeline.ClassDocument,
			start:          time.UnixMicro(1750395680485432),
			end:            time.UnixMicro(1750395686970176),
		},
	}

	if len(item.Edges) != len(expectedAttachments) {
		t.Fatalf("want %d attachments, but got %d", len(expectedAttachments), len(item.Edges))
	}

	for i, attachment := range item.Edges {
		expected := expectedAttachments[i]
		actual := attachment.To.Item

		if actual.Content.MediaType != expected.mediaType {
			t.Errorf("expected attachment '%s' to have media type '%s' but got '%s'", expected.name, expected.mediaType, actual.Content.MediaType)
		}

		f, err := dirEntry.Open(expected.filename)
		if err != nil {
			t.Fatalf("test needs the expected attachment '%s': %v", expected.filename, err)
		}
		testhelpers.ValidateItemData(t, "", f, actual.Content, "ArchiveBox attachment '%s' is incorrect", expected.name)

		if actual.Timestamp.UnixMicro() != expected.start.UnixMicro() {
			t.Errorf("expected attachment '%s' to have timestamp '%v' but got '%v'", expected.name, expected.start.UnixMicro(), actual.Timestamp.UnixMicro())
		}
		if actual.Timespan.UnixMicro() != expected.end.UnixMicro() {
			t.Errorf("expected attachment '%s' to have timespan '%v' but got '%v'", expected.name, expected.end.UnixMicro(), actual.Timespan.UnixMicro())
		}
	}
}
