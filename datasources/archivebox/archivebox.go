package archivebox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "archivebox",
		Title:           "ArchiveBox",
		Icon:            "archivebox.png",
		Description:     "Import the website snapshots from your ArchiveBox instance to your timeline",
		NewFileImporter: func() timeline.FileImporter { return new(Importer) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// The filename that holds the metadata in archivebox folders
const metadataPath = "index.json"

// The ArchiveBox history.status string that indicates a successful archival
const statusSucceeded = "succeeded"

// Importer can import the data from a zip file or folder.
type Importer struct{}

// Recognize returns whether the input is supported.
func (Importer) Recognize(_ context.Context, dirEntry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	info, err := dirEntry.Open(metadataPath)
	if err != nil {
		if os.IsNotExist(err) {
			return timeline.Recognition{}, nil
		}
		return timeline.Recognition{}, err
	}

	var basics struct {
		ArchivePath string `json:"archive_path"`
		IsArchived  bool   `json:"is_archived"`
	}
	if err := json.NewDecoder(info).Decode(&basics); err != nil {
		return timeline.Recognition{}, err
	}

	if basics.ArchivePath == "" || !basics.IsArchived {
		return timeline.Recognition{Confidence: 0.1}, nil
	}

	return timeline.Recognition{Confidence: 1}, nil
}

// FileImport imports data from the file or folder.
func (i *Importer) FileImport(_ context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	info, err := dirEntry.Open(metadataPath)
	if err != nil {
		return err
	}

	var meta metadata
	if err := json.NewDecoder(info).Decode(&meta); err != nil {
		return err
	}

	// TODO: Localisation
	description := fmt.Sprintf("Archive of web page: [%s](%s) (from %s)", meta.Title, meta.URL, meta.Domain)
	if meta.Title == "" {
		description = fmt.Sprintf("Archive of [web page at _%s_](%s)", meta.Domain, meta.URL)
	}

	g := &timeline.Graph{
		Item: &timeline.Item{
			ID:             meta.Hash,
			Classification: timeline.ClassSnapshot,
			Timestamp:      meta.Timestamp.Time,
			Metadata: timeline.Metadata{
				"Title": meta.Title,
				"URL":   meta.URL,
				"Hash":  meta.Hash,
			},
			Content: timeline.ItemData{
				Data:      timeline.StringData(description),
				MediaType: "text/markdown",
			},
		},
	}

	hasLocalArchive := false

	if meta.Canonical.ArchiveOrgPath != "" {
		g.Item.Metadata["Archive.org URL"] = meta.Canonical.ArchiveOrgPath
	}

	if meta.Canonical.PDFPath != "" {
		history, ok := findHistory(meta, "pdf", meta.Canonical.PDFPath)
		if ok {
			hasLocalArchive = true
			g.ToItem(timeline.RelAttachment, &timeline.Item{
				Classification: timeline.ClassDocument,
				Timestamp:      history.Start.Time,
				Timespan:       history.End.Time,
				Content: timeline.ItemData{
					// Use the same filename as archivebox does
					Filename:  meta.Canonical.PDFPath,
					MediaType: "application/pdf",
					Data: func(_ context.Context) (io.ReadCloser, error) {
						return dirEntry.Open(meta.Canonical.PDFPath)
					},
				},
			})
		}
	}

	if meta.Canonical.ScreenshotPath != "" {
		history, ok := findHistory(meta, "screenshot", meta.Canonical.ScreenshotPath)
		if ok {
			hasLocalArchive = true
			g.ToItem(timeline.RelAttachment, &timeline.Item{
				Classification: timeline.ClassScreen,
				Timestamp:      history.Start.Time,
				Timespan:       history.End.Time,
				Content: timeline.ItemData{
					// Use the same filename as archivebox does
					Filename: meta.Canonical.ScreenshotPath,
					Data: func(_ context.Context) (io.ReadCloser, error) {
						return dirEntry.Open(meta.Canonical.ScreenshotPath)
					},
				},
			})
		}
	}

	if meta.Canonical.SingleFilePath != "" {
		history, ok := findHistory(meta, "singlefile", meta.Canonical.SingleFilePath)
		if ok {
			hasLocalArchive = true
			g.ToItemWithValue(timeline.RelAttachment, &timeline.Item{
				Classification: timeline.ClassDocument,
				Timestamp:      history.Start.Time,
				Timespan:       history.End.Time,
				Content: timeline.ItemData{
					// Use the same filename as archivebox does
					Filename:  meta.Canonical.SingleFilePath,
					MediaType: "text/html",
					Data: func(_ context.Context) (io.ReadCloser, error) {
						return dirEntry.Open(meta.Canonical.SingleFilePath)
					},
				},
			}, "SingleFile format")
		}
	}

	if !hasLocalArchive {
		// Ignore archives that don't hold one of the supported archive formats
		return nil
	}

	params.Pipeline <- g
	return nil
}

func findHistory(meta metadata, key string, output string) (history, bool) {
	for _, item := range meta.History[key] {
		if item.Status != statusSucceeded || item.Output != output {
			continue
		}

		return item, true
	}
	return history{}, false
}
