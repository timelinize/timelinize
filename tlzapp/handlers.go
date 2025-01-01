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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func (s *server) handleFileSelectorRoots(w http.ResponseWriter, _ *http.Request) error {
	results, err := s.app.fileSelectorRoots()
	return jsonResponse(w, results, err)
}

func (s *server) handleRepositoryEmpty(w http.ResponseWriter, r *http.Request) error {
	repoID := r.Context().Value(ctxKeyPayload).(*string)
	empty, err := s.app.RepositoryIsEmpty(*repoID)
	return jsonResponse(w, empty, err)
}

func (s *server) handleItemClassifications(w http.ResponseWriter, r *http.Request) error {
	repoID := r.Context().Value(ctxKeyPayload).(*string)
	empty, err := s.app.ItemClassifications(*repoID)
	return jsonResponse(w, empty, err)
}

type addEntityPayload struct {
	RepoID string          `json:"repo_id"`
	Entity timeline.Entity `json:"entity"`
}

func (s *server) handleAddEntity(w http.ResponseWriter, r *http.Request) error {
	payload := r.Context().Value(ctxKeyPayload).(*addEntityPayload)
	return jsonResponse(w, nil, s.app.AddEntity(payload.RepoID, payload.Entity))
}

type getEntityPayload struct {
	RepoID   string `json:"repo_id"`
	EntityID int64  `json:"entity_id"`
}

func (s *server) handleGetEntity(w http.ResponseWriter, r *http.Request) error {
	payload := r.Context().Value(ctxKeyPayload).(*getEntityPayload)
	entity, err := s.app.GetEntity(payload.RepoID, payload.EntityID)
	return jsonResponse(w, entity, err)
}

type mergeEntitiesPayload struct {
	RepoID         string  `json:"repo_id"`
	BaseEntityID   int64   `json:"base_entity_id"`
	OtherEntityIDs []int64 `json:"other_entity_ids"`
}

func (s *server) handleMergeEntities(w http.ResponseWriter, r *http.Request) error {
	payload := r.Context().Value(ctxKeyPayload).(*mergeEntitiesPayload)
	err := s.app.MergeEntities(payload.RepoID, payload.BaseEntityID, payload.OtherEntityIDs)
	return jsonResponse(w, nil, err)
}

func (s *server) handleStats(w http.ResponseWriter, r *http.Request) error {
	statName, repoID := r.FormValue("name"), r.FormValue("repo_id")
	q := r.URL.Query()
	q.Del("name")
	q.Del("repo_id")
	r.URL.RawQuery = q.Encode()
	stats, err := s.app.LoadItemStats(statName, repoID, r.URL.Query())
	return jsonResponse(w, stats, err)
}

type jobsPayload struct {
	RepoID string  `json:"repo_id"`
	JobIDs []int64 `json:"job_ids"`
}

func (s *server) handleJobs(w http.ResponseWriter, r *http.Request) error {
	payload := r.Context().Value(ctxKeyPayload).(*jobsPayload)
	jobs, err := s.app.Jobs(payload.RepoID, payload.JobIDs)
	return jsonResponse(w, jobs, err)
}

func (s *server) handleCancelJobs(w http.ResponseWriter, r *http.Request) error {
	payload := r.Context().Value(ctxKeyPayload).(*jobsPayload)
	var firstErr error
	for _, jobID := range payload.JobIDs {
		err := s.app.CancelJob(r.Context(), payload.RepoID, jobID)
		if err != nil {
			s.log.Error("canceling job failed",
				zap.Int64("job_id", jobID),
				zap.Error(err))
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return jsonResponse(w, nil, firstErr)
}

type jobPayload struct {
	RepoID    string `json:"repo_id"`
	JobID     int64  `json:"job_id"`
	StartOver bool   `json:"start_over,omitempty"` // only used with StartJob
}

func (s *server) handlePauseJob(w http.ResponseWriter, r *http.Request) error {
	payload := r.Context().Value(ctxKeyPayload).(*jobPayload)
	err := s.app.PauseJob(r.Context(), payload.RepoID, payload.JobID)
	return jsonResponse(w, nil, err)
}

func (s *server) handleUnpauseJob(w http.ResponseWriter, r *http.Request) error {
	payload := r.Context().Value(ctxKeyPayload).(*jobPayload)
	err := s.app.UnpauseJob(r.Context(), payload.RepoID, payload.JobID)
	return jsonResponse(w, nil, err)
}

func (s *server) handleStartJob(w http.ResponseWriter, r *http.Request) error {
	payload := r.Context().Value(ctxKeyPayload).(*jobPayload)
	err := s.app.StartJob(r.Context(), payload.RepoID, payload.JobID, payload.StartOver)
	return jsonResponse(w, nil, err)
}

func (s *server) handleSettings(w http.ResponseWriter, r *http.Request) error {
	allSettings, err := s.app.GetSettings(r.Context())
	if allSettings.Application != nil {
		allSettings.Application.RLock()
		defer allSettings.Application.RUnlock()
	}
	return jsonResponse(w, allSettings, err)
}

type changeSettingsPayload struct {
	Application map[string]json.RawMessage            `json:"application"`
	Timelines   map[string]map[string]json.RawMessage `json:"timelines"` // map of repo ID to map of setting keys to their new values
}

func (s *server) handleChangeSettings(w http.ResponseWriter, r *http.Request) error {
	payload := r.Context().Value(ctxKeyPayload).(*changeSettingsPayload)
	err := s.app.ChangeSettings(r.Context(), payload)
	return jsonResponse(w, nil, err)
}

func (s *server) handleFileStat(w http.ResponseWriter, r *http.Request) error {
	filename := r.Context().Value(ctxKeyPayload).(*string)
	info, err := os.Stat(*filename)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, fs.ErrNotExist) {
			status = http.StatusNotFound
		}
		return Error{
			Err:        err,
			HTTPStatus: status,
			Log:        "stat'ing a file",
			Message:    "Had trouble getting info about that file.",
		}
	}
	var fullName string
	if abs, err := filepath.Abs(*filename); err == nil {
		fullName = abs
	}
	result := localFile{
		FullName: fullName,
		Name:     info.Name(),
		Size:     info.Size(),
		Mode:     info.Mode(),
		ModTime:  info.ModTime(),
		IsDir:    info.IsDir(),
	}
	return jsonResponse(w, result, nil)
}

func (server) handleLogs(w http.ResponseWriter, r *http.Request) error {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return Error{
			Err:        err,
			HTTPStatus: http.StatusBadRequest,
			Log:        "upgrading request to websocket",
			Message:    "This endpoint expects a WebSocket client.",
		}
	}
	defer conn.Close()

	// while the client is connected, broadcast the logs to it
	timeline.AddLogConn(conn)
	defer timeline.RemoveLogConn(conn)

	// simply keep the connection open until the client closes it
	for {
		_, _, err = conn.ReadMessage()
		if err != nil {
			break
		}
	}

	return nil
}

func (s *server) handleRepos(w http.ResponseWriter, _ *http.Request) error {
	return jsonResponse(w, s.app.getOpenRepositories(), nil)
}

func (s *server) handleBuildInfo(w http.ResponseWriter, _ *http.Request) error {
	return jsonResponse(w, s.app.BuildInfo(), nil)
}

func (s *server) handleGetDataSources(w http.ResponseWriter, _ *http.Request) error {
	return jsonResponse(w, s.app.DataSources(), nil)
}

func (s *server) handleDataSource(w http.ResponseWriter, r *http.Request) error {
	dsID := r.Context().Value(ctxKeyPayload).(*string)
	ds, err := s.app.DataSource(*dsID)
	return jsonResponse(w, ds, err)
}

type openRepoPayload struct {
	RepoPath string `json:"repo_path"`
	Create   bool   `json:"create"`
}

func (s *server) handleOpenRepo(w http.ResponseWriter, r *http.Request) error {
	payload := r.Context().Value(ctxKeyPayload).(*openRepoPayload)

	// TODO: maybe have the app methods return structured errors
	openedTL, err := s.app.openRepository(r.Context(), payload.RepoPath, payload.Create)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Error{
				Err:        err,
				HTTPStatus: http.StatusNotFound,
				Log:        "repo folder does not exist",
				Message:    "No timeline found.",
				Data:       err,
			}
		}
		return Error{
			Err:        err,
			HTTPStatus: http.StatusBadRequest,
			Log:        "failure opening timeline",
			Message:    "Failed to open that timeline.",
			Data:       err,
		}
	}

	return jsonResponse(w, openedTL, nil)
}

func (s *server) handleCloseRepo(w http.ResponseWriter, r *http.Request) error {
	repoID := r.Context().Value(ctxKeyPayload).(*string)
	return jsonResponse(w, nil, s.app.CloseRepository(*repoID))
}

// // TODO: we might need a way to "add identity"
// func (s *server) handleAddAccount(w http.ResponseWriter, r *http.Request) error {
// 	var payload struct {
// 		Repo       string `json:"repo"`
// 		DataSource string `json:"data_source"`
// 		// Owner      timeline.Person `json:"owner"` // TODO: necessary?

// 		// sometimes useful when interacting with the data
// 		// source, like setting up account or processing
// 		DataSourceOptions json.RawMessage `json:"data_source_options"`

// 		// if true, also get authorization with data source's auth endpoint (like for API access)
// 		Auth bool `json:"auth"`
// 	}
// 	err := json.NewDecoder(r.Body).Decode(&payload)
// 	if err != nil {
// 		return err
// 	}

// 	acct, err := s.app.AddAccount(payload.Repo, payload.DataSource, payload.Auth, payload.DataSourceOptions)
// 	return jsonResponse(w, acct, err)
// }

// TODO: revise this once we update accounts
// func (s *server) handleGetAccounts(w http.ResponseWriter, r *http.Request) error {
// 	var payload struct {
// 		Repo             string  `json:"repo"`
// 		IDs              []int64 `json:"ids"`         // optionally get specific accounts
// 		DataSource       string  `json:"data_source"` // optionally filter by data source
// 		ExpandDataSource bool    `json:"expand_data_source"`
// 	}
// 	err := json.NewDecoder(r.Body).Decode(&payload)
// 	if err != nil {
// 		return jsonDecodeErr(err)
// 	}

// 	allInfo, err := s.app.GetAccounts(payload.Repo, payload.IDs, payload.DataSource, payload.ExpandDataSource)
// 	if err != nil {
// 		return err
// 	}

// 	return jsonResponse(w, allInfo)
// }

// func (s *server) handleAuthAccount(_ http.ResponseWriter, r *http.Request) error {
// 	var payload struct {
// 		Repo              string          `json:"repo"`
// 		AccountID         int64           `json:"account_id"`
// 		DataSourceOptions json.RawMessage `json:"data_source_options"`
// 	}
// 	err := json.NewDecoder(r.Body).Decode(&payload)
// 	if err != nil {
// 		return err
// 	}
// 	return s.app.AuthAccount(payload.Repo, payload.AccountID, payload.DataSourceOptions)
// }

func (s *server) handlePlanImport(w http.ResponseWriter, r *http.Request) error {
	plannerOptions := *r.Context().Value(ctxKeyPayload).(*PlannerOptions)
	importPlan, err := s.app.PlanImport(r.Context(), plannerOptions)
	return jsonResponse(w, importPlan, err)
}

func (s *server) handleImport(w http.ResponseWriter, r *http.Request) error {
	params := *r.Context().Value(ctxKeyPayload).(*ImportParameters)
	jobID, err := s.app.Import(params)
	return jsonResponse(w, map[string]any{"job_id": jobID}, err)
}

func (s *server) handleNextGraph(w http.ResponseWriter, r *http.Request) error {
	repoID, jobIDStr := r.FormValue("repo_id"), r.FormValue("job_id")
	jobID, err := strconv.ParseInt(jobIDStr, 10, 64)
	if err != nil {
		return jsonResponse(w, nil, fmt.Errorf("job ID must be an integer: %w", err))
	}
	graph, err := s.app.NextGraph(repoID, jobID)
	return jsonResponse(w, graph, err)
}

type submitGraphPayload struct {
	RepoID string          `json:"repo_id"`
	JobID  int64           `json:"job_id"`
	Graph  *timeline.Graph `json:"graph"`
	Skip   bool            `json:"skip"`
}

func (s *server) handleSubmitGraph(w http.ResponseWriter, r *http.Request) error {
	params := *r.Context().Value(ctxKeyPayload).(*submitGraphPayload)
	err := s.app.SubmitGraph(params.RepoID, params.JobID, params.Graph, params.Skip)
	return jsonResponse(w, nil, err)
}

func (s *server) handleSearchItems(w http.ResponseWriter, r *http.Request) error {
	params := r.Context().Value(ctxKeyPayload).(*timeline.ItemSearchParams)
	results, err := s.app.SearchItems(*params)
	return jsonResponse(w, results, err)
}

func (s *server) handleSearchEntities(w http.ResponseWriter, r *http.Request) error {
	params := r.Context().Value(ctxKeyPayload).(*timeline.EntitySearchParams)
	results, err := s.app.SearchEntities(*params)
	return jsonResponse(w, results, err)
}

func (s *server) handleRecentConversations(w http.ResponseWriter, r *http.Request) error {
	params := r.Context().Value(ctxKeyPayload).(*timeline.ItemSearchParams)
	results, err := s.app.LoadRecentConversations(r.Context(), *params)
	return jsonResponse(w, results, err)
}

func (s *server) handleConversation(w http.ResponseWriter, r *http.Request) error {
	params := r.Context().Value(ctxKeyPayload).(*timeline.ItemSearchParams)
	results, err := s.app.LoadConversation(r.Context(), *params)
	return jsonResponse(w, results, err)
}

type deleteItemsPayload struct {
	RepoID  string  `json:"repo_id"`
	ItemIDs []int64 `json:"item_ids"`
	timeline.DeleteOptions
}

func (s *server) handleDeleteItems(w http.ResponseWriter, r *http.Request) error {
	payload := *r.Context().Value(ctxKeyPayload).(*deleteItemsPayload)
	err := s.app.DeleteItems(payload.RepoID, payload.ItemIDs, payload.DeleteOptions)
	return jsonResponse(w, nil, err)
}

// func (app) handleAutocompletePerson(w http.ResponseWriter, r *http.Request) error {
// 	var payload struct {
// 		Repo   string `json:"repo"`
// 		Prefix string `json:"prefix"`
// 	}
// 	err := json.NewDecoder(r.Body).Decode(&payload)
// 	if err != nil {
// 		return jsonDecodeErr(err)
// 	}
// 	return nil
// }

type payloadFileListing struct {
	Path       string `json:"path"`
	OnlyDirs   bool   `json:"only_dirs"`
	ShowHidden bool   `json:"show_hidden"`
}

func (server) handleFileListing(w http.ResponseWriter, r *http.Request) error {
	listingReq := *r.Context().Value(ctxKeyPayload).(*payloadFileListing)

	if listingReq.Path == "" {
		listingReq.Path = userHomeDir()
	}

	// for some reason, on Windows, requesting the file listing of "C:" shows
	// the contents of C:\Windows\system32, but requesting "C:\" works fine; so
	// let's go ahead and fix that, shall we?
	if runtime.GOOS == osWindows && len(listingReq.Path) == 2 && listingReq.Path[1] == ':' {
		listingReq.Path += `\`
	}

	// give appropriate HTTP status code for the situation
	properError := func(err error) error {
		status := http.StatusInternalServerError
		if errors.Is(err, fs.ErrNotExist) {
			status = http.StatusNotFound
		} else if errors.Is(err, fs.ErrPermission) {
			status = http.StatusForbidden
		}
		return Error{
			Err:        err,
			HTTPStatus: status,
			Log:        "Accessing path",
			Message:    fmt.Sprintf("We couldn't access that file path (%s).", listingReq.Path),
			Recommendations: []string{
				"Make sure the file or folder exists.",
				"Make sure permission is granted to access the file or folder.",
			},
		}
	}

	// get info about the path because we need to differentiate file from directory
	info, err := os.Stat(listingReq.Path)
	if err != nil {
		return properError(err)
	}

	// prepare response; make absolute path, compute up-dir,
	// and if a file was requested instead of a dir, list the
	// dir but mark the file as selected
	var result fileListing
	absDir, err := filepath.Abs(listingReq.Path)
	if err != nil {
		return Error{
			Err:        err,
			HTTPStatus: http.StatusBadRequest,
			Log:        "Computing absolute path",
		}
	}
	if up := filepath.Join(absDir, ".."); up != absDir {
		result.Up = up
	}
	if info.IsDir() {
		result.Dir = listingReq.Path
	} else {
		result.Selected = filepath.Base(listingReq.Path)
		result.Dir = filepath.Dir(listingReq.Path)
	}

	// open the directory
	dir, err := os.Open(result.Dir)
	if err != nil {
		return properError(err)
	}
	defer dir.Close()

	// get the directory listing (TODO: support pagination with this API...)
	const maxEntries = 2000
	fileInfos, err := dir.Readdir(maxEntries)
	if err != nil && !errors.Is(err, io.EOF) {
		return Error{
			Err:        err,
			HTTPStatus: http.StatusBadRequest,
			Log:        "Reading directory",
			Message:    "Unable to list the contents of that directory.",
		}
	}

	// convert the list of file infos into a list of files for the client
	result.Files = make([]localFile, 0, len(fileInfos))
	for _, info := range fileInfos {
		name := info.Name()
		fullName := filepath.Join(result.Dir, name)

		// filter the listing
		if listingReq.OnlyDirs && !info.IsDir() {
			continue
		}
		if !listingReq.ShowHidden && fileHidden(name) {
			continue
		}

		result.Files = append(result.Files, localFile{
			FullName: fullName,
			Name:     name,
			Size:     info.Size(),
			Mode:     info.Mode(),
			ModTime:  info.ModTime(),
			IsDir:    info.IsDir(),
		})
	}

	// sort alphabetically, with folders grouped at the top
	sort.Slice(result.Files, func(i, j int) bool {
		if result.Files[i].IsDir == result.Files[j].IsDir {
			return strings.ToLower(result.Files[i].Name) < strings.ToLower(result.Files[j].Name)
		}
		return result.Files[i].IsDir
	})

	return jsonResponse(w, result, nil)
}

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(_ *http.Request) bool { return true }, // we check Origin earlier
}
