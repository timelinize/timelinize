package goodreads

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/timelinize/timelinize/timeline"
)

func TestISBN10To13(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "0140449132", want: "9780140449136"},
		{input: "043942089X", want: "9780439420891"},
		{input: "123", want: ""},
	}

	for _, test := range tests {
		got := isbn10To13(test.input)
		if got != test.want {
			t.Fatalf("isbn10To13(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestISBN13To10(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "9780140449136", want: "0140449132"},
		{input: "9780439420891", want: "043942089X"},
		{input: "9790439420891", want: ""},
	}

	for _, test := range tests {
		got := isbn13To10(test.input)
		if got != test.want {
			t.Fatalf("isbn13To10(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestNormalizeCoverSize(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "", want: defaultCoverSize},
		{input: "m", want: "M"},
		{input: "S", want: "S"},
		{input: "bad", want: defaultCoverSize},
	}

	for _, test := range tests {
		got := normalizeCoverSize(test.input)
		if got != test.want {
			t.Fatalf("normalizeCoverSize(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestGoodreadsRecognize(t *testing.T) {
	fi := FileImporter{}
	ctx := context.Background()

	t.Run("filename match", func(t *testing.T) {
		entry := mustDirEntry(t, "goodreads_library_export.csv")
		rec, err := fi.Recognize(ctx, entry, timeline.RecognizeParams{})
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if rec.Confidence != 1 {
			t.Fatalf("expected confidence 1, got %v", rec.Confidence)
		}
	})

	t.Run("header match", func(t *testing.T) {
		entry := mustDirEntry(t, "goodreads-header.csv")
		rec, err := fi.Recognize(ctx, entry, timeline.RecognizeParams{})
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if rec.Confidence != 0.9 {
			t.Fatalf("expected confidence 0.9, got %v", rec.Confidence)
		}
	})
}

func TestGoodreadsFileImport(t *testing.T) {
	ctx := context.Background()
	fi := FileImporter{}

	t.Run("emits start and end items", func(t *testing.T) {
		params := timeline.ImportParams{
			DataSourceOptions: &Options{DisableCovers: true},
		}
		graphs := runImport(ctx, t, fi, "goodreads-library.csv", params)
		if len(graphs) != 2 {
			t.Fatalf("expected 2 graphs, got %d", len(graphs))
		}
		for _, graph := range graphs {
			if graph.Item == nil {
				t.Fatalf("expected item, got nil")
			}
			if graph.Item.Classification.Name != timeline.ClassDocument.Name {
				t.Fatalf("expected classification %q, got %q", timeline.ClassDocument.Name, graph.Item.Classification.Name)
			}
			if graph.Item.Content.MediaType != "text/markdown" {
				t.Fatalf("expected media type text/markdown, got %q", graph.Item.Content.MediaType)
			}
		}
	})

	t.Run("timeframe skips items", func(t *testing.T) {
		since := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		params := timeline.ImportParams{
			Timeframe:         timeline.Timeframe{Since: &since},
			DataSourceOptions: &Options{DisableCovers: true},
		}
		graphs := runImport(ctx, t, fi, "goodreads-library.csv", params)
		if len(graphs) != 0 {
			t.Fatalf("expected 0 graphs, got %d", len(graphs))
		}
	})

	t.Run("checkpoint skips earlier records", func(t *testing.T) {
		raw, err := json.Marshal(1)
		if err != nil {
			t.Fatalf("marshal checkpoint: %v", err)
		}
		params := timeline.ImportParams{
			Checkpoint:        raw,
			DataSourceOptions: &Options{DisableCovers: true},
		}
		graphs := runImport(ctx, t, fi, "goodreads-three-records.csv", params)
		if len(graphs) != 1 {
			t.Fatalf("expected 1 graph, got %d", len(graphs))
		}
	})
}

func runImport(ctx context.Context, t *testing.T, fi FileImporter, filename string, params timeline.ImportParams) []*timeline.Graph {
	t.Helper()

	itemChan := make(chan *timeline.Graph, 10)
	params.Pipeline = itemChan
	if params.Continue == nil {
		params.Continue = func() error { return nil }
	}

	entry := mustDirEntry(t, filename)
	if err := fi.FileImport(ctx, entry, params); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	close(itemChan)

	var graphs []*timeline.Graph
	for graph := range itemChan {
		graphs = append(graphs, graph)
	}
	return graphs
}

func mustDirEntry(t *testing.T, filename string) timeline.DirEntry {
	t.Helper()

	fullpath := filepath.Join("testdata", "fixtures", filename)
	info, err := os.Stat(fullpath)
	if err != nil {
		t.Fatalf("stat %s: %v", filename, err)
	}

	return timeline.DirEntry{
		DirEntry: fs.FileInfoToDirEntry(info),
		FS:       os.DirFS(filepath.Join("testdata", "fixtures")),
		Filename: filename,
	}
}
