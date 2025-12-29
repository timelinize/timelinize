// import Goodreads cvs from https://www.goodreads.com/review/import . Go Read a book ! 

package goodreads

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

const (
	dataSourceName = "goodreads"
	exportFilename = "goodreads_library_export.csv"
	dateLayout     = "2006/01/02"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            dataSourceName,
		Title:           "Goodreads",
		Icon:            "goodreads.jpg",
		NewFileImporter: func() timeline.FileImporter { return FileImporter{} },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// FileImporter imports Goodreads CSV exports.
type FileImporter struct{}

// Recognize reports support for Goodreads CSV exports.
func (FileImporter) Recognize(_ context.Context, entry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	rec := timeline.Recognition{DirThreshold: 1}

	if entry.IsDir() {
		return rec, nil
	}

	if strings.EqualFold(entry.Name(), exportFilename) {
		rec.Confidence = 1
		return rec, nil
	}

	if strings.ToLower(path.Ext(entry.Name())) != ".csv" {
		return rec, nil
	}

	file, err := entry.Open("")
	if err != nil {
		return rec, nil // cannot confirm, stay at zero
	}
	defer file.Close()

	r := csv.NewReader(file)
	headers, err := r.Read()
	if err != nil {
		return rec, nil
	}
	for i := range headers {
		headers[i] = strings.TrimSpace(headers[i])
	}
	if len(headers) > 0 && strings.EqualFold(headers[0], "Book Id") {
		rec.Confidence = 0.9
	}

	return rec, nil
}

// FileImport reads the Goodreads CSV and emits items.
func (FileImporter) FileImport(ctx context.Context, entry timeline.DirEntry, params timeline.ImportParams) error {
	if entry.IsDir() {
		return errors.New("expected a CSV file, got directory")
	}

	file, err := entry.Open("")
	if err != nil {
		return fmt.Errorf("opening CSV: %w", err)
	}
	defer file.Close()

	r := csv.NewReader(file)
	r.FieldsPerRecord = -1

	headers, err := r.Read()
	if err != nil {
		return fmt.Errorf("reading header: %w", err)
	}

	col := make(map[string]int, len(headers))
	for i, h := range headers {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading record: %w", err)
		}
		if len(record) == 0 {
			continue
		}

		bookID := field(col, record, "book id")
		title := field(col, record, "title")
		if bookID == "" && title == "" {
			continue
		}

		author := field(col, record, "author")
		publisher := field(col, record, "publisher")
		pages := field(col, record, "number of pages")
		avgRating := field(col, record, "average rating")
		myRating := field(col, record, "my rating")
		yearPublished := field(col, record, "year published")
		origYear := field(col, record, "original publication year")
		dateReadStr := field(col, record, "date read")
		dateAddedStr := field(col, record, "date added")
		review := field(col, record, "my review")
		isbn := field(col, record, "isbn")
		isbn13 := field(col, record, "isbn13")
		shelf := field(col, record, "exclusive shelf")
		bookshelves := field(col, record, "bookshelves")

		timestamp := parseDate(dateReadStr)
		if timestamp.IsZero() {
			timestamp = parseDate(dateAddedStr)
		}

		var content strings.Builder
		content.WriteString(title)
		if author != "" {
			content.WriteString(" — ")
			content.WriteString(author)
		}
		if review != "" {
			content.WriteString("\n\nReview:\n")
			content.WriteString(review)
		}

		meta := timeline.Metadata{
			"Author":                    author,
			"Publisher":                 publisher,
			"Number of Pages":           pages,
			"Average Rating":            avgRating,
			"My Rating":                 myRating,
			"Year Published":            yearPublished,
			"Original Publication Year": origYear,
			"Date Read":                 dateReadStr,
			"Date Added":                dateAddedStr,
			"Exclusive Shelf":           shelf,
			"Bookshelves":               bookshelves,
			"ISBN":                      isbn,
			"ISBN13":                    isbn13,
		}
		meta.Clean()

		item := &timeline.Item{
			ID:             bookID,
			Classification: timeline.ClassDocument,
			Timestamp:      timestamp,
			IntermediateLocation: entry.Name(),
			Content: timeline.ItemData{
				MediaType: "text/plain",
				Data:      timeline.StringData(content.String()),
			},
			Metadata: meta,
		}

		params.Pipeline <- &timeline.Graph{Item: item}
		params.Continue()
	}

	return nil
}

func field(cols map[string]int, rec []string, name string) string {
	idx, ok := cols[name]
	if !ok || idx >= len(rec) {
		return ""
	}
	return strings.TrimSpace(rec[idx])
}

func parseDate(val string) time.Time {
	val = strings.TrimSpace(val)
	if val == "" {
		return time.Time{}
	}
	t, err := time.Parse(dateLayout, val)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
