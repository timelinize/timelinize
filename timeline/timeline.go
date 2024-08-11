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
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	cuckoo "github.com/seiflotfy/cuckoofilter"
	"go.uber.org/zap"
)

var (
	BuildPeriod = time.Date(2024, 5, 1, 0, 0, 0, 0, time.Local)
	Expiry      = BuildPeriod.AddDate(0, 4, 0)
)

// Timeline represents an opened timeline repository.
// The zero value is NOT valid; use Open() to obtain
// a valid value.
type Timeline struct {
	// A context used primarily for cancellation.
	ctx    context.Context
	cancel context.CancelFunc // to be called only by the shutdown routine

	repoDir      string              // path of the timeline repository
	cacheDir     string              // path of the cache folder (not including the repository subfolder); this generally exists outside the timeline
	rateLimiters map[int64]RateLimit // keyed by account ID
	id           uuid.UUID

	// caches of name -> ID
	cachesMu        sync.RWMutex // protects these maps
	classifications map[string]int64
	entityTypes     map[string]int64
	relations       map[string]int64
	dataSources     map[string]int64

	// The database handle and its mutex. Why a mutex for a DB handle? Because
	// high-volume imports can sometimes yield "database is locked" errors,
	// presumably because of scanning rows (`for rows.Next()`) while trying
	// to do a write from another query at the same time:
	// https://github.com/mattn/go-sqlite3/issues/607#issuecomment-808739698
	// and this is totally possible since we run our DB ops concurrently.
	// It's unfortunate that DB locking doesn't handle this for us, but by
	// wrapping DB calls in this mutex I've noticed the problem disappear.
	db   *sql.DB
	dbMu sync.RWMutex
}

func (t *Timeline) String() string { return fmt.Sprintf("%s:%s", t.id, t.repoDir) }
func (t *Timeline) Dir() string    { return t.repoDir }
func (t *Timeline) ID() uuid.UUID  { return t.id }

// TODO: refactor Create() and Open() to use AssessFolder() instead of duplicating logic

// Create creates and opens a new timeline in the given repo path, which need not
// already exist. If the path exists, it must be a directory. If it does not exist
// or is an empty directory, it will become the timeline's repo path; if it does
// exist and is not empty, however, a new folder within the path will be created
// in which the timeline will be provisioned. If the path already exists and is
// already a Timelinize repo, or the folder which would be created inside the
// path is already a Timelinize repo, then fs.ErrExist is returned.
//
// Timelines should always be Close()'d for a clean shutdown when done.
func Create(repoPath, cacheDir string) (*Timeline, error) {
	// ensure the directory exists
	err := os.MkdirAll(repoPath, 0755)
	if err != nil {
		return nil, fmt.Errorf("creating requested repo folder: %w", err)
	}

	// ensure directory is empty
	dirEmpty, problematicFile, err := directoryEmpty(repoPath, false)
	if err != nil {
		return nil, err
	}
	if !dirEmpty {
		// the selected folder is not empty, so it's not suitable for a timeline;
		// is it already a timeline repo?
		repoDBFile := filepath.Join(repoPath, DBFilename)
		if FileExists(repoDBFile) {
			return nil, fmt.Errorf("%w: selected folder already contains a timeline repository: %s", fs.ErrExist, repoPath)
		}

		// if not, then the expected thing to do is to create a new folder within it,
		// since the user doesn't know the folder needs to be empty (they may even think
		// the timeline is a single file, so any folder will do) -- try to create a
		// new, empty parent folder for a timeline if it doesn't exist yet
		repoPath = filepath.Join(repoPath, "My timeline")
		err := os.MkdirAll(repoPath, 0755)
		if err != nil {
			return nil, fmt.Errorf("folder already existed but was not empty (%s), so tried to create new empty repo folder within it: %w", problematicFile, err)
		}

		// if the updated repo path is still not empty, then it already existed and we can't use it; return appropriate error value so UI can inform user
		dirEmpty, problematicFile, err := directoryEmpty(repoPath, false)
		if err != nil {
			return nil, err
		}
		if !dirEmpty {
			// is it a timeline?
			repoDBFile := filepath.Join(repoPath, DBFilename)
			if FileExists(repoDBFile) {
				return nil, fmt.Errorf("%w: selected folder already contains a timeline repository: %s", fs.ErrExist, repoPath)
			}
			// if not, well... we shouldn't really use it I guess
			return nil, fmt.Errorf("timeline cannot be created at %s because it is not empty: %s", repoPath, problematicFile)
		}
	}

	db, err := openAndProvisionDB(repoPath)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	return openTimeline(repoPath, cacheDir, db)
}

// directoryEmpty returns true if dirPath is an empty directory. If false,
// the name of the first discovered file is returned. If deletePointlessFiles
// is true, then unintentional files (like .DS_Store) will be deleted while
// considering whether a dir is empty. (It is not required to delete the file
// to still consider it empty, but if preparing an empty dir for deletion,
// emptying the dir of pointless files will come in handy.)
func directoryEmpty(dirPath string, deletePointlessFiles bool) (bool, string, error) {
	dir, err := os.Open(dirPath)
	if err != nil {
		return false, "", fmt.Errorf("opening folder: %w", err)
	}
	defer dir.Close()

	// no need to read whole listing; all we need to do is find one non-intentional file
	// (reading 2 allows for 1 pointless file)
	fileList, err := dir.Readdirnames(2)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, "", fmt.Errorf("reading folder contents: %w", err)
	}

	for _, f := range fileList {
		// ignore, and possibly delete, pointless files
		if f == ".DS_Store" || f == "Thumbs.db" {
			if deletePointlessFiles {
				err := os.Remove(filepath.Join(dirPath, f))
				if err != nil && !errors.Is(err, fs.ErrNotExist) {
					return false, f, fmt.Errorf("unable to delete pointless file: %v", err)
				}
			}
			continue
		}
		return false, f, nil
	}

	return true, "", nil
}

type FolderAssessment struct {
	HasTimeline          bool   `json:"has_timeline"`
	TimelineCanBeCreated bool   `json:"timeline_can_be_created"`
	TimelinePath         string `json:"timeline_path,omitempty"`
	Reason               string `json:"reason,omitempty"`
}

func (fa FolderAssessment) Error() string {
	return fmt.Sprintf("Has timeline: %t - Timeline can be created: %t - Path: %s - Reason: '%s'",
		fa.HasTimeline, fa.TimelineCanBeCreated, fa.TimelinePath, fa.Reason)
}

func AssessFolder(fpath string) FolderAssessment {
	info, err := os.Stat(fpath)
	if errors.Is(err, fs.ErrNotExist) {
		return FolderAssessment{
			TimelineCanBeCreated: true,
			TimelinePath:         filepath.Clean(fpath),
			Reason:               "folder does not exist yet",
		}
	}
	if err != nil {
		return FolderAssessment{Reason: err.Error()}
	}
	if !info.IsDir() {
		return FolderAssessment{Reason: "path is not a directory"}
	}

	// see if directory is empty
	dirEmpty, problematicFile, err := directoryEmpty(fpath, false)
	if err != nil {
		return FolderAssessment{Reason: err.Error()}
	}
	if dirEmpty {
		return FolderAssessment{
			TimelineCanBeCreated: true,
			TimelinePath:         fpath,
		}
	}

	// the selected folder is not empty, so it's not suitable for a timeline;
	// is it already a timeline repo?
	repoDBFile := filepath.Join(fpath, DBFilename)
	if FileExists(repoDBFile) {
		return FolderAssessment{
			HasTimeline:  true,
			TimelinePath: fpath,
			Reason:       "timeline database exists",
		}
	}

	// if it's not empty and not already a timeline repo, the user probably thinks the
	// timeline is a single file and that they can put it in any folder; to that end,
	// we can recommend creating the timeline in a new "file" in that folder (but it's
	// actually a folder, of course)
	proposedPath := filepath.Join(fpath, "My timeline")

	info, err = os.Stat(proposedPath)
	if errors.Is(err, fs.ErrNotExist) {
		return FolderAssessment{
			TimelineCanBeCreated: true,
			TimelinePath:         proposedPath,
			Reason:               fmt.Sprintf("folder is not empty (has file named <code>%s</code>), but timeline can be created within it", problematicFile),
		}
	}
	if err != nil {
		return FolderAssessment{Reason: err.Error()}
	}
	if !info.IsDir() {
		return FolderAssessment{Reason: fmt.Sprintf("folder name is already taken by a file: <code>%s</code>", proposedPath)}
	}

	dirEmpty, problematicFile2, err := directoryEmpty(proposedPath, false)
	if err != nil {
		return FolderAssessment{Reason: err.Error()}
	}
	if dirEmpty {
		return FolderAssessment{
			TimelineCanBeCreated: true,
			TimelinePath:         proposedPath,
			Reason:               fmt.Sprintf("folder is not empty (has file named <code>%s</code>), but timeline can be created within it", problematicFile),
		}
	}

	// folder within it is not empty; is it a timeline?
	repoDBFile = filepath.Join(proposedPath, DBFilename)
	if FileExists(repoDBFile) {
		return FolderAssessment{
			HasTimeline:  true,
			TimelinePath: proposedPath,
			Reason:       fmt.Sprintf("selected folder is not empty (has file <code>%s</code>), and timeline database already exists within at <code>%s</code>", problematicFile, repoDBFile),
		}
	}
	return FolderAssessment{
		Reason: fmt.Sprintf("selected folder is not empty (has file <code>%s</code>), neither is folder for new timeline repo: <code>%s</code> (has file <code>%s</code>)", problematicFile, proposedPath, problematicFile2),
	}
}

// Open strictly opens an existing timeline at the given repo folder;
// it does not attempt to create one if it does not already exist.
// Timelines should always be Close()'d for a clean shutdown when done.
// TODO: what happens if a timeline folder is (re)moved while it is open?
func Open(repo, cache string) (*Timeline, error) {
	// construct filenames within this repo folder specifically
	repoDBFile := filepath.Join(repo, DBFilename)
	repoDataFolder := filepath.Join(repo, DataFolderName)

	// check folder existence and accessibility
	if _, err := os.Stat(repo); err != nil {
		return nil, fmt.Errorf("checking repo folder: %w", err)
	}
	// also check for database file
	if _, err := os.Stat(repoDBFile); err != nil {
		return nil, fmt.Errorf("checking repo DB file: %w", err)
	}
	// if data folder exists but DB does not, that's confusing, and likely a problem,
	// since we cannot reconstruct the DB from the data folder
	if FileExists(repoDataFolder) && !FileExists(repoDBFile) {
		return nil, fmt.Errorf("data folder exists but database is missing within %s - please choose a folder that is either empty or a fully-initialized timeline", repo)
	}

	db, err := openAndProvisionDB(repo)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	return openTimeline(repo, cache, db)
}

func openTimeline(repo, cache string, db *sql.DB) (*Timeline, error) {
	repoMarkerFile := filepath.Join(repo, MarkerFilename)

	var err error
	defer func() {
		if err != nil {
			Log.Warn("closing database due to error when opening timeline", zap.Error(err))
			db.Close()
		}
	}()

	id, err := loadRepoID(db)
	if err != nil {
		return nil, fmt.Errorf("loading repo ID: %w", err)
	}

	// create marker file; for informational purposes only
	if !FileExists(repoMarkerFile) {
		timelineMarkerFileContents := strings.ReplaceAll(timelineMarkerContents, "{{repo_id}}", id.String())
		err = os.WriteFile(repoMarkerFile, []byte(timelineMarkerFileContents), 0644)
		if err != nil {
			return nil, fmt.Errorf("writing marker file: %w", err)
		}
	}

	// load data source IDs, item classification IDs, and entity type IDs into map by name; we use these often
	dbDataSources, err := mapNamesToIDs(db, "data_sources")
	if err != nil {
		return nil, fmt.Errorf("mapping entity types names to IDs: %v", err)
	}
	classes, err := mapNamesToIDs(db, "classifications")
	if err != nil {
		return nil, fmt.Errorf("mapping classification names to IDs: %v", err)
	}
	entityTypes, err := mapNamesToIDs(db, "entity_types")
	if err != nil {
		return nil, fmt.Errorf("mapping entity types names to IDs: %v", err)
	}
	relations, err := mapNamesToIDs(db, "relations")
	if err != nil {
		return nil, fmt.Errorf("mapping entity types names to IDs: %v", err)
	}

	// in case of unclean shutdown last time, set all imports that are on "started" status to "aborted"
	// (no imports can be running currently since we haven't finished opening the timeline yet)
	_, err = db.Exec(`UPDATE imports SET status='abort' WHERE status='started'`)
	if err != nil {
		return nil, fmt.Errorf("resetting all uncleanly-stopped imports to 'abort' status: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	tl := &Timeline{
		ctx:             ctx,
		cancel:          cancel,
		repoDir:         repo,
		cacheDir:        cache,
		rateLimiters:    make(map[int64]RateLimit),
		id:              id,
		db:              db,
		dataSources:     dbDataSources,
		classifications: classes,
		entityTypes:     entityTypes,
		relations:       relations,
	}

	// if thumbnail cache does not exist, start building cache
	// (this is useful after clearing cache or opening the repo on
	// a different file system for the first time)
	if _, err := os.Stat(thumbnailDir(cache, id.String())); errors.Is(err, fs.ErrNotExist) {
		go func() {
			Log.Info("thumbnail cache not found; regenerating")
			if err := tl.regenerateAllThumbnails(); err != nil {
				Log.Error("generating thumbnails", zap.Error(err))
			}
		}()
	}

	// start maintenance goroutine; this erases items that have been
	// deleted and have fulfilled their retention period
	go tl.maintenanceLoop()

	return tl, nil
}

func mapNamesToIDs(db *sql.DB, table string) (map[string]int64, error) {
	nameCol := "name"
	if table == "relations" {
		nameCol = "label" // TODO: this is annoying... right?
	}
	rows, err := db.Query(`SELECT id, ` + nameCol + ` FROM ` + table)
	if err != nil {
		return nil, fmt.Errorf("querying %s table: %v", table, err)
	}
	defer rows.Close()

	namesToIDs := make(map[string]int64)
	for rows.Next() {
		var rowID int64
		var name string
		err := rows.Scan(&rowID, &name)
		if err != nil {
			return nil, fmt.Errorf("scanning: %v", err)
		}
		namesToIDs[name] = rowID
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating rows: %v", err)
	}

	return namesToIDs, nil
}

// Close frees up resources allocated from Open.
func (t *Timeline) Close() error {
	for key, rl := range t.rateLimiters {
		if rl.ticker != nil {
			rl.ticker.Stop()
			rl.ticker = nil
		}
		delete(t.rateLimiters, key) // TODO: maybe racey?
	}
	t.cancel() // cancel this timeline's context, so anything waiting on it knows we're closing
	if t.db != nil {
		t.dbMu.Lock()
		defer t.dbMu.Unlock()
		return t.db.Close()
	}
	return nil
}

// Empty returns true if there are no entities in the timeline. (TODO: Can we remember once it's not empty, to avoid repeated queries?)
func (t *Timeline) Empty() bool {
	t.dbMu.RLock()
	defer t.dbMu.RUnlock()
	err := t.db.QueryRow("SELECT id FROM entities LIMIT 1").Scan()
	return err == sql.ErrNoRows
}

func (tl *Timeline) storeRelationship(ctx context.Context, tx *sql.Tx, rel rawRelationship) error {
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO relations (label, directed, subordinating) VALUES (?, ?, ?)`,
		rel.Label, rel.Directed, rel.Subordinating)
	if err != nil {
		return fmt.Errorf("inserting relation: %v (rawRelationship=%s)", err, rel)
	}

	// we don't use "RETURNING id" because I think that doesn't return anything if the OR IGNORE clause is invoked
	var relID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM relations WHERE label=? LIMIT 1`, rel.Label).Scan(&relID)
	if err != nil {
		return fmt.Errorf("loading relation ID: %v (rawRelationship=%s)", err, rel)
	}

	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO relationships
		(relation_id, value,
			from_item_id, from_attribute_id,
			to_item_id, to_attribute_id,
			start, end, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, relID, rel.value,
		rel.fromItemID, rel.fromAttributeID,
		rel.toItemID, rel.toAttributeID,
		rel.start, rel.end, rel.metadata,
	)
	if err != nil {
		return fmt.Errorf("inserting relationship: %v (relationID=%d rawRelationship=%s)", err, relID, rel)
	}

	return nil
}

func (tl *Timeline) entityTypeNameToID(name string) (int64, error) {
	tl.cachesMu.RLock()
	id, ok := tl.entityTypes[name]
	tl.cachesMu.RUnlock()
	if ok {
		return id, nil
	}

	// might be new or one we haven't seen yet
	tl.dbMu.RLock()
	err := tl.db.QueryRow(`SELECT id FROM entity_types WHERE name=? LIMIT 1`, name).Scan(&id)
	tl.dbMu.RUnlock()
	if err != nil {
		return 0, err
	}

	tl.cachesMu.Lock()
	tl.entityTypes[name] = id
	tl.cachesMu.Unlock()

	return id, nil
}

func (tl *Timeline) classificationNameToID(name string) (int64, error) {
	tl.cachesMu.RLock()
	id, ok := tl.classifications[name]
	tl.cachesMu.RUnlock()
	if ok {
		return id, nil
	}

	// might be new or one we haven't seen yet
	tl.dbMu.RLock()
	err := tl.db.QueryRow(`SELECT id FROM classifications WHERE name=? LIMIT 1`, name).Scan(&id)
	tl.dbMu.RUnlock()
	if err != nil {
		return 0, err
	}

	tl.cachesMu.Lock()
	tl.classifications[name] = id
	tl.cachesMu.Unlock()

	return id, err
}

func (tl *Timeline) ItemClassifications() ([]Classification, error) {
	tl.dbMu.RLock()
	defer tl.dbMu.RUnlock()

	rows, err := tl.db.Query("SELECT id, standard, name, labels, description FROM classifications")
	if err != nil {
		return nil, fmt.Errorf("querying classifications: %v", err)
	}
	defer rows.Close()

	var results []Classification
	for rows.Next() {
		var c Classification
		var labels string
		err := rows.Scan(&c.id, &c.Standard, &c.Name, &labels, &c.Description)
		if err != nil {
			return nil, fmt.Errorf("scanning: %v", err)
		}
		c.Labels = strings.Split(labels, ",")
		results = append(results, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating rows: %v", err)
	}

	return results, nil
}

func (tl *Timeline) StoreEntity(ctx context.Context, entity Entity) error {
	tl.normalizeEntity(&entity)

	metaStr, err := entity.metadataString()
	if err != nil {
		return err
	}

	tl.dbMu.Lock()
	defer tl.dbMu.Unlock()

	tx, err := tl.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	err = tx.QueryRow(`INSERT INTO entities (type_id, name, metadata) VALUES (?, ?, ?) RETURNING id`,
		entity.typeID, entity.Name, metaStr).Scan(&entity.ID)
	if err != nil {
		return fmt.Errorf("inserting entity: %v", err)
	}

	for _, attr := range entity.Attributes {
		if _, err := storeLinkBetweenEntityAndNonIDAttribute(ctx, tx, entity.ID, attr); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// storeLinkBetweenEntityAndNonIDAttribute simply stores a row in entity_attributes that
// links the entity with the attribute. The link has no data source ID associated, so it
// is not an identity attribute. It just ensures that the attribute is linked with the
// existing entity. If the attribute does not yet exist it will be created. The ID of
// the attribute is returned.
func storeLinkBetweenEntityAndNonIDAttribute(ctx context.Context, tx *sql.Tx, entityID int64, attr Attribute) (int64, error) {
	attrID, err := storeAttribute(ctx, tx, attr)
	if err != nil {
		return 0, err
	}

	// we can't use a UNIQUE constraint here because that requires a data source ID
	// (you can imagine that an entity can use the same attribute to identify at
	// multiple data sources, like their email address for example) - so we have to
	// check a count ourselves before inserting
	var count int
	err = tx.QueryRow(`SELECT count() FROM entity_attributes WHERE entity_id=? AND attribute_id=? LIMIT 1`,
		entityID, attrID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("querying to see if entity_attribute row already exists: %v", err)
	}

	if count == 0 {
		_, err = tx.Exec(`INSERT INTO entity_attributes (entity_id, attribute_id) VALUES (?, ?)`,
			entityID, attrID)
		if err != nil {
			return attrID, fmt.Errorf("linking attribute %d to entity %d: %v", entityID, attrID, err)
		}
	}

	return attrID, nil
}

func (tl *Timeline) LoadEntity(id int64) (Entity, error) {
	p := Entity{ID: id}

	tl.dbMu.RLock()
	defer tl.dbMu.RUnlock()

	tx, err := tl.db.Begin()
	if err != nil {
		return p, err
	}
	defer tx.Rollback()

	err = tx.QueryRow(`SELECT entity_types.name, entities.type_id, entities.name, entities.picture_file
		FROM entities, entity_types
		WHERE entities.id=? AND entity_types.id = entities.type_id
		LIMIT 1`, id).Scan(&p.Type, &p.typeID, &p.name, &p.Picture)
	if err != nil {
		return p, err
	}

	if p.name != nil {
		p.Name = *p.name
	}

	rows, err := tx.Query(`SELECT attributes.name, attributes.value, attributes.alt_value, attributes.metadata, entity_attributes.data_source_id
		FROM attributes, entity_attributes
		WHERE entity_attributes.entity_id=?
			AND attributes.id = entity_attributes.attribute_id`, id)
	if err != nil {
		return p, fmt.Errorf("querying attributes: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var attr Attribute
		var dsID *int64
		var altValue, meta *string

		err := rows.Scan(&attr.Name, &attr.Value, &altValue, &meta, &dsID)
		if dsID != nil {
			attr.Identity = true
			attr.Identifying = true
		}
		if err != nil {
			return p, fmt.Errorf("scanning: %v", err)
		}

		if altValue != nil {
			attr.AltValue = *altValue
		}
		if meta != nil {
			err := json.Unmarshal([]byte(*meta), &attr.Metadata)
			if err != nil {
				return p, fmt.Errorf("loading metadata: %v", err)
			}
		}

		p.Attributes = append(p.Attributes, attr)
	}
	if err := rows.Err(); err != nil {
		return p, fmt.Errorf("iterating attribute rows: %v", err)
	}

	// TODO: why would we need to commit a read-only tx?
	return p, tx.Commit()
}

// DeleteOptions configures how to perform a delete.
type DeleteOptions struct {
	Remember     bool           `json:"remember,omitempty"`
	Retain       *time.Duration `json:"retain,omitempty"` // if not specified, use global default
	PreserveNote bool           `json:"preserve_note,omitempty"`
	Subtrees     bool           `json:"subtrees,omitempty"` // TODO: probably a good idea for the UI to make this the default
}

// DeleteItems deletes data from the items table with the given row IDs, according to the given deletion options.
// If a retention period is configured, it marks items for erasure; otherwise it erases them right away.
func (tl *Timeline) DeleteItems(ctx context.Context, itemRowIDs []int64, options DeleteOptions) error {
	if len(itemRowIDs) == 0 {
		return nil
	}

	if options.Retain == nil {
		// TODO: get globally-configured default retention period; for now just hard-coded here
		defaultRetention := time.Hour * 24 * 90
		options.Retain = &defaultRetention
	}
	retention := *options.Retain
	if retention < 0 {
		return fmt.Errorf("invalid retention period: %s", retention)
	}

	tl.dbMu.Lock()
	defer tl.dbMu.Unlock()

	tx, err := tl.db.Begin()
	if err != nil {
		return fmt.Errorf("beginning transaction: %v", err)
	}
	defer tx.Rollback()

	// add row IDs of items that are related, if configured
	if options.Subtrees {
		itemRowIDs, err = tl.followItemSubtrees(ctx, tx, itemRowIDs)
		if err != nil {
			return fmt.Errorf("recursively expanding item subtree: %v", err)
		}
	}

	// using the row IDs, prepare to query for associated data files to delete
	rowIDArray, rowIDArgs := sqlArray(itemRowIDs)
	var dataFilesToDelete []string

	// we only need to query each item if we are either remembering it (have to compute its hash)
	// or if we are deleting it immediately (have to see if the data file is referenced by other items)
	if options.Remember || retention == 0 {
		for _, rowID := range itemRowIDs {
			// get the item
			ir, err := tl.loadItemRow(ctx, tx, rowID, nil, nil, nil, false)
			if err != nil {
				return fmt.Errorf("could not load item to delete: %v", err)
			}

			// if not remembering, clear its row hashes
			if !options.Remember {
				_, err = tx.ExecContext(ctx, "UPDATE items SET original_id_hash=NULL AND initial_content_hash=NULL WHERE id=?", rowID) // TODO: Limit 1?
				if err != nil {
					return fmt.Errorf("unable to clear hashes to forget item deletion: %v", err)
				}
			}

			// see if any other items not being deleted now refer to the same data file; if not, we can delete the data file
			if retention == 0 && ir.DataFile != nil && *ir.DataFile != "" {
				var count int
				err := tx.QueryRow(`SELECT count() FROM items WHERE id NOT IN `+rowIDArray+` AND data_file=? LIMIT 1`,
					append(rowIDArgs, *ir.DataFile)...).Scan(&count)
				if err != nil {
					return fmt.Errorf("counting rows that share data file: %v", err)
				}
				if count == 0 {
					dataFilesToDelete = append(dataFilesToDelete, *ir.DataFile)
				}
			}
		}
	}

	// if no retention period, delete right away
	if retention == 0 {
		// zero-out the item rows
		err := tl.deleteDataInItemRows(tl.ctx, tx, itemRowIDs, false)
		if err != nil {
			return fmt.Errorf("erasing deleted items (before deleting data files): %v", err)
		}

		// commit transaction which deletes each item info first (so the DB is the source
		// of truth), then we'll delete all their data files that aren't referenced anymore
		if err = tx.Commit(); err != nil {
			return fmt.Errorf("commiting transaction (no data files have been deleted yet): %v", err)
		}

		// delete data files only if they are no longer referenced by any items
		numFilesDeleted, err := tl.deleteDataFiles(tl.ctx, Log, dataFilesToDelete)
		if err != nil {
			Log.Error("error when deleting data files of erased items (items have already been marked as deleted in DB)", zap.Error(err))
		}

		// delete thumbnails, if present
		for _, itemID := range itemRowIDs {
			for _, format := range []ThumbnailType{ImageThumbnail, VideoThumbnail} {
				thumbPath := tl.ThumbnailPath(itemID, format)
				err = os.Remove(thumbPath)
				if err != nil && !errors.Is(err, fs.ErrNotExist) {
					Log.Error("unable to delete thumbnail file for immediately-erased item",
						zap.Int64("item_id", itemID),
						zap.String("thumbnail_file", thumbPath),
						zap.Error(err))
				}
			}
		}

		Log.Info("erased deleted items",
			zap.Int("count", len(itemRowIDs)),
			zap.Int("deleted_data_files", numFilesDeleted))

		// deletion is completion :D
		return nil
	}

	// non-zero retention period; mark all the items as deleted with a
	// future timestamp after which the data will be permanently erased

	deleteAt := time.Now().Add(retention)

	_, err = tx.ExecContext(ctx, "UPDATE items SET deleted=? WHERE id IN "+rowIDArray,
		append([]any{deleteAt.Unix()}, rowIDArgs...)...)
	if err != nil {
		return fmt.Errorf("marking items as deleted: %v", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %v", err)
	}

	Log.Info("marked item(s) for deletion",
		zap.Int64s("ids", itemRowIDs),
		zap.String("retention_period", retention.String()),
		zap.Time("deletion_scheduled", deleteAt))

	return nil
}

func (tl *Timeline) followItemSubtrees(ctx context.Context, tx *sql.Tx, rowIDs []int64) ([]int64, error) {
	startingLen := len(rowIDs)

	rowIDArray, rowIDArgs := sqlArray(rowIDs)

	// this query filters duplicates so we don't potentially go in circles forever
	rowIDArgs = append(rowIDArgs, rowIDArgs...)
	rows, err := tx.QueryContext(ctx, `SELECT to_item_id FROM relationships, relations
		WHERE relations.id = relationships.relation_id
			AND relations.directed=true
			AND from_item_id IN `+rowIDArray+`
			AND to_item_id NOT IN `+rowIDArray,
		rowIDArgs...)
	if err != nil {
		return nil, fmt.Errorf("querying for subtree item IDs: %v", err)
	}

	for rows.Next() {
		var id int64
		err := rows.Scan(&id)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("scanning subtree item ID: %v", err)
		}
		rowIDs = append(rowIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating subtree item rows: %v", err)
	}

	// recursively add row IDs by following item relationships;
	// our base case is when there are no new IDs added
	if len(rowIDs) > startingLen {
		rowIDs, err = tl.followItemSubtrees(ctx, tx, rowIDs)
		if err != nil {
			return nil, fmt.Errorf("recursively gathering subtree row IDs: %v", err)
		}
	}

	return rowIDs, nil
}

// sqlArray builds a placeholder array for SQL queries that will have
// all the row IDs in it, e.g. "(?, ?, ?)" for use with 'IN' clauses,
// returning the array as a string and also the values to pass in.
func sqlArray(rowIDs []int64) (string, []any) {
	var sb strings.Builder
	rowIDArgs := make([]any, 0, len(rowIDs))
	sb.WriteRune('(')
	for i, rowID := range rowIDs {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteRune('?')
		rowIDArgs = append(rowIDArgs, rowID)
	}
	sb.WriteRune(')')
	return sb.String(), rowIDArgs
}

// Valid returns true if an initialized timeline exists at repo. It is not a
// super-thorough check, but it does look for a couple of key factors: database
// file existence, and a table and value within the database.It returns an
// error only if it is unable to assess whether a valid timeline exists.
func Valid(repo string) (bool, error) {
	db, err := openDB(repo)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("opening database: %w", err)
	}
	// load and parse the timeline's UUID as a sanity check
	_, err = loadRepoID(db)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("database does not contain valid repo UUID: %w", err)
	}
	return true, nil
}

type concurrentCuckoo struct {
	*cuckoo.Filter
	*sync.Mutex
}

// FileExists returns true if file exists.
func FileExists(file string) bool {
	_, err := os.Stat(file)
	return err == nil || errors.Is(err, fs.ErrPermission)
}

// FileExistsFS returns true if name exists in fsys.
func FileExistsFS(fsys fs.FS, name string) bool {
	_, err := fs.Stat(fsys, name)
	return err == nil || errors.Is(err, fs.ErrPermission)
}

// StringData makes it easy to return a simple string as an item's data.
func StringData(data string) func(_ context.Context) (io.ReadCloser, error) {
	return func(_ context.Context) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(data)), nil
	}
}

// ByteData makes it easy to return a simple byte array as an item's data.
func ByteData(data []byte) func(_ context.Context) (io.ReadCloser, error) {
	return func(_ context.Context) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	}
}

// DownloadData returns a function that opens the response body for the given url.
func DownloadData(ctx context.Context, url string) DataFunc {
	return func(ctx context.Context) (io.ReadCloser, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		return resp.Body, err
	}
}

// ctxKey is used for contexts, as recommended by
// https://golang.org/pkg/context/#WithValue. It
// is unexported so values stored by this package
// can only be accessed by this package.
type ctxKey string

// processorCtxKey is how the context value is accessed.
var processorCtxKey ctxKey = "processor"

// // Checkpoint saves a checkpoint for the processing associated
// // with the provided context. It overwrites any previous
// // checkpoint. Any errors are logged.
// // TODO: get proper logger
// func checkpoint(ctx context.Context, checkpoint any) {
// 	proc, ok := ctx.Value(processorCtxKey).(*processor)
// 	if !ok {
// 		log.Printf("[ERROR]Checkpoint function not available; got type %T (%#v)",
// 			proc, proc)
// 		return
// 	}

// 	chkpt, err := MarshalGob(Checkpoint{proc.commandParams, checkpoint})
// 	if err != nil {
// 		log.Printf("[ERROR] Encoding checkpoint wrapper: %v", err)
// 		return
// 	}

// 	_, err = proc.tl.db.Exec(`UPDATE imports SET checkpoint=? WHERE id=?`, // TODO: LIMIT 1 (see https://github.com/mattn/go-sqlite3/pull/564)
// 		chkpt, proc.imp.ID)
// 	if err != nil {
// 		log.Printf("[ERROR] Checkpoint: %v", err)
// 		return
// 	}
// }

// TODO: update godoc
// checkpointWrapper stores a provider's checkpoint along with the
// parameters of the command that initiated the process; the checkpoint
// will only be loaded and restored to the provider on next run if
// the parameters match, because it doesn't make sense to restore a
// process that has different, potentially conflicting, parameters,
// such as timeframe.
type checkpoint struct {
	Filenames []string
	ProcOpt   ProcessingOptions
	Data      any // provided by, and passed back into, the data source
}

// ProcessingOptions configures how item processing is carried out.
type ProcessingOptions struct {
	GetLatest      bool      `json:"get_latest,omitempty"`
	Prune          bool      `json:"prune,omitempty"`
	Integrity      bool      `json:"integrity,omitempty"`
	Timeframe      Timeframe `json:"timeframe,omitempty"`
	KeepEmptyItems bool      `json:"keep_empty_items,omitempty"` // TODO: not used?

	// If true, items with manual modifications may be updated, overwriting local changes.
	OverwriteModifications bool `json:"overwrite_modifications,omitempty"`

	// Names of columns in the items table to check for sameness when loading an item
	// that doesn't have data_source+original_id. The field/column is the same if the
	// values are identical or if one of the values is NULL. If the map value is true,
	// however, strict NULL comparison is applied, where NULL=NULL only.
	ItemUniqueConstraints map[string]bool `json:"item_unique_constraints,omitempty"`

	// The policies to apply when updating an item in the DB, specified per-field.
	// Note: Some fields are described in aggregate, such as data and location.
	ItemFieldUpdates map[string]fieldUpdatePolicy `json:"item_field_updates,omitempty"`
}

func (po ProcessingOptions) IsEmpty() bool {
	return !po.GetLatest && !po.Prune && !po.Integrity &&
		po.Timeframe.IsEmpty() && !po.KeepEmptyItems &&
		po.ItemUniqueConstraints == nil && po.ItemFieldUpdates == nil
}

// fieldUpdatePolicy values specify how to update a field/column of an item in the DB.
type fieldUpdatePolicy int

const (
	// COALESCE(existing, incoming)
	updatePolicyPreferExisting fieldUpdatePolicy = iota + 1

	// COALESCE(incoming, existing)
	updatePolicyPreferIncoming

	// SET existing=incoming
	// (i.e. prefer incoming even if incoming is NULL)
	updatePolicyOverwriteExisting

	// TODO: choose one based on properties of the item? like larger or smaller one, etc... (e.g. if we want to prefer the higher-quality photo...)
)

// ListingOptions specifies parameters for listing items
// from a data source. Some data sources might not be
// able to honor all fields.
// TODO: rename to ImportOptions probably?
type ListingOptions struct {
	// The logger to use.
	Log *zap.Logger

	// Time bounds on which data to retrieve.
	// The respective time and item ID fields
	// which are set must never conflict.
	Timeframe Timeframe

	// A checkpoint from which to resume
	// item retrieval.
	Checkpoint any

	// Options specific to the data source,
	// as provided by NewOptions.
	DataSourceOptions any

	// Maximum number of items to list; useful
	// for previews. Data sources should not
	// checkpoint previews.
	// TODO: still should enforce this in the processor... but this is good for the DS to know too, so it can limit its API calls, for example
	MaxItems int
}

// Files belonging at the root within the timeline repository.
const (
	// The name of the database file.
	DBFilename = "timeline.db"

	// The folder containing data files.
	DataFolderName = "data"

	// The folder containing related assets, but not core timeline data
	// (for example, profile pictures, etc).
	AssetsFolderName = "assets"

	// An optional file that is placed for informational purposes only.
	MarkerFilename = "timelinize_repo.txt"
)

const timelineMarkerContents = `This folder is a Timelinize repository.

It contains a timeline, which consists of a database and associated data
files. Timelines are portable, so you can move this folder around as needed
as long as its structure is preserved. Files should not be added, removed,
or edited, as this can corrupt the index or throw the database out of sync.
Only use Timelinize to make changes.

Timelines may contain private, sensitive, or personal information, so be
careful how these files are shared.

For more information, visit https://timelinize.com.

Repo ID: {{repo_id}}
`
