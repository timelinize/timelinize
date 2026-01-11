// import Goodreads cvs from https://www.goodreads.com/review/import . Go Read a book !

package goodreads

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

const (
	dataSourceName              = "goodreads"
	exportFilename              = "goodreads_library_export.csv"
	dateLayout                  = "2006/01/02"
	defaultCoverSize            = "L"
	coverISBNURLFormat          = "https://covers.openlibrary.org/b/isbn/%s-%s.jpg?default=false"
	coverOLIDURLFormat          = "https://covers.openlibrary.org/b/olid/%s-%s.jpg?default=false"
	coverIDURLFormat            = "https://covers.openlibrary.org/b/id/%d-%s.jpg?default=false"
	openLibraryEditionURLFormat = "https://openlibrary.org/isbn/%s.json"
	openLibrarySearchURLFormat  = "https://openlibrary.org/search.json?isbn=%s&limit=1&fields=cover_i,cover_edition_key,edition_key"
	openLibraryRequestTimeout   = 8 * time.Second
	coverUserAgent              = "Timelinize Goodreads Importer (+https://github.com/timelinize/timelinize)"
)

var coverHTTPClient = &http.Client{Timeout: openLibraryRequestTimeout}

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            dataSourceName,
		Title:           "Goodreads",
		Icon:            "goodreads.jpg",
		Description:     "Imports Goodreads library export CSV files.",
		NewOptions:      func() any { return new(Options) },
		NewFileImporter: func() timeline.FileImporter { return FileImporter{} },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// FileImporter imports Goodreads CSV exports.
type FileImporter struct{}

// Options configures the Goodreads importer.
type Options struct {
	DisableCovers bool   `json:"disable_covers"`
	CoverSize     string `json:"cover_size"`
}

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

	opt := optionsFromParams(params)

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

	var line, checkpoint int
	if params.Checkpoint != nil {
		if err := json.Unmarshal(params.Checkpoint, &checkpoint); err != nil {
			return fmt.Errorf("decoding checkpoint: %w", err)
		}
	}

	coverCache := make(map[string]string)
	coverCacheChecked := make(map[string]bool)

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

		if checkpoint > 0 && line <= checkpoint {
			line++
			continue
		}

		bookID := field(col, record, "book id")
		title := field(col, record, "title")
		if bookID == "" && title == "" {
			line++
			if err := params.Continue(); err != nil {
				return err
			}
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
		dateAdded := parseDate(dateAddedStr)
		sendStart := !dateAdded.IsZero() && params.Timeframe.Contains(dateAdded)
		sendEnd := !readDate.IsZero() && params.Timeframe.Contains(readDate)
		if !sendStart && !sendEnd {
			line++
			if err := params.Continue(); err != nil {
				return err
			}
			continue
		}

		coverTimestamp := pickCoverTimestamp(readDate, dateAdded, sendEnd)
		coverISBN10, coverISBN13 := coverISBNPair(isbn, isbn13)
		coverURL := ""
		if !opt.DisableCovers && !coverTimestamp.IsZero() {
			cacheKey := coverCacheKey(coverISBN10, coverISBN13, opt.CoverSize)
			if cacheKey != "" {
				if coverCacheChecked[cacheKey] {
					coverURL = coverCache[cacheKey]
				} else {
					coverURL = resolveCoverURL(ctx, params.Log, coverISBN10, coverISBN13, opt.CoverSize)
					coverCache[cacheKey] = coverURL
					coverCacheChecked[cacheKey] = true
				}
			}
		}

		// Item start (Date Added) if available
		if sendStart {
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
			graph := &timeline.Graph{Item: itemStart, Checkpoint: line}
			if !sendEnd && coverURL != "" && !coverTimestamp.IsZero() {
				graph.ToItem(timeline.RelAttachment, coverItem(bookID, coverTimestamp, coverURL, coverISBN10, coverISBN13, entry))
			}
			params.Pipeline <- graph
			if err := params.Continue(); err != nil {
				return err
			}
		}

		// Item end (Date Read) with review/private notes
		if sendEnd {
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
			graph := &timeline.Graph{Item: itemEnd, Checkpoint: line}
			if coverURL != "" && !coverTimestamp.IsZero() {
				graph.ToItem(timeline.RelAttachment, coverItem(bookID, coverTimestamp, coverURL, coverISBN10, coverISBN13, entry))
			}
			params.Pipeline <- graph
			if err := params.Continue(); err != nil {
				return err
			}
		}

		line++
	}

	return nil
}

func optionsFromParams(params timeline.ImportParams) Options {
	opt := Options{CoverSize: defaultCoverSize}
	if params.DataSourceOptions != nil {
		if dsOpt, ok := params.DataSourceOptions.(*Options); ok && dsOpt != nil {
			opt = *dsOpt
		}
	}
	opt.CoverSize = normalizeCoverSize(opt.CoverSize)
	return opt
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
	var b strings.Builder
	b.Grow(len(val))
	for _, r := range val {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == 'X' || r == 'x':
			b.WriteByte('X')
		}
	}
	return b.String()
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
		return review + "\n\n> Notes privees :\n" + privateNotes
	case privateNotes != "":
		return "> Notes privees :\n" + privateNotes
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

func pickCoverTimestamp(readDate, dateAdded time.Time, preferRead bool) time.Time {
	if preferRead && !readDate.IsZero() {
		return readDate
	}
	if !dateAdded.IsZero() {
		return dateAdded
	}
	return readDate
}

func coverISBNPair(isbn10, isbn13 string) (string, string) {
	isbn10 = strings.TrimSpace(isbn10)
	isbn13 = strings.TrimSpace(isbn13)
	if isbn13 == "" && isbn10 != "" {
		if derived := isbn10To13(isbn10); derived != "" {
			isbn13 = derived
		}
	}
	if isbn10 == "" && isbn13 != "" {
		if derived := isbn13To10(isbn13); derived != "" {
			isbn10 = derived
		}
	}
	return isbn10, isbn13
}

func normalizeCoverSize(size string) string {
	size = strings.ToUpper(strings.TrimSpace(size))
	switch size {
	case "S", "M", "L":
		return size
	}
	return defaultCoverSize
}

func coverCacheKey(isbn10, isbn13, size string) string {
	size = normalizeCoverSize(size)
	if isbn13 != "" {
		return size + ":13:" + isbn13
	}
	if isbn10 != "" {
		return size + ":10:" + isbn10
	}
	return ""
}

func resolveCoverURL(ctx context.Context, log *zap.Logger, isbn10, isbn13, size string) string {
	size = normalizeCoverSize(size)
	isbn10, isbn13 = coverISBNPair(isbn10, isbn13)
	candidates := isbnCandidates(isbn10, isbn13)

	for _, isbn := range candidates {
		url := coverURLForISBN(isbn, size)
		if tryCoverURL(ctx, log, url) {
			return url
		}
	}

	for _, isbn := range candidates {
		edition, err := fetchOpenLibraryEdition(ctx, isbn)
		if err != nil {
			logCoverError(log, openLibraryEditionURL(isbn), err)
			if ctx.Err() != nil {
				return ""
			}
			continue
		}
		if edition == nil {
			continue
		}
		if len(edition.Covers) > 0 {
			url := coverURLForID(edition.Covers[0], size)
			if tryCoverURL(ctx, log, url) {
				return url
			}
		}
		if olid := extractOLID(edition.Key); olid != "" {
			url := coverURLForOLID(olid, size)
			if tryCoverURL(ctx, log, url) {
				return url
			}
		}
	}

	for _, isbn := range candidates {
		doc, err := fetchOpenLibrarySearch(ctx, isbn)
		if err != nil {
			logCoverError(log, openLibrarySearchURL(isbn), err)
			if ctx.Err() != nil {
				return ""
			}
			continue
		}
		if doc == nil {
			continue
		}
		if doc.CoverID > 0 {
			url := coverURLForID(doc.CoverID, size)
			if tryCoverURL(ctx, log, url) {
				return url
			}
		}
		olid := strings.TrimSpace(doc.CoverEditionKey)
		if olid == "" && len(doc.EditionKey) > 0 {
			olid = strings.TrimSpace(doc.EditionKey[0])
		}
		if olid != "" {
			url := coverURLForOLID(olid, size)
			if tryCoverURL(ctx, log, url) {
				return url
			}
		}
	}

	return ""
}

func isbnCandidates(isbn10, isbn13 string) []string {
	var candidates []string
	if isbn13 != "" {
		candidates = append(candidates, isbn13)
	}
	if isbn10 != "" && isbn10 != isbn13 {
		candidates = append(candidates, isbn10)
	}
	return candidates
}

func coverURLForISBN(isbn, size string) string {
	if isbn == "" {
		return ""
	}
	return fmt.Sprintf(coverISBNURLFormat, isbn, size)
}

func coverURLForOLID(olid, size string) string {
	if olid == "" {
		return ""
	}
	return fmt.Sprintf(coverOLIDURLFormat, olid, size)
}

func coverURLForID(coverID int, size string) string {
	if coverID <= 0 {
		return ""
	}
	return fmt.Sprintf(coverIDURLFormat, coverID, size)
}

func openLibraryEditionURL(isbn string) string {
	if isbn == "" {
		return ""
	}
	return fmt.Sprintf(openLibraryEditionURLFormat, isbn)
}

func openLibrarySearchURL(isbn string) string {
	if isbn == "" {
		return ""
	}
	return fmt.Sprintf(openLibrarySearchURLFormat, isbn)
}

func tryCoverURL(ctx context.Context, log *zap.Logger, url string) bool {
	if url == "" {
		return false
	}
	exists, err := coverURLExists(ctx, url)
	if err != nil {
		logCoverError(log, url, err)
		return false
	}
	return exists
}

func newCoverRequest(ctx context.Context, method, url string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", coverUserAgent)
	return req, nil
}

func coverURLExists(ctx context.Context, url string) (bool, error) {
	req, err := newCoverRequest(ctx, http.MethodHead, url)
	if err != nil {
		return false, err
	}
	resp, err := coverHTTPClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	case http.StatusMethodNotAllowed:
		return coverURLExistsWithGet(ctx, url)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, nil
	}
	return false, fmt.Errorf("unexpected status: %s", resp.Status)
}

func coverURLExistsWithGet(ctx context.Context, url string) (bool, error) {
	req, err := newCoverRequest(ctx, http.MethodGet, url)
	if err != nil {
		return false, err
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := coverHTTPClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusPartialContent:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, nil
	}
	return false, fmt.Errorf("unexpected status: %s", resp.Status)
}

type openLibraryEdition struct {
	Key    string `json:"key"`
	Covers []int  `json:"covers"`
}

func fetchOpenLibraryEdition(ctx context.Context, isbn string) (*openLibraryEdition, error) {
	url := openLibraryEditionURL(isbn)
	if url == "" {
		return nil, nil
	}
	req, err := newCoverRequest(ctx, http.MethodGet, url)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := coverHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status: %s", resp.Status)
	}

	var edition openLibraryEdition
	if err := json.NewDecoder(resp.Body).Decode(&edition); err != nil {
		return nil, err
	}
	return &edition, nil
}

type openLibrarySearchResponse struct {
	Docs []openLibrarySearchDoc `json:"docs"`
}

type openLibrarySearchDoc struct {
	CoverID         int      `json:"cover_i"`
	CoverEditionKey string   `json:"cover_edition_key"`
	EditionKey      []string `json:"edition_key"`
}

func fetchOpenLibrarySearch(ctx context.Context, isbn string) (*openLibrarySearchDoc, error) {
	url := openLibrarySearchURL(isbn)
	if url == "" {
		return nil, nil
	}
	req, err := newCoverRequest(ctx, http.MethodGet, url)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := coverHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status: %s", resp.Status)
	}

	var search openLibrarySearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&search); err != nil {
		return nil, err
	}
	if len(search.Docs) == 0 {
		return nil, nil
	}
	return &search.Docs[0], nil
}

func extractOLID(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	parts := strings.Split(key, "/")
	return strings.TrimSpace(parts[len(parts)-1])
}

func logCoverError(log *zap.Logger, url string, err error) {
	if log == nil || err == nil || url == "" {
		return
	}
	log.Debug("cover lookup failed", zap.String("url", url), zap.Error(err))
}

func isbn10To13(isbn10 string) string {
	if len(isbn10) != 10 {
		return ""
	}
	for i := 0; i < 9; i++ {
		if isbn10[i] < '0' || isbn10[i] > '9' {
			return ""
		}
	}
	core := "978" + isbn10[:9]
	sum := 0
	for i := 0; i < len(core); i++ {
		digit := int(core[i] - '0')
		if i%2 == 0 {
			sum += digit
		} else {
			sum += digit * 3
		}
	}
	check := (10 - (sum % 10)) % 10
	return core + strconv.Itoa(check)
}

func isbn13To10(isbn13 string) string {
	if len(isbn13) != 13 {
		return ""
	}
	if !strings.HasPrefix(isbn13, "978") {
		return ""
	}
	for i := 0; i < len(isbn13); i++ {
		if isbn13[i] < '0' || isbn13[i] > '9' {
			return ""
		}
	}
	core := isbn13[3:12]
	sum := 0
	for i := 0; i < len(core); i++ {
		digit := int(core[i] - '0')
		sum += (10 - i) * digit
	}
	check := 11 - (sum % 11)
	switch check {
	case 10:
		return core + "X"
	case 11:
		return core + "0"
	}
	if check < 0 || check > 9 {
		return ""
	}
	return core + strconv.Itoa(check)
}

func coverItem(bookID string, ts time.Time, url, isbn, isbn13 string, entry timeline.DirEntry) *timeline.Item {
	filename := coverFilename(bookID, isbn, isbn13)
	return &timeline.Item{
		ID:                   coverItemID(bookID, isbn, isbn13),
		Classification:       timeline.ClassMedia,
		Timestamp:            ts,
		IntermediateLocation: entry.Name(),
		Content: timeline.ItemData{
			Filename:  filename,
			MediaType: "image/jpeg",
			Data:      timeline.DownloadData(url),
		},
		Metadata: timeline.Metadata{
			"ISBN":   isbn,
			"ISBN13": isbn13,
			"Source": "Open Library",
			"URL":    url,
		},
	}
}

func coverItemID(bookID, isbn, isbn13 string) string {
	base := bookID
	if base == "" {
		base = isbn13
	}
	if base == "" {
		base = isbn
	}
	if base == "" {
		return "cover"
	}
	return composeID(base, "cover")
}

func coverFilename(bookID, isbn, isbn13 string) string {
	key := isbn13
	if key == "" {
		key = isbn
	}
	if key == "" {
		key = bookID
	}
	if key == "" {
		return "goodreads_cover.jpg"
	}
	return "goodreads_cover_" + key + ".jpg"
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
