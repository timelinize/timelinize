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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"time"

	"github.com/mholt/archives"
)

// DataSource has information about a
// data source that can be registered.
type DataSource struct {
	// A snake_cased name of the service
	// that uniquely identifies it from
	// all others. This is NOT the same
	// primary key used in the DB.
	Name string `json:"name"`

	// The human-readable or brand name of
	// the service.
	Title string `json:"title"`

	// The name of the image representing
	// this data source, relative to the
	// frontend/resources/images/data-sources
	// folder.
	// TODO: If we could get all icons to the same format (svg, ideally) we could remove this
	Icon string `json:"icon"`

	// Information that will help the user when choosing a data source.
	Description string `json:"description"`

	NewOptions func() any `json:"-"`

	NewFileImporter func() FileImporter `json:"-"`
	NewAPIImporter  func() APIImporter  `json:"-"`

	// // TODO: a way to declare what this data source needs, like SMS backup & restore needs the person_identity for the user this came from (their phone number)
	// // TODO: Maybe, if this is set, then we presume the data source requires a person identity to start with.
	// NewIdentity func(input Person, dataSourceOptions any) (Person, error) `json:"-"`
}

// UnmarshalOptions unmarshals the data source options into the data source's options type.
func (ds DataSource) UnmarshalOptions(jsonOpt json.RawMessage) (any, error) {
	if ds.NewOptions == nil {
		return nil, nil
	}
	dsOpt := ds.NewOptions()
	if len(jsonOpt) == 0 {
		return dsOpt, nil
	}
	err := json.Unmarshal(jsonOpt, &dsOpt)
	if err != nil {
		return nil, fmt.Errorf("decoding data source options: %w", err)
	}
	return dsOpt, nil
}

// // authFunc gets the authentication function for this
// // service. If s.Authenticate is set, it returns that;
// // if s.OAuth2 is set, it uses a standard OAuth2 func.
// func (ds DataSource) authFunc() AuthenticateFn {
// 	if ds.Authenticate != nil {
// 		return ds.Authenticate
// 	} else if ds.OAuth2.ProviderID != "" {
// 		return func(ctx context.Context, userID string, dataSourceOptions any) ([]byte, error) {
// 			return authorizeWithOAuth2(ctx, ds.OAuth2)
// 		}
// 	}
// 	return nil
// }

// RegisterDataSource registers ds as a data source.
func RegisterDataSource(ds DataSource) error {
	if ds.Name == "" {
		return errors.New("missing ID")
	}
	if ds.Title == "" {
		return errors.New("missing title")
	}

	// register the data source
	if _, ok := dataSources[ds.Name]; ok {
		return fmt.Errorf("data source already registered: %s", ds.Name)
	}
	dataSources[ds.Name] = ds

	return nil
}

// GetDataSource gets the data source with the given name (not database row ID).
func GetDataSource(name string) (DataSource, error) {
	for _, ds := range dataSources {
		if ds.Name == name {
			return ds, nil
		}
	}
	return DataSource{}, fmt.Errorf("data source not found: %s", name)
}

// AllDataSources returns all registered data sources sorted by ID strings.
func AllDataSources() []DataSource {
	sources := make([]DataSource, 0, len(dataSources))
	for _, ds := range dataSources {
		sources = append(sources, ds)
	}
	sort.Slice(sources, func(i, j int) bool {
		return sources[i].Name < sources[j].Name
	})
	return sources
}

// TODO: WIP...
type RecognizeParams struct {
}

// DataSourceRecognition stores the result of whether a data source recognizes an input.
type DataSourceRecognition struct {
	DataSource DataSource `json:"data_source"`
	Recognition
}

// Recognition is a type that indicates how well, if at all, an importer
// recognized or supports an input, as well as any relevant information
// regarding the data set that may be useful later or for storage.
type Recognition struct {
	// TODO: rename to Score? Actually, just make a bool? Why would a source ever be unsure? (they shouldn't be traversing direcctories anyway)
	Confidence float64 `json:"confidence"`

	// If > 0, and the data source matches this much of all the entries
	// in a directory (sans hidden files), assign the entire directory
	// to the data source; 0 <= DirThreshold <= 1
	DirThreshold float64 `json:"dir_threshold,omitempty"`

	// Optional; TODO: used?
	SnapshotDate *time.Time `json:"snapshot_date,omitempty"`
}

// DirEntry is a fs.DirEntry that represents a directory entry (file
// or folder), and also carries the associated file system it came
// from, the filename of the file (within the FS), and the root path
// of the OS (as an OS-compatible filepath).
type DirEntry struct {
	fs.DirEntry

	// FS is a file system that can be used to access the
	// file represented by this DirEntry. During Recognize,
	// it is rooted at the file itself. During import, it
	// may be rooted at the file itself (if it's a directory)
	// or at the parent folder (if it's a file). Use
	// the Filename field to get the path of the DirEntry
	// within the FS.
	FS fs.FS

	// FSRoot is the root of the FS (OS-compatible filepath).
	FSRoot string

	// Filename is the name of the file in the FS represented
	// by this DirEntry. It is the fs.FS-compatible path that
	// can and should be used to access the file within the
	// associated FS. Thus, it is not an OS path, it has no
	// root component (no leading "/" or drive letter, for
	// example), may be either a directory or a file, and may
	// be "." or a true  filename. To always get the true
	// filename, use the Name() method. (TODO: Verify this)
	Filename string
}

func (d DirEntry) Open() (fs.File, error) { return d.FS.Open(d.Filename) }

// TODO: see if we can get by without the data sources needing to call these (shouldn't our new recursive importer be able to choose the right directory?)

func (d DirEntry) TopDirOpen(filename string) (fs.File, error) {
	return archives.TopDirOpen(d.FS, path.Join(d.Filename, filename))
}

func (d DirEntry) TopDirStat(filename string) (fs.FileInfo, error) {
	return archives.TopDirStat(d.FS, path.Join(d.Filename, filename))
}

func (d DirEntry) TopDirReadDir(filename string) ([]fs.DirEntry, error) {
	return archives.TopDirReadDir(d.FS, path.Join(d.Filename, filename))
}

// FullPath returns the full path of the directory entry, including the FS
// root (if a known FS type from the archives package), and the filename
// in the archive.
func (d DirEntry) FullPath() string {
	var root string
	switch fsys := d.FS.(type) {
	case archives.FileFS:
		root = fsys.Path
	case archives.DirFS:
		root = string(fsys)
	case *archives.ArchiveFS:
		root = fsys.Path
	case *archives.DeepFS:
		root = fsys.Root
	}
	return filepath.Join(root, filepath.FromSlash(d.Filename))
}

// DataSourcesRecognize returns the list of data sources that reportedly
// recognize the file described by the DirEntry.
func DataSourcesRecognize(ctx context.Context, entry DirEntry, opts RecognizeParams) ([]DataSourceRecognition, error) {
	var results []DataSourceRecognition
	tryDataSource := func(ctx context.Context, ds DataSource) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if ds.NewFileImporter == nil {
			return nil
		}
		result, err := ds.NewFileImporter().Recognize(ctx, entry, opts)
		if err != nil {
			return fmt.Errorf("%s: %w", ds.Name, err)
		}
		if result.Confidence > 0 {
			results = append(results, DataSourceRecognition{ds, result})
		}
		return nil
	}

	const maxDur = 120 * time.Second
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, maxDur)
	defer cancel()

	for _, ds := range dataSources {
		if ds.Name == "generic" {
			continue // this is only a special fallback data source, to be applied in special cases elsewhere
		}
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("trying data sources: %w", err)
		}
		if err := tryDataSource(ctx, ds); err != nil {
			return nil, err
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Confidence < results[j].Confidence
	})

	return results, nil
}

// OAuth2 defines which OAuth2 provider a service
// uses and which scopes it requires.
type OAuth2 struct {
	// The ID of the service must be recognized
	// by the OAuth2 app configuration.
	ProviderID string `json:"provider_id,omitempty"`

	// The list of scopes to ask for during auth.
	Scopes []string `json:"scopes,omitempty"`
}

// TODO: unused?
// AuthenticateFn is a function that authenticates userID with a service.
// It returns the authorization or credentials needed to operate. The return
// value should be byte-encoded so it can be stored in the DB to be reused.
// To store arbitrary types, encode the value as a gob, for example.
type AuthenticateFn func(ctx context.Context, userID string, dataSourceOptions any) ([]byte, error)

// // NewClientFn is a function that returns a client which, given
// // the account passed in, can interact with a service provider.
// // It must honor context cancellation if there are any async calls.
// type NewClientFn func(ctx context.Context, acc Account, dataSourceOptions any) (Client, error)

// // Client is a type that can interact with a data source.
// type Client interface {
// 	// ListItems lists the items on the account. Items should be
// 	// sent on itemChan as they are discovered, but related items
// 	// should be combined onto a single ItemGraph so that their
// 	// relationships can be stored. If the relationships are not
// 	// discovered until later, that's OK: item processing is
// 	// idempotent, so repeating an item from earlier should have
// 	// no adverse effects.
// 	//
// 	// Implementations must honor the context's cancellation. If
// 	// ctx.Done() is closed, the function should return. Typically,
// 	// this is done by having an outer loop select over ctx.Done()
// 	// and default, where the next page or set of items is handled
// 	// in the default case.
// 	//
// 	// ListItems MUST close itemChan when returning. A
// 	// `defer close(itemChan)` will usually suffice. Closing
// 	// this channel signals to the processing goroutine that
// 	// no more items are coming.
// 	//
// 	// Further options for listing items may be passed in opt.
// 	//
// 	// If opt.Filename is specified, the implementation is expected
// 	// to open and list items from that file. If this is not
// 	// supported, an error should be returned. Conversely, if a
// 	// filename is not specified but required, an error should be
// 	// returned.
// 	//
// 	// opt.Timeframe consists of two optional timestamp and/or item
// 	// ID values. If set, item listings should be bounded in the
// 	// respective direction by that timestamp / item ID. (Items
// 	// are assumed to be part of a chronology; both timestamp and
// 	// item ID *may be* provided, when possible, to accommodate
// 	// data sources which do not constrain by timestamp but which
// 	// do by item ID instead.) The respective time and item ID
// 	// fields, if set, will not be in conflict, so either may be
// 	// used if both are present. While it should be documented if
// 	// timeframes are not supported, an error need not be returned
// 	// if they cannot be honored.
// 	//
// 	// opt.Checkpoint consists of the last checkpoint for this
// 	// account if the last call to ListItems did not finish and
// 	// if a checkpoint was saved. If not nil, the checkpoint
// 	// should be used to resume the listing instead of starting
// 	// over from the beginning. Checkpoint values usually consist
// 	// of page tokens or whatever state is required to resume. Call
// 	// timeline.Checkpoint to set a checkpoint. Checkpoints are not
// 	// required, but if the implementation sets checkpoints, it
// 	// should be able to resume from one, too.
// 	ListItems(ctx context.Context, itemChan chan<- *ItemGraph, opt ListingOptions) error
// }

// Timeframe represents a start and end time and/or
// a start and end item, where either value could be
// nil which means unbounded in that direction.
// When items are used as the timeframe boundaries,
// the ItemID fields will be populated. It is not
// guaranteed that any particular field will be set
// or unset just because other fields are set or unset.
// However, if both Since or both Until fields are
// set, that means the timestamp and items are
// correlated; i.e. the Since timestamp is (approx.)
// that of the item ID. Or, put another way: there
// will never be conflicts among the fields which
// are non-nil.
//
// A Contains method is provided to determine if a
// time is within the timeframe, but because item IDs
// are opaque strings, the respective data sources
// are the only ones that can interpret their IDs and
// determine if item IDs are within the timeframe.
// (Most data sources use times, not item IDs, to
// constrain time anyway.)
//
// Since ~= "After", Until ~= "Before"
type Timeframe struct {
	Since *time.Time `json:"since,omitempty"`
	Until *time.Time `json:"until,omitempty"`

	// TODO: where are we actually enforcing these? are these still useful? (I think we used it for Twitter API results or maybe just any paginated API results IIRC?)
	SinceItemID *string `json:"since_item_id,omitempty"`
	UntilItemID *string `json:"until_item_id,omitempty"`
}

// IsEmpty returns true if the timeframe is not set in any way.
func (tf Timeframe) IsEmpty() bool {
	return tf.Since == nil && tf.Until == nil && tf.SinceItemID == nil && tf.UntilItemID == nil
}

func (tf Timeframe) String() string {
	var sinceItemID, untilItemID string
	if tf.SinceItemID != nil {
		sinceItemID = *tf.SinceItemID
	}
	if tf.UntilItemID != nil {
		untilItemID = *tf.UntilItemID
	}
	return fmt.Sprintf("{Since:%s Until:%s SinceItemID:%s UntilItemID:%s}",
		tf.Since, tf.Until, sinceItemID, untilItemID)
}

// Contains returns true if the given time ts is inside the timeframe tf.
// Only tf.Since and tf.Until are used; tf.SinceItemID and tf.UntilItemID
// are ignored.
//
// A zero-value timestamp is considered to be in all timeframes. TODO: It's so that we don't omit items from the timeline... Is that surprising though?
//
// If both Since and Until are set, then the time must be between those
// two times. If only Since is set, the time must be after Since. If only
// Until is set, the time must be before Until. If neither are set, true
// is always returned.
func (tf Timeframe) Contains(ts time.Time) bool {
	if ts.IsZero() {
		return true
	}
	afterSince := tf.Since == nil || ts.After(*tf.Since)
	beforeUntil := tf.Until == nil || ts.Before(*tf.Until)
	return afterSince && beforeUntil
}

// ContainsItem returns true if the timeframe contains the item,
// according to its timestamp and timespan (start and end) values,
// with respect to strict mode. If strict mode is enabled, both the
// item's timestamp and timespan must be entirely inside the timeframe;
// otherwise, a timeframe is considered to contain an item if part of
// its timespan is within the timeframe.
func (tf Timeframe) ContainsItem(it *Item, strict bool) bool {
	if it == nil {
		return false
	}
	if it.Timestamp.IsZero() {
		return true
	}
	if strict && tf.Since != nil && tf.Until != nil {
		return it.Timestamp.After(*tf.Since) && it.Timespan.Before(*tf.Until)
	}
	afterSince := tf.Since == nil || it.Timestamp.After(*tf.Since)
	beforeUntil := tf.Until == nil || it.Timestamp.Before(*tf.Until)
	return afterSince && beforeUntil
}

// FileImporter is a type that can import data from files or folders.
//
// Implementations MUST treat the input parameters as read-only; i.e.
// the values should not be changed. Doing so will cause bugs.
type FileImporter interface {
	// Recognize determines whether the data source supports the input described by the DirEntry.
	// It should be implemented in a way that is efficient to be called many times on multiple
	// files throughout a walk (pool buffers for reuse, skip work that doesn't need to be done,
	// etc; for example, ignore hidden files).
	//
	// Recognize should perform any combination of 3 primary recognition algorithms:
	//
	// 1. Filename match (fast and easy, but less reliable)
	// 2. Content match (ideally just read a file's header or other small amount; more reliable)
	// 3. Directory structure match (for directories, check for presence of certain files and
	//    possibly verify by reading part of them; i.e. also perform 1 and/or 2 for expected files).
	//
	// Recognize MUST NOT walk/traverse a directory if possible; spot-checking specific known or
	// expected files within it is okay. The walking is already performed by the import planner.
	//
	// To keep import planning quick, opening the DirEntry or any other files in the FS should
	// only happen if necessary, due to the possibility that it is within a compressed tar file,
	// which is not efficient as it requires decompressing potentially most of the archive to
	// find the file to open. File extensions may be a good way to avoid unnecessary Open() calls.
	Recognize(context.Context, DirEntry, RecognizeParams) (Recognition, error)

	// TODO: write godoc. It is expected that context cancellation will be honored (i.e. return
	// if the context has an error or its done channel is closed), and that the function will not
	// return until all sends to the pipeline channel have completed. No more sends may happen
	// after returning, since the receiving goroutines will be terminated (sends would be
	// deadlocked without a receiver) or the pipeline will be closed (sending would cause a
	// panic). SUBTLETY: If spawning goroutines which send to the pipeline, they must also
	// be waited upon to terminate before returning.
	FileImport(context.Context, DirEntry, ImportParams) error
}

// TODO: unused?
// APIImporter is a type that can import data via a remote service API.
type APIImporter interface {
	Authenticate(ctx context.Context, acc Account, dsOpt any) error
	APIImport(context.Context, Account, chan<- *Graph, ImportParams) error
}

// TODO: experimental
type SizeEstimator interface {
	EstimateSize(context.Context, DirEntry, ImportParams) (int, error)
}

var dataSources = make(map[string]DataSource) // keyed by name (not DB row ID)
