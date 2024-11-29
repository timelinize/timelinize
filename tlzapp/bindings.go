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

package tlzapp

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/mholt/archives"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func (a App) fileSelectorRoots() ([]fileSelectorRoot, error) {
	return getFileSelectorRoots()
}

func (a App) getOpenRepositories() []openedTimeline {
	openTimelinesMu.RLock()
	repos := make([]openedTimeline, 0, len(openTimelines))
	for _, otl := range openTimelines {
		// make sure repository still exists after it was opened
		dbFile := filepath.Join(otl.RepoDir, timeline.DBFilename)
		_, err := os.Stat(dbFile)
		if err != nil {
			// huh, it's either gone or we can't access it...
			// so close it; this removes it from our list
			a.log.Error("timeline database no longer found; closing",
				zap.String("repo", otl.InstanceID.String()),
				zap.String("db", dbFile))
			// defer because it needs a write lock on the openTimelinesMu
			defer func() {
				err := a.CloseRepository(otl.InstanceID.String())
				if err != nil {
					a.log.Error("closing repository", zap.Error(err))
				}
			}()
		} else {
			// ok, it's still there
			repos = append(repos, otl)
		}
	}
	openTimelinesMu.RUnlock()
	return repos
}

// openRepository opens the timeline at repoDir as long as it
// is not already open.
func (a *App) openRepository(ctx context.Context, repoDir string, create bool) (openedTimeline, error) {
	absRepo, err := filepath.Abs(repoDir)
	if err != nil {
		return openedTimeline{}, fmt.Errorf("forming absolute path to repo at '%s': %w", repoDir, err)
	}

	openTimelinesMu.Lock()
	defer openTimelinesMu.Unlock()

	// don't allow a timeline to be opened twice (folder path is a good
	// pre-check, but in theory a timeline is only unique by its ID, which
	// we check later)
	for _, otl := range openTimelines {
		if otl.RepoDir == absRepo {
			return openedTimeline{}, fmt.Errorf("timeline at %s is already open", absRepo)
		}
	}

	// determine if timeline can be opened or created here
	assessment := timeline.AssessFolder(absRepo)
	if (!create && !assessment.HasTimeline) || (create && !assessment.TimelineCanBeCreated) {
		return openedTimeline{}, assessment
	}

	var tl *timeline.Timeline
	if create {
		tl, err = timeline.Create(ctx, absRepo, DefaultCacheDir())
	} else {
		tl, err = timeline.Open(ctx, absRepo, DefaultCacheDir())
	}
	if err != nil {
		return openedTimeline{}, err
	}
	tlID := tl.ID().String()

	// check once more that the timeline is not already open; we only
	// compared folder paths, now we have actual IDs to compare
	for _, otl := range openTimelines {
		if otl.InstanceID == tl.ID() {
			err = tl.Close()
			if err != nil {
				a.log.Error("closing redundantly-opened timeline",
					zap.Error(err),
					zap.String("timeline", absRepo))
			}
			return openedTimeline{}, fmt.Errorf("timeline with ID %s is already open", otl.InstanceID)
		}
	}

	// for serving static data files from the timeline
	fileServerPrefix := "/" + path.Join("repo", tlID)
	fileRoot := absRepo
	fileServer := http.FileServer(http.Dir(fileRoot))

	otl := openedTimeline{
		RepoDir:    absRepo,
		InstanceID: tl.ID(),
		Timeline:   tl,
		fileServer: http.StripPrefix(fileServerPrefix, fileServer),
	}

	openTimelines[tlID] = otl

	// make appropriate log message
	action := "opened"
	if create {
		action = "created"
	}
	a.log.Info(action+" timeline",
		zap.String("repo", absRepo),
		zap.String("id", tlID))

	// persist newly opened repo so it can be resumed on restart
	if err := a.cfg.syncOpenRepos(); err != nil {
		a.log.Error("unable to persist config", zap.Error(err))
	}

	// TODO: start jobs in queued or aborted (interrupted) states (maybe, at most... 3 jobs?)

	return otl, nil
}

func (a *App) CloseRepository(repoID string) error {
	openTimelinesMu.Lock()
	defer openTimelinesMu.Unlock()

	otl, ok := openTimelines[repoID]
	if !ok {
		return fmt.Errorf("timeline %s is not open", repoID)
	}

	if err := otl.Close(); err != nil {
		return err
	}

	delete(openTimelines, repoID)

	a.log.Info("closed timeline",
		zap.String("repo", repoID),
		zap.String("id", otl.ID().String()))

	// persist newly closed repo
	if err := a.cfg.syncOpenRepos(); err != nil {
		a.log.Error("unable to persist config", zap.Error(err))
	}

	return nil
}

func (a App) AddEntity(repoID string, entity timeline.Entity) error {
	tl, err := getOpenTimeline(repoID)
	if err != nil {
		return err
	}
	return tl.StoreEntity(context.TODO(), entity)
}

func (a App) GetEntity(repoID string, entityID int64) (timeline.Entity, error) {
	tl, err := getOpenTimeline(repoID)
	if err != nil {
		return timeline.Entity{}, err
	}
	return tl.LoadEntity(entityID)
}

func (a App) AddAccount(repoID string, dataSourceID string, auth bool, dsOpt json.RawMessage) (timeline.Account, error) {
	tl, err := getOpenTimeline(repoID)
	if err != nil {
		return timeline.Account{}, err
	}

	ds, err := timeline.GetDataSource(dataSourceID)
	if err != nil {
		return timeline.Account{}, err
	}

	// // for the 'files' data source, the default user ID can and probably should be user@hostname.
	// if payload.DataSource == files.DataSourceID && payload.Owner.UserID == "" {
	// 	var username, hostname string

	// 	u, err := user.Current()
	// 	if err == nil {
	// 		username = u.Username
	// 	} else {
	// 		s.log.Error("looking up current user", zap.Error(err))
	// 	}

	// 	hostname, err = os.Hostname()
	// 	if err != nil {
	// 		s.log.Error("looking up hostname", zap.Error(err))
	// 	}

	// 	// set some sane, slightly recognizable defaults, I guess
	// 	if username == "" {
	// 		if payload.Owner.Name != "" {
	// 			username = payload.Owner.Name
	// 		} else {
	// 			username = "me"
	// 		}
	// 	}
	// 	if hostname == "" {
	// 		hostname = "localhost"
	// 	}

	// 	payload.Owner.UserID = username + "@" + hostname
	// }

	acct, err := tl.AddAccount(a.ctx, dataSourceID, dsOpt)
	if err != nil {
		return timeline.Account{}, err
	}

	if auth {
		err = ds.NewAPIImporter().Authenticate(a.ctx, acct, dsOpt)
		if err != nil {
			return timeline.Account{}, err
		}
	}

	return acct, nil
}

func (a App) RepositoryIsEmpty(repo string) (bool, error) {
	tl, err := getOpenTimeline(repo)
	if err != nil {
		return false, err
	}
	return tl.Empty(), nil
}

// TODO: ....
// func (a App) GetAccounts(repo string, accountIDs []int64, dataSourceID string, expandDS bool) ([]expandedAccount, error) {
// 	tl, err := getOpenTimeline(repo)
// 	if err != nil {
// 		return nil, err
// 	}

// 	// by default, list all accounts (no ID or data source filter), unless specified
// 	var dataSources []string
// 	if dataSourceID != "" {
// 		dataSources = []string{dataSourceID}
// 	}

// 	accounts, err := tl.LoadAccounts(accountIDs, dataSources)
// 	if err != nil {
// 		return nil, err
// 	}

// 	// expand data sources and owners if requested
// 	allInfo := make([]expandedAccount, len(accounts))
// 	for i, acc := range accounts {
// 		allInfo[i], err = a.expandAccount(tl, acc, expandDS)
// 		if err != nil {
// 			return nil, err
// 		}
// 	}

// 	return allInfo, nil
// }

func (a App) AuthAccount(repo string, accountID int64, dsOpt json.RawMessage) error {
	tl, err := getOpenTimeline(repo)
	if err != nil {
		return err
	}

	account, err := tl.LoadAccount(a.ctx, accountID)
	if err != nil {
		return err
	}

	if account.DataSource.NewAPIImporter == nil {
		return fmt.Errorf("data source does not support authentication: %s", account.DataSource.Name)
	}

	dataSourceOpts, err := account.DataSource.UnmarshalOptions(dsOpt)
	if err != nil {
		return fmt.Errorf("unmarshaling data source options: %w", err)
	}

	apiImporter := account.DataSource.NewAPIImporter()

	err = apiImporter.Authenticate(a.ctx, account, dataSourceOpts)
	if err != nil {
		return err
	}

	return nil
}

type PlannerOptions struct {
	Path             string `json:"path"` // file system path (with OS separators)
	Recursive        bool   `json:"recursive"`
	TraverseArchives bool   `json:"traverse_archives"`
	timeline.RecognizeParams
}

func (a *App) PlanImport(ctx context.Context, options PlannerOptions) (timeline.ProposedImportPlan, error) {
	var plan timeline.ProposedImportPlan

	var fsys fs.FS
	if options.TraverseArchives {
		fsys = &archives.DeepFS{Root: options.Path, Context: ctx}
	} else {
		var err error
		fsys, err = archives.FileSystem(ctx, options.Path, nil)
		if err != nil {
			return plan, err
		}
	}

	var pairings []timeline.ProposedFileImport // the matches accumulated through the walk
	var currentDir string                      // our current directory
	var currentDirPairingsMarker int           // the index in pairings where the current dir started

	// finalizeLastDirectory is called during a walk as we move into
	// a new directory, or when a walk is finished. It counts the
	// number of matches by data source and checks if any of them
	// can "claim" the directory as a whole after having matched
	// enough individual entries within it. If so, the individual
	// matches are replaced with a single match for the whole
	// directory. (More than 1 data source may match the dir.)
	finalizeLastDirectory := func() {
		// TODO: Make sure this effect bubbles up in the end, so
		// that all parent directories that match the data source
		// get folded into the top-most directory, so we can have
		// just 1 plan for the whole folder tree

		// start by iterating the pairings of matches from this
		// directory only, and counting the number of entries that
		// each data source matched; then sort by most matches
		var counts dataSourceCounts
		for _, p := range pairings[currentDirPairingsMarker:] {
			for _, match := range p.RecognizeResults {
				if match.DirThreshold > 0 {
					counts.count(match)
				}
			}
		}
		sort.Slice(counts, func(i, j int) bool {
			return counts[i].count > counts[j].count
		})

		// now find any data sources that met their threshold for folding all
		// the matches in the directory into the directory itself; if none
		// reached the threshold, then there's nothing to do (just keep the
		// individual recognition matches as-is); otherwise, we will replace
		// those with one for the whole dir
		var consolidatedMatches []timeline.RecognizeResult
		for _, c := range counts {
			percentage := float64(c.count) / float64(len(currentDir))
			if percentage > c.dirThreshold {
				// this data source matched enough entries in the directory to
				// meet its self-specified threshold, so consider the entire
				// directory a match instead of each individual item
				consolidatedMatches = append(consolidatedMatches, timeline.RecognizeResult{
					DataSource: c.ds,
					// TODO: we need to reduce all the recognition values down to one... what's the best way to do this?
					Recognition: timeline.Recognition{
						Confidence: percentage,
					},
				})
			}
		}

		if len(consolidatedMatches) > 0 {
			// this entire directory is being consolidated, since at least one
			// data source reached the threshold for individual matches within
			// the directory; delete the individual pairings from the walk and
			// replace them all with our single new pairing representing the
			// whole directory
			pairings = pairings[:currentDirPairingsMarker]
			pairings = append(pairings, timeline.ProposedFileImport{
				Filename:         currentDir,
				RecognizeResults: consolidatedMatches,
			})
		}
	}

	err := fs.WalkDir(fsys, ".", func(fpath string, d fs.DirEntry, err error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err != nil {
			return err
		}

		// skip hidden files and folders
		if strings.HasPrefix(path.Base(fpath), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		// check if we've entered a new directory, and if so,
		// check if we need to fold all the individual matches
		// into a single directory-wide match
		dir := path.Dir(fpath)
		if dir != currentDir {
			finalizeLastDirectory()

			// reset state for new directory
			currentDir = dir
			currentDirPairingsMarker = len(pairings)
		}

		log.Println("WALKING:", fpath, d.Name(), err)

		// we make this DirEntry slightly different than we do when importing, due
		// to the nature of the recognition process (we're doing the walk for the
		// data source so they don't have to)
		walkedFile := timeline.DirEntry{
			DirEntry: d,
			FS:       fsys,
			FSRoot:   options.Path,
			Filename: fpath,
		}

		results, err := timeline.DataSourcesRecognize(ctx, walkedFile, options.RecognizeParams)
		if err != nil {
			return fmt.Errorf("recognizing %s: %w", fpath, err)
		}

		// fast-path if no results
		if len(results) == 0 {
			if options.Recursive {
				return nil // traverse into directory (if it is one)
			}
			if d.IsDir() {
				return fs.SkipDir // skip directory
			}
			return nil
		}

		log.Println("RECOGNIZED:", fpath, results)

		// TODO: This should be in order... do we need to use BinarySearch/Insert if not?
		pairings = append(pairings, timeline.ProposedFileImport{Filename: filepath.Join(options.Path, filepath.FromSlash(fpath)), RecognizeResults: results})

		// skip directory; since this filename was recognized, if it was a directory,
		// we don't need to traverse into it even if recursion is enabled, as a data
		// souce will be handling it
		if d.IsDir() {
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		return plan, fmt.Errorf("walking tree rooted at %s: %w (options=%+v)", options.Path, err, options)
	}

	finalizeLastDirectory()

	return plan, nil
}

type dataSourceCount struct {
	ds           timeline.DataSource
	count        int
	dirThreshold float64
}

type dataSourceCounts []dataSourceCount

func (dsCounts *dataSourceCounts) count(match timeline.RecognizeResult) {
	idx := -1
	for i, c := range *dsCounts {
		if c.ds.Name == match.DataSource.Name {
			idx = i
			break
		}
	}
	if idx < 0 {
		*dsCounts = append(*dsCounts, dataSourceCount{
			ds:           match.DataSource,
			count:        1,
			dirThreshold: match.DirThreshold,
		})
		return
	}
	(*dsCounts)[idx].count++
	(*dsCounts)[idx].dirThreshold = match.DirThreshold
}

type ImportParameters struct {
	Repo string             `json:"repo"`
	Job  timeline.ImportJob `json:"job"`

	// For external data sources: (TODO: ... figure this out)
	// DataSource timeline.DataSource // required: Name, Title, Icon, Description
}

func (a App) Import(params ImportParameters) error {
	tl, err := getOpenTimeline(params.Repo)
	if err != nil {
		return err
	}

	return tl.CreateJob(params.Job, 0, 0)
}

func (a *App) SearchItems(params timeline.ItemSearchParams) (timeline.SearchResults, error) {
	tl, err := getOpenTimeline(params.Repo)
	if err != nil {
		return timeline.SearchResults{}, err
	}
	results, err := tl.Search(a.ctx, params)
	if err != nil {
		return timeline.SearchResults{}, err
	}
	if obfuscate() {
		// TODO: obfuscation options
		results.Anonymize(timeline.ObfuscationOptions{
			Logger: a.log,
		})
	}
	return results, nil
}

// TODO: all of these methods should be cancelable by the browser... somehow

func (a *App) SearchEntities(params timeline.EntitySearchParams) ([]timeline.Entity, error) {
	tl, err := getOpenTimeline(params.Repo)
	if err != nil {
		return nil, err
	}
	results, err := tl.SearchEntities(a.ctx, params)
	if err != nil {
		return nil, err
	}
	if obfuscate() {
		for i := range results {
			results[i].Anonymize()
		}
	}
	return results, nil
}

func (a *App) DataSources() []timeline.DataSource {
	return timeline.AllDataSources()
}

func (a *App) DataSource(name string) (timeline.DataSource, error) {
	return timeline.GetDataSource(name)
}

func (a *App) ItemClassifications(repo string) ([]timeline.Classification, error) {
	tl, err := getOpenTimeline(repo)
	if err != nil {
		return nil, err
	}
	return tl.ItemClassifications()
}

// TODO: Very WIP / experimental
func (*App) LoadItemStats(statName, repoID string, params url.Values) (any, error) {
	tl, err := getOpenTimeline(repoID)
	if err != nil {
		return nil, err
	}
	switch statName {
	case "periodical":
		return tl.RecentItemStats(params)
	case "classifications":
		return tl.ItemTypeStats()
	case "datasources":
		return tl.DataSourceUsageStats()
	}
	return nil, fmt.Errorf("unknown stat name: %s", statName)
}

// TODO: very experimental
func (a *App) LoadRecentConversations(params timeline.ItemSearchParams) ([]*timeline.Conversation, error) {
	tl, err := getOpenTimeline(params.Repo)
	if err != nil {
		return nil, err
	}
	convos, err := tl.RecentConversations(params)
	if err != nil {
		return nil, err
	}
	if obfuscate() {
		for _, convo := range convos {
			for i := range convo.Entities {
				convo.Entities[i].Anonymize()
			}
			for i := range convo.RecentMessages {
				convo.RecentMessages[i].Anonymize(timeline.ObfuscationOptions{Logger: a.log})
			}
		}
	}
	return convos, nil
}

func (a App) LoadConversation(params timeline.ItemSearchParams) (timeline.SearchResults, error) {
	tl, err := getOpenTimeline(params.Repo)
	if err != nil {
		return timeline.SearchResults{}, err
	}
	convo, err := tl.LoadConversation(a.ctx, params)
	if err != nil {
		return timeline.SearchResults{}, err
	}
	if obfuscate() {
		// TODO: obfuscation options
		convo.Anonymize(timeline.ObfuscationOptions{Logger: a.log})
	}
	return convo, nil
}

func (a App) MergeEntities(repo string, base int64, others []int64) error {
	tl, err := getOpenTimeline(repo)
	if err != nil {
		return err
	}
	return tl.MergeEntities(a.ctx, base, others)
}

func (a App) DeleteItems(repo string, itemRowIDs []int64, options timeline.DeleteOptions) error {
	tl, err := getOpenTimeline(repo)
	if err != nil {
		return err
	}
	return tl.DeleteItems(a.ctx, itemRowIDs, options)
}

type BuildInfo struct {
	GoOS   string `json:"go_os"`
	GoArch string `json:"go_arch"`
}

func (a *App) BuildInfo() BuildInfo {
	return BuildInfo{
		GoOS:   runtime.GOOS,
		GoArch: runtime.GOARCH,
	}
}

func (a *App) Obfuscation(enable bool) {
	if enable {
		atomic.StoreInt32(obfuscationEnabled, 1)
	} else {
		atomic.StoreInt32(obfuscationEnabled, 0)
	}
}

func obfuscate() bool {
	return atomic.LoadInt32(obfuscationEnabled) == 1
}

// If 1, obfuscate personal or identifying information in output. (accessed atomically)
var obfuscationEnabled = new(int32)
