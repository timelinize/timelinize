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
	throttleSize         = max(int(float64(runtime.NumCPU())*maxThrottleCPUPct), 1)
	cpuIntensiveThrottle = make(chan struct{}, throttleSize)
)

// CreateJob creates and runs a job described by action, with an estimated total units
// of work, to be repeated after a certain interval (if > 0). If an identical job is
// already running, the job will be queued. If an identical job is already queued, this
// will be a no-op. The created job ID is returned.
func (tl *Timeline) CreateJob(action JobAction, scheduled time.Time, repeat time.Duration, total int, parentJobID uint64) (uint64, error) {
	config, err := json.Marshal(action)
	if err != nil {
		return 0, fmt.Errorf("JSON-encoding job action: %w", err)
	}

	var jobType JobType
	switch action.(type) {
	case *ImportJob:
		jobType = JobTypeImport
	case thumbnailJob:
		jobType = JobTypeThumbnails
	case embeddingJob:
		jobType = JobTypeEmbeddings
	default:
		return 0, fmt.Errorf("unexpected job action: %#v", action)
	}

	var repeatPtr *time.Duration
	var startPtr *time.Time
	var totalPtr *int
	var parentJobIDPtr *uint64
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
		Type:        jobType,
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
	// longer-term status logger doesn't get made until runJob... so we have
	// to make a logger with the same name and many of the same fields...
	Log.Named("job.status").Info("created",
		zap.String("repo_id", tl.ID().String()),
		zap.Uint64("id", jobID),
		zap.String("type", string(job.Type)),
		zap.Time("created", time.Now()),
		zap.Timep("start", job.Start),
		zap.Uint64p("parent_job_id", parentJobIDPtr),
		zap.Intp("size", totalPtr))

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
func (tl *Timeline) storeJob(tx *sql.Tx, job Job) (uint64, error) {
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

	var id uint64
	err = tx.QueryRowContext(tl.ctx, `
		INSERT INTO jobs (type, configuration, hash, hostname, start, total, repeat, parent_job_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		job.Type, job.Config, job.Hash, job.Hostname, start, job.Total, job.Repeat, job.ParentJobID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("inserting new job row: %w", err)
	}

	return id, nil
}

// loadJob loads a job from the database, using tx if set. A lock MUST be obtained on the
// timeline database when calling this function!
func (tl *Timeline) loadJob(ctx context.Context, tx *sql.Tx, jobID uint64, parentAndChildJobs bool) (Job, error) {
	q := `SELECT
	id, type, name, configuration, hash, state, hostname,
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
		job, err := scanJob(rows, tlID)
		if err != nil {
			return Job{}, err
		}
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
func (tl *Timeline) startJob(ctx context.Context, tx *sql.Tx, jobID uint64) error {
	// if an identical job is already running, we shouldn't start yet
	var count int
	err := tx.QueryRowContext(tl.ctx,
		`SELECT count()
		FROM jobs
		WHERE id!=?
			AND hash=(SELECT hash FROM jobs WHERE id=? LIMIT 1)
			AND (state=? OR state=?)
		LIMIT 1`,
		jobID, jobID, JobStarted, JobPaused).Scan(&count)
	if err != nil {
		return fmt.Errorf("checking for duplicate running job: %w", err)
	}
	if count > 0 {
		return errors.New("identical job is already running")
	}

	// verify the job is not already loaded (using UnpauseJob is suitable in that case, so we don't
	// double-load it) and then load the job to verify it is in a startable state
	tl.activeJobsMu.RLock()
	_, runningOrPaused := tl.activeJobs[jobID]
	tl.activeJobsMu.RUnlock()
	if runningOrPaused {
		return fmt.Errorf("job %d is already active/loaded and cannot be loaded and started again", jobID)
	}
	job, err := tl.loadJob(tl.ctx, tx, jobID, false)
	if err != nil {
		return fmt.Errorf("loading job %d: %w", jobID, err)
	}
	if job.State == JobStarted || job.State == JobSucceeded {
		// paused is an allowed state to start from, as long as it's not already loaded/active (checked above),
		// because the process could have stopped while the job was paused
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

				// ensure job was not aborted while we were waiting for it to start
				var unabortedJobCount int
				err = tx.QueryRowContext(ctx, `SELECT count() FROM jobs WHERE id=? AND state!=? LIMIT 1`, jobID, JobAborted).Scan(&unabortedJobCount)
				if errors.Is(err, sql.ErrNoRows) || unabortedJobCount == 0 {
					logger.Error("job was aborted before it started")
					return
				}

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
	switch row.Type {
	case JobTypeImport:
		var importJob *ImportJob
		if err := json.Unmarshal([]byte(row.Config), &importJob); err != nil {
			return fmt.Errorf("unmarshaling import job config: %w", err)
		}
		action = importJob
	case JobTypeThumbnails:
		var thumbnailJob thumbnailJob
		if err := json.Unmarshal([]byte(row.Config), &thumbnailJob); err != nil {
			return fmt.Errorf("unmarshaling thumbnail job config: %w", err)
		}
		action = thumbnailJob
	case JobTypeEmbeddings:
		var embeddingJob embeddingJob
		if err := json.Unmarshal([]byte(row.Config), &embeddingJob); err != nil {
			return fmt.Errorf("unmarshaling embedding job config: %w", err)
		}
		action = embeddingJob
	default:
		return fmt.Errorf("unknown job type '%s'", row.Type)
	}

	baseLogger := Log.Named("job").With(
		zap.String("repo_id", tl.ID().String()),
		zap.Uint64("id", row.ID),
		zap.String("type", string(row.Type)),
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
		pause:           make(chan chan struct{}),
		done:            make(chan struct{}),
		id:              row.ID,
		tl:              tl,
		logger:          baseLogger.Named("action"),
		statusLog:       statusLog,
		parentJobID:     row.ParentJobID,
		action:          action,
		currentState:    row.State,
		currentProgress: row.Progress,
		currentTotal:    row.Total,
		currentMessage:  row.Message,
	}

	// run the job asynchronously -- never block the calling goroutine!
	go func(job *ActiveJob, logger, statusLog *zap.Logger, row Job, action JobAction) {
		// don't allow a job, which is doing who-knows-what and processing who-knows-what
		// input, to bring down the program
		defer func() {
			if r := recover(); r != nil {
				logger.Error("panic", zap.Any("error", r))
			}
		}()

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
			// clear message and checkpoint in the DB if job succeeds
			job.Message("")
			if err = job.sync(tx, nil); err != nil {
				logger.Error("clearing job checkpoint and message from successful job", zap.Error(err))
			}
		} else {
			// for any other termination, sync only the job state to the DB
			job.mu.Lock()
			checkpoint := job.currentCheckpoint
			job.mu.Unlock()
			err := job.sync(tx, checkpoint)
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
		var nextJobID uint64
		err = tx.QueryRowContext(tl.ctx,
			`SELECT id
			FROM jobs
			WHERE (state=? OR state=?) AND (hostname=? OR type!=?)
			ORDER BY start, created
			LIMIT 1`,
			JobQueued, JobInterrupted, hostname, JobTypeImport).Scan(&nextJobID)
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
	pause       chan chan struct{} // send to the outer channel to pause, receive on the inner channel to unpause
	done        chan struct{}      // signaling channel that is closed when the job action returns
	id          uint64
	tl          *Timeline
	logger      *zap.Logger
	statusLog   *zap.Logger
	parentJobID *uint64
	action      JobAction

	mu sync.Mutex

	// protected by mu
	paused            chan struct{} // set by the job manager when pausing; closing this unpauses the job
	currentState      JobState
	currentProgress   *int
	currentTotal      *int
	currentMessage    *string // nil => unchanged, "" => clear
	currentCheckpoint any
	lastCheckpoint    *time.Time // when last checkpoint was created (may be more recent than last DB sync, which is throttled)
	lastSync          time.Time  // last DB update
	lastFlush         time.Time  // last frontend update
}

// Context returns the context the job is being run in. It should
// (usually) be the parent context of all others in the job.
func (j *ActiveJob) Context() context.Context { return j.ctx }

// ID returns the database row ID of the job.
func (j *ActiveJob) ID() uint64 { return j.id }

// Timeline returns the timeline associated with the job.
func (j *ActiveJob) Timeline() *Timeline { return j.tl }

// Logger returns a logger for the job.
func (j *ActiveJob) Logger() *zap.Logger { return j.logger }

// Continue should be called by all job actions frequently, typically at
// the beginning of their main loop (and any longer-running inner loops).
// It blocks if the job is paused, until it is unpaused. It returns an
// error if the job context has been canceled; i.e. the job is being
// canceled and should terminate. It does not block if the job is not
// paused or canceled.
func (j *ActiveJob) Continue() error {
	select {
	case <-j.ctx.Done():
		return j.ctx.Err()
	case unpause := <-j.pause:
		// block until either cancelled or unpaused
		select {
		case <-j.ctx.Done():
			return j.ctx.Err()
		case <-unpause:
		}
	default:
	}
	return nil
}

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
			zap.Uint64p("parent_job_id", j.parentJobID))
		j.lastFlush = time.Now()
		_ = logger.Sync() // ensure it gets written promptly
	}
}

// Message updates the current job message or status to show the user.
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
// where to begin/resume the job, since it's often the starting index in a
// loop. For example, if a job started at unit of work ("task"?) index 0 and
// finished up through 3, the progress value would have been updated to be 4
// and the checkpoint would contain index 4. The job would then resume with
// starting at index 4, which had not been completed yet.
//
// Checkpoint values are opaque and MUST be JSON-marshallable. They will be
// returned to the job action in the form of JSON bytes.
func (j *ActiveJob) Checkpoint(newCheckpoint any) error {
	return j.sync(nil, newCheckpoint)
	// TODO: emit log for the frontend to update the UI?
}

// sync writes changes/updates to the DB. In case of rapid job progression, we avoid
// writing to the DB with every call; only if enough time has passed, or if the job
// is done. If tx is non-nil, it is assumed the job is done (but if that assumption
// no longer holds true, we can change the signature of this method). In other words,
// passing in a tx forces a flush/sync to the DB.
//
// This is how a checkpoint is stored as well, since it doesn't make sense to only
// have a checkpoint in memory (pausing a job does not use a checkpoint since it
// remains in memory) -- only resuming a job after it has been removed from memory
// (usually by program exit or intentional abort, etc.) needs a checkpoint, so
// creating a checkpoint and syncing to the DB are the same operation. By the same
// token, the progress, message, and total fields in the DB row must also correspond
// with the checkpoint, otherwise resuming a job will be out of whack. So it is
// important that syncing to the DB only happens with a checkpoint to go with it
// (if there is a checkpoint at all). Passing a nil checkpoint here will clear it.
func (j *ActiveJob) sync(tx *sql.Tx, checkpoint any) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.currentCheckpoint = checkpoint

	// no-op if last sync was too recent and the job isn't done yet (job done <==> tx!=nil)
	if time.Since(j.lastSync) < jobSyncInterval && tx == nil {
		return nil
	}

	var chkpt []byte
	if checkpoint != nil {
		var err error
		chkpt, err = json.Marshal(checkpoint)
		if err != nil {
			return fmt.Errorf("JSON-encoding checkpoint %#v: %w", checkpoint, err)
		}
		now := time.Now()
		j.lastCheckpoint = &now
	}

	q := `UPDATE jobs SET progress=?, total=?, message=?, checkpoint=?, updated=? WHERE id=?` // TODO: LIMIT 1
	vals := []any{j.currentProgress, j.currentTotal, j.currentMessage, chkpt, time.Now().UnixMilli(), j.id}

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

// GetJobs loads the jobs with the specified IDs, or by the most recent jobs, whichever is set.
// Both technically can be set, but why?
func (tl *Timeline) GetJobs(ctx context.Context, jobIDs []uint64, mostRecent int) ([]Job, error) {
	var jobs []Job //nolint:prealloc // false positive! can't always know how many we'll have in this case

	// load most recent jobs
	if mostRecent > 0 {
		q := `SELECT
	id, type, name, configuration, hash, state, hostname,
	created, updated, start, ended,
	message, total, progress, checkpoint,
	repeat, parent_job_id
	FROM jobs
	ORDER BY created DESC
	LIMIT ?`

		var rows *sql.Rows
		var err error
		rows, err = tl.db.QueryContext(ctx, q, mostRecent)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		tlID := tl.id.String()

		for rows.Next() {
			job, err := scanJob(rows, tlID)
			if err != nil {
				return nil, err
			}
			jobs = append(jobs, job)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if len(jobs) == 0 {
			return nil, sql.ErrNoRows
		}
	}

	// load specific jobs by ID
	for _, id := range jobIDs {
		tl.dbMu.RLock()
		job, err := tl.loadJob(ctx, nil, id, true)
		tl.dbMu.RUnlock()
		if err != nil {
			return nil, fmt.Errorf("loading job %d: %w", id, err)
		}
		jobs = append(jobs, job)
	}

	for i := range jobs {
		// if job is running, we can provide more recent information than what
		// was last synced to the DB, which may be out-of-date at the moment
		if jobs[i].State == JobStarted {
			tl.activeJobsMu.RLock()
			activeJob, ok := tl.activeJobs[jobs[i].ID]
			tl.activeJobsMu.RUnlock()
			if ok {
				// we don't use a RWMutex because this mutex is write-heavy
				activeJob.mu.Lock()
				jobs[i].Progress = activeJob.currentProgress
				jobs[i].Message = activeJob.currentMessage
				jobs[i].Total = activeJob.currentTotal
				activeJob.mu.Unlock()
			}
		}
	}

	return jobs, nil
}

func (tl *Timeline) CancelJob(ctx context.Context, jobID uint64) error {
	tl.activeJobsMu.Lock()
	activeJob, ok := tl.activeJobs[jobID]
	tl.activeJobsMu.Unlock()
	if ok {
		// job is actively running -- fun! we get to cancel it,
		// and our job is actually easier this way
		activeJob.cancel()

		// wait for job action to return; this ensures its
		// state has been synced to the DB and it has been
		// removed from the map of active jobs
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-activeJob.done:
		}

		return nil
	}

	// the job is not actively running; it could have been paused
	// from a prior execution of the program, for example; or maybe
	// it is still queued and they just want to prevent it from
	// running; etc, let's check the DB

	tl.dbMu.Lock()
	defer tl.dbMu.Unlock()

	tx, err := tl.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	job, err := tl.loadJob(ctx, tx, jobID, false)
	if err != nil {
		return fmt.Errorf("loading job %d from database: %w", jobID, err)
	}

	switch job.State {
	case JobQueued, JobStarted, JobPaused, JobInterrupted:
		// Started and Interrupted should probably not be encountered since
		// running jobs should be active (taken care of above by canceling
		// its context), and we should resume interrupted jobs at startup,
		// but might as well allow them here since it's no matter
		job.State = JobAborted
		_, err = tl.db.ExecContext(ctx, `UPDATE jobs SET state=? WHERE id=?`, job.State, jobID) // TODO: LIMIT 1
		if err != nil {
			return fmt.Errorf("updating job state: %w", err)
		}
	default:
		return fmt.Errorf("job %d is in state %s, which cannot be canceled", jobID, job.State)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	// update any UI elements that may be showing info about this inactive job
	statusLog := Log.Named("job.status").With(
		zap.String("repo_id", tl.ID().String()),
		zap.Uint64("id", job.ID),
		zap.String("type", string(job.Type)),
		zap.Time("created", job.Created),
		zap.Timep("start", job.Start))
	inactiveJob := &ActiveJob{
		id:              job.ID,
		statusLog:       statusLog,
		parentJobID:     job.ParentJobID,
		currentState:    job.State,
		currentProgress: job.Progress,
		currentTotal:    job.Total,
		currentMessage:  job.Message,
		lastCheckpoint:  job.Updated,
	}
	inactiveJob.flushProgress(statusLog)

	return nil
}

func (tl *Timeline) PauseJob(ctx context.Context, jobID uint64) error {
	tl.activeJobsMu.Lock()
	job, ok := tl.activeJobs[jobID]
	tl.activeJobsMu.Unlock()
	if !ok {
		return fmt.Errorf("job %d is either inactive or does not exist", jobID)
	}

	// create the channel that the job will block on; that is the pausing
	// (closing this channel will unpause the job)
	paused := make(chan struct{})

	// What happens next is done deliberately in this order:
	// 1. Update the job's state in the DB. This is authoritative, so if the program
	//    terminates before the job pauses, at least the user's intent will be preserved.
	// 2. Signal the job to pause. The UI is currently showing temporary "Pausing..."
	//    text, and it can take time for the job to receive our signal and pause,
	//    for example if it's in the middle of a batch of thumbnails being generated;
	//    while it's finishing those, it's emitting job status updates and the UI will
	//    update accordingly, so if we set the state to "paused" before the job has
	//    actually paused, it looks dumb because it'll be streaming progress while paused.
	// 3. Update the UI's state to "paused" so that controls appear to allow resumption.

	// step 1. update the DB with the authoritative job state
	tl.dbMu.Lock()
	_, err := tl.db.ExecContext(ctx, `UPDATE jobs SET state=? WHERE state=? AND id=?`, JobPaused, JobStarted, job.id) // TODO: LIMIT 1
	tl.dbMu.Unlock()
	if err != nil {
		return fmt.Errorf("job paused, but error updating job state in DB: %w", err)
	}

	// step 2. the job should soon select on the pause channel to receive this, then
	// immediately try to receive from the paused channel, which is effectively the
	// pause behavior, as it blocks the job's goroutine until we unpause
	job.pause <- paused

	// step 3. store reference to the new channel so it can be closed (to unpause) later,
	// and update the UI with the job's paused state
	job.mu.Lock()
	if job.paused != nil {
		// already paused (not an error, just a no-op)
		job.mu.Unlock()
		return nil
	}
	job.paused = paused
	job.currentState = JobPaused
	job.flushProgress(job.statusLog) // note that the UI might get the state update before this function, and thus its HTTP request, returns -- TODO: not true if this change works
	job.mu.Unlock()

	return nil
}

func (tl *Timeline) UnpauseJob(ctx context.Context, jobID uint64) error {
	tl.activeJobsMu.Lock()
	job, ok := tl.activeJobs[jobID]
	tl.activeJobsMu.Unlock()
	if !ok {
		// not active; see if it's in the DB... if so, it's the same as starting the job
		var state JobState
		tl.dbMu.Lock()
		err := tl.db.QueryRowContext(ctx, `SELECT state FROM jobs WHERE id=? LIMIT 1`, jobID).Scan(&state)
		tl.dbMu.Unlock()
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("job %d not found", jobID)
		}
		if err != nil {
			return err
		}
		if state != JobStarted && state != JobPaused && state != JobFailed {
			return fmt.Errorf("job %d is in %s state and cannot be resumed", jobID, state)
		}
		return tl.StartJob(ctx, jobID, false)
	}

	// update the DB first because it's authoritative for the job's state
	tl.dbMu.Lock()
	_, err := tl.db.ExecContext(ctx, `UPDATE jobs SET state=? WHERE state=? AND id=?`, JobStarted, JobPaused, job.id) // TODO: LIMIT 1
	tl.dbMu.Unlock()
	if err != nil {
		return fmt.Errorf("job resumed, but error updating job state in DB: %w", err)
	}

	// unpause by closing the pause channel, delete the pause channel,
	// and then update the UI as to the job's new state
	job.mu.Lock()
	defer job.mu.Unlock()

	if job.paused == nil {
		// already not paused (not an error, just a no-op)
		return nil
	}
	close(job.paused)
	job.paused = nil
	job.currentState = JobStarted

	job.flushProgress(job.statusLog)

	return nil
}

func (tl *Timeline) StartJob(ctx context.Context, jobID uint64, startOver bool) error {
	tl.dbMu.Lock()
	defer tl.dbMu.Unlock()

	tx, err := tl.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if startOver {
		_, err = tx.ExecContext(ctx, `UPDATE jobs SET checkpoint=NULL, progress=NULL, message=NULL WHERE id=?`, jobID) // TODO: LIMIT 1
		if err != nil {
			return fmt.Errorf("clearing checkpoint, progress, and message to start over: %w", err)
		}
	}

	if err = tl.startJob(ctx, tx, jobID); err != nil {
		return err
	}

	return tx.Commit()
}

func scanJob(rows *sql.Rows, repoID string) (Job, error) {
	var job Job
	var created, updated, start, end *int64

	err := rows.Scan(
		&job.ID, &job.Type, &job.Name, &job.Config, &job.Hash, &job.State, &job.Hostname,
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
	job.RepoID = repoID

	return job, nil
}

// Job is only to be used for shuttling job data in and out of the DB,
// it should not be assumed to accurately reflect the current state of the
// job. Mainly used for inserting a job row and getting a job row for the
// frontend.
type Job struct {
	// not a field in the DB, but useful for bookkeeping where multiple timelines are open
	RepoID string `json:"repo_id"`

	ID          uint64         `json:"id"`
	Type        JobType        `json:"type"`
	Name        *string        `json:"name,omitempty"`
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
	ParentJobID *uint64        `json:"parent_job_id,omitempty"`

	// only used when loading jobs from the DB for the frontend
	Parent   *Job  `json:"parent,omitempty"`
	Children []Job `json:"children,omitempty"`
}

func (j Job) hash() []byte {
	h := blake3.New()
	_, _ = h.WriteString(string(j.Type))
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

type JobType string

const (
	JobTypeImport     JobType = "import"
	JobTypeThumbnails JobType = "thumbnails"
	JobTypeEmbeddings JobType = "embeddings"
)

type JobState string

const (
	JobQueued      JobState = "queued"      // on deck
	JobStarted     JobState = "started"     // currently running
	JobPaused      JobState = "paused"      // intentional suspension (don't auto-resume) (still considered "active" since its action remains blocked in memory)
	JobAborted     JobState = "aborted"     // intentional termination (don't auto-resume) (job unloaded from memory)
	JobInterrupted JobState = "interrupted" // unintentional, forced interruption (may be auto-resumed)
	JobSucceeded   JobState = "succeeded"   // final (cannot be restarted) // TODO: what if units of work fail, though? maybe this should be "done" or "completed"
	JobFailed      JobState = "failed"      // final (can be manually restarted)
)

const (
	jobSyncInterval  = 2 * time.Second        // how often to update the DB and save checkpoints
	jobFlushInterval = 100 * time.Millisecond // how often to update the frontend
)
