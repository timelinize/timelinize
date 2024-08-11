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
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"sync/atomic"
	"time"

	"github.com/timelinize/timelinize/datasources/generic"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

// type dialogOptions struct {
// 	DirectoryPicker            bool   `json:"directory_picker,omitempty"`
// 	DefaultDirectory           string `json:"default_directory,omitempty"`
// 	DefaultFilename            string `json:"default_filename,omitempty"`
// 	Title                      string `json:"title,omitempty"`
// 	Filters                    []wailsruntime.FileFilter
// 	ShowHiddenFiles            bool `json:"show_hidden_files,omitempty"`
// 	CanCreateDirectories       bool `json:"can_create_directories,omitempty"`
// 	ResolvesAliases            bool `json:"resolve_aliases,omitempty"`
// 	TreatPackagesAsDirectories bool `json:"treat_packages_as_directories,omitempty"`
// }

// func (a *App) OpenDialog(opts dialogOptions) string {
// 	// the frontend probably won't know whether a path exists or is a directory
// 	// or file; let's be smart about this on its behalf so we can avoid errors
// 	if opts.DirectoryPicker {
// 		opts.DefaultFilename = ""
// 		if opts.DefaultDirectory != "" {
// 			info, err := os.Stat(opts.DefaultDirectory)
// 			if err != nil {
// 				opts.DefaultDirectory = ""
// 			} else if !info.IsDir() {
// 				// tried to set a file for the directory picker; use its parent dir instead
// 				opts.DefaultDirectory = filepath.Dir(opts.DefaultDirectory)
// 			}
// 		}
// 	} else {
// 		opts.DefaultDirectory = ""
// 	}

// 	dialogOptions := wailsruntime.OpenDialogOptions{
// 		DefaultDirectory:           opts.DefaultDirectory,
// 		DefaultFilename:            opts.DefaultFilename,
// 		Title:                      opts.Title,
// 		Filters:                    opts.Filters,
// 		ShowHiddenFiles:            opts.ShowHiddenFiles,
// 		CanCreateDirectories:       opts.CanCreateDirectories,
// 		ResolvesAliases:            opts.ResolvesAliases,
// 		TreatPackagesAsDirectories: opts.TreatPackagesAsDirectories,
// 	}

// 	var path string
// 	var err error
// 	if opts.DirectoryPicker {
// 		path, err = wailsruntime.OpenDirectoryDialog(a.ctx, dialogOptions)
// 	} else {
// 		// TODO: use multiple file selection dialog -- importers will need to support multiple filenames as input
// 		path, err = wailsruntime.OpenFileDialog(a.ctx, dialogOptions)
// 	}
// 	if err != nil {
// 		a.log.Error("opening file/folder dialog", zap.Error(err))
// 	}

// 	return path
// }

func (a App) FileSelectorRoots() ([]fileSelectorRoot, error) {
	return getFileSelectorRoots()
}

func (a App) GetOpenRepositories() []openedTimeline {
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
			defer a.CloseRepository(otl.InstanceID.String())
		} else {
			// ok, it's still there
			repos = append(repos, otl)
		}
	}
	openTimelinesMu.RUnlock()
	return repos
}

// OpenRepository opens the timeline at repoDir as long as it
// is not already open.
func (a *App) OpenRepository(repoDir string, create bool) (openedTimeline, error) {
	absRepo, err := filepath.Abs(repoDir)
	if err != nil {
		return openedTimeline{}, fmt.Errorf("forming absolute path to repo at '%s': %v", repoDir, err)
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
		tl, err = timeline.Create(absRepo, DefaultCacheDir())
	} else {
		tl, err = timeline.Open(absRepo, DefaultCacheDir())
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

	return otl, nil
}

func slicesEqual(s1, s2 []string) bool {
	if len(s1) != len(s2) {
		return false
	}
	for i := range s1 {
		if s1[i] != s2[i] {
			return false
		}
	}
	return true
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

func (a *App) Recognize(filenames []string) ([]timeline.RecognizeResult, error) {
	for _, fname := range filenames {
		if _, err := os.Stat(fname); err != nil {
			return nil, err
		}
	}
	results, err := timeline.DataSourcesRecognize(a.ctx, filenames, 2*time.Second)
	if err != nil {
		return nil, err
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Name != generic.DataSourceID
	})
	return results, nil
}

func (a App) Import(params ImportParameters) (activeJob, error) {
	tl, err := getOpenTimeline(params.Repo)
	if err != nil {
		return activeJob{}, err
	}

	activeJobsMu.Lock()
	defer activeJobsMu.Unlock()

	params.JobID = params.Hash()

	if _, ok := activeJobs[params.JobID]; ok {
		return activeJob{}, fmt.Errorf("job is not unique; another similar job is already running")
	}

	ctx, cancel := context.WithCancel(a.ctx)
	job := activeJob{
		ID:               params.JobID,
		Type:             "import",
		Started:          time.Now(),
		ImportParameters: &params,
		ctx:              ctx,
		cancel:           cancel,
	}

	go func() {
		defer job.cleanUp()

		logger := timeline.Log.Named("job_manager")

		logger.Info("start",
			zap.String("id", params.JobID),
			zap.String("type", "import"),
			zap.Any("parameters", params),
		)

		start := time.Now()

		err := tl.Import(job.ctx, params.ImportParameters)

		logFn := logger.Info
		if err != nil {
			logFn = logger.Error
		}
		logFn("end",
			zap.String("id", params.JobID),
			zap.String("type", "import"),
			zap.Any("parameters", params),
			zap.Duration("duration", time.Since(start)),
			zap.Error(err),
		)
	}()

	activeJobs[params.JobID] = job

	return job, nil

	//////////////////////////////////////////////

	// // if resuming, load the import we're resuming and adjust the payload accordingly
	// var imp timeline.Import
	// if payload.ResumeImportID > 0 {
	// 	imp, err = tl.LoadImport(r.Context(), payload.ResumeImportID)
	// 	if err != nil {
	// 		return err
	// 	}
	// 	if len(imp.Checkpoint) == 0 {
	// 		return fmt.Errorf("import %d has no checkpoint to resume from", imp.ID)
	// 	}
	// 	if payload.DataSource != "" || payload.AccountID != 0 ||
	// 		payload.Filename != "" || payload.ProcessingOptions != nil {
	// 		// no need to specify these; it only risks being different and thus in conflict
	// 		return fmt.Errorf("pointless to specify data source, account ID, filename, and/or processing options separately when resuming import")
	// 	}

	// 	// decode checkpoint and adjust job payload to set up resumption
	// 	var chk timeline.Checkpoint
	// 	err = timeline.UnmarshalGob(imp.Checkpoint, &chk)
	// 	if err != nil {
	// 		return fmt.Errorf("decoding checkpoint: %v", err)
	// 	}
	// 	payload.Filename = chk.Filename
	// 	payload.DataSource = imp.DataSourceID
	// 	if imp.AccountID != nil {
	// 		payload.AccountID = *imp.AccountID
	// 	}
	// 	payload.ProcessingOptions = &chk.ProcOpt
	// }

	// var acc timeline.Account
	// if payload.AccountID > 0 {
	// 	acc, err = tl.LoadAccount(r.Context(), payload.AccountID)
	// 	if err != nil {
	// 		return err
	// 	}
	// }

	// // make sure this is the only active job to be running for this account
	// activeJobsMu.Lock()
	// defer activeJobsMu.Unlock()

	// jobID := payload.jobID()

	// if _, ok := activeJobs[jobID]; ok {
	// 	return Error{
	// 		Err:        fmt.Errorf("a job for that file or account is already running"),
	// 		HTTPStatus: http.StatusTooManyRequests,
	// 		Log:        "job is not unique",
	// 		Message:    "A processing job for this file or account is already running. We only allow one operation per file/account at a time.",
	// 	}
	// }

	// ctx, cancel := context.WithCancel(context.Background())
	// job := activeJob{
	// 	ID:         jobID,
	// 	Started:    time.Now(),
	// 	Parameters: payload,
	// 	ctx:        ctx,
	// 	cancel:     cancel,
	// }

	// go func() {
	// 	defer job.cleanUp()

	// 	err := tl.Import(job.ctx, payload.DataSource, payload.Filename, acc, imp, payload.ProcessingOptions, payload.DataSourceOptions)
	// 	if err != nil {
	// 		timeline.Log.Named(jobID).Error("import failed",
	// 			zap.Error(err),
	// 			zap.Any("parameters", payload),
	// 		)
	// 	}
	// }()

	// activeJobs[jobID] = job

	// return jsonResponse(w, map[string]any{"job": job})
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

func (a *App) ActiveJobs() ([]activeJob, error) {
	activeJobsMu.Lock()
	jobs := make([]activeJob, 0, len(activeJobs))
	for _, job := range activeJobs {
		jobs = append(jobs, job)
	}
	activeJobsMu.Unlock()

	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].Started.Before(jobs[j].Started)
	})

	return jobs, nil
}

func (*App) CancelJob(jobID string) error {
	activeJobsMu.Lock()
	job, ok := activeJobs[jobID]
	activeJobsMu.Unlock()
	if !ok {
		return fmt.Errorf("no job %s is running", jobID)
	}
	job.cleanUp()
	return nil
}

type ImportParameters struct {
	Repo string `json:"repo"`
	timeline.ImportParameters
}

func (params ImportParameters) Hash() string {
	return params.ImportParameters.Hash(params.Repo)
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
