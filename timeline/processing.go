/*
	Timelinize
	Copyright (c) 2013 Matthew Holt

	This program is free software: you can redistribute it and/or modify
	it under the terms of the GNU Affero General Public License as published
	by the Free Software Foundation, either version 3 of the License, or
	(at your option) any later version.

	This program is distributed in the hope that it will be useful,
	but WITHOUT ANY WARRANTY; without even the implied warranty of
	MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
	GNU Affero General Public License for more details.

	You should have received a copy of the GNU Affero General Public License
	along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package timeline

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/zeebo/blake3"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// defaultBatchSize is how many items/entities (approximately) to process per transaction
// if not specified by the user. See the docs for batch size on ProcessingOoptions.
const defaultBatchSize = 10

func (p *processor) beginProcessing(ctx context.Context, po ProcessingOptions, countOnly bool, done <-chan struct{}) (*sync.WaitGroup, chan<- *Graph) {
	wg := new(sync.WaitGroup)
	ch := make(chan *Graph)

	if po.BatchSize <= 0 {
		po.BatchSize = defaultBatchSize
	}

	wg.Add(1)
	go func() {
		defer wg.Done()

		// addToBatch adds g to the batch, and if the batch is full, it
		// sends it for processing and resets the batch. If g is nil,
		// the batch is processed regardless of its size.
		addToBatch := func(g *Graph) {
			var batch []*Graph

			// add the new graph to the batch, keeping track of its actual
			// nested size; and if the batch is now large enough to process,
			// copy it (just the slice header) then reset the batch
			if g != nil {
				p.batch = append(p.batch, g)
				p.batchSize += g.Size()
			}
			if p.batchSize >= po.BatchSize || (g == nil && len(p.batch) > 0) {
				batch = p.batch
				p.batch = make([]*Graph, 0, po.BatchSize)
				p.batchSize = 0
			}

			if len(batch) > 0 {
				err := p.pipeline(ctx, batch)
				if err != nil {
					p.log.Error("batch pipeline", zap.Error(err))
				}

				lastChkptIdx := len(batch) - 1
				for i := len(batch) - 1; i >= 0; i-- {
					if batch[i].Checkpoint != nil {
						lastChkptIdx = i
						break
					}
				}

				var batchSizeToCheckpoint int
				for i := 0; i <= lastChkptIdx; i++ {
					batchSizeToCheckpoint += batch[i].Size()
				}

				p.ij.job.Progress(batchSizeToCheckpoint)

				// persist the last (most recent) checkpoint in the batch
				if batch[lastChkptIdx].Checkpoint != nil {
					if err := p.ij.checkpoint(p.estimatedCount, p.outerLoopIdx, p.innerLoopIdx, batch[lastChkptIdx].Checkpoint); err != nil {
						p.log.Error("checkpointing", zap.Error(err))
					}
				}
			}
		}

		// read all incoming graphs and add them to a batch, and'
		// process the batch if it is full
		for {
			// block here if job is paused; but don't return if canceled
			// (non-nil error) for the reason described just below
			_ = p.ij.job.Continue()

			// it may seem weird that we don't select on ctx.Done()
			// here, and that's because we expect data sources to
			// honor context cancellation; once they return, the
			// done channel will be closed, and that's what we
			// terminate on; if we return of our own accord when
			// the context is canceled, we may leave data source
			// goroutines hanging if they're trying to send on
			// the pipeline channel... they can't know the context
			// has canceled because they're blocked on a send,
			// so we need to receive their sends until they
			// are done (and data sources are expected to wait
			// for their own goroutines to finish before they
			// return completely)
			select {
			case <-done:
				// process the remaining items in the last batch
				// if the context hasn't been cancelled
				if ctx.Err() == nil {
					addToBatch(nil)
				}
				return
			case g := <-ch:
				if g == nil {
					continue
				}
				if countOnly {
					// don't call SetTotal() yet -- wait until we're done counting,
					// so progress bars don't think we are done with the estimate
					atomic.AddInt64(p.estimatedCount, int64(g.Size()))
					continue
				}
				if po.Interactive != nil {
					if err := p.interactiveGraph(ctx, g, po.Interactive); err != nil {
						p.log.Error("sending interactive graph", zap.Error(err))
					}
					continue
				}
				addToBatch(g)
			}
		}
	}()

	return wg, ch
}

func (p *processor) interactiveGraph(ctx context.Context, root *Graph, opts *InteractiveImport) error {
	p.assignGraphIDs(root)

	if err := p.saveInteractiveGraphFromRootNode(root); err != nil {
		return err
	}

	// download the data from the graph in the background while we present the initial structure to the user
	if err := p.downloadGraphDataFiles(ctx, root, opts); err != nil {
		return err
	}

	p.log.Info("graph ready", zap.String("graph_id", root.ProcessingID))

	opts.Graphs <- &InteractiveGraph{
		Graph:         root,
		DataFileReady: make(chan struct{}),
	}

	return errors.New("TODO: WIP")
}

func (p *processor) saveInteractiveGraphFromRootNode(rootNode *Graph) error {
	graphPath := p.tempGraphFolder()
	if err := os.MkdirAll(graphPath, 0700); err != nil {
		return err
	}
	file, err := os.Create(filepath.Join(graphPath, "root.graph"))
	if err != nil {
		return err
	}
	defer file.Close()
	if err := json.NewEncoder(file).Encode(rootNode); err != nil {
		return err
	}
	return file.Sync()
}

func (p *processor) assignGraphIDs(g *Graph) {
	if g == nil {
		return
	}
	if g.ProcessingID == "" {
		g.ProcessingID = uuid.New().String()
	}
	for _, edge := range g.Edges {
		p.assignGraphIDs(edge.From)
		p.assignGraphIDs(edge.To)
	}
}

//nolint:unparam // TODO: file bug; opts is definitely used!
func (p *processor) downloadGraphDataFiles(ctx context.Context, g *Graph, opts *InteractiveImport) error {
	if g == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if (g.Item != nil && g.Item.Content.Data != nil) ||
		(g.Entity != nil && g.Entity.NewPicture != nil) {
		go func() {
			// TODO: Use CoW (write to a .tmp or .dl file first, then rename when finished, so we can know if it is complete)
			file, err := p.openInteractiveGraphDataFile(g)
			if err != nil {
				p.log.Error("opening graph data file", zap.Error(err))
				return
			}
			defer file.Close()

			// open the reader for either the item data or the entity picture
			var dataReader io.ReadCloser
			if g.Item != nil && g.Item.Content.Data != nil {
				dataReader, err = g.Item.Content.Data(ctx)
			} else if g.Entity != nil && g.Entity.NewPicture != nil {
				dataReader, err = g.Entity.NewPicture(ctx)
			}
			if err != nil {
				p.log.Error("opening data reader from graph", zap.Error(err))
				return
			}
			defer dataReader.Close()

			// now copy the data to the file
			if _, err := io.Copy(file, dataReader); err != nil {
				p.log.Error("copying data to temporary file", zap.Error(err))
				return
			}
			if err := file.Sync(); err != nil {
				p.log.Error("syncing data file", zap.Error(err))
			}
		}()
	}
	if g.Item != nil && g.Item.Owner.NewPicture != nil {
		// TODO: download the item owner's profile picture too, if available (though I don't know of anywhere this happens yet)
	}
	for _, edge := range g.Edges {
		if err := p.downloadGraphDataFiles(ctx, edge.From, opts); err != nil {
			return err
		}
		if err := p.downloadGraphDataFiles(ctx, edge.To, opts); err != nil {
			return err
		}
	}
	return nil
}

func (p *processor) openInteractiveGraphDataFile(g *Graph) (*os.File, error) {
	// We store interactive graph data files, temporarily while the user is
	// interacting with the graph, somewhat deep in the system temp folder.
	// It's in a system temp folder because import jobs are not typically
	// portable; especially starting on one system and continuing on another,
	// though I guess we could simply change the path to be something within
	// the timeline if desired. Still, this seems more proper at least for now.
	tmpFilePath := filepath.Join(p.tempGraphFolder(), g.ProcessingID+".graph.data")

	// ensure folder tree exists or we're gonna have a bad time
	if err := os.MkdirAll(filepath.Dir(tmpFilePath), 0700); err != nil {
		return nil, err
	}

	return os.Create(tmpFilePath)
}

func (p *processor) tempGraphFolder() string {
	return filepath.Join(
		os.TempDir(),
		"timelinize",
		fmt.Sprintf("job-%d", p.ij.job.ID()))
}

func (p *processor) pipeline(ctx context.Context, batch []*Graph) error {
	// During large imports, I've found that running ANALYZE every so often
	// can be helpful for improving performance, since an import is much more
	// than just an INSERT, there's lots of SELECTs along the way that use
	// indexes. For example, in my tests importing about a quarter million
	// messages (relation-heavy, since they are sent to an attribute), which
	// I repeated twice, it would take 30 minutes to import without ANALYZE.
	// But when running ANALYZE every so often, it only took 23 minutes.
	p.rootGraphCount += len(batch)
	if p.rootGraphCount%15000 < len(batch) {
		p.tl.optimizeDB(p.log.Named("optimizer"))
	}

	err := p.phase1(ctx, batch)
	if err != nil {
		return err
	}
	// TODO: We don't need to do phase2 or phase3 if there are no data files in the graph.
	// But since graphs can have edges, we would need to carry that information through
	// the recursive calls to processing the graph in phase1. This is doable, but it adds
	// an extra parameter or return value. Phases 2 and 3 do make some allocations even if
	// there aren't any data files, but I'd want to dig deeper (likely with a profile) to
	// determine if avoiding these phases entirely is worth the effort.
	// (Phase 3 has logging used for live updates by the frontend)
	if err := p.phase2(ctx, batch); err != nil {
		return err
	}
	if err := p.phase3(ctx, batch); err != nil {
		return err
	}
	return nil
}

// phase1 inserts items into the database and preps data files for writing.
func (p *processor) phase1(ctx context.Context, batch []*Graph) error {
	// TODO: maybe if we first go through the batch in a readlock, we can determine what are
	// duplicates, before acquiring a write lock, and that could help for faster resumption
	// (especially if we have even more workers)
	p.tl.dbMu.Lock()
	defer p.tl.dbMu.Unlock()

	tx, err := p.tl.db.Begin()
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

// phase2 downloads data files. Significantly, there is no lock on the DB in this phase,
// so it can be run in parallel.
func (p *processor) phase2(ctx context.Context, batch []*Graph) error {
	var wg sync.WaitGroup
	for _, g := range batch {
		if g.err != nil {
			continue
		}
		wg.Add(1)
		go func(g *Graph) {
			defer wg.Done()
			if err := p.downloadDataFilesInGraph(ctx, g); err != nil {
				p.log.Error("downloading data files in graph", zap.Error(err))
				g.err = err
			}
		}(g)
	}
	wg.Wait()
	return nil
}

// phase3 updates the DB with info about the data files that were downloaded in phase 2.
func (p *processor) phase3(ctx context.Context, batch []*Graph) error {
	p.tl.dbMu.Lock()
	defer p.tl.dbMu.Unlock()

	tx, err := p.tl.db.Begin()
	if err != nil {
		return fmt.Errorf("beginning transaction for batch phase 3: %w", err)
	}
	defer tx.Rollback()

	for _, g := range batch {
		if g.err != nil {
			continue
		}
		if err := p.finishProcessingDataFiles(ctx, tx, g); err != nil {
			p.log.Error("finalizing data files in graph", zap.Error(err))
			g.err = err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction for batch phase 3: %w", err)
	}

	return nil
}

func (p *processor) downloadDataFilesInGraph(ctx context.Context, g *Graph) error {
	if g == nil {
		return nil
	}

	// download main item's data file (root node of graph), only if there is one
	if g.Item != nil && g.Item.dataFileIn != nil && g.Item.dataFileOut != nil {
		if err := p.downloadDataFile(ctx, g.Item); err != nil {
			return err
		}
	}

	// traverse graph and download their data files
	for _, edge := range g.Edges {
		if err := p.downloadDataFilesInGraph(ctx, edge.From); err != nil {
			return err
		}
		if err := p.downloadDataFilesInGraph(ctx, edge.To); err != nil {
			return err
		}
	}

	return nil
}

func (p *processor) finishProcessingDataFiles(ctx context.Context, tx *sql.Tx, g *Graph) error {
	if g == nil {
		return nil
	}

	// this whole big thing is one huge log so the UI can stream
	// a sample of live import data
	defer func() {
		graphType := "item"
		if g.Entity != nil {
			graphType = "entity"
		}
		l := p.log.With(
			zap.String("graph", fmt.Sprintf("%p", g)),
			zap.String("type", graphType),
			zap.Int64("row_id", g.rowID.id()),
			zap.Int64("new_entities", atomic.LoadInt64(p.ij.newEntityCount)),
			zap.Int64("new_items", atomic.LoadInt64(p.ij.newItemCount)),
			zap.Int64("updated_items", atomic.LoadInt64(p.ij.updatedItemCount)),
			zap.Int64("skipped_items", atomic.LoadInt64(p.ij.skippedItemCount)),
			zap.Int64("total_items", atomic.LoadInt64(p.ij.itemCount)),
		)
		if g.Item != nil && !g.Item.Timestamp.IsZero() {
			l = l.With(zap.Time("item_timestamp", g.Item.Timestamp))
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
		if options, obfuscate := p.tl.obfuscationMode(); obfuscate {
			// this kind of nukes the actual values from this point forward,
			// but we're done with them now, so it should be okay
			if g.Item != nil {
				g.Item.row.Anonymize(options)
				g.Item.Owner.Anonymize()
			}
			if g.Entity != nil {
				g.Entity.ID = g.rowID.id()
				g.Entity.Anonymize()
			}
		}
		if g.Item != nil {
			size := g.Item.dataFileSize
			if g.Item.row.DataText != nil {
				size = int64(len(*g.Item.row.DataText))
			}
			preview := g.Item.row.DataText
			const maxPreviewLen = 30
			if preview != nil && len(*preview) > maxPreviewLen {
				shortPreview := (*preview)[:maxPreviewLen]
				preview = &shortPreview
			}
			l = l.With(
				zap.String("status", string(g.Item.row.howStored)),
				zap.String("classification", g.Item.Classification.Name),
				zap.Stringp("preview", preview),
				zap.Stringp("filename", g.Item.row.Filename),
				zap.Int64("size", size),
				zap.Float64p("lat", g.Item.row.Latitude),
				zap.Float64p("lon", g.Item.row.Longitude),
				zap.String("media_type", g.Item.Content.MediaType),
				entityAttr(g.Item.Owner))
		}
		if g.Entity != nil {
			l = l.With(entityAttr(*g.Entity))
		}
		l.Info("finished graph")
	}()

	if err := p.finishDataFileProcessing(ctx, tx, g.Item); err != nil {
		return err
	}
	for _, edge := range g.Edges {
		if err := p.finishProcessingDataFiles(ctx, tx, edge.From); err != nil {
			return err
		}
		if err := p.finishProcessingDataFiles(ctx, tx, edge.To); err != nil {
			return err
		}
	}

	return nil
}

func (p *processor) processGraph(ctx context.Context, tx *sql.Tx, ig *Graph) error {
	if ig == nil {
		return nil
	}

	// validate node type
	if ig.Item != nil && ig.Entity != nil {
		return fmt.Errorf("ambiguous node in graph is both an item and entity node (item_graph=%p)", ig)
	}

	// process root node
	switch {
	case ig.Entity != nil:
		var err error
		ig.rowID, err = p.processEntity(ctx, tx, *ig.Entity)
		if err != nil {
			return fmt.Errorf("processing entity node: %w", err)
		}
	case ig.Item != nil:
		var err error
		ig.rowID, err = p.processItem(ctx, tx, ig.Item)
		if err != nil {
			return fmt.Errorf("processing item node: %w", err)
		}
	}

	// process connected nodes
	for _, r := range ig.Edges {
		err := p.processRelationship(ctx, tx, r, ig)
		if err != nil {
			p.log.Error("processing relationship",
				zap.Int64("item_or_attribute_row_id", ig.rowID.id()),
				zap.Error(err))
		}
	}

	return nil
}

func (p *processor) processItem(ctx context.Context, tx *sql.Tx, it *Item) (latentID, error) {
	// skip item if outside of timeframe (data source should do this for us, but
	// ultimately we should enforce it: it just means the data source is being
	// less efficient than it could be)
	// TODO: also consider Timespan
	if !it.Timestamp.IsZero() {
		if !p.ij.ProcessingOptions.Timeframe.Contains(it.Timestamp) {
			p.log.Warn("ignoring item outside of designated timeframe (data source should not send this item; it is probably being less efficient than it could be)",
				zap.String("item_id", it.ID),
				zap.Timep("tf_since", p.ij.ProcessingOptions.Timeframe.Since),
				zap.Timep("tf_until", p.ij.ProcessingOptions.Timeframe.Until),
				zap.Time("item_timestamp", it.Timestamp),
			)
			return latentID{}, errors.New("item is outside of designated timeframe")
		}

		// end time must come after start time
		if !it.Timespan.IsZero() && !it.Timespan.After(it.Timestamp) {
			return latentID{}, fmt.Errorf("item's ending timespan is not after its starting timestamp (item_id=%s timestamp=%s timespan=%s)",
				it.ID, it.Timestamp, it.Timespan)
		}
	}

	itemRowID, err := p.storeItem(ctx, tx, it)
	if err != nil {
		return latentID{itemID: itemRowID}, err
	}

	return latentID{itemID: itemRowID}, nil
}

// TODO: godoc about return value of 0, nil
func (p *processor) storeItem(ctx context.Context, tx *sql.Tx, it *Item) (int64, error) {
	// keep count of number of items processed, mainly for logging
	defer atomic.AddInt64(p.ij.itemCount, 1)

	// obtain a handle on the item data (if any), and determine whether
	// it'll be stored in the database or on disk
	var processDataFile bool // if true, we'll be storing the data as a file on disk, not in the DB

	if it.Content.Data != nil {
		rc, err := it.Content.Data(ctx)
		if err != nil {
			return 0, fmt.Errorf("getting item's data stream: %w (item_id=%s)", err, it.ID)
		}
		if rc != nil {
			it.dataFileIn = rc
			defer func() {
				if !processDataFile {
					it.dataFileIn.Close()
					it.dataFileIn = nil
				}
			}()

			// if Content-Type is empty, try to detect it, first by sniffing the start
			// of data (as that is more reliable, in theory), then by looking at the
			// file extension if needed (but this is less reliable in theory)
			if it.Content.MediaType == "" {
				fileReader := bufio.NewReader(it.dataFileIn)

				// not really concerned with errors here; we don't need the max number of bytes
				// and if it fails to read later, we'll deal with the error then
				const bytesNeededToSniff = 512
				peekedBytes, _ := fileReader.Peek(bytesNeededToSniff)

				detectContentType(peekedBytes, it)

				// since peeking reads from the underlying reader, make sure to read from
				// the buffered reader when we save the file
				it.dataFileIn = io.NopCloser(fileReader)

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
			}

			if it.Content.isPlainTextOrMarkdown() {
				// store plain text in database unless it's too big to fit comfortably;
				// read as much as we would feel good about storing in the DB, and if we
				// fill that buffer, then it's probably big enough to go on disk
				//
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

				n, err := io.ReadFull(it.dataFileIn, buf)
				if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
					return 0, fmt.Errorf("buffering item's data stream to peek size: %w", err)
				}
				if n == len(buf) {
					// content is at least as large as our buffer, so it probably belongs on disk;
					// recover the bytes we already buffered when we go to write the file
					processDataFile = true
					it.dataFileIn = io.NopCloser(io.MultiReader(bytes.NewReader(buf), it.dataFileIn))
				} else if n > 0 {
					// NOTE: We trim leading/trailing spaces for this because it can be hard
					// for some data sources to strip them, and I don't think we need them,
					// especially for short text content stored in the DB
					dataTextStr := string(buf[:n])
					dataTextStr = strings.TrimSpace(dataTextStr)
					it.dataText = &dataTextStr
				}
			} else {
				processDataFile = true
			}
		}
	}

	// at this point, we have the text data, or a handle to the
	// file data, but we won't download the full file until later;
	// first we need to do some more preparation and insert its
	// entry into the DB.

	// prepare for DB queries to see if we have this same item already
	// in some form or another
	var dsName *string
	if p.ds.Name != "" {
		dsName = &p.ds.Name
	}
	it.makeIDHash(dsName)
	it.makeContentHash()

	// the user's update policies may be overridden on a per-item basis depending on
	// what makes the most sense, like if an item was found in the DB with an original
	// ID but has no content, from the same import especially, always update the row,
	// because it just means the data source gave us the item in two (or more) parts
	var updateOverrides map[string]fieldUpdatePolicy

	// if the item is already in our DB, load it
	ir, err := p.tl.loadItemRow(ctx, tx, 0, it, dsName, p.ij.ProcessingOptions.ItemUniqueConstraints, true)
	if err != nil {
		return 0, fmt.Errorf("looking up item in database: %w", err)
	}
	if ir.ID > 0 {
		// found it in our DB; skip it?
		var reprocessItem, reprocessDataFile bool
		reprocessItem, reprocessDataFile, updateOverrides = p.shouldProcessExistingItem(it, ir, processDataFile)
		if !reprocessItem {
			// don't confuse phase 2 which downloads data files, by setting
			// a reader (above) but not a writer (below), so make sure the
			// dataFileIn gets closed and nilified
			processDataFile = false

			atomic.AddInt64(p.ij.skippedItemCount, 1)
			p.log.Debug("skipping processing of existing item",
				zap.Int64("row_id", ir.ID),
				zap.String("filename", it.Content.Filename),
				zap.String("item_original_id", it.ID))
			ir.howStored = itemSkipped
			it.row = ir
			return ir.ID, nil
		}
		processDataFile = reprocessDataFile

		// if we are in fact processing this data file, move any old one out of the way temporarily
		// as a safe measure, and also because our filename-generator will not allow a file to be
		// overwritten, but we want to replace the existing file in this case...
		if processDataFile && ir.DataFile != nil {
			origFile := p.tl.FullPath(*ir.DataFile)
			bakFile := p.tl.FullPath(*ir.DataFile + ".bak")
			err = os.Rename(origFile, bakFile)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return 0, fmt.Errorf("temporarily moving data file: %w", err)
			}

			// if this function returns with an error,
			// restore the original file in case it was
			// partially written or something; otherwise
			// delete the old file altogether
			defer func() {
				if err == nil {
					err := os.Remove(bakFile)
					if err != nil && !errors.Is(err, fs.ErrNotExist) {
						p.log.Error("deleting data file backup",
							zap.Error(err),
							zap.String("backup_file", bakFile))
					}
				} else {
					err := os.Rename(bakFile, origFile)
					if err != nil && !errors.Is(err, fs.ErrNotExist) {
						p.log.Error("restoring original data file from backup",
							zap.Error(err),
							zap.String("backup_file", bakFile),
							zap.String("original_file", origFile))
					}
				}
			}()
		}
	}

	// get the filename for the data file if we are processing it
	if processDataFile {
		it.dataFileOut, it.dataFileName, err = p.tl.openUniqueCanonicalItemDataFile(tx, p.log, it, p.ds.Name)
		if err != nil {
			return 0, fmt.Errorf("opening output data file: %w", err)
		}
	}

	// make a copy of this 'cause we might use it later to clean up a data file if we ended up setting it to NULL
	startingDataFile := ir.DataFile

	err = p.fillItemRow(ctx, tx, &ir, it)
	if err != nil {
		return 0, fmt.Errorf("assembling item for storage: %w", err)
	}

	// run the database query to insert or update the item (and clean up data file if it was changed to NULL),
	// but carefully so as to not allow zeroing out an item; for example, if a related item is provided only
	// with its original ID, we can still link a relationship, but if the incoming item has no content we
	// should not zero out any existing version of the item in the database; the intent by the data source is
	// to merely link the item by ID (or create a placeholder item), not zero it out!
	ir.ID, ir.howStored, err = p.insertOrUpdateItem(ctx, tx, ir, startingDataFile, it.HasContent(), updateOverrides)
	if err != nil {
		return 0, fmt.Errorf("storing item in database: %w (row_id=%d item_id=%v)", err, ir.ID, ir.OriginalID)
	}

	it.row = ir

	return ir.ID, nil
}

type itemStoreResult string

const (
	itemInserted itemStoreResult = "inserted"
	itemSkipped  itemStoreResult = "skipped"
	itemUpdated  itemStoreResult = "updated"
)

func (p *processor) processRelationship(ctx context.Context, tx *sql.Tx, r Relationship, ig *Graph) error {
	// both sides can be set, or if this graph has a node then just
	// one needs to be set; but at least one of these needs always
	// to be set since we need a node on both sides of an edge
	if r.From == nil && r.To == nil {
		return fmt.Errorf("invalid edge: must have node on both sides: %+v", r)
	}

	rawRel := rawRelationship{Relation: r.Relation}

	if r.Value != "" {
		rawRel.value = &r.Value
	}
	if r.Start != nil {
		unixSec := r.Start.Unix()
		rawRel.start = &unixSec
	}
	if r.End != nil {
		unixSec := r.End.Unix()
		rawRel.end = &unixSec
	}

	r.Metadata.Clean()
	if len(r.Metadata) > 0 {
		metaJSON, err := json.Marshal(r.Metadata)
		if err != nil {
			return fmt.Errorf("encoding relationship metadata: %w", err)
		}
		rawRel.metadata = metaJSON
	}

	// if the relationship explicitly has a "from" node set, use that;
	// otherwise, assume this node is the "from" side
	if err := p.linkRelation(ctx, ig, tx, r, &rawRel, relationFrom); err != nil {
		return err
	}

	// if the relationship explicitly has a "to" node set, use that;
	// otherwise, assume this node is the "to" side
	if err := p.linkRelation(ctx, ig, tx, r, &rawRel, relationTo); err != nil {
		return err
	}

	err := p.tl.storeRelationship(ctx, tx, rawRel)
	if err != nil {
		return fmt.Errorf("storing relationship: %w", err)
	}

	return nil
}

// I know this function is hard to read, but I initially had this inline above, and the linter complained it was duplicated code,
// despite the whole "from-to" parts being different; it's just annoying enough to have to change what you are assigning to that
// I didn't want to refactor this, but I did it anyway, I hope the linter is happy.
func (p *processor) linkRelation(ctx context.Context, ig *Graph, tx *sql.Tx, r Relationship, rawRel *rawRelationship, fromOrTo string) error {
	otherGraph := r.From
	if fromOrTo == relationTo {
		otherGraph = r.To
	}

	if otherGraph != nil {
		err := p.processGraph(ctx, tx, otherGraph)
		if err != nil {
			return fmt.Errorf("%s node: %w", fromOrTo, err)
		}
		if otherGraph.Item != nil {
			if fromOrTo == relationFrom {
				rawRel.fromItemID = &otherGraph.rowID.itemID
			} else if fromOrTo == relationTo {
				rawRel.toItemID = &otherGraph.rowID.itemID
			}
		} else if otherGraph.Entity != nil {
			attrID, err := otherGraph.rowID.identifyingAttributeID(ctx, tx)
			if err != nil {
				return fmt.Errorf("getting identifying attribute ID for connected entity (on %s side): %w", fromOrTo, err)
			}
			if fromOrTo == relationFrom {
				rawRel.fromAttributeID = &attrID
			} else if fromOrTo == relationTo {
				rawRel.toAttributeID = &attrID
			}
		}
	} else {
		switch {
		case ig.Item != nil:
			if fromOrTo == relationFrom {
				rawRel.fromItemID = &ig.rowID.itemID
			} else if fromOrTo == relationTo {
				rawRel.toItemID = &ig.rowID.itemID
			}
		case ig.Entity != nil:
			attrID, err := ig.rowID.identifyingAttributeID(ctx, tx)
			if err != nil {
				return fmt.Errorf("getting identifying attribute ID for graph entity (on %s side): %w", fromOrTo, err)
			}
			if fromOrTo == relationFrom {
				rawRel.fromAttributeID = &attrID
			} else if fromOrTo == relationTo {
				rawRel.toAttributeID = &attrID
			}
		default:
			return fmt.Errorf("incomplete relationship: no '%s' node available: %+v (item_graph=%p %+v)", fromOrTo, r, ig, ig)
		}
	}

	return nil
}

const (
	relationFrom = "from"
	relationTo   = "to"
)

func (tl *Timeline) cleanDataFile(tx *sql.Tx, dataFilePath string) error {
	var count int
	err := tx.QueryRow(`SELECT count() FROM items WHERE data_file=? LIMIT 1`, dataFilePath).Scan(&count)
	if err != nil {
		return fmt.Errorf("querying to check if data file is unused: %w", err)
	}
	if count > 0 {
		return nil
	}
	if err := os.Remove(tl.FullPath(dataFilePath)); err != nil {
		return fmt.Errorf("deleting unused data file: %w", err)
	}
	return nil
}

func (p *processor) integrityCheck(dbItem ItemRow) error {
	if p.ij.ProcessingOptions.Integrity || dbItem.DataFile == nil {
		return nil
	}

	// expected hash must be set; if missing, data file was not completely downloaded last time
	if dbItem.DataHash == nil {
		return errors.New("checksum missing")
	}

	// file must open successfully
	datafile, err := os.Open(p.tl.FullPath(*dbItem.DataFile))
	if err != nil {
		return fmt.Errorf("opening existing data file: %w", err)
	}
	defer datafile.Close()

	// file must be read successfully
	h := newHash()
	_, err = io.Copy(h, datafile)
	if err != nil {
		return fmt.Errorf("reading existing data file: %w", err)
	}

	// file checksum must be identical
	if itemHash := h.Sum(nil); !bytes.Equal(itemHash, dbItem.DataHash) {
		return fmt.Errorf("checksum mismatch (expected=%x actual=%x)", dbItem.DataHash, itemHash)
	}

	return nil
}

// shouldProcessExistingItem determines whether an item should be processed given the existing
// item in the database. It returns true for item if the whole item should be reprocessed, and
// it returns true for dataFile if at least the dataFile should be processed.
// Valid return values: false false, true false, true true.
func (p *processor) shouldProcessExistingItem(it *Item, dbItem ItemRow, dataFileIncoming bool) (item bool, dataFile bool, updateOverrides map[string]fieldUpdatePolicy) {
	// An item may be referenced by the data source more than once, and thus the same item may be processed concurrently;
	// when this happens, multiple data files are created in the repo: the first will presumably have the original filename,
	// while the later ones will have random strings appended. The problem is if a later one end up finishing first, the
	// mutated filename will persist instead of the original... this is not strictly bad, but is annoying since it doesn't
	// need to be mutated - also, it implies processing the data file multiple times, unnecessarily! Ideally we only process
	// an item's data file once per import.
	//
	// There are two primary races: both items are in phase1 (they create an empty file, then in lock-step, they insert a
	// row into the DB), or one is already in or past phase2 while the other is in phase1. The former race is hard to
	// detect -- more on that later. The latter, we can do something about right now. If the existing row is from this same
	// import, and the data file exists, and the data hash is nil, we can presume that the item is still being processed and
	// thus skip processing of our data file entirely. Is this a perfect check? No, because without a data file, the existing
	// row was likely only detected by way of other columns (timestamp, filename?)  which may be too strict or too loose for
	// a perfect match. Also, if the first data file did actually have an error, we wouldn't necessarily know; then again, if
	// it did, there's little chance that our processing would fare any better since we're operating in the same import on the
	// same data source...
	//
	// To detect the first race, the only idea I have right now is to add a scan at the end of each import when things have
	// settled, to compare the filename column with the base of the filename in the data_file column; if they don't match
	// up, and the filename using the un-mutated filename is available, then we could rename it and update data_file.
	// (Example: filename is IMG_1234.HEIC, but data_file ends in IMG_1234__abcd.HEIC. We could rename to IMG_1234.HEIC
	// if that filename is available in the repo and update the DB row to match.)
	// TODO: Try to figure this out to make it correct. We might need a process-wide map mutex or something to avoid hacky solutions?
	if dbItem.JobID != nil && *dbItem.JobID == p.ij.job.id &&
		dbItem.DataHash == nil &&
		dbItem.DataFile != nil && FileExists(p.tl.FullPath(*dbItem.DataFile)) {
		p.log.Debug("processing existing item, but skipping data file because it is already being processed by this import",
			zap.Int64("item_row_id", dbItem.ID),
			zap.String("filename", it.Content.Filename),
			zap.String("item_original_id", it.ID),
			zap.String("data_file_path", p.tl.FullPath(*dbItem.DataFile)))
		return true, false, nil
	}

	// within the same import, reprocess an item if the data source gives us the item in pieces;
	// for example, at first we might only get just enough of the item to satisfy a relationship
	// (like an ID), then later as it iterates it finds that related item and fills out the rest
	// of the item's information -- so if our current item row is missing information, we can at
	// least safely add new info I think
	if dbItem.JobID != nil && *dbItem.JobID == p.ij.job.id {
		updateOverrides = make(map[string]fieldUpdatePolicy)

		// if there's an incoming data file and we don't have one, then update
		if dbItem.DataText == nil && dbItem.DataFile == nil && (it.dataText != nil || it.Content.Data != nil) {
			dataFile = true
			updateOverrides["data"] = updatePolicyPreferIncoming
		}

		// if incoming item is linked to an owner attribute, but the one in the DB is null,
		// prefer the incoming attribute/owner
		if dbItem.AttributeID == nil {
			var hasIDAttr bool
			for _, attr := range it.Owner.Attributes {
				if attr.Identity {
					hasIDAttr = true
					break
				}
			}
			if hasIDAttr {
				updateOverrides["attribute_id"] = updatePolicyPreferIncoming
			}
		}

		// reprocess the item row if there's new data to be added
		if (dbItem.Latitude == nil && it.Location.Latitude != nil) ||
			(dbItem.Longitude == nil && it.Location.Longitude != nil) ||
			(dbItem.Altitude == nil && it.Location.Altitude != nil) {
			updateOverrides["location"] = updatePolicyPreferIncoming
		}
		if (dbItem.Timestamp == nil || dbItem.TimeOffset == nil) && !it.Timestamp.IsZero() {
			updateOverrides["timestamp"] = updatePolicyPreferIncoming
		}
		if dbItem.Timespan == nil && !it.Timespan.IsZero() {
			updateOverrides["timespan"] = updatePolicyPreferIncoming
		}
		if dbItem.Timeframe == nil && !it.Timeframe.IsZero() {
			updateOverrides["timeframe"] = updatePolicyPreferIncoming
		}
		if dbItem.Filename == nil && it.Content.Filename != "" {
			updateOverrides["filename"] = updatePolicyPreferIncoming
		}
		if dbItem.Classification == nil && it.Classification.Name != "" {
			updateOverrides["classification_id"] = updatePolicyPreferIncoming
		}
		if dbItem.OriginalLocation == nil && it.OriginalLocation != "" {
			updateOverrides["original_location"] = updatePolicyPreferIncoming
		}
		if dbItem.OriginalID == nil && it.ID != "" {
			updateOverrides["original_id"] = updatePolicyPreferIncoming
		}

		// metadata is a little tricky, especially to decide efficiently, unless
		// the incoming item obiously has some and the existing one does not...
		if len(it.Metadata) > 0 && len(dbItem.Metadata) == 0 {
			updateOverrides["metadata"] = updatePolicyOverwriteExisting
		} else if len(it.Metadata) > 0 {
			var decoded Metadata
			err := json.Unmarshal(dbItem.Metadata, &decoded)
			if err == nil {
				for k, incomingVal := range it.Metadata {
					// this is not perfect, since the value could be any type, but string seems most common
					if incomingVal == nil || incomingVal == "" {
						continue
					}
					if _, ok := decoded[k]; !ok {
						// incoming item has metadata field that DB does not; we should strive to be additive
						// so I guess we take this as a hint to replace the existing... but obviously, there
						// could be fields that DB has that the incoming item does not...
						// TODO: we may need a way to apply metadata merge policy ?
						updateOverrides["metadata"] = updatePolicyOverwriteExisting
						break
					}
				}
			}
		}

		item = len(updateOverrides) > 0

		if !item {
			p.log.Debug("skipping processing of existing item because it was already processed in this import and there are no update overrides",
				zap.Int64("item_row_id", dbItem.ID),
				zap.String("filename", it.Content.Filename),
				zap.String("item_original_id", it.ID))
		}

		return
	}

	// the presence of a retrieval key implies that the data source may not be able to fully
	// provide the whole item in one import, so in that case, always reprocess, but make sure
	// to account for the update overrides specified by the data source
	if len(it.Retrieval.key) > 0 {
		updateOverrides = make(map[string]fieldUpdatePolicy)
		for _, field := range it.Retrieval.PreferFields {
			updateOverrides[field] = updatePolicyOverwriteExisting
		}
		item, dataFile = true, true
		return
	}

	// perform integrity check (no-op if not enabled) and log if it fails;
	// we'll decide what to do about it next; but writing the logs can be
	// important even if no data file is incoming
	integrityCheckErr := p.integrityCheck(dbItem)
	if integrityCheckErr != nil {
		// this sometimes happens when an item/file is referenced more than once and
		// is currently being processed, and has been inserted into the DB, but the
		// data file is still downloading while we get another reference to it; in
		// this case, the integrity check does truthfully fail, it simply means it
		// might be processed twice (oh well)
		p.log.Warn("integrity check failed",
			zap.Int64("item_row_id", dbItem.ID),
			zap.Stringp("data_file", dbItem.DataFile),
			zap.Error(integrityCheckErr))
	}

	// if modified manually, do not overwrite changes unless specifically enabled
	if dbItem.Modified != nil && !p.ij.ProcessingOptions.OverwriteModifications {
		p.log.Debug("skipping processing of existing item because it has been manually modified within the repo (enable modification overwrites to override)",
			zap.Int64("item_row_id", dbItem.ID),
			zap.String("filename", it.Content.Filename),
			zap.String("item_original_id", it.ID))
		return false, false, nil
	}

	// if the item data is explicitly configured to overwrite existing, then it
	// should always be reprocessed, even if NULL
	dataUpdatePolicy, dataUpdateEnabled := p.ij.ProcessingOptions.ItemFieldUpdates["data"]
	if dataUpdatePolicy == updatePolicyOverwriteExisting {
		return true, true, nil
	}

	if dataFileIncoming {
		// if a data file is incoming and integrity check failed, always reprocess regardless of
		// specific update policy for this field (because integrity check is explicitly opt-in too)
		if integrityCheckErr != nil {
			return true, true, nil
		}

		// If the data_hash is missing (data file did not finish processing), and a data file is incoming,
		// we'll process it, but if the update policy for data_file is PreferExisting, it wouldn't actually
		// update because it "looks" like a data file already exists (it is non-NULL), since it doesn't also
		// look at data_hash. Thus, as a special case, if updating the data_file field is enabled at all,
		// we always process the data file if the hash is missing, regardless of the update policy.
		// We do this regardless of integrity checks because, in this case, there's no integrity to check,
		// even though the file is obviously missing and needs to be replaced.
		dataFileMissing := dbItem.DataFile != nil && dbItem.DataHash == nil
		if dataUpdateEnabled && dataFileMissing {
			return true, true, nil
		}

		// by this point, we know that if it has a data file, it has good integrity
		// (if integrity checks are enabled) and it was completely downloaded (hash
		// exists), so we should update it according to configured policy
		if dataUpdatePolicy == updatePolicyPreferExisting {
			// only update data file if there is NOT an existing one
			dataFile = dbItem.DataFile == nil
		} else if dataUpdatePolicy > 0 {
			// we know a data file is incoming, so any other non-zero update policy is good
			dataFile = true
		}

		// if we are supposed to process the data file, also process the item row
		item = dataFile

		// if we already know we are supposed to reprocess the item, might as well return
		if item {
			return
		}
	}

	// TODO: since we selected the existing item row on some designated fields, we know
	// those fields already equal the incoming item (depending on how NULL was treated),
	// so we could be smarter about our decision to reprocess if, for example, all the
	// fields to update -- together with their policies -- would mean that no values in
	// the DB would actually be changed

	// if the item in the DB is basically empty, go ahead and reprocess
	if !dbItem.hasContent() {
		item = true
		return
	}

	// finally, if the user has configured/enabled updates, reprocess the item
	item = len(p.ij.ProcessingOptions.ItemFieldUpdates) > 0

	return item || dataFile, dataFile, nil
}

func (p *processor) fillItemRow(ctx context.Context, tx *sql.Tx, ir *ItemRow, it *Item) error {
	// unpack the item's information into values to use in the row

	// insert and/or retrieve owner information
	rowID, err := p.processEntity(ctx, tx, it.Owner)
	if err != nil {
		return fmt.Errorf("getting person associated with item: %w", err)
	}

	// remove unnecessary entries first
	it.Metadata.Clean()

	// encode metadata as JSON
	var metadata json.RawMessage
	if len(it.Metadata) > 0 {
		metadata, err = json.Marshal(it.Metadata)
		if err != nil {
			return fmt.Errorf("encoding metadata as JSON: %w", err)
		}
	}

	// convert classification name to ID
	var clID int64
	if it.Classification.Name != "" {
		clID, err = p.tl.classificationNameToID(it.Classification.Name)
		if err != nil {
			return fmt.Errorf("unable to get classification ID: %w (classification=%+v)", err, it.Classification)
		}
	}

	// if this item has an owner entity, get the associated attribute ID
	var attrID int64
	if rowID.entityID > 0 {
		attrID, err = rowID.identifyingAttributeID(ctx, tx)
		if err != nil {
			return fmt.Errorf("getting identifying attribute row ID: %w", err)
		}
	}

	ir.DataSourceID = &p.dsRowID
	ir.DataSourceName = &p.ds.Name
	ir.JobID = &p.ij.job.id
	if attrID != 0 {
		ir.AttributeID = &attrID
	}
	if clID != 0 {
		ir.ClassificationID = &clID
	}
	if it.ID != "" {
		ir.OriginalID = &it.ID
	}
	if it.OriginalLocation != "" {
		ir.OriginalLocation = &it.OriginalLocation
	}
	if it.IntermediateLocation != "" {
		ir.IntermediateLocation = &it.IntermediateLocation
	}
	if it.Content.Filename != "" {
		ir.Filename = &it.Content.Filename
	}
	if !it.Timestamp.IsZero() {
		ir.Timestamp = &it.Timestamp
		_, offsetSec := it.Timestamp.Zone()
		if offsetSec != 0 {
			ir.TimeOffset = &offsetSec
		}
	}
	if !it.Timespan.IsZero() {
		ir.Timespan = &it.Timespan
	}
	if !it.Timeframe.IsZero() {
		ir.Timeframe = &it.Timeframe
	}
	if it.TimeUncertainty > 0 {
		uncert := int64(it.TimeUncertainty / time.Millisecond)
		ir.TimeUncertainty = &uncert
	} else if it.TimeUncertainty == -1 {
		// TODO: I forgot what this was all about... it's not even used? why would we set it to -1 and what is "General uncertainty" -- just that we have no clue?
		generalUncert := int64(it.TimeUncertainty)
		ir.TimeUncertainty = &generalUncert
	}
	if it.Content.MediaType != "" {
		ir.DataType = &it.Content.MediaType
	}
	ir.DataText = it.dataText
	if it.dataFileName != "" {
		// BIG TIME bug fix :)
		// When deduplicating data files, if this is not a copy of the dataFileName, then we end up not
		// updating values in the DB with the existing filename later on, because we end up changing
		// the value of it.dataFileName if it's a duplicate... but if ir.DataFile points to it, that also
		// ends up changing even though we expect that to remain the originally-planned filename so that
		// we can use it in a DB query to update the rows to point to the existing filename...
		df := it.dataFileName
		ir.DataFile = &df
	}
	ir.Metadata = metadata
	ir.Location = it.Location

	// enforce valid timestamp and timespan values
	if ir.Timespan != nil {
		if ir.Timestamp == nil {
			return fmt.Errorf("timespan cannot be set without timestamp (timestamp=%v timespan=%v)",
				ir.Timestamp, *ir.Timespan)
		}
		if ir.Timespan.Equal(*ir.Timestamp) || ir.Timespan.Before(*ir.Timestamp) {
			return fmt.Errorf("timespan must be after timestamp (timestamp=%v timespan=%v)",
				*ir.Timestamp, *ir.Timespan)
		}
	}

	// create the row hashes so we can prevent duplicating imported data later
	ir.OriginalIDHash = it.idHash
	ir.InitialContentHash = it.contentHash

	ir.RetrievalKey = it.Retrieval.key

	return nil
}

// loadItemRow loads an item from the DB that matches the input criteria. There are two primary modes.
// If rowID is > 0, then that solely is used to retrieve the item row.
// Otherwise, the rest of the parameters are used to look up the item: the properties of the item
// itself, together with the data source from which it comes; the Item must not be nil.
// The last parameter, uniqueConstraints, configures which properties/columns to select on to
// find the specific item. The most specific, exact matches should use all the available fields
// to match; if no columns are specified, then it is an error if the item does not have an
// original ID. If the original ID is specified, the sole criteria used to look up a unique item
// is the data source and the original ID.
// TODO: checkDeleted is more like "use hashes to retrieve rows for deduplication purposes"
func (tl *Timeline) loadItemRow(ctx context.Context, tx *sql.Tx, rowID int64, it *Item, dataSourceName *string, uniqueConstraints map[string]bool, checkDeleted bool) (ItemRow, error) {
	var sb strings.Builder

	sb.WriteString("SELECT ")
	sb.WriteString(itemDBColumns)
	sb.WriteString(" FROM extended_items AS items WHERE ")
	args := make([]any, 0, 1+len(uniqueConstraints)*2)

	if rowID != 0 {
		// select the row directly with its row ID
		sb.WriteString("id=?")
		args = append(args, rowID)
	} else {
		// select the row by the various properties of the item

		// Without a row ID, we first try matching on the data source + item original ID, if
		// provided. That is a very fast, reliable, and simple lookup. If it doesn't return
		// any results OR if no original ID was provided, we use the long-form query that
		// compares every configured field.

		if dataSourceName != nil && it.ID != "" {
			row := tx.QueryRow(`SELECT `+itemDBColumns+`
				FROM extended_items AS items
				WHERE data_source_name=? AND original_id=?
				LIMIT 1`, dataSourceName, &it.ID)
			ir, err := scanItemRow(row, nil)
			if err == nil {
				return ir, nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return ItemRow{}, fmt.Errorf("querying by original id: %w", err)
			}
		} else if len(uniqueConstraints) == 0 {
			// if no fields were specified (by mistake?), this could be problematic
			// as it would match any item with the same data source, I think
			return ItemRow{}, errors.New("missing unique constraints; at least 1 required when no original ID specified")
		}

		// check for identical item that may have been deleted; there are two "row hashes" we check:
		//
		// 1) the initial ID hash consists of data source and original ID - this is robust against
		//    edits on the original data source, assuming the ID is static.
		// 2) the initial content of the item (timestamp, together with data text/file or location).
		//
		// The first is only needed if the item has been deleted from our table (because if it hasn't
		// been, the query above should have returned a row). The second is useful if the item has
		// been either modified OR deleted in our table, since it tracks the content of the item as
		// it was when it was originally imported from the data source.
		if checkDeleted {
			sb.WriteString(`
				((deleted IS NOT NULL AND original_id_hash=?)
					OR (modified IS NOT NULL OR deleted IS NOT NULL) AND initial_content_hash=?)
				OR (`)
			args = append(args, it.idHash, it.contentHash)
		}

		// collection items are special cases; always ignore the user's unique constraint settings, since we will almost always
		// have to retrieve collections by their name alone (and of course, item class and data source have to match) -- their
		// name is their data and that's generally all we have to go on
		if it.Classification.Name == ClassCollection.Name {
			uniqueConstraints = map[string]bool{
				"classification_name": true,
				"data_source_name":    true,
				"data":                true,
			}
		}

		// iterate each field to be selected on to finish building WHERE clause
		firstIter := true
		for field, strictNull := range uniqueConstraints {
			if !firstIter {
				sb.WriteString(" AND ")
			}
			firstIter = false

			// match other fields, accounting for whether NULL should be compared like a value
			// (if "OR", either the input or the DB's value can be NULL;
			// if "AND", both the input and DB's value have to be NULL)
			op := "OR"
			if strictNull {
				op = "AND"
			}

			// TODO: should we take into account time_uncertainty and coordinate_uncertainty
			// and allow any value in that range to be a match?

			switch field {
			case "data": //nolint:goconst
				sb.WriteString("(data_text=? OR (data_text IS NULL ")
				sb.WriteString(op)
				sb.WriteString(" ? IS NULL)) AND (data_hash=? OR ? IS NULL)")
			case "location": //nolint:goconst
				sb.WriteString("(longitude=? OR (longitude IS NULL ")
				sb.WriteString(op)
				sb.WriteString(" ? IS NULL)) AND (latitude=? OR (latitude IS NULL ")
				sb.WriteString(op)
				sb.WriteString(" ? IS NULL)) AND (altitude=? OR (altitude IS NULL ")
				sb.WriteString(op)
				sb.WriteString(" ? IS NULL)) AND (coordinate_system=? OR (coordinate_system IS NULL ")
				sb.WriteString(op)
				sb.WriteString(" ? IS NULL))")
			default:
				sb.WriteRune('(')
				sb.WriteString(field)
				sb.WriteString("=? OR (")
				sb.WriteString(field)
				sb.WriteString(" IS NULL ")
				sb.WriteString(op)
				sb.WriteString(" ? IS NULL))")
			}

			switch field {
			case "data_source_name":
				args = append(args, dataSourceName, dataSourceName)
			case "classification_name":
				var className *string
				if it.Classification.Name != "" {
					className = &it.Classification.Name
				}
				args = append(args, className, className)
			case "original_location":
				var origLoc *string
				if it.OriginalLocation != "" {
					origLoc = &it.OriginalLocation
				}
				args = append(args, origLoc, origLoc)
			case "intermediate_location":
				var interLoc *string
				if it.IntermediateLocation != "" {
					interLoc = &it.IntermediateLocation
				}
				args = append(args, interLoc, interLoc)
			case "filename":
				var filename *string
				if it.Content.Filename != "" {
					filename = &it.Content.Filename
				}
				args = append(args, filename, filename)
			case "timestamp":
				timestamp := it.timestampUnix()
				args = append(args, timestamp, timestamp)
			case "timespan":
				timespan := it.timespanUnix()
				args = append(args, timespan, timespan)
			case "timeframe":
				timeframe := it.timeframeUnix()
				args = append(args, timeframe, timeframe)
			case "data":
				args = append(args,
					it.dataText, it.dataText,
					it.dataFileHash, it.dataFileHash)
			case "data_type", "data_text", "data_hash":
				return ItemRow{}, errors.New("cannot select on specific components of item data such as text or file hash; specify 'data' instead")
			case "location":
				args = append(args,
					it.Location.Longitude, it.Location.Longitude,
					it.Location.Latitude, it.Location.Latitude,
					it.Location.Altitude, it.Location.Altitude,
					it.Location.CoordinateSystem, it.Location.CoordinateSystem)
			case "longitude", "latitude", "altitude", "coordinate_system", "coordinate_uncertainty":
				// unlike the data fields, there's no good reason for this other than "the other way doesn't make sense and may be error-prone"
				return ItemRow{}, errors.New("cannot select on specific components of item location such as latitude or longitude: specify 'location' instead")
			default:
				return ItemRow{}, fmt.Errorf("item unique constraints configure unsupported/unrecognized field: %s", field)
			}
		}

		if checkDeleted {
			sb.WriteRune(')')
		}

		// also honor the retrieval key, if set, which allows an item to be pieced together
		// regardless of what values are in the row already... since the whole item may not
		// be known yet or some parts may be changing (for reasons known only to the data
		// source which we trust), we use the retrieval key as a globally unique key to
		// check for an existing item (even if only part of it is in the DB)
		if len(it.Retrieval.key) > 0 {
			sb.WriteString(" OR retrieval_key=?")
			args = append(args, it.Retrieval.key)
		}
	}

	sb.WriteString(" LIMIT 1")

	row := tx.QueryRowContext(ctx, sb.String(), args...)

	return scanItemRow(row, nil)
}

// insertOrUpdateItem inserts the fully-populated ir into the database (TODO: finish godoc)
func (p *processor) insertOrUpdateItem(ctx context.Context, tx *sql.Tx, ir ItemRow, startingDataFile *string, allowOverwrite bool, updateOverrides map[string]fieldUpdatePolicy) (int64, itemStoreResult, error) {
	// new item? insert it
	if ir.ID == 0 {
		var rowID int64

		err := tx.QueryRowContext(ctx,
			`INSERT INTO items
				(data_source_id, job_id, attribute_id, classification_id,
				original_id, original_location, intermediate_location, filename,
				timestamp, timespan, timeframe, time_offset, time_uncertainty,
				data_type, data_text, data_file, data_hash, metadata,
				longitude, latitude, altitude, coordinate_system, coordinate_uncertainty,
				note, starred, original_id_hash, initial_content_hash, retrieval_key)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			RETURNING id`,
			ir.DataSourceID, ir.JobID, ir.AttributeID, ir.ClassificationID,
			ir.OriginalID, ir.OriginalLocation, ir.IntermediateLocation, ir.Filename,
			ir.timestampUnix(), ir.timespanUnix(), ir.timeframeUnix(), ir.TimeOffset, ir.TimeUncertainty,
			ir.DataType, ir.DataText, ir.DataFile, ir.DataHash, string(ir.Metadata),
			ir.Location.Longitude, ir.Location.Latitude, ir.Location.Altitude,
			ir.Location.CoordinateSystem, ir.Location.CoordinateUncertainty,
			ir.Note, ir.Starred, ir.OriginalIDHash, ir.InitialContentHash, ir.RetrievalKey,
		).Scan(&rowID)

		atomic.AddInt64(p.ij.newItemCount, 1)

		return rowID, itemInserted, err
	}

	// existing item; update it

	// ...only if any fields are configured to be updated
	if len(p.ij.ProcessingOptions.ItemFieldUpdates) == 0 && len(updateOverrides) == 0 {
		return ir.ID, itemSkipped, nil
	}

	var sb strings.Builder
	var args []any
	var needsComma bool

	sb.WriteString(`UPDATE items SET `)

	// set the modified_job_id (the ID of the import that most recently modified the item) only if
	// it's not the original import, I think it makes sense to count the original import only once
	if ir.JobID != nil && *ir.JobID != p.ij.job.id {
		sb.WriteString(`modified_job_id=?`)
		args = append(args, p.ij.job.id)
		needsComma = true
	}

	appendToQuery := func(field string, policy fieldUpdatePolicy) {
		switch policy {
		case updatePolicyPreferExisting:
			if needsComma {
				sb.WriteString(", ")
			}
			sb.WriteString(field)
			sb.WriteString("=COALESCE(")
			sb.WriteString(field)
			sb.WriteString(", ?)")
		case updatePolicyOverwriteExisting:
			if allowOverwrite {
				if needsComma {
					sb.WriteString(", ")
				}
				sb.WriteString(field)
				sb.WriteString("=?")
				break
			}
			fallthrough
		case updatePolicyPreferIncoming:
			if needsComma {
				sb.WriteString(", ")
			}
			sb.WriteString(field)
			sb.WriteString("=COALESCE(?, ")
			sb.WriteString(field)
			sb.WriteRune(')')
		}
		needsComma = true
	}

	applyUpdatePolicy := func(field string, policy fieldUpdatePolicy) error {
		switch field {
		case "data":
			appendToQuery("data_type", policy)
			appendToQuery("data_text", policy)
			appendToQuery("data_file", policy)
			appendToQuery("data_hash", policy)
		case "location":
			appendToQuery("longitude", policy)
			appendToQuery("latitude", policy)
			appendToQuery("altitude", policy)
			appendToQuery("coordinate_system", policy)
			appendToQuery("coordinate_uncertainty", policy)
		default:
			appendToQuery(field, policy)
		}

		switch field {
		case "attribute_id":
			args = append(args, ir.AttributeID)
		case "classification_id":
			args = append(args, ir.ClassificationID)
		case "original_location":
			args = append(args, ir.OriginalLocation)
		case "intermediate_location":
			args = append(args, ir.IntermediateLocation)
		case "filename":
			args = append(args, ir.Filename)
		case "timestamp":
			args = append(args, ir.timestampUnix())
		case "timespan":
			args = append(args, ir.timespanUnix())
		case "timeframe":
			args = append(args, ir.timeframeUnix())
		case "time_offset":
			args = append(args, ir.TimeOffset)
		case "time_uncertainty":
			args = append(args, ir.TimeUncertainty)
		case "data":
			args = append(args, ir.DataType)
			args = append(args, ir.DataText)
			args = append(args, ir.DataFile)
			args = append(args, ir.DataHash)
		case "data_type", "data_text", "data_file", "data_hash":
			return errors.New("data components cannot be individually configured for updates; use 'data' as field name instead")
		case "metadata":
			args = append(args, string(ir.Metadata))
		case "location":
			args = append(args, ir.Longitude)
			args = append(args, ir.Latitude)
			args = append(args, ir.Altitude)
			args = append(args, ir.CoordinateSystem)
			args = append(args, ir.CoordinateUncertainty)
		case "longitude", "latitude", "altitude", "coordinate_system", "coordinate_uncertainty":
			// unlike the data fields, there's no good reason for this other than "individually doesn't make sense and may be tedious"
			return errors.New("location components cannot be individually configured for updates; use 'location' as field name instead")
		case "note":
			args = append(args, ir.Note)
		case "starred":
			args = append(args, ir.Starred)
		default:
			return fmt.Errorf("unrecognized field with update policy %v: %s", policy, field)
		}

		return nil
	}

	// build the SET clause field by field

	// apply update overrides first
	for field, policy := range updateOverrides {
		if err := applyUpdatePolicy(field, policy); err != nil {
			return 0, "", err
		}
	}

	// then for every remaining field, apply the default policy
	for field, policy := range p.ij.ProcessingOptions.ItemFieldUpdates {
		// skip overrides; already applied
		if _, ok := updateOverrides[field]; ok {
			continue
		}
		if err := applyUpdatePolicy(field, policy); err != nil {
			return 0, "", err
		}
	}

	sb.WriteString(" WHERE id=?")
	args = append(args, ir.ID)

	_, err := tx.ExecContext(ctx, sb.String(), args...)
	if err != nil {
		return 0, "", fmt.Errorf("updating item row: %w", err)
	}

	// if there's a chance that we just set the data_file to NULL, check to see if the
	// file is no longer referenced in the DB; if not, clean it up
	if startingDataFile != nil && ir.DataFile == nil {
		if err := p.tl.cleanDataFile(tx, *startingDataFile); err != nil {
			p.log.Error("cleaning up data file",
				zap.Int64("item_row_id", ir.ID),
				zap.Stringp("data_file_name", startingDataFile),
				zap.Error(err))
		}
	}

	atomic.AddInt64(p.ij.updatedItemCount, 1)

	return ir.ID, itemUpdated, nil
}

// detectContentType strives to detect the media type of the item using the
// peeked bytes. It sets it.Content.MediaType.
func detectContentType(peekedBytes []byte, it *Item) {
	// the value returned by http.DetectContentType() if it has no answer
	const defaultContentType = "application/octet-stream"

	// Go's sniffer can detect a handful of common media types
	contentType := http.DetectContentType(peekedBytes)

	// but if it couldn't, then we can detect a couple more common ones
	// (last checked Q1 2024: Go's standard lib doesn't support HEIC or
	// quicktime---a specific kind of .mv/.mp4 video---files,
	// which are common with Apple devices)
	if contentType == defaultContentType {
		if bytes.Contains(peekedBytes[:16], []byte("ftypheic")) {
			contentType = "image/heic"
		} else if bytes.Contains(peekedBytes[:16], []byte("ftypqt")) {
			contentType = "video/quicktime"
		}
	}

	// if we still don't know, try the file extension as a last resort
	ext := path.Ext(it.Content.Filename)
	if contentType == defaultContentType {
		if typeByExt := typeByExtension(ext); typeByExt != "" {
			contentType = typeByExt
		}
	}

	// Markdown gets detected as plaintext or even HTML (if the first part of the file has HTML),
	// so check for Markdown just in case; the file extension is actually a better indicator here;
	// because you wouldn't have an HTML document or even a plaintext file with a .md extension,
	// for example.
	if strings.HasPrefix(contentType, "text/plain") || strings.HasPrefix(contentType, "text/html") {
		// file extension can be the best indicator since, with Markdown, it's an explicit declaration of file type
		if typeByExt := typeByExtension(ext); typeByExt != "" {
			contentType = typeByExt
		} else if couldBeMarkdown(peekedBytes) {
			contentType = "text/markdown"
		}
	}

	it.Content.MediaType = contentType
}

// TODO: do we really need to use the default 32-byte digest? What if 16 bytes or even 8 is enough for us?
func newHash() hash.Hash { return blake3.New() }

// commonFileTypes is used as a last resort if the system couldn't
// identify the type of file; we select some common file types that
// we probably want to have proper support for.
var commonFileTypes = map[string]string{
	// raw photos (only some of these are common)
	".arw": "image/x-sony-arw",
	".cr2": "image/x-canon-cr2", // also "image/x-dcraw
	".crw": "image/x-canon-crw",
	".dcr": "image/x-kodak-dcr",
	".dng": "image/x-adobe-dng", // also "image/dng", but I think that's less common and non-standard
	".erf": "image/x-epson-erf",
	".k25": "image/x-kodak-k25",
	".kdc": "image/x-kodak-kdc",
	".mrw": "image/x-minolta-mrw",
	".nef": "image/x-nikon-nef",
	".orf": "image/x-olympus-orf",
	".pef": "image/x-pentax-pef",
	".raf": "image/x-fuji-raf",
	".raw": "image/x-panasonic-raw", // also "image/x-dcraw"
	".sr2": "image/x-sony-sr2",
	".srf": "image/x-sony-srf",
	".x3f": "image/x-sigma-x3f",

	// ftyp box might need to be inspected to know for sure,
	// but this is a good guess I suppose
	".heif": "image/heif",
	".heic": "image/heic",
	".hif":  "image/heif", // fujifilm's heif extension

	// video
	".3gp": "video/3gpp",

	// markdown
	".md":       "text/markdown",
	".mdown":    "text/markdown",
	".markdown": "text/markdown",
}

func typeByExtension(ext string) string {
	if ctByExt := mime.TypeByExtension(ext); ctByExt != "" {
		return ctByExt
	}
	// Ugh, still not recognized. I was surprised that DNG and HEIC files don't
	// have a match even on modern Macs (but they are recognized by Linux... go
	// figure) -- so let's at least maintain our own list of common file types
	// as a final fallback.
	if hardcodedType, ok := commonFileTypes[strings.ToLower(ext)]; ok {
		return hardcodedType
	}
	return ""
}

// Used to see if the size of content is big enough to go on disk
var sizePeekBufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, maxTextSizeForDB)
		return &buf
	},
}

// maxTextSizeForDB is the maximum size of text data we want
// to store in the DB. Sqlite doesn't have a limit per-se, but
// it's not comfortable to store huge text files in the DB,
// they belong in files; we just want to avoid lots of little
// text files on disk.
const maxTextSizeForDB = 1024 * 1024 * 50 // 50 KiB
