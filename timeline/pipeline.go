package timeline

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func (p *processor) pipeline(ctx context.Context, batch []*Graph) error {
	// During large imports, I've found that running ANALYZE every so often
	// can be helpful for improving performance, since an import is much more
	// than just an INSERT, there's lots of SELECTs along the way that use
	// indexes. For example, in my tests importing about a quarter million
	// messages (relation-heavy, since they are sent to an attribute), which
	// I repeated twice, it would take 30 minutes to import without ANALYZE.
	// But when running ANALYZE every so often, it only took 23 minutes.
	// (This was before the DB indexes in the import process were optimized.)
	p.rootGraphCount += len(batch)
	if p.rootGraphCount%15000 < len(batch) {
		p.tl.optimizeDB(p.log.Named("optimizer"))
	}

	// sanitization / normalization phase
	if err := p.phase0(ctx, batch); err != nil {
		return err
	}

	// download item datas so we know what we're dealing with
	if err := p.phase1(ctx, batch); err != nil {
		return err
	}

	// now that we have all the data, and it's sanitized, add it to the timeline
	if err := p.phase2(ctx, batch); err != nil {
		return err
	}

	return nil
}

// phase0 sanitizes graphs in the batch, and also marks items for skipping if they
// cannot be sanitized in a way that makes them processable.
func (p *processor) phase0(ctx context.Context, batch []*Graph) error {
	// quick input sanitization: timestamps outside a certain range are
	// invalid and obviously wrong (cannot be serialized to JSON), and
	// clean up any metadata as well
	for _, g := range batch {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := p.sanitize(g); err != nil {
			p.log.Error("sanitizing batch", zap.Error(err))
		}
	}
	return nil
}

func (p *processor) sanitize(g *Graph) error {
	if g == nil {
		return nil
	}

	if g.Item != nil {
		// items outside the configured timeframe should be skipped
		// (data sources should also elide these items and not even
		// send them, if possible)
		if p.skip(g.Item) {
			return nil
		}

		g.Item.Metadata.Clean()
		g.Item.Timestamp = validTime(g.Item.Timestamp)
		g.Item.Timespan = validTime(g.Item.Timespan)
		g.Item.Timeframe = validTime(g.Item.Timeframe)

		// TODO: Also ensure Timestamp, Timespan, and Timeframe, and all
		// other time values are in the same zone as Timestamp. (If not,
		// change them to that zone?)
	}

	// traverse graph nodes recursively to sanitize them
	for _, edge := range g.Edges {
		if err := p.sanitize(edge.From); err != nil {
			return err
		}
		if err := p.sanitize(edge.To); err != nil {
			return err
		}
	}

	return nil
}

// skip returns true if the item is marked for skipping. Future processing
// phases should check an item's skipped field and no-op if it is true.
func (p *processor) skip(it *Item) bool {
	// skip item if outside of timeframe (data source should do this for us, but
	// ultimately we should enforce it: it just means the data source is being
	// less efficient than it could be)
	// TODO: also consider Timespan, Timeframe
	if !it.Timestamp.IsZero() && !p.ij.ProcessingOptions.Timeframe.Contains(it.Timestamp) {
		p.log.Warn("ignoring item outside of designated timeframe (data source should not send this item; it is probably being less efficient than it could be)",
			zap.String("item_id", it.ID),
			zap.Timep("tf_since", p.ij.ProcessingOptions.Timeframe.Since),
			zap.Timep("tf_until", p.ij.ProcessingOptions.Timeframe.Until),
			zap.Time("item_timestamp", it.Timestamp),
		)
		it.skip = true
		return true
	}
	return false
}

// phase1 downloads the data of each item, no lock on the DB.
// The whole batch is downloaded concurrently.
func (p *processor) phase1(ctx context.Context, batch []*Graph) error {
	wg := new(sync.WaitGroup)
	for _, g := range batch {
		if g.err != nil {
			continue
		}
		if err := p.downloadDataFilesInGraph(ctx, g, wg); err != nil {
			p.log.Error("downloading data files in graph", zap.Error(err))
			g.err = err
		}
	}
	wg.Wait()
	return nil
}

func (p *processor) downloadDataFilesInGraph(ctx context.Context, g *Graph, wg *sync.WaitGroup) error {
	if g == nil {
		return nil
	}

	if g.Item != nil && !g.Item.skip {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.downloadItemData(ctx, g.Item); err != nil {
				p.log.Error("failed downloading item's data", zap.Error(err))
			}
		}()
	}

	// traverse graph and download their data files
	for _, edge := range g.Edges {
		if err := p.downloadDataFilesInGraph(ctx, edge.From, wg); err != nil {
			return err
		}
		if err := p.downloadDataFilesInGraph(ctx, edge.To, wg); err != nil {
			return err
		}
	}

	return nil
}

func (p *processor) downloadItemData(ctx context.Context, it *Item) error {
	if it == nil || it.Content.Data == nil {
		return nil
	}

	source, err := it.Content.Data(ctx)
	if err != nil {
		return fmt.Errorf("getting item's data stream: %w", err)
	}
	if source == nil {
		// this is probably not intentional? warn developers so they hopefully notice
		p.log.Warn("item's data func returned no reader and no error")
		return nil
	}
	defer source.Close()

	// if we don't know the content-type, try to detect it, first by sniffing
	// the start of the data (more reliable), or look at the file extension if
	// needed (less reliable) -- also do this if the content-type is known to
	// be plaintext or markdown, which we can store in the table, since we will
	// need to see if it's small enough to comfortably fit in the table
	if it.Content.MediaType == "" || it.Content.isPlainTextOrMarkdown() {
		// to detect it, we have to read the file into memory (or at least part
		// of it); and for plaintext data, this also tells us whether the data
		// is too large for us to want to put in this table or if we'll store it
		// elsewhere

		// NOTE: if this code ever gets moved into a separate function, make sure
		// that the buffer is not returned until we're done processing the item; or
		// copy it to a new buffer first, due to buffer pooling & reuse
		bufPtr := sizePeekBufPool.Get().(*[]byte)
		buf := *bufPtr
		defer func() {
			// From the standard lib's crypto/tls package:
			// "You might be tempted to simplify this by just passing &buf to Put,
			// but that would make the local copy of the buf slice header escape
			// to the heap, causing an allocation. Instead, we keep around the
			// pointer to the slice header returned by Get, which is already on the
			// heap, and overwrite and return that."
			// See: https://github.com/dominikh/go-tools/issues/1336
			*bufPtr = buf
			sizePeekBufPool.Put(bufPtr)
		}()

		n, err := io.ReadFull(source, buf)
		if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
			return fmt.Errorf("buffering item's data stream to peek size/content-type: %w", err)
		}

		// We carefully use only the first n bytes of the buffer, since beyond
		// that is likely data from another item that this pooled buffer was
		// previously used with
		bufferedContent := buf[:n]

		// now that we have a sample of the data (or possibly all of it),
		// we can sniff the content-type
		if it.Content.MediaType == "" {
			detectContentType(bufferedContent, it)
		}

		if n == len(buf) {
			// content is at least as large as our buffer, so it probably belongs outside
			// of the items table; wrap the reader so that we can recover the bytes we
			// already buffered when we read the data later
			source = io.NopCloser(io.MultiReader(bytes.NewReader(bufferedContent), source))
		} else if n > 0 && it.Content.isPlainTextOrMarkdown() {
			// this item's data is text and will be stored in the items table,
			// so store it with the item so we can put back the buffer.
			// trim leading/trailing spaces for this because it can be hard for
			// some data sources to strip them, and I don't think we need them,
			// especially for short text content stored in the DB
			dataTextStr := strings.TrimSpace(string(bufferedContent))
			it.dataText = &dataTextStr
			source = nil
		}
	}

	// if the item classification is missing, but the item is clearly
	// a common media type, we can probably classify the item anyway
	// TODO: not sure if a good idea... ho hum.
	if it.Classification.Name == "" {
		if strings.HasPrefix(it.Content.MediaType, "image/") ||
			strings.HasPrefix(it.Content.MediaType, "video/") ||
			strings.HasPrefix(it.Content.MediaType, "audio/") {
			it.Classification = ClassMedia
		}
	}

	// if we don't need a data file (e.g. short text), we can just return now
	if source == nil {
		return nil
	}

	// open the output file and copy the data
	var destination *os.File
	destination, it.dataFilePath, err = p.tl.openUniqueCanonicalItemDataFile(ctx, p.log, nil, it, p.ds.Name)
	if err != nil {
		return fmt.Errorf("opening output data file: %w", err)
	}
	defer destination.Close()

	if err := p.downloadDataFile(it, source, destination); err != nil {
		return err
	}

	return nil
}

// downloadDataFile downloads the data file and hashes it. It attaches the
// results to the item.
func (p *processor) downloadDataFile(it *Item, source io.Reader, destination *os.File) error {
	if it == nil {
		return nil
	}

	h := newHash()

	dataFileSize, err := p.downloadAndHashDataFile(source, destination, h)
	if err != nil {
		return err
	}

	if dataFileSize == 0 {
		// don't keep empty data files laying around
		p.log.Warn("downloaded data file was empty; removing file",
			zap.String("item_original_id", it.ID),
			zap.String("data_file_name", it.dataFilePath),
			zap.Int64("bytes_written", it.dataFileSize))

		if err := p.tl.deleteRepoFile(it.dataFilePath); err != nil {
			p.log.Error("could not delete empty data file",
				zap.String("item_original_id", it.ID),
				zap.String("data_file_name", it.dataFilePath),
				zap.Error(err))
		}
		// make sure it doesn't look like we still have a data file
		it.dataFilePath = ""
	} else {
		it.dataFileSize = dataFileSize
		it.dataFileHash = h.Sum(nil)
	}

	return nil
}

// handleDuplicateItemDataFile checks to see if the checksum of the file downloaded for the item
// already exists in the database. It returns true if the file already exists and a replacement
// occurred to use the existing file (or, if integrity check on the existing file failed, the new
// file instead). If false is returned, the downloaded file is unique.
func (p *processor) handleDuplicateItemDataFile(ctx context.Context, tx *sql.Tx, it *Item) (bool, error) {
	if it.dataFilePath == "" || len(it.dataFileHash) == 0 {
		return false, errors.New("missing data filename and/or hash of contents")
	}

	var existingDataFilePath *string
	err := tx.QueryRowContext(ctx, `SELECT data_file FROM items WHERE data_hash=? AND data_file!=? LIMIT 1`,
		it.dataFileHash, it.dataFilePath).Scan(&existingDataFilePath)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil // file is unique; carry on
	}
	if err != nil {
		return false, fmt.Errorf("querying DB: %w", err)
	}

	// file is a duplicate! by the time this function returns (if successful),
	// it.dataFileName should not exist anymore and should be reassigned to
	// *existingDatafileName instead.

	p.log.Info("data file is a duplicate",
		zap.String("duplicate_data_file", it.dataFilePath),
		zap.Stringp("existing_data_file", existingDataFilePath),
		zap.Binary("checksum", it.dataFileHash))

	if existingDataFilePath == nil {
		// ... that's weird, how's this possible? it has a hash but no file name recorded
		return false, fmt.Errorf("item with matching hash is missing data file name; hash: %x", it.dataFileHash)
	}

	// TODO: maybe this all should be limited to only when integrity checks are enabled? how do we know that this download has the right version/contents?
	p.log.Debug("verifying existing file is still the same",
		zap.Stringp("existing_data_file", existingDataFilePath),
		zap.Binary("checksum", it.dataFileHash))

	// ensure the existing file is still the same
	h := newHash()
	f, err := os.Open(p.tl.FullPath(*existingDataFilePath))
	if err != nil {
		return false, fmt.Errorf("opening existing file: %w", err)
	}
	defer f.Close()

	_, err = io.Copy(h, f)
	if err != nil {
		return false, fmt.Errorf("reading file for opportunistic integrity check: %w", err)
	}

	existingFileHash := h.Sum(nil)

	if !bytes.Equal(it.dataFileHash, existingFileHash) {
		// the existing file was corrupted, so restore it with
		// what we just downloaded, which presumably succeeded
		// (by simply renaming the file on disk, we don't have
		// to update any entries in the DB)
		p.log.Warn("existing data file failed integrity check (checksum on disk changed; file corrupted or modified?) - replacing existing file with this one",
			zap.Stringp("existing_data_file", existingDataFilePath),
			zap.Binary("expected_checksum", it.dataFileHash),
			zap.Binary("actual_checksum", existingFileHash),
			zap.String("replacement_data_file", it.dataFilePath))
		err := os.Rename(p.tl.FullPath(it.dataFilePath), p.tl.FullPath(*existingDataFilePath))
		if err != nil {
			return false, fmt.Errorf("replacing modified data file: %w", err)
		}
	} else {
		// everything checks out; delete the newly-downloaded file
		// and use the existing file instead of duplicating it
		p.log.Debug("existing file passed integrity check; using it instead of newly-downloaded duplicate",
			zap.Stringp("existing_data_file", existingDataFilePath),
			zap.String("new_data_file", it.dataFilePath),
			zap.Binary("checksum", it.dataFileHash))
		err = p.tl.deleteRepoFile(it.dataFilePath)
		if err != nil {
			return false, fmt.Errorf("removing duplicate data file: %w", err)
		}
	}

	p.log.Info("removed duplicate data file based on integrity check",
		zap.String("duplicate_data_file", it.dataFilePath),
		zap.Stringp("existing_data_file", existingDataFilePath),
		zap.Binary("checksum", it.dataFileHash))

	it.dataFilePath = *existingDataFilePath

	return true, nil
}

// downloadAndHashDataFile writes source to destination, hashing it along the way.
// It returns the number of bytes copied.
func (p *processor) downloadAndHashDataFile(source io.Reader, destination *os.File, h hash.Hash) (int64, error) {
	// give the hasher a copy of the file bytes
	tr := io.TeeReader(source, h)

	n, err := io.Copy(destination, tr)
	if err != nil {
		// TODO: The error should be highlighted as a notification of some sort. Ideally, keep what we have, but somehow indicate in the DB that it's corrupt/incomplete (like not filling out a data_hash)
		_ = p.tl.deleteRepoFile(destination.Name())
		return n, fmt.Errorf("copying contents: %w", err)
	}

	// TODO: If n == 0, should we retry? (would need to call h.Reset() first) - to help handle sporadic I/O issues maybe

	// we can probably increase performance if we don't sync all the time, but that might be less reliable...
	if n > 0 {
		if err := destination.Sync(); err != nil {
			return n, fmt.Errorf("syncing file after downloading: %w", err)
		}
	}

	p.log.Debug("downloaded data file",
		zap.String("filename", destination.Name()),
		zap.Int64("size", n),
	)

	return n, nil
}

// phase2 inserts or updates the graphs in the database. It tidies up the data file, if any, according
// to information found in the database. This is the sole phase that obtains a DB lock and must come
// after the data files have been downloaded. It also emits logs for the frontend.
func (p *processor) phase2(ctx context.Context, batch []*Graph) error {
	p.tl.dbMu.Lock()
	defer p.tl.dbMu.Unlock()

	tx, err := p.tl.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction for batch: %w", err)
	}
	defer tx.Rollback()

	for _, g := range batch {
		if err = p.processGraph(ctx, tx, g); err != nil {
			p.log.Error("processing graph", zap.String("graph", g.String()), zap.Error(err))
			g.err = err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction for batch: %w", err)
	}

	return nil
}

func (p *processor) processGraph(ctx context.Context, tx *sql.Tx, g *Graph) error {
	if g == nil {
		return nil
	}

	// validate node type
	if g.Item != nil && g.Entity != nil {
		return fmt.Errorf("ambiguous node in graph is both an item and entity node (item_graph=%p)", g)
	}

	// this whole big thing is one huge log so the UI can stream
	// a sample of live import data
	defer func() {
		// logging the progress every item/entity that gets processed is actually
		// not super efficient, so we use this trick to only prepare the log entry
		// if it will, in fact, be logged (we sample logs to increase efficiency,
		// but those gains are most realized when we avoid our own processing if
		// a particular log entry will be dropped too, hence the call to Check())
		if checkedLog := p.log.Check(zapcore.InfoLevel, "finished graph"); checkedLog != nil {
			graphType := "item"
			if g.Entity != nil {
				graphType = "entity"
			}

			fields := []zapcore.Field{
				zap.String("graph", fmt.Sprintf("%p", g)),
				zap.String("type", graphType),
				zap.Uint64("row_id", g.rowID.id()),
				zap.Int64("new_entities", atomic.LoadInt64(p.ij.newEntityCount)),
				zap.Int64("new_items", atomic.LoadInt64(p.ij.newItemCount)),
				zap.Int64("updated_items", atomic.LoadInt64(p.ij.updatedItemCount)),
				zap.Int64("skipped_items", atomic.LoadInt64(p.ij.skippedItemCount)),
				zap.Int64("total_items", atomic.LoadInt64(p.ij.itemCount)),
			}

			if g.Item != nil && !g.Item.Timestamp.IsZero() {
				fields = append(fields, zap.Time("item_timestamp", g.Item.Timestamp))
			}

			entityAttr := func(e Entity) zapcore.Field {
				if e.Name != "" {
					return zap.String("entity", e.Name)
				}
				for _, attr := range e.Attributes {
					if attr.Identifying || attr.Identity {
						return zap.Any("entity", attr.Value)
					}
				}
				return zap.Stringp("entity", nil)
			}
			item, entity := g.Item, g.Entity
			if options, obfuscate := p.tl.obfuscationMode(); obfuscate {
				// We tediously (shallow-ish) copy the values that are anonymized,
				// since we have to log them with obfuscation mode enabled. Why
				// do we need to copy them first? Because the tx isn't finished
				// yet. This whole method is one iteration's call as part of a
				// batch, so if we change the values right now, they'll go into
				// the DB like that -- I have verified this by inspecting the DB,
				// and found obfuscated values -- yikes! so we do have to copy
				// the values that get anonymized, even if we don't log them.
				if item != nil {
					anonItem := *item
					anonItem.row.Anonymize(options)
					anonItem.row.Metadata = make(json.RawMessage, len(item.row.Metadata))
					copy(anonItem.row.Metadata, item.row.Metadata)
					anonItem.Owner.Attributes = make([]Attribute, len(item.Owner.Attributes))
					copy(anonItem.Owner.Attributes, item.Owner.Attributes)
					for i := range anonItem.Owner.Attributes {
						anonItem.Owner.Attributes[i].Metadata = make(Metadata)
						maps.Copy(anonItem.Owner.Attributes[i].Metadata, item.Owner.Attributes[i].Metadata)
					}
					anonItem.Owner.Anonymize(options)
					item = &anonItem
				}
				if entity != nil {
					anonEntity := *entity
					anonEntity.Metadata = make(Metadata, len(entity.Metadata))
					maps.Copy(anonEntity.Metadata, entity.Metadata)
					anonEntity.Attributes = make([]Attribute, len(entity.Attributes))
					copy(anonEntity.Attributes, entity.Attributes)
					for i := range anonEntity.Attributes {
						anonEntity.Attributes[i].Metadata = make(Metadata)
						maps.Copy(anonEntity.Attributes[i].Metadata, entity.Attributes[i].Metadata)
					}
					anonEntity.ID = g.rowID.id()
					anonEntity.Anonymize(options)
					entity = &anonEntity
				}
			}
			if item != nil {
				size := item.dataFileSize
				if item.row.DataText != nil {
					size = int64(len(*item.row.DataText))
				}
				preview := item.row.DataText
				const maxPreviewLen = 30
				if preview != nil && len(*preview) > maxPreviewLen {
					shortPreview := (*preview)[:maxPreviewLen]
					preview = &shortPreview
				}
				fields = append(fields,
					zap.String("status", string(item.row.howStored)),
					zap.String("classification", item.Classification.Name),
					zap.Stringp("preview", preview),
					zap.Stringp("filename", item.row.Filename),
					zap.Int64("size", size),
					zap.Float64p("lat", item.row.Latitude),
					zap.Float64p("lon", item.row.Longitude),
					zap.String("media_type", item.Content.MediaType),
					entityAttr(item.Owner))
			} else if entity != nil {
				fields = append(fields, entityAttr(*entity))
			}
			checkedLog.Write(fields...)
		}
	}()

	// process root node
	switch {
	case g.Entity != nil:
		var err error
		g.rowID, err = p.processEntity(ctx, tx, *g.Entity)
		if err != nil {
			return fmt.Errorf("processing entity node: %w", err)
		}
	case g.Item != nil:
		var err error
		g.rowID, err = p.processItem(ctx, tx, g.Item)
		if err != nil {
			return fmt.Errorf("processing item node: %w", err)
		}
	}

	// process connected nodes
	for _, r := range g.Edges {
		err := p.processRelationship(ctx, tx, r, g)
		if err != nil {
			p.log.Error("processing relationship",
				zap.Uint64("item_or_attribute_row_id", g.rowID.id()),
				zap.Error(err))
		}
	}

	return nil
}

func (p *processor) processItem(ctx context.Context, tx *sql.Tx, it *Item) (latentID, error) {
	if it.skip {
		return latentID{}, nil
	}

	// if this item has a data file, check if it's a duplicate and handle accordingly
	if it.dataFilePath != "" {
		_, err := p.handleDuplicateItemDataFile(ctx, tx, it)
		if err != nil {
			return latentID{}, fmt.Errorf("checking if item data file was  %w", err)
		}
	}

	itemRowID, err := p.storeItem(ctx, tx, it)
	if err != nil {
		return latentID{itemID: itemRowID}, err
	}

	return latentID{itemID: itemRowID}, nil
}

// storeItem stores the item in the database, updating an existing row if pertinent.
// It returns the row ID of the item.
func (p *processor) storeItem(ctx context.Context, tx *sql.Tx, it *Item) (uint64, error) {
	// keep count of number of items processed, mainly for logging
	defer atomic.AddInt64(p.ij.itemCount, 1)

	// prepare for DB queries to see if we have this same item already
	// in some form or another
	var dsName *string
	if p.ds.Name != "" {
		dsName = &p.ds.Name
	}
	it.makeIDHash(dsName)
	it.makeContentHash()

	// if this function returns with an error, we can avoid an orphaned data file by deleting it
	// TODO: a background maintenance routine/job that regularly deletes orphaned data files
	var err error
	if it.dataFilePath != "" {
		defer func() {
			if err != nil {
				if err := p.tl.deleteRepoFile(it.dataFilePath); err != nil {
					p.log.Warn("could not clean up data file of failed item",
						zap.String("data_file_path", it.dataFilePath),
						zap.Error(err))
				}
			}
		}()
	}

	uniqueConstraints := it.Retrieval.finalUniqueConstraints(p.ij.ProcessingOptions.ItemUniqueConstraints)

	// if the item is already in our DB, load it
	var ir ItemRow
	ir, err = p.tl.loadItemRow(ctx, tx, 0, 0, it, dsName, uniqueConstraints, true)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("looking up item in database: %w", err)
	}
	defer func() { it.row = ir }() // used later in the pipeline for logging

	// if the item is already in the DB, figure out what, if anything, will be updated
	if ir.ID > 0 {
		// first we need to distill user's update preferences down to update policies for the DB
		if err := p.distillUpdatePolicies(it, ir); err != nil {
			return 0, fmt.Errorf("distilling initial update policies: %w", err)
		}

		// now determine if we should process this duplicate/existing item at all
		reprocessItem, _ := p.shouldProcessExistingItem(it, ir, it.dataFilePath != "")
		if !reprocessItem {
			atomic.AddInt64(p.ij.skippedItemCount, 1)
			p.log.Debug("skipping processing of existing item",
				zap.Uint64("row_id", ir.ID),
				zap.String("intermediate_path", it.IntermediateLocation),
				zap.String("filename", it.Content.Filename),
				zap.String("classification", it.Classification.Name),
				zap.String("original_id", it.ID))
			// add some info for logging purposes
			ir.howStored = itemSkipped
			return ir.ID, nil
		}

		p.log.Debug("found existing item row according to configured unique constraints",
			zap.Uint64("row_id", ir.ID),
			zap.String("filename", it.Content.Filename),
			zap.String("intermediate_path", it.IntermediateLocation),
			zap.String("original_id", it.ID))

		// if existing item row already has a data file, see if the incoming item changes a filename;
		// this can happen if the filename differs from what is in the DB (or adds one where it was
		// missing), or if there is no filename and the timestamp is different
		if ir.DataFile != nil {
			tsUpdatePol := it.fieldUpdatePolicies["timestamp"]
			filenameUpdatePol := it.fieldUpdatePolicies["filename"]

			incomingItemAddsFilename := ir.Filename == nil && it.Content.Filename != ""
			incomingItemUpdatesFilename := filenameUpdatePol > 0 && (ir.Filename == nil || it.Content.Filename != *ir.Filename)
			incomingItemUpdatesFilenameWithTime := ir.Filename == nil && tsUpdatePol > 0 &&
				!it.Timestamp.IsZero() && (ir.Timestamp == nil || !it.Timestamp.Equal(*ir.Timestamp))

			if incomingItemAddsFilename || incomingItemUpdatesFilename || incomingItemUpdatesFilenameWithTime {
				newDataFilePath, err := p.renameDataFile(ctx, tx, it, *ir.DataFile, it.Content.Filename)
				if err != nil {
					p.log.Error("could not rename existing data file given new filename information", zap.Error(err))
				} else {
					p.log.Info("renamed existing data file given new filename information",
						zap.String("new_filename", it.Content.Filename),
						zap.String("old_data_file_path", *ir.DataFile),
						zap.String("new_data_file_path", newDataFilePath))
					ir.DataFile = &newDataFilePath
					ir.Filename = &it.Content.Filename
				}
			}
		}

		// if a filename is already in the database, but is not in this item graph, use the one in the database
		// (useful if the data source sends an item piecewise, like filename first, then actual data file in a later graph)
		if ir.Filename != nil && it.Content.Filename == "" && it.dataFilePath != "" {
			newDataFilePath, err := p.renameDataFile(ctx, tx, it, it.dataFilePath, *ir.Filename)
			if err != nil {
				p.log.Error("could not rename data file given filename information found in database", zap.Error(err))
			} else {
				p.log.Info("renamed data file given filename information found in database",
					zap.String("discovered_filename", *ir.Filename),
					zap.String("old_data_file_path", it.dataFilePath),
					zap.String("new_data_file_path", newDataFilePath))
				it.dataFilePath = newDataFilePath
			}
		}
	}

	// we assume we're on a case-sensitive file system; that means that the filename might collide
	// with another if moved to a case-sensitive file system - that'd be really bad, so let's make
	// sure the filename is unique case-insensitively too
	if it.dataFilePath != "" {
		// query the DB for a data_file that matches case-insensitively (data_file is defined to be COLLATE NOCASE)
		// and doesn't match exactly ("COLLATE BINARY")
		var count int
		err = tx.QueryRowContext(ctx, `SELECT count() FROM items WHERE data_file=? AND data_file!=? COLLATE BINARY LIMIT 1`,
			it.dataFilePath, it.dataFilePath).Scan(&count)
		if err != nil {
			return 0, fmt.Errorf("checking DB for case-insensitive filename uniqueness: %w", err)
		}
		if count > 0 {
			newDataFileName, err := p.renameDataFile(ctx, tx, it, it.dataFilePath, path.Base(it.dataFilePath))
			if err == nil {
				p.log.Info("data file renamed to be case-insensitively unique",
					zap.String("old_data_file_path", it.dataFilePath),
					zap.String("new_data_file_path", newDataFileName))
				it.dataFilePath = newDataFileName
			} else {
				p.log.Error("could not rename existing data file to avoid potential case-insensitive collisions", zap.Error(err))
			}
		}
	}

	// make a copy of this 'cause we might use it later to clean up a data file if we ended up setting it to NULL or replacing it
	startingDataFile := ir.DataFile

	// convert incoming item to a row that can be inserted into the DB
	err = p.fillItemRow(ctx, tx, &ir, it)
	if err != nil {
		return 0, fmt.Errorf("assembling item for storage: %w", err)
	}

	// execute query to insert or update the item
	ir.ID, ir.howStored, err = p.insertOrUpdateItem(ctx, tx, ir, it.HasContent(), it.fieldUpdatePolicies)
	if err != nil {
		return 0, fmt.Errorf("storing item in database: %w (row_id=%d item_id=%v)", err, ir.ID, ir.OriginalID)
	}

	// items that are updated should have new embeddings generated; delete any old ones
	if ir.howStored == itemUpdated {
		_, err = tx.ExecContext(ctx, `DELETE FROM embeddings WHERE item_id=?`, ir.ID)
		if err != nil {
			return 0, fmt.Errorf("deleting old embeddings of updated item(s): %w", err)
		}
	}

	// if the incoming data file didn't end up being used, clean it up
	if it.dataFilePath != "" {
		var count int
		err = tx.QueryRowContext(ctx, `SELECT count() FROM items WHERE data_file=? LIMIT 1`, it.dataFilePath).Scan(&count)
		if err != nil {
			return 0, fmt.Errorf("checking DB for unused incoming data file: %w", err)
		}
		if count == 0 {
			if err = p.tl.deleteRepoFile(it.dataFilePath); err != nil {
				p.log.Error("unable to clean up unused incoming data file",
					zap.String("data_file_path", it.dataFilePath),
					zap.Error(err))
			}
		}
	}

	// if there's a chance that we just set the data_file to NULL, check to see if the
	// old file is no longer referenced in the DB, and if not, clean it up
	if startingDataFile != nil && ir.DataFile == nil {
		if err := p.tl.deleteDataFileAndThumbnailIfUnreferenced(ctx, tx, *startingDataFile); err != nil {
			p.log.Error("cleaning up unused data file",
				zap.Uint64("item_row_id", ir.ID),
				zap.Stringp("data_file_name", startingDataFile),
				zap.Error(err))
		}
	}

	// if this replaced an item's previous data file, clean up the old one if it is no longer
	// referenced by any other items
	if it.dataFilePath != "" && startingDataFile != nil && it.dataFilePath != *startingDataFile {
		if err := p.tl.deleteDataFileAndThumbnailIfUnreferenced(ctx, tx, *startingDataFile); err != nil {
			p.log.Error("could not clean up old, unreferenced data file and any associated thumbnail",
				zap.String("old_data_file", *startingDataFile),
				zap.String("replaced_by", it.dataFilePath),
				zap.Uint64("row_id", ir.ID),
				zap.Error(err))
		}

		// additionally, if the incoming file has the same filename as the one we just deleted, let's see
		// if now that filename is available, to try to preserve the item's original filename best we can
		// (for example, if an item with a file named "A.JPG" is being replaced by a new "A.JPG", the
		// processor will initially create "A__asdf.jpg" because "A.JPG" already exists; but once we get
		// here, that old file has been deleted, so we can now restore the original name to the new file)
		if it.intendedDataFileName != "" && path.Base(it.dataFilePath) != it.intendedDataFileName {
			desiredFilename := path.Join(path.Dir(it.dataFilePath), it.intendedDataFileName)
			desiredFilenameFullPath := p.tl.FullPath(desiredFilename)
			f, err := os.OpenFile(desiredFilenameFullPath, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0600)
			if err == nil {
				f.Close()
				if err := os.Rename(p.tl.FullPath(it.dataFilePath), desiredFilenameFullPath); err != nil {
					p.log.Error("could not restore data file name from temporary name",
						zap.String("temporary_name", it.dataFilePath),
						zap.String("name_to_restore", desiredFilename),
						zap.Uint64("row_id", ir.ID),
						zap.Error(err))
				} else {
					_, err = tx.ExecContext(ctx, "UPDATE items SET data_file=? WHERE data_file=?", desiredFilename, it.dataFilePath)
					if err == nil {
						it.dataFilePath = desiredFilename
					} else {
						p.log.Error("could not update row with renamed data file paths",
							zap.String("previous_path", it.dataFilePath),
							zap.String("new_path", desiredFilename),
							zap.Uint64("row_id", ir.ID),
							zap.Error(err))
					}
				}
			} else if !errors.Is(err, fs.ErrExist) {
				p.log.Error("could not create placeholder file for data file of the which original filename is being restored",
					zap.String("temporary_name", it.dataFilePath),
					zap.String("name_to_restore", desiredFilename),
					zap.Uint64("row_id", ir.ID),
					zap.Error(err))
			}
		}
	}

	// Count data files which are eligible for a thumbnail and which are not excluded from
	// receiving a thumbnail (like live photos/motion picture sidecar files, which are
	// generally an exception from normal related items). This count is not used to configure
	// the thumbnail job's total size, but it was in a previous version of the code. Now the
	// job calculates it when it starts. This count is mainly used to determine whether to
	// even start a thumbnail job. TODO: That said, maybe we should always start a thumbnail
	// job even if it does nothing? Then we don't have to count here.
	if qualifiesForThumbnail(ir.DataType) && !it.skipThumb {
		atomic.AddInt64(p.ij.thumbnailCount, 1)
	}

	return ir.ID, nil
}

func (p *processor) renameDataFile(ctx context.Context, tx *sql.Tx, incoming *Item, oldDataFilePath, newFilename string) (string, error) {
	incoming.Content.Filename = newFilename

	placeholderFile, newDataFilePath, err := p.tl.openUniqueCanonicalItemDataFile(ctx, p.log, tx, incoming, p.ds.Name)
	if err != nil {
		return "", fmt.Errorf("could not rename data file: %w (old_data_file_name=%s new_filename=%s)", err, oldDataFilePath, newFilename)
	}
	_ = placeholderFile.Close()

	// NOTE: if DB transaction fails, this rename does not get rolled back
	err = os.Rename(p.tl.FullPath(oldDataFilePath), p.tl.FullPath(newDataFilePath))
	if err != nil {
		return "", fmt.Errorf("could not rename data file: %w (old=%s new=%s)", err, oldDataFilePath, newDataFilePath)
	}

	logger := p.log.With(
		zap.String("old_data_file_path", oldDataFilePath),
		zap.String("new_data_file_path", newDataFilePath),
		zap.String("new_filename", newFilename),
	)

	// update all rows that may point to this data file to use the new path
	if _, err := tx.ExecContext(ctx, "UPDATE items SET data_file=? WHERE data_file=?", newDataFilePath, oldDataFilePath); err != nil {
		logger.Error("renamed data file, but failed to update timeline database", zap.Error(err))
	} else {
		logger.Info("renamed data file")
	}

	// also update thumbnails DB in case any thumbnails were already generated for the item
	p.tl.thumbsMu.Lock()
	_, err = p.tl.thumbs.ExecContext(ctx, "UPDATE thumbnails SET data_file=? WHERE data_file=?", newDataFilePath, oldDataFilePath)
	p.tl.thumbsMu.Unlock()
	if err != nil {
		logger.Error("renamed data file, but failed to update thumbnail database", zap.Error(err))
	}

	// if the rename left the folder empty, clean it up
	oldDir := path.Dir(oldDataFilePath)
	if err := p.tl.cleanDirs(oldDir); err != nil {
		logger.Error("failed tidying potentially empty folder of old path",
			zap.String("old_data_file_path", oldDir),
			zap.Error(err))
	}

	return newDataFilePath, nil
}
