// import Goodreads cvs from https://www.goodreads.com/review/import . Go Read a book ! 

package goodreads

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
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

		additionalAuthors := field(col, record, "additional authors")
		author := field(col, record, "author")
		publisher := field(col, record, "publisher")
		pages := field(col, record, "number of pages")
		avgRating := parseFloat(field(col, record, "average rating"))
		myRating := parseFloat(field(col, record, "my rating"))
		yearPublished := parseInt(field(col, record, "year published"))
		origYear := parseInt(field(col, record, "original publication year"))
		dateReadStr := field(col, record, "date read")
		dateAddedStr := field(col, record, "date added")
		review := field(col, record, "my review")
		privateNotes := field(col, record, "private notes")
		isbn := cleanISBN(field(col, record, "isbn"))
		isbn13 := cleanISBN(field(col, record, "isbn13"))
		binding := field(col, record, "binding")
		shelf := field(col, record, "exclusive shelf")
		bookshelves := splitList(field(col, record, "bookshelves"))
		readCount := parseInt(field(col, record, "read count"))
		ownedCopies := parseInt(field(col, record, "owned copies"))
		spoiler := strings.EqualFold(strings.TrimSpace(field(col, record, "spoiler")), "true")
		readFlag := hasReadShelf(shelf, bookshelves)

		readDate := parseDate(dateReadStr)

		// Item start (Date Added) if available
		dateAdded := parseDate(dateAddedStr)
		if !dateAdded.IsZero() {
			metaStart := baseMetadata(author, additionalAuthors, publisher, pages, avgRating, myRating, yearPublished, origYear, dateReadStr, dateAddedStr, shelf, bookshelves, isbn, isbn13, binding, readCount, ownedCopies, spoiler, readFlag)
			contentStart := markdownContent(title, author, myRating, avgRating, "")
			itemStart := &timeline.Item{
				ID:                   composeID(bookID, "start"),
				Classification:       timeline.ClassDocument,
				Timestamp:            dateAdded,
				IntermediateLocation: entry.Name(),
				Content: timeline.ItemData{
					MediaType: "text/markdown",
					Data:      timeline.StringData(contentStart),
				},
				Metadata: metaStart,
			}
			params.Pipeline <- &timeline.Graph{Item: itemStart}
			_ = params.Continue()
		}

		// Item end (Date Read) with review/private notes
		if !readDate.IsZero() {
			metaEnd := baseMetadata(author, additionalAuthors, publisher, pages, avgRating, myRating, yearPublished, origYear, dateReadStr, dateAddedStr, shelf, bookshelves, isbn, isbn13, binding, readCount, ownedCopies, spoiler, readFlag)
			contentEnd := markdownContent(title, author, myRating, avgRating, reviewWithNotes(review, privateNotes))
			itemEnd := &timeline.Item{
				ID:                   composeID(bookID, "end"),
				Classification:       timeline.ClassDocument,
				Timestamp:            readDate,
				IntermediateLocation: entry.Name(),
				Content: timeline.ItemData{
					MediaType: "text/markdown",
					Data:      timeline.StringData(contentEnd),
				},
				Metadata: metaEnd,
			}
			params.Pipeline <- &timeline.Graph{Item: itemEnd}
			_ = params.Continue()
		}
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

func parseInt(val string) int {
	val = strings.TrimSpace(val)
	if val == "" {
		return 0
	}
	n, _ := strconv.Atoi(val)
	return n
}

func parseFloat(val string) float64 {
	val = strings.TrimSpace(val)
	if val == "" {
		return 0
	}
	f, _ := strconv.ParseFloat(val, 64)
	return f
}

func cleanISBN(val string) string {
	val = strings.TrimSpace(val)
	val = strings.Trim(val, "=\"")
	return val
}

func splitList(val string) []string {
	val = strings.TrimSpace(val)
	if val == "" {
		return nil
	}
	parts := strings.Split(val, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func markdownContent(title, author string, myRating, avgRating float64, body string) string {
	var sb strings.Builder
	sb.WriteString("# ")
	sb.WriteString(title)
	if author != "" {
		sb.WriteString("\n\n**Auteur :** ")
		sb.WriteString(author)
	}
	if myRating > 0 {
		sb.WriteString(fmt.Sprintf("\n\n**Ma note :** %.1f", myRating))
	}
	if avgRating > 0 {
		sb.WriteString(fmt.Sprintf("\n\n**Note moyenne :** %.2f", avgRating))
	}
	if body != "" {
		sb.WriteString("\n\n---\n\n")
		sb.WriteString(body)
	}
	return sb.String()
}

func reviewWithNotes(review, privateNotes string) string {
	review = strings.TrimSpace(review)
	privateNotes = strings.TrimSpace(privateNotes)
	switch {
	case review != "" && privateNotes != "":
		return review + "\n\n> Notes privées :\n" + privateNotes
	case privateNotes != "":
		return "> Notes privées :\n" + privateNotes
	default:
		return review
	}
}

func parseSeriesPosition(title string) int {
	return 0
}

func composeID(base, suffix string) string {
	if base == "" {
		return suffix
	}
	return base + ":" + suffix
}

func baseMetadata(author, additionalAuthors, publisher, pages string, avgRating, myRating float64, yearPublished, origYear int, dateReadStr, dateAddedStr, shelf string, bookshelves []string, isbn, isbn13, binding string, readCount, ownedCopies int, spoiler bool, readFlag bool) timeline.Metadata {
	meta := timeline.Metadata{
		"Author":                    author,
		"Additional Authors":        additionalAuthors,
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
		"Binding":                   binding,
		"Read Count":                readCount,
		"Owned Copies":              ownedCopies,
		"Spoiler":                   spoiler,
		"Read":                      readFlag,
	}
	meta.Clean()
	return meta
}

func hasReadShelf(shelf string, bookshelves []string) bool {
	if strings.EqualFold(strings.TrimSpace(shelf), "read") {
		return true
	}
	for _, s := range bookshelves {
		if strings.EqualFold(strings.TrimSpace(s), "read") {
			return true
		}
	}
	return false
}
