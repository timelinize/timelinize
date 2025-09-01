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
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"mime"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zeebo/blake3"
	"go.uber.org/zap"
)

// defaultBatchSize is how many items/entities (approximately) to process per transaction
// if not specified by the user. See the docs for batch size on ProcessingOptions.
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

	// if the relationship explicitly has a "from" node set, use that;
	// otherwise, assume this node is the "from" side
	if err := p.linkRelation(ctx, ig, tx, r, &rawRel, relationFrom); err != nil {
		return fmt.Errorf("linking from relation: %w", err)
	}

	// if the relationship explicitly has a "to" node set, use that;
	// otherwise, assume this node is the "to" side
	if err := p.linkRelation(ctx, ig, tx, r, &rawRel, relationTo); err != nil {
		return fmt.Errorf("linking to relation: %w", err)
	}

	err := p.tl.storeRelationship(ctx, tx, rawRel, r.Metadata)
	if err != nil {
		return fmt.Errorf("storing relationship: %w", err)
	}

	return nil
}

// I know this function is hard to read, but I initially had this inline above, and the linter complained it was duplicated code,
// despite the whole "from-to" parts being different; it's just annoying enough to have to change what you are assigning to that
// I didn't want to refactor this, but I did it anyway, I hope the linter is happy.
func (p *processor) linkRelation(ctx context.Context, g *Graph, tx *sql.Tx, r Relationship, rawRel *rawRelationship, fromOrTo string) error {
	otherGraph := r.From
	if fromOrTo == relationTo {
		otherGraph = r.To
	}

	if otherGraph != nil {
		// sidecar live photos don't get thumbnails; this is the only/best spot
		// in the pipeline where we will know that an item is a sidecar live photo,
		// so we set this here to avoid counting this toward the expected thumbnail
		// job size later
		// (TODO: We could move the definition of RelMotion into this package, but, eh. this works for now.)
		if r.Relation.Label == "motion" && otherGraph.Item != nil {
			otherGraph.Item.skipThumb = true
		}

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
		case g.Item != nil:
			if fromOrTo == relationFrom {
				rawRel.fromItemID = &g.rowID.itemID
			} else if fromOrTo == relationTo {
				rawRel.toItemID = &g.rowID.itemID
			}
		case g.Entity != nil:
			attrID, err := g.rowID.identifyingAttributeID(ctx, tx)
			if err != nil {
				return fmt.Errorf("getting identifying attribute ID for graph entity (on %s side): %w", fromOrTo, err)
			}
			if fromOrTo == relationFrom {
				rawRel.fromAttributeID = &attrID
			} else if fromOrTo == relationTo {
				rawRel.toAttributeID = &attrID
			}
		default:
			return fmt.Errorf("incomplete relationship: no '%s' node available: %+v (item_graph=%p %+v)", fromOrTo, r, g, g)
		}
	}

	return nil
}

const (
	relationFrom = "from"
	relationTo   = "to"
)

// deleteDataFileAndThumbnailIfUnreferenced deletes the data file and its thumbnail if there are no item rows referring to it.
func (tl *Timeline) deleteDataFileAndThumbnailIfUnreferenced(ctx context.Context, tx *sql.Tx, dataFilePath string) error {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT count() FROM items WHERE data_file=? LIMIT 1`, dataFilePath).Scan(&count)
	if err != nil {
		return fmt.Errorf("querying to check if data file is unused: %w", err)
	}
	if count > 0 {
		return nil
	}
	if err := tl.deleteRepoFile(dataFilePath); err != nil {
		return fmt.Errorf("deleting unused data file: %w", err)
	}
	tl.thumbsMu.Lock()
	_, err = tl.thumbs.ExecContext(ctx, "DELETE FROM thumbnails WHERE data_file=?", dataFilePath)
	tl.thumbsMu.Unlock()
	if err != nil {
		return fmt.Errorf("deleting unused data file's thumbnail: %w", err)
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
//
// TODO: Returning a second value for whether to process the data file doesn't really make sense
// since we already downloaded it, but maybe it can help us decide whether we keep/use it or not?
func (p *processor) shouldProcessExistingItem(it *Item, dbItem ItemRow, dataFileIncoming bool) (item bool, dataFile bool) {
	if it == nil {
		return
	}

	// don't re-import deleted items
	if dbItem.Deleted != nil {
		p.log.Error("skipping incoming item since it was previously deleted",
			zap.Uint64("row_id", dbItem.ID),
			zap.Time("deleted", *dbItem.Deleted))
		return
	}

	// when the function returns, we can trim update policies by comparing what values we already have with
	// what's going in -- if it turns out they're all the same, then no updates are needed; or depending on
	// the policy, even if they're not the same, no updates may be needed, which can, in theory, greatly
	// speed up repeated imports, since we skip redundant DB writes
	defer func() {
		if !item && !dataFile {
			return // already not reprocessing, so don't worry about double-checking
		}
		for field, policy := range it.fieldUpdatePolicies {
			// "keep existing" is the same as "no update", and it's simpler to just remove it from the map so that the
			// only policies that exist are those which actually perform an update
			if policy == UpdatePolicyKeepExisting {
				delete(it.fieldUpdatePolicies, field)
				continue
			}
			switch field {
			case "attribute_id":
				if (dbItem.AttributeID == nil && it.Owner.IsEmpty()) || // both empty
					// (tedious to determine attribute ID equality, so we'll just not worry about it)
					(it.Owner.IsEmpty() && policy != UpdatePolicyOverwriteExisting) { // update would no-op
					delete(it.fieldUpdatePolicies, field)
				}
			case "classification_id":
				if (dbItem.ClassificationID == nil && it.Classification.id == nil) || // both empty
					// (tedious to determine classification ID equality, so we'll just not worry about it)
					(it.Classification.Name == "" && policy != UpdatePolicyOverwriteExisting) { // update would no-op
					delete(it.fieldUpdatePolicies, field)
				}
			case "original_location":
				if (dbItem.OriginalLocation == nil && it.OriginalLocation == "") || // both empty
					(dbItem.OriginalLocation != nil && *dbItem.OriginalLocation == it.OriginalLocation) || // both the same
					(it.OriginalLocation == "" && policy != UpdatePolicyOverwriteExisting) { // update would no-op
					delete(it.fieldUpdatePolicies, field)
				}
			case "intermediate_location":
				if (dbItem.IntermediateLocation == nil && it.IntermediateLocation == "") || // both empty
					(dbItem.IntermediateLocation != nil && *dbItem.IntermediateLocation == it.IntermediateLocation) || // both the same
					(it.IntermediateLocation == "" && policy != UpdatePolicyOverwriteExisting) { // update would no-op
					delete(it.fieldUpdatePolicies, field)
				}
			case "filename":
				if (dbItem.Filename == nil && it.Content.Filename == "") || // both empty
					(dbItem.Filename != nil && *dbItem.Filename == it.Content.Filename) || // both the same
					(it.Content.Filename == "" && policy != UpdatePolicyOverwriteExisting) { // update would no-op
					delete(it.fieldUpdatePolicies, field)
				}
			case "timestamp":
				if (dbItem.Timestamp == nil && it.Timestamp.IsZero()) || // both empty
					(dbItem.Timestamp != nil && dbItem.Timestamp.Equal(it.Timestamp)) || // both the same
					(it.Timestamp.IsZero() && policy != UpdatePolicyOverwriteExisting) { // update would no-op
					delete(it.fieldUpdatePolicies, field)
				}
			case "timespan":
				if (dbItem.Timespan == nil && it.Timespan.IsZero()) || // both empty
					(dbItem.Timespan != nil && dbItem.Timespan.Equal(it.Timespan)) || // both the same
					(it.Timespan.IsZero() && policy != UpdatePolicyOverwriteExisting) { // update would no-op
					delete(it.fieldUpdatePolicies, field)
				}
			case "timeframe":
				if (dbItem.Timeframe == nil && it.Timeframe.IsZero()) || // both empty
					(dbItem.Timeframe != nil && dbItem.Timeframe.Equal(it.Timeframe)) || // both the same
					(it.Timeframe.IsZero() && policy != UpdatePolicyOverwriteExisting) { // update would no-op
					delete(it.fieldUpdatePolicies, field)
				}
			case "time_offset":
				_, offsetSec := it.Timestamp.Zone()
				if (dbItem.TimeOffset == nil && offsetSec == 0) || // both empty
					(dbItem.TimeOffset != nil && *dbItem.TimeOffset == offsetSec) || // both the same
					(offsetSec == 0 && policy != UpdatePolicyOverwriteExisting) { // update would no-op
					delete(it.fieldUpdatePolicies, field)
				}
			case "time_uncertainty":
				if (dbItem.TimeUncertainty == nil && it.TimeUncertainty == 0) || // both empty
					(dbItem.TimeUncertainty != nil && *dbItem.TimeUncertainty == int64(it.TimeUncertainty)) || // both the same
					(it.TimeUncertainty == 0 && policy != UpdatePolicyOverwriteExisting) { // update would no-op
					delete(it.fieldUpdatePolicies, field)
				}
			case "data":
				// only handling the case where data is text and small enough to fit nicely into DB page
				if (it.dataFileHash == nil && dbItem.DataFile == nil && dbItem.DataText == nil && it.dataText == nil) || // not a data file, and both texts are empty
					(dbItem.DataText != nil && it.dataText != nil && *dbItem.DataText == *it.dataText) || // both the same data text
					(dbItem.DataHash != nil && it.dataFileHash != nil && bytes.Equal(dbItem.DataHash, it.dataFileHash)) || // both the same data file
					(it.Content.Data == nil && policy != UpdatePolicyOverwriteExisting) { // update would no-op
					delete(it.fieldUpdatePolicies, field)
				}
			case "metadata":
				// easy cases first: both metadatas are empty, or the incoming would be a no-op
				if (dbItem.Metadata == nil && len(it.Metadata) == 0) || // both empty
					(len(it.Metadata) == 0 && policy != UpdatePolicyOverwriteExisting) { // update would no-op
					delete(it.fieldUpdatePolicies, field)
					break
				}

				// Metadata is a bit of a special case in that we apply the update policy to each key of metadata
				// (except KeepExisting, since that suggests not updating the metadata at all).
				// Unfortunately this involves decoding the existing JSON and re-encoding values to compare them :')
				if dbItem.Metadata != nil {
					var existingMetadata Metadata
					if err := json.Unmarshal(dbItem.Metadata, &existingMetadata); err != nil {
						p.log.Error("could not unmarshal existing item metadata to combine with incoming metadata; it may get overwritten",
							zap.Uint64("row_id", dbItem.ID),
							zap.String("filename", it.Content.Filename),
							zap.String("item_original_id", it.ID),
							zap.Error(err))
					}
					var metadataUpdateRequired bool // once we're done comparing keys/values, we may not need to update metadata at all
					// iterate each existing metadata key that's already in the DB, and see whether we need to update metadata
					// at all, and if so, make sure the incoming metadata reflects the update policy
					// (we only need to update metadata if a new key is incoming, or if a different value for an existing key is
					// incoming and the update policy prefers incoming)
					for k, v := range existingMetadata {
						switch policy {
						case UpdatePolicyPreferExisting:
							if !isEmpty(v) {
								it.Metadata[k] = v
							}
						case UpdatePolicyPreferIncoming:
							if incomingV, ok := it.Metadata[k]; !ok {
								it.Metadata[k] = v
							} else if !sameJSON(incomingV, v) {
								metadataUpdateRequired = true
							}
						}
					}
					if !metadataUpdateRequired {
						// the above loop should have told us if we need to update based on what's already in the DB,
						// but now check to see if there's new keys incoming that the DB row doesn't have yet
						for k := range it.Metadata {
							if _, ok := existingMetadata[k]; !ok {
								metadataUpdateRequired = true
								break
							}
						}
					}
					if metadataUpdateRequired {
						// ensure the processor updates the metadata field, now that we've done the merging of individual keys
						it.fieldUpdatePolicies[field] = UpdatePolicyOverwriteExisting
					} else {
						// ensure the processor does not unnecessarily update the metadata field
						delete(it.fieldUpdatePolicies, field)
					}
				}
			case "latlon":
				// skipping coordinate_system and coordinate_uncertainty for now
				// use same decimal precision as loadItemRow does
				lowLon, highLon, lonDecimals := latLonBounds(it.Location.Longitude, itemCoordDecimalPrecision)
				lowLat, highLat, latDecimals := latLonBounds(it.Location.Latitude, itemCoordDecimalPrecision)
				allNil := dbItem.Longitude == nil && it.Location.Longitude == nil && dbItem.Latitude == nil && it.Location.Latitude == nil
				valuesWithinBounds := (dbItem.Longitude != nil && it.Location.Longitude != nil && (*lowLon <= coordRound(*dbItem.Longitude, lonDecimals) && coordRound(*dbItem.Longitude, lonDecimals) <= *highLon)) &&
					(dbItem.Latitude != nil && it.Location.Latitude != nil && (*lowLat <= coordRound(*dbItem.Latitude, latDecimals) && coordRound(*dbItem.Latitude, latDecimals) <= *highLat))
				wouldNoOp := policy != UpdatePolicyOverwriteExisting && it.Location.Longitude == nil && it.Location.Latitude == nil
				if allNil || // both empty
					valuesWithinBounds || // both (approximately) the same
					wouldNoOp { // update would no-op
					delete(it.fieldUpdatePolicies, field)
				}
			case "altitude":
				lowAlt, highAlt := altitudeBounds(it.Location.Altitude)
				if (dbItem.Altitude == nil && it.Location.Altitude == nil) || // both empty
					(dbItem.Altitude != nil && it.Location.Altitude != nil && (*lowAlt <= *dbItem.Altitude && *dbItem.Altitude < *highAlt)) || // both (approximately) the same
					(policy != UpdatePolicyOverwriteExisting && it.Location.Altitude == nil) { // update would no-op
					delete(it.fieldUpdatePolicies, field)
				}
			}
		}
		item = item && len(it.fieldUpdatePolicies) > 0
		dataFile = item && dataFile && it.fieldUpdatePolicies["data"] > 0
	}()

	// within the same import, reprocess an item if the data source gives us the item in pieces;
	// for example, at first we might only get just enough of the item to satisfy a relationship
	// (like an ID), then later as it iterates it finds that related item and fills out the rest
	// of the item's information -- so if our current item row is missing information, we can at
	// least safely add new info I think
	if dbItem.JobID != nil && *dbItem.JobID == p.ij.job.id {
		if it.fieldUpdatePolicies == nil {
			it.fieldUpdatePolicies = make(map[string]FieldUpdatePolicy)
		}

		// if there's an incoming data file and we don't have one, then update
		if dbItem.DataText == nil && dbItem.DataFile == nil && (it.dataText != nil || it.dataFileHash != nil) {
			dataFile = true
			it.fieldUpdatePolicies["data"] = UpdatePolicyPreferIncoming
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
				it.fieldUpdatePolicies["attribute_id"] = UpdatePolicyPreferIncoming
			}
		}

		// reprocess the item row if there's new data to be added
		if (dbItem.Latitude == nil && it.Location.Latitude != nil) ||
			(dbItem.Longitude == nil && it.Location.Longitude != nil) {
			it.fieldUpdatePolicies["latlon"] = UpdatePolicyPreferIncoming
			if dbItem.Altitude == nil && it.Location.Altitude != nil {
				it.fieldUpdatePolicies["altitude"] = UpdatePolicyPreferIncoming
			}
		}
		if !it.Timestamp.IsZero() {
			// time and zone are stored separately in the DB, so consider those parts separately (not doing so was a bug: we reprocessed items unnecessarily)
			if dbItem.Timestamp == nil {
				it.fieldUpdatePolicies["timestamp"] = UpdatePolicyPreferIncoming
			} else if dbItem.TimeOffset == nil {
				_, offsetSec := it.Timestamp.Zone()
				if offsetSec != 0 {
					it.fieldUpdatePolicies["time_offset"] = UpdatePolicyPreferIncoming
				}
			}
		}
		if dbItem.Timespan == nil && !it.Timespan.IsZero() {
			it.fieldUpdatePolicies["timespan"] = UpdatePolicyPreferIncoming
		}
		if dbItem.Timeframe == nil && !it.Timeframe.IsZero() {
			it.fieldUpdatePolicies["timeframe"] = UpdatePolicyPreferIncoming
		}
		if dbItem.Filename == nil && it.Content.Filename != "" {
			it.fieldUpdatePolicies["filename"] = UpdatePolicyPreferIncoming
		}
		if dbItem.Classification == nil && it.Classification.Name != "" {
			it.fieldUpdatePolicies["classification_id"] = UpdatePolicyPreferIncoming
		}
		if dbItem.OriginalLocation == nil && it.OriginalLocation != "" {
			it.fieldUpdatePolicies["original_location"] = UpdatePolicyPreferIncoming
		}
		if dbItem.IntermediateLocation == nil && it.IntermediateLocation != "" {
			it.fieldUpdatePolicies["intermediate_location"] = UpdatePolicyPreferIncoming
		}
		if dbItem.OriginalID == nil && it.ID != "" {
			it.fieldUpdatePolicies["original_id"] = UpdatePolicyPreferIncoming
		}
		if dbItem.DataText == nil && dbItem.DataFile == nil && (it.dataText != nil || it.dataFileHash != nil) {
			it.fieldUpdatePolicies["data"] = UpdatePolicyPreferIncoming
		}

		// the deferred function above will take care of merging the metadata
		if len(it.Metadata) > 0 {
			it.fieldUpdatePolicies["metadata"] = UpdatePolicyPreferIncoming
		}

		item = len(it.fieldUpdatePolicies) > 0

		if !item {
			p.log.Debug("skipping processing of existing item because it was already processed in this import and there are no update overrides",
				zap.Uint64("item_row_id", dbItem.ID),
				zap.String("item_classification", it.Classification.Name),
				zap.String("filename", it.Content.Filename),
				zap.String("item_original_id", it.ID))
		}

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
			zap.Uint64("item_row_id", dbItem.ID),
			zap.Stringp("data_file", dbItem.DataFile),
			zap.Error(integrityCheckErr))
	}

	// if modified manually, do not overwrite changes unless specifically enabled
	if dbItem.Modified != nil && !p.ij.ProcessingOptions.OverwriteLocalChanges {
		p.log.Debug("skipping processing of existing item because it has been manually modified within the repo (enable modification overwrites to override)",
			zap.Uint64("item_row_id", dbItem.ID),
			zap.String("filename", it.Content.Filename),
			zap.String("item_original_id", it.ID))
		return false, false
	}

	// if a field update policy is deferred, it's because it requires knowing something about the incoming data
	// file that we won't know until we download it in a later phase; so re-process the item and the file
	for _, pol := range it.fieldUpdatePolicies {
		if pol < 0 {
			return true, true
		}
	}

	// if the item data is explicitly configured to overwrite existing, then it
	// should always be reprocessed, even if NULL
	dataUpdatePolicy, dataUpdateEnabled := it.fieldUpdatePolicies["data"]
	if dataUpdatePolicy == UpdatePolicyOverwriteExisting {
		return true, true
	}

	if dataFileIncoming {
		// if a data file is incoming and integrity check failed, always reprocess regardless of
		// specific update policy for this field (because integrity check is explicitly opt-in too)
		if integrityCheckErr != nil {
			return true, true
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
			return true, true
		}

		// by this point, we know that if it has a data file, it has good integrity
		// (if integrity checks are enabled) and it was completely downloaded (hash
		// exists), so we should update it according to configured policy
		if dataUpdatePolicy == UpdatePolicyPreferExisting {
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
	item = len(it.fieldUpdatePolicies) > 0

	return item || dataFile, dataFile
}

func (p *processor) fillItemRow(ctx context.Context, tx *sql.Tx, ir *ItemRow, it *Item) error {
	// unpack the item's information into values to use in the row

	// insert and/or retrieve owner information
	rowID, err := p.processEntity(ctx, tx, it.Owner)
	if err != nil {
		return fmt.Errorf("getting person associated with item: %w", err)
	}

	// encode metadata as JSON
	var metadata json.RawMessage
	if len(it.Metadata) > 0 {
		metadata, err = json.Marshal(it.Metadata) // should already be cleaned
		if err != nil {
			return fmt.Errorf("encoding metadata as JSON: %w", err)
		}
	}

	// convert classification name to ID
	var clID uint64
	if it.Classification.Name != "" {
		clID, err = p.tl.classificationNameToID(it.Classification.Name)
		if err != nil {
			return fmt.Errorf("unable to get classification ID: %w (classification=%+v)", err, it.Classification)
		}
	}

	// if this item has an owner entity, get the associated attribute ID
	var attrID uint64
	if rowID.entityID > 0 {
		attrID, err = rowID.identifyingAttributeID(ctx, tx)
		if err != nil {
			return fmt.Errorf("getting identifying attribute row ID: %w", err)
		}
	}

	ir.DataSourceID = &p.dsRowID
	ir.DataSourceName = &p.ds.Name
	ir.DataSourceTitle = &p.ds.Title
	if ir.JobID == nil {
		// if the row was loaded from the DB, we don't want to wipe out if it
		// already had its original job ID associated with it
		ir.JobID = &p.ij.job.id
	}
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
	if !it.Timespan.IsZero() && !it.Timespan.Equal(it.Timestamp) {
		ir.Timespan = &it.Timespan
	}
	if !it.Timeframe.IsZero() && !it.Timeframe.Equal(it.Timestamp) {
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
		ir.DataFile = &it.dataFileName
	}
	if len(it.dataFileHash) > 0 {
		ir.DataHash = it.dataFileHash
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
// find the specific item. The most specific/exact matches should use all the available fields
// to match; if no columns are specified, then it is an error if the item does not have an
// original ID. If the original ID is specified, the sole criteria used to look up a unique item
// is the data source and the original ID. If checkDeleted is true, an item which has been deleted
// will also be included as a possible result, it requires that idHash and contentHash are set on
// the item already.
func (tl *Timeline) loadItemRow(ctx context.Context, tx *sql.Tx, rowID, notRowID uint64, it *Item, dataSourceName *string, uniqueConstraints map[string]bool, checkDeleted bool) (ItemRow, error) {
	if rowID != 0 && notRowID != 0 {
		return ItemRow{}, errors.New("cannot load item row both by row ID and not by row ID")
	}

	// little helper function that returns the most correct equality operator (IS or =) based on the value being compared against
	eq := func(arg any) string {
		if isNil(arg) {
			return " IS "
		}
		return "="
	}

	// Build a highly-optimized query since this occurs in a hot path during imports.
	// There are two main parts to the optimization (confirmed via EXPLAIN QUERY PLAN):
	// (1) indexes on the items table on the timestamp column, and then a couple special
	// partial indexes related to the initial content hash and deleted columns; and (2)
	// using multiple "branches" that get UNION ALL'ed, which allows the use of those
	// indexes by the query planner, rather than a big complex outer OR which results in
	// a full table scan.

	var sb strings.Builder
	var args []any

	// outer SELECT combines the results of our unioned queries
	sb.WriteString(`
SELECT * FROM (
	SELECT `)
	sb.WriteString(itemDBColumns)
	sb.WriteString(`
	FROM extended_items AS items
	WHERE`)

	if rowID != 0 {
		// select the row directly with its row ID
		sb.WriteString(" id=?")
		args = append(args, rowID)
	} else {
		// select the row by the various properties of the item

		if len(uniqueConstraints) == 0 && (dataSourceName == nil || it.ID == "") && len(it.Retrieval.key) == 0 {
			// if no unique constraints were specified (by mistake?), this could be problematic
			// as it would match, uh, probably any item from the same data source
			return ItemRow{}, errors.New("missing unique constraints; at least 1 required when no original ID specified")
		}

		args = make([]any, 0, 1+len(uniqueConstraints))

		// first, honor the retrieval key if set, which allows an item to be pieced
		// together regardless of what values are in the row already... since the whole
		// item may not be known yet or some parts may be changing (for reasons known
		// only to the data source), we use the retrieval key as a globally unique key
		// to check for an  existing item (even if only part of it is in the DB); we
		// don't use the retrieval key if selecting by row ID (or not row ID), since
		// that doesn't make sense to do
		if len(it.Retrieval.key) > 0 && notRowID == 0 {
			sb.WriteString(" retrieval_key=?")
			args = append(args, it.Retrieval.key)
		}

		// an easy way to select an item is by the ID assigned from the data source;
		// this should be exclusive enough to uniquely select an item
		// TODO: See if this is optimized
		if dataSourceName != nil && it.ID != "" {
			if len(args) > 0 {
				sb.WriteString("\n\t\tOR")
			}
			sb.WriteString(" (data_source_name=? AND original_id=?)")
			args = append(args, dataSourceName, it.ID)
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
			if len(args) > 0 {
				sb.WriteString("\n\t\tOR ")
			}
			sb.WriteString("\n\t\t(deleted IS NOT NULL AND original_id_hash")
			sb.WriteString(eq(it.idHash))
			sb.WriteString("?)\n\t\tOR ((modified IS NOT NULL OR deleted IS NOT NULL) AND initial_content_hash")
			sb.WriteString(eq(it.contentHash))
			sb.WriteString("?)")
			args = append(args, it.idHash, it.contentHash)

			// the above criteria are sufficient for one query, the next one that uses
			// the unique constraints will be its own query that gets unioned together
			if len(uniqueConstraints) > 0 {
				sb.WriteString("\n\tUNION ALL\n\t")
				sb.WriteString("SELECT ")
				sb.WriteString(itemDBColumns)
				sb.WriteString("\n\tFROM extended_items AS items\n\tWHERE\n\t\t")
			}
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
		if notRowID > 0 {
			sb.WriteString("id != ?")
			args = append(args, rowID)
			firstIter = false
		}
		for field := range uniqueConstraints {
			// Note: the value in uniqueConstraints is supposed to be whether NULLs are significant/strictly compared, but it is not currently implemented, and I am not sure if it is useful.

			if !firstIter {
				sb.WriteString("\n\t\tAND ")
			}
			firstIter = false

			// TODO: should we take into account time_uncertainty and coordinate_uncertainty and allow any value in that range to be a match?

			switch field {
			case "data":
				sb.WriteString("(data_text")
				sb.WriteString(eq(it.dataText))
				sb.WriteString("? AND data_hash")
				sb.WriteString(eq(it.dataFileHash))
				sb.WriteString("?)")
				args = append(args, it.dataText, it.dataFileHash)
			case "latlon":
				sb.WriteString("((longitude")
				sb.WriteString(eq(it.Location.Longitude))
				sb.WriteString("? OR (? <= round(longitude, ?) AND round(longitude, ?) <= ?)) \n\t\t\tAND (latitude")
				sb.WriteString(eq(it.Location.Latitude))
				sb.WriteString("? OR (? <= round(latitude, ?) AND round(latitude, ?) <= ?)) \n\t\t\tAND coordinate_system")
				sb.WriteString(eq(it.Location.CoordinateSystem))
				sb.WriteString("?)")
				lowLon, highLon, lonDecimals := latLonBounds(it.Location.Longitude, itemCoordDecimalPrecision)
				lowLat, highLat, latDecimals := latLonBounds(it.Location.Latitude, itemCoordDecimalPrecision)
				args = append(args,
					it.Location.Longitude, lowLon, lonDecimals, lonDecimals, highLon,
					it.Location.Latitude, lowLat, latDecimals, latDecimals, highLat,
					it.Location.CoordinateSystem)
			case "altitude":
				// doesn't make sense on its own
				if _, ok := uniqueConstraints["latlon"]; !ok {
					return ItemRow{}, errors.New("altitude requires latlon also be used as a unique constraint")
				}
				sb.WriteString("(altitude")
				sb.WriteString(eq(it.Location.Altitude))
				sb.WriteString("? OR (? <= altitude AND altitude < ?))")
				lowAlt, highAlt := altitudeBounds(it.Location.Altitude)
				args = append(args, it.Location.Altitude, lowAlt, highAlt)
			default:
				var arg any
				switch field {
				case "data_source_name":
					arg = dataSourceName
				case "classification_name":
					if it.Classification.Name != "" {
						arg = &it.Classification.Name
					}
				case "original_location":
					if it.OriginalLocation != "" {
						arg = &it.OriginalLocation
					}
				case "intermediate_location":
					if it.IntermediateLocation != "" {
						arg = &it.IntermediateLocation
					}
				case "filename":
					if it.Content.Filename != "" {
						arg = &it.Content.Filename
					}
				case "timestamp":
					arg = it.timestampUnix()
				case "timespan":
					arg = it.timespanUnix()
				case "timeframe":
					arg = it.timeframeUnix()
				case "data_type", "data_text", "data_hash":
					return ItemRow{}, errors.New("cannot select on specific components of item data such as text or file hash; specify 'data' instead")
				case "longitude", "latitude", "coordinate_system", "coordinate_uncertainty":
					// unlike the data fields, there's no good reason for this other than "the other way doesn't make sense and may be error-prone"
					return ItemRow{}, errors.New("cannot select on specific components of item coordinates such as latitude or longitude: specify 'latlon'+'altitude' instead")
				default:
					return ItemRow{}, fmt.Errorf("item unique constraints configure unsupported/unrecognized field: %s", field)
				}

				sb.WriteString(field)
				sb.WriteString(eq(arg))
				sb.WriteRune('?')

				args = append(args, arg)
			}
		}
	}

	sb.WriteString("\n)\nLIMIT 1")

	row := tx.QueryRowContext(ctx, sb.String(), args...)

	return scanItemRow(row, nil)
}

// insertOrUpdateItem inserts the fully-populated ir into the database (TODO: finish godoc)
func (p *processor) insertOrUpdateItem(ctx context.Context, tx *sql.Tx, ir ItemRow, allowOverwrite bool, fieldUpdatePolicies map[string]FieldUpdatePolicy) (uint64, itemStoreResult, error) {
	// new item? insert it
	if ir.ID == 0 {
		var metadata *string
		if len(ir.Metadata) > 0 {
			metaStr := string(ir.Metadata)
			metadata = &metaStr
		}

		var rowID uint64

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
			ir.DataType, ir.DataText, ir.DataFile, ir.DataHash, metadata,
			ir.Location.Longitude, ir.Location.Latitude, ir.Location.Altitude,
			ir.Location.CoordinateSystem, ir.Location.CoordinateUncertainty,
			ir.Note, ir.Starred, ir.OriginalIDHash, ir.InitialContentHash, ir.RetrievalKey,
		).Scan(&rowID)

		atomic.AddInt64(p.ij.newItemCount, 1)

		return rowID, itemInserted, err
	}

	// existing item; update it

	// ...only if any fields are configured to be updated
	if len(fieldUpdatePolicies) == 0 {
		return ir.ID, itemSkipped, nil
	}

	var sb strings.Builder
	var args []any
	var needsComma bool

	sb.WriteString("UPDATE items SET ")

	// set the modified_job_id (the ID of the import that most recently modified the item) only if
	// it's not the original import, I think it makes sense to count the original import only once
	if ir.JobID != nil && *ir.JobID != p.ij.job.id {
		sb.WriteString("modified_job_id=?")
		args = append(args, p.ij.job.id)
		needsComma = true
	}

	appendToQuery := func(field string, policy FieldUpdatePolicy) {
		if field == "metadata" {
			// we already applied the update policy on a per-key basis earlier, which also merged
			// keys the DB row already had, so we can always safely prefer the incoming metadata
			policy = UpdatePolicyPreferIncoming
		}
		switch policy {
		case UpdatePolicyPreferExisting:
			if needsComma {
				sb.WriteString(", ")
			}
			sb.WriteString(field)
			sb.WriteString("=COALESCE(")
			sb.WriteString(field)
			sb.WriteString(", ?)")
		case UpdatePolicyOverwriteExisting:
			if allowOverwrite {
				if needsComma {
					sb.WriteString(", ")
				}
				sb.WriteString(field)
				sb.WriteString("=?")
				break
			}
			fallthrough
		case UpdatePolicyPreferIncoming:
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

	applyUpdatePolicy := func(field string, policy FieldUpdatePolicy) error {
		if policy <= 0 {
			// 0  = no update
			// -1 = this happens if we're still waiting for information before we can
			// apply the policy; the processor should do this again later
			return nil
		}

		switch field {
		case "data":
			appendToQuery("data_type", policy)
			appendToQuery("data_text", policy)
			appendToQuery("data_file", policy)
			appendToQuery("data_hash", policy)
		case "latlon":
			appendToQuery("longitude", policy)
			appendToQuery("latitude", policy)
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
			// TODO: should this be bundled with timestamp? would users ever want to update time but not the zone? what if an incoming timestamp is more correct but lacks zone?
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
			var metadata *string
			if len(ir.Metadata) > 0 {
				metaStr := string(ir.Metadata)
				metadata = &metaStr
			}
			args = append(args, metadata)
		case "latlon":
			args = append(args, ir.Longitude)
			args = append(args, ir.Latitude)
			args = append(args, ir.CoordinateSystem)
			args = append(args, ir.CoordinateUncertainty)
		case "altitude":
			args = append(args, ir.Altitude)
		case "longitude", "latitude", "coordinate_system", "coordinate_uncertainty":
			// unlike the data fields, there's no good reason for this other than "individually doesn't make sense and may be tedious"
			return errors.New("location components cannot be individually configured for updates other than altitude; use 'latlon' as field name instead")
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

	for field, policy := range fieldUpdatePolicies {
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
	if contentType == defaultContentType || contentType == "" {
		if bytes.Contains(peekedBytes[:16], []byte("ftypheic")) {
			contentType = "image/heic"
		} else if bytes.Contains(peekedBytes[:16], []byte("ftypqt")) {
			contentType = "video/quicktime"
		}
	}

	// if we still don't know, try the file extension as a last resort
	ext := path.Ext(it.Content.Filename)
	if contentType == defaultContentType || contentType == "" {
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

// validTime returns a valid form of the given time, as far as our application is concerned.
// For example, JSON-serializing a time with a year > 9999 panics, so we clear/ignore the
// time value altogether in that case. Or, if the timezone offset is a ridiculous number,
// we drop the time zone but keep the rest of the data.
func validTime(t time.Time) time.Time {
	// make sure time zone is within bounds; if not, just strip it; I found several that shifted the year into the 6000s BC
	const maxTimezoneOffsetSecFromUTC = 50400 // most distant time zone from UTC is apparently +-14 hours
	if _, offsetSec := t.Zone(); offsetSec > maxTimezoneOffsetSecFromUTC || offsetSec < -maxTimezoneOffsetSecFromUTC {
		t = time.Date(t.Year(), t.Month(), t.Day(),
			t.Hour(), t.Minute(), t.Second(), t.Nanosecond(),
			time.UTC)
	}

	// make sure year is within bounds
	const minJSONSerializableYear, maxJSONSerializableYear = 0, 9999
	if t.Year() <= minJSONSerializableYear || maxJSONSerializableYear <= t.Year() {
		return time.Time{}
	}

	return t
}

func coordRound(x float64, decimalPlaces int) float64 {
	magnitude := math.Pow(10, float64(decimalPlaces)) //nolint:mnd
	return math.Round(x*magnitude) / magnitude
}

// latLonBounds returns low and high bounds for an acceptable lat/lon value,
// as well as the decimal precision used, which can be useful in SQL queries
// for its round() function.
//
// Pass in the max decimal places of precision:
// 3 ~= 111 meters,
// 4 ~= 11.1 meters,
// 5 ~= 1.11 meters,
// 6 ~= .111 meters (11 centimeters)
func latLonBounds(latOrLon *float64, maxDecimalPlaces int) (lo, hi *float64, decimalPlaces int) {
	if latOrLon == nil {
		return
	}
	x := *latOrLon
	decimalPlaces = min(numDecimalPlaces(x), maxDecimalPlaces)
	precision := math.Pow(10, float64(decimalPlaces)) //nolint:mnd
	low, high := math.Floor(x*precision)/precision, math.Ceil(x*precision)/precision
	if low == high {
		// if the input is less precision than our target, separate the min and max by 1 unit of precision
		high += 1 / precision
	}
	return &low, &high, decimalPlaces
}

func altitudeBounds(altMeters *float64) (lo, hi *float64) {
	if altMeters == nil {
		return
	}
	// altitude is in meters, I don't think sub-meter precision is necessary
	l, h := math.Floor(*altMeters), math.Ceil(*altMeters)
	lo, hi = &l, &h
	return
}

func numDecimalPlaces(x float64) int {
	s := strconv.FormatFloat(x, 'f', -1, 64)
	if decimalPoint := strings.IndexByte(s, '.'); decimalPoint >= 0 {
		return len(s) - decimalPoint - 1
	}
	return 0
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

type itemStoreResult string

const (
	itemInserted itemStoreResult = "inserted"
	itemSkipped  itemStoreResult = "skipped"
	itemUpdated  itemStoreResult = "updated"
)

// Used to see if the size of content is big enough to go on disk
var sizePeekBufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, maxTextSizeForDB)
		return &buf
	},
}

// itemCoordDecimalPrecision is how many decimal places to
// use for comparing item coordinates
const itemCoordDecimalPrecision = 5

// maxTextSizeForDB is the maximum size of text data we want
// to store in the DB. Sqlite doesn't have a limit per-se, but
// it's not comfortable to store huge text files in the DB,
// they belong in files; we just want to avoid lots of little
// text files on disk.
const maxTextSizeForDB = 1024 * 100 // 100 KiB
