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
	"sync"
	"time"

	"github.com/zeebo/blake3"
	"go.uber.org/zap"
)

const maxThrottleCPUPct = 0.25

var (
	throttleSize         = int(float64(runtime.NumCPU()) * maxThrottleCPUPct)
	cpuIntensiveThrottle = make(chan struct{}, throttleSize)
)

// CreateJob creates and runs a job described by action, with an estimated total units
// of work, to be repeated after a certain interval (if > 0). If an identical job is
// already running, the job will be queued. If an identical job is already queued, this
// will be a no-op. The created job ID is returned.
func (tl *Timeline) CreateJob(action JobAction, scheduled time.Time, total int, repeat time.Duration, parentJobID int64) (int64, error) {
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
		name = JobNameEmbeddings
	}

	var repeatPtr *time.Duration
	var startPtr *time.Time
	var totalPtr *int
	var parentJobIDPtr *int64
	if repeat > 0 {
		repeatPtr = &repeat
	}
	if total > 0 {
		totalPtr = &total
	}
	if parentJobID > 0 {
		parentJobIDPtr = &parentJobID
	}
	if !scheduled.IsZero() {
		startPtr = &scheduled
	}

	// this value is not the one that gets run, it is only used for storing
	job := Job{
		Name:        name,
		Config:      string(config),
		Start:       startPtr,
		Total:       totalPtr,
		Repeat:      repeatPtr,
		ParentJobID: parentJobIDPtr,
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

	// TODO: This log is a bit of a hack; it's to notify the UI that a new job
	// has been created, so it can be shown in the UI if relevant; but the
	// longer-term status logger doesn't get made until runJob...
	Log.Named("job.status").Info("created",
		zap.String("repo_id", tl.ID().String()),
		zap.Int64("id", jobID),
		zap.String("name", string(job.Name)),
		zap.Time("created", time.Now()),
		zap.Timep("start", job.Start),
		zap.Int64p("parent_job_id", parentJobIDPtr))

	err = tl.startJob(tl.ctx, tx, jobID)
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
func (tl *Timeline) storeJob(tx *sql.Tx, job Job) (int64, error) {
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

	var start *int64
	if job.Start != nil {
		startVal := job.Start.UnixMilli()
		start = &startVal
	}

	var id int64
	err = tx.QueryRowContext(tl.ctx, `
		INSERT INTO jobs (name, configuration, hash, hostname, start, total, repeat, parent_job_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		job.Name, job.Config, job.Hash, job.Hostname, start, job.Total, job.Repeat, job.ParentJobID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("inserting new job row: %w", err)
	}

	return id, nil
}

// loadJob loads a job from the database, using tx if set. A lock MUST be obtained on the
// timeline database when calling this function!
func (tl *Timeline) loadJob(ctx context.Context, tx *sql.Tx, jobID int64, parentAndChildJobs bool) (Job, error) {
	q := `SELECT
	id, name, configuration, hash, state, hostname,
	created, updated, start, ended,
	message, total, progress, checkpoint,
	repeat, parent_job_id
	FROM jobs
	WHERE id=?`
	vals := []any{jobID}
	if parentAndChildJobs {
		// we can get child jobs now, parent job will be had in a second query
		q += "OR parent_job_id=? LIMIT 100" // limit just in case, I guess
		vals = append(vals, jobID)
	} else {
		q += " LIMIT 1"
	}

	var rows *sql.Rows
	var err error
	if tx == nil {
		rows, err = tl.db.QueryContext(ctx, q, vals...)
	} else {
		rows, err = tx.QueryContext(ctx, q, vals...)
	}
	if err != nil {
		return Job{}, err
	}
	defer rows.Close()

	tlID := tl.id.String()
	var jobs []Job

	// scan all the job rows into the same slice for now, we'll figure out
	// which is the "main"/parent job later (the result set is not usually more
	// than a few rows)
	for rows.Next() {
		var job Job
		var created, updated, start, end *int64

		err = rows.Scan(
			&job.ID, &job.Name, &job.Config, &job.Hash, &job.State, &job.Hostname,
			&created, &updated, &start, &end,
			&job.Message, &job.Total, &job.Progress, &job.Checkpoint,
			&job.Repeat, &job.ParentJobID)
		if err != nil {
			return job, fmt.Errorf("scanning job fields: %w", err)
		}

		if created != nil {
			job.Created = time.UnixMilli(*created)
		}
		if updated != nil {
			ts := time.UnixMilli(*updated)
			job.Updated = &ts
		}
		if start != nil {
			ts := time.UnixMilli(*start)
			job.Start = &ts
		}
		if end != nil {
			ts := time.UnixMilli(*end)
			job.Ended = &ts
		}
		job.RepoID = tlID

		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return Job{}, err
	}

	if len(jobs) == 0 {
		return Job{}, sql.ErrNoRows
	}

	var parentJob Job
	for _, job := range jobs {
		if job.ID == jobID {
			parentJob = job
		} else {
			parentJob.Children = append(parentJob.Children, job)
		}
	}

	// load the parent of the parent, I guess
	if parentAndChildJobs && parentJob.ParentJobID != nil {
		grandparent, err := tl.loadJob(ctx, tx, *parentJob.ParentJobID, false)
		if err != nil {
			return Job{}, fmt.Errorf("loading parent job: %w", err)
		}
		parentJob.Parent = &grandparent
	}

	return parentJob, nil
}

// startJob makes sure an identical job is not already running, then
// sets the job's state to started and syncs it with the DB, then
// calls the action function in a goroutine.
func (tl *Timeline) startJob(ctx context.Context, tx *sql.Tx, jobID int64) error {
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
	job, err := tl.loadJob(tl.ctx, tx, jobID, false)
	if err != nil {
		return fmt.Errorf("loading job %d: %w", jobID, err)
	}
	if job.State == JobStarted || job.State == JobSucceeded {
		return fmt.Errorf("job %d has already %s and is not startable in this state", jobID, job.State)
	}

	start := func(tx *sql.Tx) error {
		// update the job's state to started
		now := time.Now()
		_, err = tx.ExecContext(ctx, `UPDATE jobs SET state=?, start=?, updated=? WHERE id=?`, // TODO: LIMIT 1
			JobStarted, now.UnixMilli(), now.UnixMilli(), jobID)
		if err != nil {
			return fmt.Errorf("updating job state: %w", err)
		}
		job.State = JobStarted
		job.Start = &now
		job.Updated = &now

		// update the job's state as we prepare to run it
		err = tx.QueryRowContext(ctx, `SELECT total, progress FROM jobs WHERE id=? LIMIT 1`, jobID).Scan(&job.Total, &job.Progress)
		if err != nil {
			return fmt.Errorf("unable to get starting progress and total amounts: %w", err)
		}

		// run the job
		if err = tl.runJob(job); err != nil {
			return fmt.Errorf("running job: %w", err)
		}

		return nil
	}

	if job.State == JobQueued && job.Start != nil && time.Now().Before(*job.Start) {
		go func(scheduledStart time.Time) {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Until(scheduledStart)):
				tl.dbMu.Lock()
				defer tl.dbMu.Unlock()

				logger := Log.Named("job")

				tx, err := tl.db.Begin()
				if err != nil {
					logger.Error("could not start transaction to start job", zap.Error(err))
					return
				}
				defer tx.Rollback()

				if err = start(tx); err != nil {
					logger.Error("could not start scheduled job", zap.Error(err))
					return
				}

				if err = tx.Commit(); err != nil {
					logger.Error("could not commit transaction for scheduled job", zap.Error(err))
					return
				}
			}
		}(*job.Start)
	} else {
		return start(tx)
	}

	return nil
}

// runJob deserializes the job's and starts a goroutine and
// calls its associated action function. The goroutine then finalizes
// the job and runs the next queued job, if any. This method does
// not sync the job's starting state to the DB, so call startJob()
// instead. It is expected that its state is running/started.
func (tl *Timeline) runJob(row Job) error {
	// create the job action by deserializing the config into its assocated struct
	var action JobAction
	switch row.Name {
	case JobNameImport:
		var importJob ImportJob
		if err := json.Unmarshal([]byte(row.Config), &importJob); err != nil {
			return fmt.Errorf("unmarshaling import job config: %w", err)
		}
		action = importJob
	case JobNameThumbnails:
		var thumbnailJob thumbnailJob
		if err := json.Unmarshal([]byte(row.Config), &thumbnailJob); err != nil {
			return fmt.Errorf("unmarshaling thumbnail job config: %w", err)
		}
		action = thumbnailJob
	case JobNameEmbeddings:
		var embeddingJob embeddingJob
		if err := json.Unmarshal([]byte(row.Config), &embeddingJob); err != nil {
			return fmt.Errorf("unmarshaling embedding job config: %w", err)
		}
		action = embeddingJob
	default:
		return fmt.Errorf("unknown job name '%s'", row.Name)
	}

	baseLogger := Log.Named("job").With(
		zap.String("repo_id", tl.ID().String()),
		zap.Int64("id", row.ID),
		zap.String("name", string(row.Name)),
		zap.Time("created", row.Created),
		zap.Timep("start", row.Start))

	// keep a pointer outside the ActiveJob struct because after
	// the job is done, we will update the logger, and it's not
	// thread-safe to change that field in the struct
	statusLog := baseLogger.Named("status")

	ctx, cancel := context.WithCancel(tl.ctx)

	job := &ActiveJob{
		ctx:             ctx,
		cancel:          cancel,
		done:            make(chan struct{}),
		id:              row.ID,
		tl:              tl,
		logger:          baseLogger.Named("action"),
		statusLog:       statusLog,
		parentJobID:     row.ParentJobID,
		currentState:    row.State,
		currentProgress: row.Progress,
		currentTotal:    row.Total,
		currentMessage:  row.Message,
	}

	// run the job asynchronously -- never block the calling goroutine!
	go func(job *ActiveJob, logger, statusLog *zap.Logger, row Job, action JobAction) {
		// when we're done here, clean up our map of active jobs
		defer func() {
			tl.activeJobsMu.Lock()
			delete(tl.activeJobs, job.id)
			tl.activeJobsMu.Unlock()
		}()

		statusLog.Info("running", zap.String("state", string(row.State)))

		// signal to any waiters when this job action returns; we intentionally
		// don't signal until after we've synced the job state to the DB
		defer close(job.done)

		// TODO: we should have a way of letting jobs report errors without
		// having to terminate, but still marking it as failed when done,
		// or at least record the errors even if it gets marked as "succeeded"
		// (maybe "succeeded" is too specific/optimistic a term, maybe "completed" or "done" or "finished" instead?)

		// run the job; we'll handle the error by logging the result and updating the state
		actionErr := action.Run(job, row.Checkpoint)

		var newState JobState
		switch {
		case actionErr == nil:
			newState = JobSucceeded
		case errors.Is(actionErr, context.Canceled):
			newState = JobAborted
		default:
			newState = JobFailed
		}

		// add info to the logger and log the result
		end := time.Now()
		statusLog = statusLog.With(zap.Time("ended", end))
		if row.Start != nil {
			statusLog = statusLog.With(zap.Duration("duration", end.Sub(*row.Start)))
		}

		job.mu.Lock()
		job.currentState = newState
		job.flushProgress(statusLog) // allow the UI to update live job progress display one last time
		job.mu.Unlock()

		// print a final message indicating the state and error, if any (TODO: could do above on that last flushProgress(), I guess)
		switch newState {
		case JobSucceeded:
			statusLog.Info(string(newState))
		case JobAborted:
			statusLog.Warn(string(newState), zap.Error(actionErr))
		case JobFailed:
			statusLog.Error(string(newState), zap.Error(actionErr))
		}

		// sync to the DB, and dequeue the next job
		tl.dbMu.Lock()
		defer tl.dbMu.Unlock()

		tx, err := tl.db.Begin()
		if err != nil {
			logger.Error("beginning transaction", zap.Error(err))
			return
		}
		defer tx.Rollback()

		_, err = tx.ExecContext(tl.ctx,
			`UPDATE jobs SET state=?, ended=?, updated=? WHERE id=?`, // TODO: LIMIT 1 (see https://github.com/mattn/go-sqlite3/pull/802)
			newState, end.UnixMilli(), end.UnixMilli(), job.id)
		if err != nil {
			logger.Error("updating job state", zap.Error(err))
		}

		if newState == JobSucceeded {
			// clear message and checkpoint in the DB if job succeeds (calling checkpoint() causes a sync to the DB)
			job.Message("")
			if err = job.checkpoint(tx, nil); err != nil {
				logger.Error("clearing job checkpoint and message from successful job", zap.Error(err))
			}
		} else {
			// for any other termination, just sync the job state to the DB
			job.mu.Lock()
			err := job.sync(tx)
			job.mu.Unlock()
			if err != nil {
				logger.Error("syncing job state with DB", zap.Error(err))
			}
		}

		hostname, err := os.Hostname()
		if err != nil {
			Log.Error("unable to lookup hostname while dequeuing next job", zap.Error(err))
		}

		// see if there's another job queued up we should run
		// (only run import jobs on the same machine they were configured from, since it uses external file paths)
		var nextJobID int64
		err = tx.QueryRowContext(tl.ctx,
			`SELECT id
			FROM jobs
			WHERE state=? AND (hostname=? OR name!=?)
			ORDER BY start, created
			LIMIT 1`,
			JobQueued, hostname, JobNameImport).Scan(&nextJobID)
		if nextJobID > 0 {
			err := tl.startJob(tl.ctx, tx, nextJobID)
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
	}(job, baseLogger, statusLog, row, action)

	tl.activeJobsMu.Lock()
	tl.activeJobs[job.id] = job
	tl.activeJobsMu.Unlock()

	return nil
}

// ActiveJob represents a job that is being actively loaded and run.
// It has methods which can manage the state of the job.
// It has a context which should be honored by implementations
// of JobAction. A ActiveJob value must not be copied. It maintains
// current progress/etc. that should be as authoritative as
// what is in the DB, although this struct is updated more
// frequently than the DB is synced.
type ActiveJob struct {
	ctx         context.Context
	cancel      context.CancelFunc
	done        chan (struct{}) // signaling channel that is closed when the job action returns
	id          int64
	tl          *Timeline
	logger      *zap.Logger
	statusLog   *zap.Logger
	parentJobID *int64

	mu sync.Mutex

	// protected by mu
	currentState      JobState
	currentProgress   *int
	currentTotal      *int
	currentMessage    *string // nil => unchanged, "" => clear
	currentCheckpoint []byte
	lastCheckpoint    *time.Time // when last checkpoint was created (may be more recent than last DB sync, which is throttled)
	lastSync          time.Time  // last DB update
	lastFlush         time.Time  // last frontend update
}

// Context returns the context the job is being run in. It should
// (usually) be the parent context of all others in the job.
func (j *ActiveJob) Context() context.Context { return j.ctx }

// ID returns the database row ID of the job.
func (j *ActiveJob) ID() int64 { return j.id }

// Timeline returns the timeline associated with the job.
func (j *ActiveJob) Timeline() *Timeline { return j.tl }

// Logger returns a logger for the job.
func (j *ActiveJob) Logger() *zap.Logger { return j.logger }

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

// Set the total size of the job in the DB. Ideally, this should only
// be set once the total size is properly known, not accumulated as
// you go, since progress bars rely on this to configure their display.
// So for example, if you do a bunch of work up front to estimate the
// size before starting the real work, don't call SetTotal() until
// that calculation has finished (use checkpoints to preserve your
// accumulated count). But if the total happens to change a little as
// you go, it's okay to call this a few times if needed. But the
// progress bar will show up as "indeterminate" as long as the total
// in the DB is nil (or 0?), so if the real work hasn't quite started
// yet, you don't want to disable that kind of display until you
// have started the work.
func (j *ActiveJob) SetTotal(total int) {
	j.mu.Lock()
	j.currentTotal = &total
	j.flushProgress(nil)
	j.mu.Unlock()
}

// FlushProgress forces a log to be written that updates the UI
// about the job.
func (j *ActiveJob) FlushProgress() {
	j.mu.Lock()
	j.flushProgress(j.statusLog)
	j.mu.Unlock()
}

// Progress updates the progress of the job in the DB by adding delta,
// which is work completed since the previous update, towards the expected
// total. This will update progress bars in the frontend UI. If the
// current progress is nil, it will be interpreted as 0 and delta will
// be the new progress value.
func (j *ActiveJob) Progress(delta int) {
	j.mu.Lock()
	newProgress := delta
	if j.currentProgress != nil {
		newProgress = *j.currentProgress + delta
	}
	j.currentProgress = &newProgress
	j.flushProgress(nil)
	j.mu.Unlock()
}

// flushProgress writes a log that the UI uses to update the progress of the job
// if it has been enough time since the last flush (we don't need to update the
// UI 1000x/sec). To force a flush regardless, pass in a logger, even if it's
// the default j.statusLog logger. This lets you add fields to the output when
// you have a reason to force a log emission.
// MUST BE CALLED IN A LOCK ON THE JOB MUTEX.
func (j *ActiveJob) flushProgress(logger *zap.Logger) {
	if logger != nil || time.Since(j.lastFlush) > jobFlushInterval {
		if logger == nil {
			logger = j.statusLog
		}
		logger.Info("progress",
			zap.String("state", string(j.currentState)),
			zap.Intp("progress", j.currentProgress),
			zap.Intp("total", j.currentTotal),
			zap.Stringp("message", j.currentMessage),
			zap.Timep("checkpointed", j.lastCheckpoint),
			zap.Int64p("parent_job_id", j.parentJobID))
		j.lastFlush = time.Now()
	}
}

// Message updates the current job message or status to show the user.
// TODO: rename to Status?
func (j *ActiveJob) Message(message string) {
	j.mu.Lock()
	if message == "" {
		j.currentMessage = nil
	} else {
		j.currentMessage = &message
	}
	j.flushProgress(nil)
	j.mu.Unlock()
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
func (j *ActiveJob) Checkpoint(newCheckpoint any) error {
	return j.checkpoint(nil, newCheckpoint)
	// TODO: emit log for the frontend to update the UI?
}

func (j *ActiveJob) checkpoint(tx *sql.Tx, newCheckpoint any) error {
	var chkpt []byte
	if newCheckpoint != nil {
		var err error
		chkpt, err = json.Marshal(newCheckpoint)
		if err != nil {
			return fmt.Errorf("JSON-encoding checkpoint %#v: %w", newCheckpoint, err)
		}
	}

	j.mu.Lock()
	defer j.mu.Unlock()

	j.currentCheckpoint = chkpt
	if chkpt != nil {
		now := time.Now()
		j.lastCheckpoint = &now
	}

	return j.sync(tx)
}

// sync writes changes/updates to the DB. In case of rapid job progression, we avoid
// writing to the DB with every call; only if enough time has passed, or if the job
// is done. If tx is non-nil, it is assumed the job is done (but if that assumption
// no longer holds true, we can change the signature of this method). In other words,
// passing in a tx forces a flush/sync to the DB.
//
// MUST BE CALLED IN A LOCK ON THE JOB MUTEX.
func (j *ActiveJob) sync(tx *sql.Tx) error {
	// no-op if last sync was too recent and the job isn't done yet (job done <==> tx!=nil)
	if time.Since(j.lastSync) < jobSyncInterval && tx == nil {
		return nil
	}

	q := `UPDATE jobs SET progress=?, total=?, message=?, checkpoint=?, updated=? WHERE id=?` // TODO: LIMIT 1
	vals := []any{j.currentProgress, j.currentTotal, j.currentMessage, j.currentCheckpoint, time.Now().UnixMilli(), j.id}

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

	j.lastSync = time.Now()

	return nil
}

func (tl *Timeline) GetJobs(ctx context.Context, jobIDs []int64) ([]Job, error) {
	jobs := make([]Job, len(jobIDs))

	for i, id := range jobIDs {
		tl.dbMu.RLock()
		job, err := tl.loadJob(ctx, nil, id, true)
		tl.dbMu.RUnlock()
		if err != nil {
			return nil, fmt.Errorf("loading job %d: %w", id, err)
		}
		job.RepoID = tl.id.String()

		// if job is running, we can provide more recent information than what
		// was last synced to the DB, which may be out-of-date at the moment
		if job.State == JobStarted {
			tl.activeJobsMu.RLock()
			activeJob, ok := tl.activeJobs[job.ID]
			tl.activeJobsMu.RUnlock()
			if ok {
				// we don't use a RWMutex because this mutex is write-heavy
				activeJob.mu.Lock()
				job.Progress = activeJob.currentProgress
				job.Message = activeJob.currentMessage
				job.Total = activeJob.currentTotal
				activeJob.mu.Unlock()
			}
		}

		jobs[i] = job
	}
	return jobs, nil
}

func (tl *Timeline) CancelJob(ctx context.Context, jobID int64) error {
	tl.activeJobsMu.Lock()
	job, ok := tl.activeJobs[jobID]
	tl.activeJobsMu.Unlock()
	if !ok {
		return fmt.Errorf("job %d is either inactive or does not exist", jobID)
	}

	job.cancel()

	// wait for job action to return; this ensures its
	// state has been synced to the DB and it has been
	// removed from the map of active jobs
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-job.done:
	}

	return nil
}

// Job is only to be used for shuttling job data in and out of the DB,
// it should not be assumed to accurately reflect the current state of the
// job. Mainly used for inserting a job row, and getting a job row for the
// frontend.
type Job struct {
	// not a field in the DB, but useful for bookkeeping where multiple timelines are open
	RepoID string `json:"repo_id"`

	ID          int64          `json:"id"`
	Name        JobName        `json:"name"`
	Config      string         `json:"config,omitempty"` // JSON encoding of, for instance, ImportParameters, etc.
	Hash        []byte         `json:"hash,omitempty"`
	State       JobState       `json:"state"`
	Hostname    *string        `json:"hostname,omitempty"`
	Created     time.Time      `json:"created,omitempty"`
	Updated     *time.Time     `json:"updated,omitempty"`
	Start       *time.Time     `json:"start,omitempty"` // could be future, so not "started"
	Ended       *time.Time     `json:"ended,omitempty"`
	Message     *string        `json:"message,omitempty"`
	Total       *int           `json:"total,omitempty"`
	Progress    *int           `json:"progress,omitempty"`
	Checkpoint  []byte         `json:"checkpoint,omitempty"`
	Repeat      *time.Duration `json:"repeat,omitempty"`
	ParentJobID *int64         `json:"parent_job_id,omitempty"`

	// only used when loading jobs from the DB for the frontend
	Parent   *Job  `json:"parent,omitempty"`
	Children []Job `json:"children,omitempty"`
}

func (j Job) hash() []byte {
	h := blake3.New()
	_, _ = h.WriteString(string(j.Name))
	_, _ = h.WriteString(j.Config)
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
	Run(job *ActiveJob, checkpoint []byte) error
}

type JobName string

const (
	JobNameImport     JobName = "import"
	JobNameThumbnails JobName = "thumbnails"
	JobNameEmbeddings JobName = "embeddings"
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

const (
	jobSyncInterval  = 2 * time.Second        // how often to update the DB
	jobFlushInterval = 250 * time.Millisecond // how often to update the frontend
)
