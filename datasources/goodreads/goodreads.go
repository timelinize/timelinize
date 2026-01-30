// import Goodreads cvs from https://www.goodreads.com/review/import . Go Read a book !

package goodreads

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

const (
	dataSourceName              = "goodreads"
	exportFilename              = "goodreads_library_export.csv"
	defaultCoverSize            = "L"
	coverISBNURLFormat          = "https://covers.openlibrary.org/b/isbn/%s-%s.jpg?default=false"
	coverOLIDURLFormat          = "https://covers.openlibrary.org/b/olid/%s-%s.jpg?default=false"
	coverIDURLFormat            = "https://covers.openlibrary.org/b/id/%d-%s.jpg?default=false"
	openLibraryEditionURLFormat = "https://openlibrary.org/isbn/%s.json"
	openLibrarySearchURLFormat  = "https://openlibrary.org/search.json?isbn=%s&limit=1&fields=cover_i,cover_edition_key,edition_key"
	openLibraryRequestTimeout   = 8 * time.Second
	// budget the total cover resolution time per book to avoid long stalls when covers are enabled
	openLibraryCoverResolveBudget = 3 * time.Second
	coverUserAgent                = "Timelinize Goodreads Importer (+https://github.com/timelinize/timelinize)"
)

var dateLayouts = []string{
	"2006/01/02",
	"2006-01-02",
	"2006-1-2",
}

var coverHTTPClient = &http.Client{Timeout: openLibraryRequestTimeout}

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            dataSourceName,
		Title:           "Goodreads",
		Icon:            "goodreads.jpg",
		Description:     "Imports Goodreads library export CSV files.",
		NewOptions:      func() any { return defaultOptions() },
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
	// Covers trigger network calls to Open Library; keep opt-in by default.
	DisableCovers bool `json:"disable_covers"`
	// CoverSize accepts "S", "M", or "L" (default: "L").
	CoverSize string `json:"cover_size"`
	// IncludeNonRead imports non-read shelves (to-read, currently-reading, etc.)
	// as "added/shelved" events using Date Added (it is not treated as a reading start).
	IncludeNonRead bool `json:"include_non_read"`
}

func defaultOptions() Options {
	return Options{
		DisableCovers:  true,
		CoverSize:      defaultCoverSize,
		IncludeNonRead: false,
	}
}

// Recognize reports support for Goodreads "Library Export" CSV data.
//
// Supported inputs:
//   - the Goodreads Library Export CSV file (typically named goodreads_library_export.csv), and
//   - a directory containing that file (for convenience).
//
// Note: recognizing a directory does not necessarily mean it will be auto-assigned/collapsed.
// DirThreshold controls when a directory is treated as a single dataset: with DirThreshold=1,
// this only happens when all non-hidden entries in the directory are recognized as Goodreads exports.
func (FileImporter) Recognize(_ context.Context, entry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	rec := timeline.Recognition{DirThreshold: 1}

	if entry.IsDir() {
		if entry.FileExists(exportFilename) {
			rec.Confidence = 0.7 // directory match: lower confidence to avoid collapsing mixed folders; DirThreshold=1 only affects auto-collapsing
		}
		return rec, nil
	}

	if strings.EqualFold(entry.Name(), exportFilename) {
		rec.Confidence = 1
		return rec, nil
	}

	if strings.ToLower(filepath.Ext(entry.Name())) != ".csv" {
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
		headers[i] = strings.TrimSpace(strings.TrimPrefix(headers[i], "\ufeff"))
	}
	if len(headers) > 0 && strings.EqualFold(headers[0], "Book Id") {
		rec.Confidence = 0.9
	}

	return rec, nil
}

// FileImport reads the Goodreads CSV and emits items.
func (FileImporter) FileImport(ctx context.Context, entry timeline.DirEntry, params timeline.ImportParams) error {
	opt := optionsFromParams(params)

	csvPath := ""
	if entry.IsDir() {
		csvPath = exportFilename
		if !entry.FileExists(csvPath) {
			return fmt.Errorf("expected %s in directory", exportFilename)
		}
	}

	file, err := entry.Open(csvPath)
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
		h = strings.TrimSpace(strings.TrimPrefix(h, "\ufeff"))
		col[strings.ToLower(h)] = i
	}

	line := -1
	checkpoint := -1
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
		line++
		if len(record) == 0 {
			if err := params.Continue(); err != nil {
				return err
			}
			continue
		}

		if checkpoint > 0 && line < checkpoint {
			if err := params.Continue(); err != nil {
				return err
			}
			continue
		}

		bookID := field(col, record, "book id")
		title := field(col, record, "title")
		if bookID == "" && title == "" {
			if err := params.Continue(); err != nil {
				return err
			}
			continue
		}

		additionalAuthors := field(col, record, "additional authors")
		author := field(col, record, "author")
		publisher := field(col, record, "publisher")
		pages := parseInt(field(col, record, "number of pages"))
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
		shelf = strings.ToLower(strings.TrimSpace(shelf))
		if !opt.IncludeNonRead && shelf != "read" {
			if err := params.Continue(); err != nil {
				return err
			}
			continue
		}
		bookshelves := splitList(field(col, record, "bookshelves"))
		readCount := parseInt(field(col, record, "read count"))
		ownedCopies := parseInt(field(col, record, "owned copies"))
		spoiler := strings.EqualFold(strings.TrimSpace(field(col, record, "spoiler")), "true")
		readFlag := hasReadShelf(shelf, bookshelves)

		readDate := parseDate(dateReadStr)
		dateAdded := parseDate(dateAddedStr)
		var ts time.Time
		contentBody := ""
		if shelf == "read" {
			if readDate.IsZero() {
				if err := params.Continue(); err != nil {
					return err
				}
				continue
			}
			ts = readDate
			contentBody = reviewWithNotes(review, privateNotes)
		} else {
			if dateAdded.IsZero() {
				if err := params.Continue(); err != nil {
					return err
				}
				continue
			}
			ts = dateAdded
		}
		if !params.Timeframe.Contains(ts) {
			if err := params.Continue(); err != nil {
				return err
			}
			continue
		}

		idBase := bookID
		if idBase == "" {
			idBase = isbn13
		}
		if idBase == "" {
			idBase = isbn
		}
		if idBase == "" {
			idBase = hashStrings(title, author)
		}

		coverTimestamp := ts
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

		meta := baseMetadata(author, additionalAuthors, publisher, pages, avgRating, myRating, yearPublished, origYear, dateReadStr, dateAddedStr, shelf, bookshelves, isbn, isbn13, binding, readCount, ownedCopies, spoiler, readFlag)
		if opt.IncludeNonRead && shelf != "read" {
			meta["Imported shelf"] = shelf
		}
		content := markdownContent(title, author, myRating, avgRating, contentBody)
		if coverURL != "" && !coverTimestamp.IsZero() {
			meta["Summary"] = content
			content = ""
		}
		item := &timeline.Item{
			ID:                   composeID(idBase, "book"),
			Classification:       timeline.ClassEvent,
			Timestamp:            ts,
			IntermediateLocation: entry.Name(),
			Content: timeline.ItemData{
				MediaType: "text/markdown",
				Data:      timeline.StringData(content),
			},
			Metadata: meta,
		}
		if coverURL != "" {
			item.Content = timeline.ItemData{
				Filename:  coverFilename(idBase, coverISBN10, coverISBN13),
				MediaType: "image/jpeg",
				Data:      timeline.DownloadData(coverURL),
			}
		}
		graph := &timeline.Graph{Item: item}
		graph.Checkpoint = line + 1
		params.Pipeline <- graph
		if err := params.Continue(); err != nil {
			return err
		}
	}

	return nil
}

func optionsFromParams(params timeline.ImportParams) Options {
	opt := defaultOptions()

	if params.DataSourceOptions != nil {
		switch v := params.DataSourceOptions.(type) {
		case *Options:
			if v != nil {
				opt.DisableCovers = v.DisableCovers
				if strings.TrimSpace(v.CoverSize) != "" {
					opt.CoverSize = v.CoverSize
				}
				opt.IncludeNonRead = v.IncludeNonRead
			}
		case map[string]any:
			if val, ok := v["disable_covers"]; ok {
				if b, ok := val.(bool); ok {
					opt.DisableCovers = b
				}
			}
			if val, ok := v["cover_size"]; ok {
				if s, ok := val.(string); ok && strings.TrimSpace(s) != "" {
					opt.CoverSize = s
				}
			}
			if val, ok := v["include_non_read"]; ok {
				if b, ok := val.(bool); ok {
					opt.IncludeNonRead = b
				}
			}
		case json.RawMessage, []byte:
			var o Options
			raw, ok := v.(json.RawMessage)
			if !ok {
				raw = json.RawMessage(v.([]byte))
			}
			if err := json.Unmarshal(raw, &o); err == nil {
				opt.DisableCovers = o.DisableCovers
				if strings.TrimSpace(o.CoverSize) != "" {
					opt.CoverSize = o.CoverSize
				}
				opt.IncludeNonRead = o.IncludeNonRead
			}
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
	for _, layout := range dateLayouts {
		if t, err := time.Parse(layout, val); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
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
		sb.WriteString("\n\n**Author:** ")
		sb.WriteString(author)
	}
	if myRating > 0 {
		sb.WriteString(fmt.Sprintf("\n\n**My rating:** %.1f", myRating))
	}
	if avgRating > 0 {
		sb.WriteString(fmt.Sprintf("\n\n**Average rating:** %.2f", avgRating))
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
		return review + "\n\n> Private notes:\n" + privateNotes
	case privateNotes != "":
		return "> Private notes:\n" + privateNotes
	default:
		return review
	}
}

func composeID(base, suffix string) string {
	if base == "" {
		return suffix
	}
	return base + ":" + suffix
}

func hashStrings(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:8])
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
	ctxCover, cancel := context.WithTimeout(ctx, openLibraryCoverResolveBudget)
	defer cancel()

	size = normalizeCoverSize(size)
	isbn10, isbn13 = coverISBNPair(isbn10, isbn13)
	candidates := isbnCandidates(isbn10, isbn13)

	for _, isbn := range candidates {
		if ctxCover.Err() != nil {
			return ""
		}
		url := coverURLForISBN(isbn, size)
		if tryCoverURL(ctxCover, log, url) {
			return url
		}
	}

	for _, isbn := range candidates {
		if ctxCover.Err() != nil {
			return ""
		}
		edition, err := fetchOpenLibraryEdition(ctxCover, isbn)
		if err != nil {
			logCoverError(log, openLibraryEditionURL(isbn), err)
			if ctxCover.Err() != nil {
				return ""
			}
			continue
		}
		if edition == nil {
			continue
		}
		if len(edition.Covers) > 0 {
			url := coverURLForID(edition.Covers[0], size)
			if tryCoverURL(ctxCover, log, url) {
				return url
			}
		}
		if olid := extractOLID(edition.Key); olid != "" {
			url := coverURLForOLID(olid, size)
			if tryCoverURL(ctxCover, log, url) {
				return url
			}
		}
	}

	for _, isbn := range candidates {
		if ctxCover.Err() != nil {
			return ""
		}
		doc, err := fetchOpenLibrarySearch(ctxCover, isbn)
		if err != nil {
			logCoverError(log, openLibrarySearchURL(isbn), err)
			if ctxCover.Err() != nil {
				return ""
			}
			continue
		}
		if doc == nil {
			continue
		}
		if doc.CoverID > 0 {
			url := coverURLForID(doc.CoverID, size)
			if tryCoverURL(ctxCover, log, url) {
				return url
			}
		}
		olid := strings.TrimSpace(doc.CoverEditionKey)
		if olid == "" && len(doc.EditionKey) > 0 {
			olid = strings.TrimSpace(doc.EditionKey[0])
		}
		if olid != "" {
			url := coverURLForOLID(olid, size)
			if tryCoverURL(ctxCover, log, url) {
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
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return
	}
	log.Debug("cover lookup failed", zap.String("url", url), zap.Error(err))
}

const (
	isbn10Len       = 10
	isbn13Len       = 13
	isbn13WeightAlt = 3
	mod10           = 10
	mod11           = 11
)

func isbn10To13(isbn10 string) string {
	if len(isbn10) != isbn10Len {
		return ""
	}
	for i := range isbn10Len - 1 {
		if isbn10[i] < '0' || isbn10[i] > '9' {
			return ""
		}
	}
	core := "978" + isbn10[:9]
	sum := 0
	for i := range len(core) {
		digit := int(core[i] - '0')
		if i%2 == 0 {
			sum += digit
		} else {
			sum += digit * isbn13WeightAlt
		}
	}
	check := (mod10 - (sum % mod10)) % mod10
	return core + strconv.Itoa(check)
}

func isbn13To10(isbn13 string) string {
	if len(isbn13) != isbn13Len {
		return ""
	}
	if !strings.HasPrefix(isbn13, "978") {
		return ""
	}
	for i := range len(isbn13) {
		if isbn13[i] < '0' || isbn13[i] > '9' {
			return ""
		}
	}
	core := isbn13[3:12]
	sum := 0
	for i := range len(core) {
		digit := int(core[i] - '0')
		sum += (mod10 - i) * digit
	}
	check := mod11 - (sum % mod11)
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

func baseMetadata(author, additionalAuthors, publisher string, pages int, avgRating, myRating float64, yearPublished, origYear int, dateReadStr, dateAddedStr, shelf string, bookshelves []string, isbn, isbn13, binding string, readCount, ownedCopies int, spoiler bool, readFlag bool) timeline.Metadata {
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
