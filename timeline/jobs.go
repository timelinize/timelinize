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
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/zeebo/blake3"
	"go.uber.org/zap"
)

const maxThrottleCPUPct = 0.75

var (
	throttleSize         = int(float64(runtime.NumCPU()) * maxThrottleCPUPct)
	cpuIntensiveThrottle = make(chan struct{}, throttleSize)
)

// CreateJob creates and runs a job described by action, with an estimated total units
// of work, to be repeated after a certain interval (if > 0). If an identical job is
// already running, the job will be queued. If an identical job is already queued, this
// will be a no-op. The created job ID is returned.
func (tl *Timeline) CreateJob(action JobAction, total int, repeat time.Duration) (int64, error) {
	config, err := json.Marshal(action)
	if err != nil {
		return 0, fmt.Errorf("JSON-encoding job action: %w", err)
	}

	var name JobName
	switch action.(type) {
	case ImportJob:
		name = JobNameImport
	case thumbnailJob:
		name = JobNameThumbnails
	case embeddingJob:
		name = JobNameEmbedding
	}

	var repeatPtr *time.Duration
	var totalPtr *int
	if repeat > 0 {
		repeatPtr = &repeat
	}
	if total > 0 {
		totalPtr = &total
	}

	// this value is not the one that gets run, it is only used for storing
	job := jobRow{
		Name:   name,
		Config: config,
		Total:  totalPtr,
		Repeat: repeatPtr,
	}

	tl.dbMu.Lock()
	defer tl.dbMu.Unlock()

	tx, err := tl.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	jobID, err := tl.storeJob(tx, job)
	if err != nil {
		return 0, err
	}
	err = tl.startJob(tx, jobID)
	if err != nil {
		return 0, err
	}

	if err = tx.Commit(); err != nil {
		return 0, fmt.Errorf("committing new job transaction: %w", err)
	}

	return jobID, nil
}

// storeJob adds the job to the database. It does not start it.
// TODO: Job configs could be compressed to save space in the DB...
func (tl *Timeline) storeJob(tx *sql.Tx, job jobRow) (int64, error) {
	job.Hash = job.hash()

	hostname, err := os.Hostname()
	if err != nil {
		Log.Error("unable to lookup hostname while adding job", zap.Error(err))
	}
	job.Hostname = &hostname

	// first see if any job with the same hash is already queued; if so,
	// no-op since this would be a duplicate
	var count int
	err = tx.QueryRowContext(tl.ctx,
		`SELECT count() FROM jobs WHERE hash=? AND state=? LIMIT 1`,
		job.Hash, JobQueued).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("checking for duplicate job: %w", err)
	}
	if count > 0 {
		return 0, nil
	}

	var id int64
	err = tx.QueryRowContext(tl.ctx, `
		INSERT INTO jobs (name, configuration, hash, hostname, total, repeat)
		VALUES (?, ?, ?, ?, ?, ?)
		RETURNING id`,
		job.Name, string(job.Config), job.Hash, job.Hostname, job.Total, job.Repeat).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("inserting new job row: %w", err)
	}

	return id, nil
}

func (tl *Timeline) loadJob(ctx context.Context, tx *sql.Tx, jobID int64) (jobRow, error) {
	var job jobRow
	var created, start, end *int64
	err := tx.QueryRowContext(ctx,
		`SELECT
			jobs.id, jobs.name, jobs.configuration, jobs.hash, jobs.state,
			jobs.hostname, jobs.created, jobs.start, jobs.end,
			jobs.message, jobs.total, jobs.progress,
			jobs.checkpoint, jobs.repeat, jobs.prev_job_id
		FROM jobs
		WHERE jobs.id=?
		LIMIT 1`,
		jobID).Scan(&job.ID, &job.Name, &job.Config, &job.Hash, &job.State,
		&job.Hostname, &created, &start, &end,
		&job.Message, &job.Total, &job.Progress,
		&job.Checkpoint, &job.Repeat, &job.PrevJobID)
	if err != nil {
		return job, err
	}
	if created != nil {
		job.Created = time.Unix(*created, 0)
	}
	if start != nil {
		ts := time.Unix(*start, 0)
		job.Start = &ts
	}
	if end != nil {
		ts := time.Unix(*end, 0)
		job.End = &ts
	}
	return job, nil
}

// startJob makes sure an identical job is not already running, then
// sets the job's state to started and syncs it with the DB, then
// calls the action function in a goroutine.
func (tl *Timeline) startJob(tx *sql.Tx, jobID int64) error {
	// if an identical job is already running, we shouldn't start yet
	var count int
	err := tx.QueryRowContext(tl.ctx,
		`SELECT count()
		FROM jobs
		WHERE id!=?
			AND hash=(SELECT hash FROM jobs WHERE id=? LIMIT 1)
			AND state=?
		LIMIT 1`,
		jobID, jobID, JobStarted).Scan(&count)
	if err != nil {
		return fmt.Errorf("checking for duplicate running job: %w", err)
	}
	if count > 0 {
		return errors.New("identical job is already running")
	}

	// load the job and verify it is in a startable state
	job, err := tl.loadJob(tl.ctx, tx, jobID)
	if err != nil {
		return fmt.Errorf("loading job %d: %w", jobID, err)
	}
	if job.State == JobStarted || job.State == JobSucceeded {
		return fmt.Errorf("job %d has already %s and is not startable in this state", jobID, job.State)
	}

	// update the job's state to started
	now := time.Now()
	_, err = tx.ExecContext(tl.ctx, `UPDATE jobs SET state=?, start=? WHERE id=?`, // TODO: LIMIT 1
		JobStarted, now.Unix(), jobID)
	if err != nil {
		return fmt.Errorf("updating job state: %w", err)
	}
	job.State = JobStarted
	job.Start = &now

	// run the job
	if err = tl.runJob(job); err != nil {
		return fmt.Errorf("running job: %w", err)
	}

	return nil
}

// runJob deserializes the job's config and starts a goroutine and
// calls its associated action function. The goroutine then finalizes
// the job and runs the next queued job, if any. This method does
// not sync the job's starting state to the DB, so call startJob()
// instead.
func (tl *Timeline) runJob(row jobRow) error {
	// create the job action by deserializing the config into its assocated struct
	var action JobAction
	switch row.Name {
	case JobNameImport:
		var importJob ImportJob
		if err := json.Unmarshal(row.Config, &importJob); err != nil {
			return fmt.Errorf("unmarshaling import job config: %w", err)
		}
		action = importJob
	case JobNameThumbnails:
		var thumbnailJob thumbnailJob
		if err := json.Unmarshal(row.Config, &thumbnailJob); err != nil {
			return fmt.Errorf("unmarshaling thumbnail job config: %w", err)
		}
		action = thumbnailJob
	case JobNameEmbedding:
		var embeddingJob embeddingJob
		if err := json.Unmarshal(row.Config, &embeddingJob); err != nil {
			return fmt.Errorf("unmarshaling embedding job config: %w", err)
		}
		action = embeddingJob
	default:
		return fmt.Errorf("unknown job name '%s'", row.Name)
	}

	job := &Job{
		ctx: tl.ctx,
		id:  row.ID,
		tl:  tl,
		logger: Log.Named("job").With(
			zap.String("job_name", string(row.Name)),
			zap.Int64("job_id", row.ID),
			zap.Time("job_created", row.Created),
			zap.Timep("job_started", row.Start),
			zap.String("timeline", tl.ID().String())),
	}

	// run the job asynchronously -- never block the calling goroutine!
	go func(job *Job, row jobRow, action JobAction) {
		job.logger.Info("starting job")

		// TODO: we should have a way of letting jobs report errors without
		// having to terminate, but still marking it as failed when done,
		// or at least record the errors even if it gets marked as "succeeded"
		// (maybe "succeeded" is too specific/optimistic a term, maybe "completed" or "done" or "finished" instead?)

		// do the job
		actionErr := action.Run(job, row.Checkpoint)

		// we'll handle the error by logging the result and updating the state

		// create new logger, because changing the job.logger is not thread-safe
		end := time.Now()
		logger := job.logger.With(zap.Time("ended", end))
		if row.Start != nil {
			logger = logger.With(zap.Duration("duration", end.Sub(*row.Start)))
		}

		var newState JobState
		switch {
		case actionErr == nil:
			newState = JobSucceeded
			logger.Info("job succeeded", zap.Error(actionErr))
		case errors.Is(actionErr, context.Canceled):
			newState = JobAborted
			logger.Error("job aborted", zap.Error(actionErr))
		default:
			newState = JobFailed
			logger.Error("job failed", zap.Error(actionErr))
		}

		// when we're done here, clean up our map of active jobs
		defer func() {
			activeJobsMu.Lock()
			delete(activeJobs, job.id)
			activeJobsMu.Unlock()
		}()

		// sync to the DB, and dequeue the next job
		tl.dbMu.Lock()
		defer tl.dbMu.Unlock()

		tx, err := tl.db.Begin()
		if err != nil {
			logger.Error("beginning transaction", zap.Error(err))
			return
		}
		defer tx.Rollback()

		_, err = tx.ExecContext(job.Context(),
			`UPDATE jobs SET state=?, end=? WHERE id=?`, // TODO: LIMIT 1 (see https://github.com/mattn/go-sqlite3/pull/802)
			newState, end.Unix(), job.id)
		if err != nil {
			logger.Error("updating job state", zap.Error(err))
		}

		// clear message and checkpoint in the DB if job succeeds (calling checkpoint() causes a sync to the DB)
		if newState == JobSucceeded {
			job.Message("")
			if err = job.checkpoint(tx, nil); err != nil {
				logger.Error("clearing job checkpoint and message from successful job", zap.Error(err))
			}
		}

		// see if there's another job queued up we should run
		var nextJobID int64
		err = tx.QueryRowContext(job.Context(),
			`SELECT id
			FROM jobs
			WHERE state=? AND hostname=?
			ORDER BY created
			LIMIT 1`,
			JobQueued, row.Hostname).Scan(&nextJobID)
		if nextJobID > 0 {
			err := tl.startJob(tx, nextJobID)
			if err != nil {
				logger.Error("starting next job", zap.Error(err))
			}
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			logger.Error("querying for next job", zap.Error(err))
		}

		if err = tx.Commit(); err != nil {
			logger.Error("committing post-job transaction", zap.Error(err))
		}
	}(job, row, action)

	activeJobsMu.Lock()
	activeJobs[job.id] = job
	activeJobsMu.Unlock()

	return nil
}

// Job represents a job that is being actively loaded and run.
// It has methods which can manage the state of the job.
// It has a context which should be honored by implementations
// of JobAction. A Job value must not be copied.
type Job struct {
	ctx    context.Context
	id     int64
	tl     *Timeline
	logger *zap.Logger

	mu sync.Mutex

	// protected by mu
	progressSinceLastSync   int
	totalSinceLastSync      *int
	checkpointSinceLastSync []byte
	clearCheckpoint         bool
	messageSinceLastSync    *string // nil => unchanged, "" => clear
	lastSync                time.Time
}

// Context returns the context the job is being run in. It should
// (usually) be the parent context of all others in the job.
func (j *Job) Context() context.Context { return j.ctx }

// ID returns the database row ID of the job.
func (j *Job) ID() int64 { return j.id }

// Timeline returns the timeline associated with the job.
func (j *Job) Timeline() *Timeline { return j.tl }

// Logger returns a logger for the job.
func (j *Job) Logger() *zap.Logger { return j.logger }

// // TODO: What we really need is a way to block until an interactive import job is ready to resume
// func (j *Job) Pause() error {
// 	j.mu.Lock()
// 	defer j.mu.Unlock()

// 	j.tl.dbMu.Lock()
// 	_, err := j.tl.db.ExecContext(j.ctx, `UPDATE jobs SET state=? WHERE id=?`, JobPaused, j.id) // TODO: LIMIT 1
// 	j.tl.dbMu.Unlock()
// 	if err != nil {
// 		return err
// 	}

// }

func (j *Job) SetTotal(total int) {
	j.mu.Lock()
	j.totalSinceLastSync = &total
	j.mu.Unlock()
	// TODO: emit log for the frontend to update the UI
}

// Progress updates the progress of the job in the DB by adding delta,
// which is work completed since the previous update, towards the expected
// total. This will update progress bars in the frontend UI.
func (j *Job) Progress(delta int) {
	j.mu.Lock()
	j.progressSinceLastSync += delta
	j.mu.Unlock()
	// TODO: emit log for the frontend to update the UI
}

// Message updates the current job message or status to show the user.
// TODO: rename to Status?
func (j *Job) Message(message string) {
	j.mu.Lock()
	j.messageSinceLastSync = &message
	j.mu.Unlock()
	// TODO: emit log for the frontend to update the UI
}

// Checkpoint creates a checkpoint and syncs the job state with the database.
// When creating a checkpoint, the total, progress, and message data should
// be current such that resuming the job from this checkpoint would have the
// correct total, progress, and message displayed. Usually, a checkpoint is
// where to begin/resume the job. For example, if a job started at unit of
// work ("task"?) index 0 and finished up through 3, the progress value would
// have been updated to be 4 and the checkpoint would contain index 4 (so,
// progress THEN checkpoint, usually).
//
// Checkpoint values are opaque and MUST be JSON-marshallable. They will be
// returned to the job action in the form of JSON bytes.
func (j *Job) Checkpoint(newCheckpoint any) error {
	return j.checkpoint(nil, newCheckpoint)
	// TODO: emit log for the frontend to update the UI
}

func (j *Job) checkpoint(tx *sql.Tx, newCheckpoint any) error {
	chkpt, err := json.Marshal(newCheckpoint)
	if err != nil {
		return fmt.Errorf("JSON-encoding checkpoint %#v: %w", newCheckpoint, err)
	}

	j.mu.Lock()
	defer j.mu.Unlock()

	if newCheckpoint == nil {
		// differentiate between an unset/un-updated checkpoint, and
		// one that has been cleared, with a separate boolean
		j.clearCheckpoint = true
		chkpt = nil
	}
	j.checkpointSinceLastSync = chkpt

	return j.sync(tx)
}

// sync writes changes/updates to the DB. In case of rapid job progression, we avoid
// writing to the DB with every call; only if enough time has passed, or if the job
// is done. If tx is non-nil, it is assumed the job is done (but if that assumption
// no longer holds true, we can change the signature of this method). In other words,
// passing in a tx forces a flush/sync to the DB.
//
// MUST BE CALLED IN A LOCK ON THE JOB MUTEX.
func (j *Job) sync(tx *sql.Tx) error {
	// no-op if last sync was too recent and the job isn't done yet (job done <==> tx!=nil)
	if time.Since(j.lastSync) < jobSyncInterval && tx == nil {
		return nil
	}

	var sb strings.Builder

	sb.WriteString("UPDATE jobs SET ")

	var vals []any
	if j.progressSinceLastSync != 0 {
		sb.WriteString("progress = COALESCE(progress, 0)+?")
		vals = append(vals, j.progressSinceLastSync)
	}
	if j.checkpointSinceLastSync != nil || j.clearCheckpoint {
		if len(vals) > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString("checkpoint=?")
		vals = append(vals, j.checkpointSinceLastSync)
	}
	if j.totalSinceLastSync != nil {
		if len(vals) > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString("total=?")
		vals = append(vals, *j.totalSinceLastSync)
	}
	if j.messageSinceLastSync != nil {
		if len(vals) > 0 {
			sb.WriteString(", ")
		}
		// a non-nil message has been set/updated; non-nil empty string clears it
		message := j.messageSinceLastSync
		if *message == "" {
			message = nil
		}
		sb.WriteString("message=?")
		vals = append(vals, message)
	}

	// no-op if no updates since last sync
	if len(vals) == 0 {
		return nil
	}

	sb.WriteString(" WHERE id=?") // TODO: LIMIT 1
	vals = append(vals, j.id)

	q := sb.String()

	var err error
	if tx == nil {
		j.tl.dbMu.Lock()
		_, err = j.tl.db.ExecContext(j.tl.ctx, q, vals...)
		j.tl.dbMu.Unlock()
	} else {
		_, err = tx.ExecContext(j.tl.ctx, q, vals...)
	}
	if err != nil {
		return fmt.Errorf("syncing progress with DB: %w", err)
	}

	j.progressSinceLastSync = 0
	j.totalSinceLastSync = nil
	j.checkpointSinceLastSync = nil
	j.clearCheckpoint = false
	j.messageSinceLastSync = nil
	j.lastSync = time.Now()

	return nil
}

// jobRow is only to be used for shuttling job data in and out of the DB,
// it should not be assumed to accurately reflect the current state of the
// job. Mainly used for inserting a job row, and getting a job row for the
// frontend.
type jobRow struct {
	ID         int64          `json:"id"`
	Name       JobName        `json:"name"`
	Config     []byte         `json:"config,omitempty"` // JSON encoding of, for instance, ImportParameters, etc.
	Hash       []byte         `json:"hash,omitempty"`
	State      JobState       `json:"state"`
	Hostname   *string        `json:"hostname,omitempty"`
	Created    time.Time      `json:"created,omitempty"`
	Start      *time.Time     `json:"start,omitempty"`
	End        *time.Time     `json:"end,omitempty"`
	Message    *string        `json:"message,omitempty"`
	Total      *int           `json:"total,omitempty"`
	Progress   *int           `json:"progress,omitempty"`
	Checkpoint []byte         `json:"checkpoint,omitempty"`
	Repeat     *time.Duration `json:"repeat,omitempty"`
	PrevJobID  *int64         `json:"prev_job_id,omitempty"`
}

func (j jobRow) hash() []byte {
	h := blake3.New()
	_, _ = h.WriteString(string(j.Name))
	_, _ = h.Write(j.Config)
	if j.Hostname != nil {
		_, _ = h.WriteString(*j.Hostname)
	}
	return h.Sum(nil)
}

// JobAction is a type that does actual work of a job.
// It must be JSON-encodable.
type JobAction interface {
	// Run runs or resumes a job. It should utilize the
	// checkpoint, if set, to resume an interrupted job.
	// CPU-intensive work should use the throttle. If
	// possible, the job status should be updated as it
	// progresses. The job has a context which should
	// be honored. The function should not return until
	// the job is done (whether successful or not); this
	// means waiting until any spawned goroutines have
	// finished (using sync.WaitGroup is good parctice).
	Run(job *Job, checkpoint []byte) error
}

type JobName string

const (
	JobNameImport     JobName = "import"
	JobNameThumbnails JobName = "thumbnails"
	JobNameEmbedding  JobName = "embedding"
)

type JobState string

const (
	JobQueued    JobState = "queued"    // on deck
	JobStarted   JobState = "started"   // currently running
	JobPaused    JobState = "paused"    // intentional, graceful (don't auto-restart)
	JobAborted   JobState = "aborted"   // unintentional, forced, interrupted (may be auto-restarted)
	JobSucceeded JobState = "succeeded" // final (cannot be restarted) // TODO: what if units of work fail, though? maybe this should be "done" or "completed"
	JobFailed    JobState = "failed"    // final (can be manually restarted)
)

const jobSyncInterval = 2 * time.Second

var (
	activeJobs   = make(map[int64]*Job)
	activeJobsMu sync.RWMutex
)
