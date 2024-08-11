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
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func shutdownTimelines() {
	// TODO: we need cleaner shutdowns, that wait until processing is complete after jobs are cancelled -- which allows updating import status properly (when possible)

	activeJobsMu.Lock()
	for _, job := range activeJobs {
		// can't call job.cleanUp() because we have the lock on activeJobs right now
		job.cancel()
		delete(activeJobs, job.ID)

		logger := timeline.Log.With(zap.Time("started", job.Started))

		switch job.Type {
		case "import":
			logger = timeline.Log.With(
				zap.String("repo", job.ImportParameters.Repo),
				zap.Strings("filenames", job.ImportParameters.Filenames),
				zap.Int64("account_id", job.ImportParameters.AccountID),
			)
		}
		logger.Warn("canceled active job")
	}
	activeJobsMu.Unlock()

	openTimelinesMu.Lock()
	for key, tl := range openTimelines {
		err := tl.Close()
		if err != nil {
			timeline.Log.Error("closing timeline",
				zap.String("repo_path", tl.Dir()),
				zap.String("instance_id", key),
				zap.Error(err))
		}
		delete(openTimelines, key)
	}
	openTimelinesMu.Unlock()
}

// getOpenTimeline gets an open timeline designated by instance ID.
// If the timeline is not open, a structured error is returned.
func getOpenTimeline(repoID string) (openedTimeline, error) {
	openTimelinesMu.RLock()
	if repoID == "" {
		// TODO: empty repo ID should probably mean ALL open repos;
		// but if only 1 is open, at least return that
		if len(openTimelines) == 1 {
			for _, tl := range openTimelines {
				openTimelinesMu.RUnlock()
				return tl, nil
			}
		}
	}
	otl, ok := openTimelines[repoID]
	openTimelinesMu.RUnlock()
	if !ok {
		return openedTimeline{}, fmt.Errorf("repository '%s' is not open", repoID)
	}
	return otl, nil
}

type activeJob struct {
	ID      string    `json:"id"`
	Type    string    `json:"type"`
	Started time.Time `json:"started"`

	// if an import job
	ImportParameters *ImportParameters `json:"import_parameters,omitempty"`

	ctx    context.Context
	cancel context.CancelFunc
}

func (job activeJob) cleanUp() {
	job.cancel()
	activeJobsMu.Lock()
	delete(activeJobs, job.ID)
	activeJobsMu.Unlock()
}

var (
	activeJobs   = make(map[string]activeJob)
	activeJobsMu sync.Mutex
)

type openedTimeline struct {
	RepoDir    string    `json:"repo_dir"` // absolute path
	InstanceID uuid.UUID `json:"instance_id"`

	*timeline.Timeline `json:"-"`

	fileServer http.Handler
}

var (
	openTimelines   = make(map[string]openedTimeline) // keyed by serialization of instance UUID
	openTimelinesMu sync.RWMutex
)
