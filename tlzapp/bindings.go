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
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

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

	// in very few places, the timeline package may emit data directly
	// to the frontend in the form of logs, so even though obfuscation
	// is an application concern, a timeline needs to know, in those places,
	// whether to obfuscate the output... since the timeline package
	// cannot import this one, we cheat and invert the dependencies
	tl.SetObfuscationFunc(func() (timeline.ObfuscationOptions, bool) {
		return a.ObfuscationMode(tl)
	})

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

// PlannerOptions configures how an import plan is created.
type PlannerOptions struct {
	Path             string `json:"path"` // file system path (with OS separators)
	Recursive        bool   `json:"recursive"`
	TraverseArchives bool   `json:"traverse_archives"`
	timeline.RecognizeParams
}

// PlanImport produces an import plan with the given settings.
func (a *App) PlanImport(ctx context.Context, options PlannerOptions) (timeline.ProposedImportPlan, error) {
	var plan timeline.ProposedImportPlan

	logger := a.log.Named("import_planner").With(zap.String("root", options.Path))

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

	var (
		tree       []string                                         // for tracking the dir tree during the walk
		currentDir string                                           // our current directory (the last element of tree)
		pairings   = make(map[string][]timeline.ProposedFileImport) // the matches accumulated through the walk
		dirSizes   = make(map[string]int)                           // number of (non-hidden) entries discovered in each directory
	)

	// finalizeDirectory is called during a walk as we move into
	// a new directory, or when a walk is finished. It counts the
	// number of matches by data source and checks if any of them
	// can "claim" the directory as a whole after having matched
	// enough individual entries within it. If so, the individual
	// matches are replaced with a single match for the whole
	// directory. (More than 1 data source may match the dir.)
	finalizeDirectory := func(dir string) {
		currentPairings := pairings[dir]

		// no nee to sort/filter/consolidate if there's only 1 file
		if len(currentPairings) <= 1 {
			return
		}

		// start by iterating the pairings of matches from this
		// directory only, and counting the number of entries that
		// each data source matched; then sort by most matches
		var counts dataSourceCounts
		// for _, p := range pairings[currentDirPairingsMarker:] {
		for _, p := range currentPairings {
			for _, match := range p.DataSources {
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
		var consolidatedMatches []timeline.DataSourceRecognition
		for _, c := range counts {
			percentage := float64(c.count) / float64(dirSizes[dir])
			if percentage > c.dirThreshold {
				// this data source matched enough entries in the directory to
				// meet its self-specified threshold, so consider the entire
				// directory a match instead of each individual item
				consolidatedMatches = append(consolidatedMatches, timeline.DataSourceRecognition{
					DataSource: c.ds,
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
			filename := filepath.Join(filepath.Dir(options.Path), filepath.FromSlash(dir))
			ftype := fileTypeDir
			if archives.PathContainsArchive(filename) {
				ftype = fileTypeArchive
			}
			pairings[dir] = []timeline.ProposedFileImport{
				{
					Filename:    filename,
					FileType:    ftype,
					DataSources: consolidatedMatches,
				},
			}
			for d := range pairings {
				if d != dir && strings.HasPrefix(d, dir) {
					delete(pairings, d)
				}
			}
		}
	}

	// Prepare for walk. it's a little inconvenient, actually, that fs.WalkDir() is the conventional
	// way to walk a file system, since it linearizes it, i.e., abstracts the recursion away to a
	// single function. The recursion would be useful for knowing exactly when we're bubbling up
	// out of a directory, or traversing deeper in, without having to keep track of the filenames
	// as we go, and doing prefix comparisons, etc. But I bet the std lib handles edge cases
	// that I don't want to think about, so I'm going to stick to using fs.WalkDir().

	startingDir := filepath.Base(options.Path)
	tree = append(tree, startingDir)
	currentDir = startingDir

	err := fs.WalkDir(fsys, ".", func(fpath string, d fs.DirEntry, err error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err != nil {
			// sometimes, archives may contain filenames with invalid encoding,
			// or directories may have an archive extension (but are not actually
			// archives; ignore such errors and just
			// Most common errors I've seen: fs.ErrInvalid, zip.Err*
			logger.Warn("encountered error during walk; skipping",
				zap.String("path", fpath),
				zap.Error(err))
			return nil
		}

		// skip hidden files and folders
		if strings.HasPrefix(path.Base(d.Name()), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		// check if we've entered a new directory, and if so,
		// check if we need to fold all the individual matches
		// into a single directory-wide match
		dir := path.Join(filepath.Base(options.Path), path.Dir(fpath)) // account for fpath being "." by just always prepending the root dir name instead
		if dir != currentDir {
			// TODO: This isn't really a helpful log, because it gets sampled, so if we're stuck on a dir for a long time, it might not be the dir that was last logged
			logger.Info("traversing directory", zap.String("dir", dir))

			// compare prefixes by appending "/" to prevent false positives with a scenario like "a/b" and "a/bb"; they are different subfolders!
			if strings.HasPrefix(dir, currentDir+"/") {
				// we have recursed into a subdirectory

				// I've found that when we enter a subdir, we may have left a subdir tree that we were in,
				// i.e. this new dir might not be a subdir of the folder we were last in; so we need to
				// check our tree and keep it in sync, popping off dirs until we get back to the closest
				// common denominator with this new one.
				for len(tree) > 0 && !strings.HasPrefix(dir, tree[len(tree)-1]+"/") {
					finalizeDirectory(tree[len(tree)-1])
					tree = tree[:len(tree)-1]
				}

				tree = append(tree, dir)
			} else {
				// we have finished a directory and are going up
				tree = tree[:len(tree)-1]
				finalizeDirectory(currentDir)
			}

			// update state for new dir
			currentDir = dir
		}

		// don't let directories count against the counts when it comes to consolidating results; doesn't seem right
		if !d.IsDir() {
			dirSizes[currentDir]++
		}

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

		ftype := fileTypeFile
		if archives.PathContainsArchive(path.Join(filepath.ToSlash(options.Path), fpath)) {
			ftype = fileTypeArchive
		} else if d.IsDir() {
			// remember that the underlying FS, if it is a DeepFS, can report
			// an archive as a directory, so do this check if it's not an archive
			ftype = fileTypeDir
		}

		// map the filename to its results, keeping all results within this directory together
		// (we need them grouped in case we consolidate all the results to the directory itself,
		// we end up deleting all the individual entry results; we linearize the pairings later)
		pairings[currentDir] = append(pairings[currentDir], timeline.ProposedFileImport{
			Filename:    filepath.Join(options.Path, filepath.FromSlash(fpath)),
			FileType:    ftype,
			DataSources: results,
		})

		// skip directory; since this filename was recognized, if it was a directory,
		// we don't need to traverse into it even if recursion is enabled, as a data
		// souce will be handling it
		if d.IsDir() {
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		return plan, fmt.Errorf("walking tree rooted at %s: %w (options=%+v fs=%#v)", options.Path, err, options, fsys)
	}

	// make sure to finalize/process/reduce the final directory we walked
	finalizeDirectory(currentDir)

	// make sure to check our tree for any base cases (end of dir; going up a dir) that
	// may have happened implicitly during the recursive walk without an opportunity
	// to close out those directories (see similar logic above in the walk fn)
	for len(tree) > 0 {
		finalizeDirectory(tree[len(tree)-1])
		tree = tree[:len(tree)-1]
	}

	// linearize the map of results
	for _, p := range pairings {
		plan.Files = append(plan.Files, p...)
	}

	return plan, nil
}

const (
	fileTypeFile    = "file"
	fileTypeDir     = "dir"
	fileTypeArchive = "archive"
)

type dataSourceCount struct {
	ds           timeline.DataSource
	count        int
	dirThreshold float64
}

type dataSourceCounts []dataSourceCount

func (dsCounts *dataSourceCounts) count(match timeline.DataSourceRecognition) {
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
	Repo string              `json:"repo"`
	Job  *timeline.ImportJob `json:"job"`

	// For external data sources: (TODO: ... figure this out)
	// DataSource timeline.DataSource // required: Name, Title, Icon, Description
}

func (a App) Import(params ImportParameters) (int64, error) {
	tl, err := getOpenTimeline(params.Repo)
	if err != nil {
		return 0, err
	}
	// queue job for a brief period to allow UI to render job page first and to help
	// user get their bearings, unless it's interactive: then just start right away
	// since the user will be waiting for the first item
	const queueDuration = 5 * time.Second
	scheduled := time.Now().Add(queueDuration)
	if params.Job.ProcessingOptions.Interactive != nil {
		scheduled = time.Time{}
	}
	return tl.CreateJob(params.Job, scheduled, 0, 0, 0)
}

func (App) NextGraph(repoID string, jobID int64) (*timeline.Graph, error) {
	tl, err := getOpenTimeline(repoID)
	if err != nil {
		return nil, err
	}
	return tl.Timeline.NextGraphFromImport(jobID)
}

func (App) SubmitGraph(repoID string, jobID int64, g *timeline.Graph, skip bool) error {
	tl, err := getOpenTimeline(repoID)
	if err != nil {
		return err
	}
	return tl.Timeline.SubmitGraph(jobID, g, skip)
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
	if options, ok := a.ObfuscationMode(tl.Timeline); ok {
		results.Anonymize(options)
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
	if _, ok := a.ObfuscationMode(tl.Timeline); ok {
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

type Settings struct {
	Application *Config `json:"application"`

	// Map of repo ID to map of property name to value.
	Timelines map[string]map[string]any `json:"timelines,omitempty"`
}

func (a *App) GetSettings(ctx context.Context) (Settings, error) {
	return Settings{
		Application: a.cfg,
	}, nil
}

func (a *App) ChangeSettings(_ context.Context, newSettings *changeSettingsPayload) error {
	a.cfg.Lock()
	defer a.cfg.Unlock()

	// TODO: any settings that require a server/application restart should set a flag and then we do so

	for key, val := range newSettings.Application {
		var err error
		switch key {
		case "app.mapbox_api_key":
			err = json.Unmarshal(val, &a.cfg.MapboxAPIKey)
		case "app.obfuscation.enabled":
			err = json.Unmarshal(val, &a.cfg.Obfuscation.Enabled)
		case "app.obfuscation.locations":
			err = json.Unmarshal(val, &a.cfg.Obfuscation.Locations)
		case "app.obfuscation.data_files":
			err = json.Unmarshal(val, &a.cfg.Obfuscation.DataFiles)
		}
		if err != nil {
			return fmt.Errorf("saving setting %s: %w (value=%s)", key, err, string(val))
		}
	}

	if err := a.cfg.unsyncedSave(); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	return nil
}

// TODO: very experimental
func (a *App) LoadRecentConversations(ctx context.Context, params timeline.ItemSearchParams) ([]*timeline.Conversation, error) {
	tl, err := getOpenTimeline(params.Repo)
	if err != nil {
		return nil, err
	}
	convos, err := tl.RecentConversations(ctx, params)
	if err != nil {
		return nil, err
	}
	if options, ok := a.ObfuscationMode(tl.Timeline); ok {
		for _, convo := range convos {
			for i := range convo.Entities {
				convo.Entities[i].Anonymize()
			}
			for i := range convo.RecentMessages {
				convo.RecentMessages[i].Anonymize(options)
			}
		}
	}
	return convos, nil
}

func (a App) LoadConversation(ctx context.Context, params timeline.ItemSearchParams) (timeline.SearchResults, error) {
	tl, err := getOpenTimeline(params.Repo)
	if err != nil {
		return timeline.SearchResults{}, err
	}
	convo, err := tl.LoadConversation(ctx, params)
	if err != nil {
		return timeline.SearchResults{}, err
	}
	if options, ok := a.ObfuscationMode(tl.Timeline); ok {
		convo.Anonymize(options)
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

func (a App) Jobs(repo string, jobIDs []int64) ([]timeline.Job, error) {
	if repo != "" {
		tl, err := getOpenTimeline(repo)
		if err != nil {
			return nil, err
		}
		return tl.GetJobs(a.ctx, jobIDs)
	}
	return nil, errors.New("TODO: Getting jobs other than by specific IDs not yet implemented")
}

func (a App) CancelJob(ctx context.Context, repo string, jobID int64) error {
	tl, err := getOpenTimeline(repo)
	if err != nil {
		return err
	}
	return tl.CancelJob(ctx, jobID)
}

func (a App) PauseJob(ctx context.Context, repo string, jobID int64) error {
	tl, err := getOpenTimeline(repo)
	if err != nil {
		return err
	}
	return tl.PauseJob(ctx, jobID)
}

func (a App) UnpauseJob(ctx context.Context, repo string, jobID int64) error {
	tl, err := getOpenTimeline(repo)
	if err != nil {
		return err
	}
	return tl.UnpauseJob(ctx, jobID)
}

func (a App) StartJob(ctx context.Context, repo string, jobID int64, startOver bool) error {
	tl, err := getOpenTimeline(repo)
	if err != nil {
		return err
	}
	return tl.StartJob(ctx, jobID, startOver)
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

func (a App) ObfuscationMode(repo *timeline.Timeline) (timeline.ObfuscationOptions, bool) {
	a.cfg.RLock()
	defer a.cfg.RUnlock()
	return a.cfg.Obfuscation, a.cfg.Obfuscation.AppliesTo(repo)
}
