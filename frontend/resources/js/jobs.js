// create interval that updates job charts every second
tlz.intervals.activeJobStats = {
	set() {
		return setInterval(updateActiveJobStats, 1000);
	}
};
tlz.intervals.activeJobStats.interval = tlz.intervals.activeJobStats.set();

function updateActiveJobStats() {
	for (const jobID in tlz.jobStats) {
		const stats = tlz.jobStats[jobID];
		if (!stats.live) {
			continue;
		}

		const throughputSinceLastPaint = stats.latestProgress - stats.progressAtLastPaint;
		stats.progressAtLastPaint =  stats.latestProgress;
		
		const MAX_WINDOW_SIZE = 3;
		stats.window.push(throughputSinceLastPaint);
		if (stats.window.length > MAX_WINDOW_SIZE) {
			stats.window = stats.window.slice(stats.window.length - MAX_WINDOW_SIZE);
		}

		const chartData = stats.chartSeries[0].data;

		// I'm not sure why we have to push this object structure (it's the same one
		// used by the example here: https://echarts.apache.org/examples/en/editor.html?c=dynamic-data2)
		// since we can also just pass in [x, y], the {name: "...", value: [x, y]}
		// structure isn't strictly necessary, BUT: when we start calling shift()
		// to lob off old data points that have moved off the screen, it causes the
		// lines to become jiggly/wavy during the transition, which looks ridiculous
		// and is illegible... for SOME reason, we have to set a "name" in this object,
		// and not only that, it has to be a unique name for this specific value, but
		// I haven't seen this name used in a tooltip, so maybe it is only so the
		// chart library can keep track of which data point is which; I dunno, but
		// the important thing to avoid the jiggly line is to set a unique name per
		// data point!
		chartData.push({
			name: stats.secondsSinceStart.toString(),
			value: [
				stats.secondsSinceStart, // x
				Math.floor(stats.window.reduce((sum, val) => sum+val, 0) / stats.window.length) // y
			]
		});
		while (chartData.length > jobThroughputXRange+jobThroughputOffScreen) {
			chartData.shift();
		}
		stats.secondsSinceStart++;
		const chartContainer = $(`#throughput-chart-container.job-id-${jobID}`);
		if (chartContainer) {
			// this element shows the mean throughput over the entire chart data
			$('.throughput-rate', chartContainer).innerText = (chartData.reduce((sum, elem) => sum+elem.value[1], 0) / chartData.length).toFixed(1).replace(".0", "");
			$('#chart-active-job-throughput', chartContainer).chart?.setOption({
				series: stats.chartSeries,
				xAxis: {
					min: chartData.length >= jobThroughputXRange
						? chartData[chartData.length-jobThroughputXRange].value[0]
						: 'dataMin',
					max: 'dataMax'
				}
			});
		}
	}
}

function renderJobPreview(containerElem, job) {
	if (!containerElem || $(`.job-preview.job-id-${job.id}`, containerElem)) {
		return; // no-op if no container, or element already exists
	}
	const elem = cloneTemplate('#tpl-job-preview');
	assignJobElements(elem, job);
	containerElem.classList.add(`job-id-${job.id}`);
	containerElem.append(elem);
}


// assignJobElements adds the .job-id-* class to the job-related
// elements within containerElem, for the given job (the 'id'
// property must be set on the job). Then it can be synced by
// log messages.
function assignJobElements(containerElem, job) {
	const jobIDClass = `job-id-${job.id}`;
	for (const elem of $$(`
		.job-title,
		.job-icon,
		.job-name,
		.job-name-suffix,
		.job-link,
		.job-progress,
		.job-progress-text,
		.job-progress-text-detailed,
		.job-message,
		.job-status-indicator,
		.job-status-dot,
		.job-status,
		.job-time-basis,
		.job-time,
		.job-duration,
		.start-job,
		.restart-job,
		.pause-job,
		.unpause-job,
		.cancel-job,
		#subsequent-jobs-container,
		#parent-job-container,
		#throughput-chart-container,
		.job-import-stream,
		.job-thumbnail-stream`, containerElem)) {
		elem.classList.add(jobIDClass);
		elem.dataset.jobId = job.id;
	}
	containerElem.classList.add(jobIDClass);
	containerElem.dataset.jobId = job.id;
}

function jobProgressUpdate(job) {
	// update live job stats (for charts, etc)
	const seriesName = "Items"; // TODO: customize per job type
	if (!tlz.jobStats[job.id]) {
		// TODO: When to clear out the job stats? save to localStorage or anything for future reference?
		tlz.jobStats[job.id] = {
			latestProgress: 0, // will be set immediately below
			progressAtLastPaint: job.progress || 0,
			secondsSinceStart: 0,
			overallMeanThroughput: 0,
			live: job.state == "started",
			window: [],
			chartSeries: [
				{
					name: seriesName,
					data: []
				}
			]
		};
	}
	if (job.progress > 0) {
		tlz.jobStats[job.id].latestProgress = job.progress;
	}


	for (const elem of $$(`.job-link.job-id-${job.id}`)) {
		elem.href = `/jobs/${job.repo_id}/${job.id}`;
	}

	// update chart(s) only if job is running
	tlz.jobStats[job.id].live = job.state == "started";

	if (job.type == "import")
	{
		for (const elem of $$(`.job-title.job-id-${job.id}`)) {
			elem.innerText = "Import job";
		}
		for (const elem of $$(`.job-name.job-id-${job.id}`)) {
			elem.innerText = "Import";
		}
		for (const elem of $$(`.job-icon.job-id-${job.id}`)) {
			elem.innerHTML = `
				<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor"
					stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
					class="icon icon-tabler icons-tabler-outline icon-tabler-database-import">
					<path stroke="none" d="M0 0h24v24H0z" fill="none" />
					<path d="M4 6c0 1.657 3.582 3 8 3s8 -1.343 8 -3s-3.582 -3 -8 -3s-8 1.343 -8 3" />
					<path d="M4 6v6c0 1.657 3.582 3 8 3c.856 0 1.68 -.05 2.454 -.144m5.546 -2.856v-6" />
					<path d="M4 12v6c0 1.657 3.582 3 8 3c.171 0 .341 -.002 .51 -.006" />
					<path d="M19 22v-6" />
					<path d="M22 19l-3 -3l-3 3" />
				</svg>`;
		}
	}
	else if (job.type == "thumbnails")
	{
		for (const elem of $$(`.job-title.job-id-${job.id}`)) {
			elem.innerText = "Generate thumbnails";
		}
		for (const elem of $$(`.job-name.job-id-${job.id}`)) {
			elem.innerText = "Thumbnails";
		}
		for (const elem of $$(`.job-icon.job-id-${job.id}`)) {
			elem.innerHTML = `
				<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor"
					stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
					class="icon icon-tabler icons-tabler-outline icon-tabler-photo-scan">
					<path stroke="none" d="M0 0h24v24H0z" fill="none" />
					<path d="M15 8h.01" />
					<path d="M6 13l2.644 -2.644a1.21 1.21 0 0 1 1.712 0l3.644 3.644" />
					<path d="M13 13l1.644 -1.644a1.21 1.21 0 0 1 1.712 0l1.644 1.644" />
					<path d="M4 8v-2a2 2 0 0 1 2 -2h2" />
					<path d="M4 16v2a2 2 0 0 0 2 2h2" />
					<path d="M16 4h2a2 2 0 0 1 2 2v2" />
					<path d="M16 20h2a2 2 0 0 0 2 -2v-2" />
				</svg>`;
		}
	}
	else if (job.type == "embeddings")
	{
		for (const elem of $$(`.job-title.job-id-${job.id}`)) {
			elem.innerText = "Generate embeddings";
		}
		for (const elem of $$(`.job-name.job-id-${job.id}`)) {
			elem.innerText = "Embeddings";
		}
		for (const elem of $$(`.job-icon.job-id-${job.id}`)) {
			elem.innerHTML = `
				<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor"
					stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
					class="icon icon-tabler icons-tabler-outline icon-tabler-sparkles">
					<path stroke="none" d="M0 0h24v24H0z" fill="none" />
					<path
						d="M16 18a2 2 0 0 1 2 2a2 2 0 0 1 2 -2a2 2 0 0 1 -2 -2a2 2 0 0 1 -2 2zm0 -12a2 2 0 0 1 2 2a2 2 0 0 1 2 -2a2 2 0 0 1 -2 -2a2 2 0 0 1 -2 2zm-7 12a6 6 0 0 1 6 -6a6 6 0 0 1 -6 -6a6 6 0 0 1 -6 6a6 6 0 0 1 6 6z" />
				</svg>`;
		}
	}

	if (job.state == "queued")
	{
		for (const elem of $$(`.job-progress.job-id-${job.id} .progress-bar`)) {
			elem.classList.add('bg-secondary', 'progress-bar-striped');
			elem.classList.remove('bg-green', 'bg-azure', 'bg-orange', 'bg-red', 'progress-bar-indeterminate', 'progress-bar-animated');
		}
		for (const elem of $$(`.job-status-indicator.job-id-${job.id}`)) {
			elem.classList.add('status-secondary', 'status-indicator-animated');
		}
		for (const elem of $$(`.job-status-dot.job-id-${job.id}`)) {
			elem.classList.add('status-secondary', 'status-dot-animated');
		}
		for (const elem of $$(`.job-status.job-id-${job.id}`)) {
			elem.innerText = "Queued";
			elem.classList.add("text-secondary");
			elem.classList.remove('text-azure', 'text-orange', 'text-red', 'text-green');
		}
		for (const elem of $$(`.job-time-basis.job-id-${job.id}`)) {
			if (job.start) {
				elem.innerText = "Starting";
			} else {
				elem.innerText = "Created";
			}
		}
		for (const elem of $$(`.job-time.job-id-${job.id}`)) {
			if (job.start) {
				setDynamicTimestamp(elem, job.start);
			} else {
				setDynamicTimestamp(elem, job.created);
			}
		}

		// buttons
		for (const elem of $$(`.pause-job.job-id-${job.id}`)) {
			elem.classList.add('d-none');
		}
		for (const elem of $$(`.unpause-job.job-id-${job.id}`)) {
			elem.classList.add('d-none');
		}
		for (const elem of $$(`.cancel-job.job-id-${job.id}`)) {
			elem.classList.remove('d-none');
		}
		for (const elem of $$(`.start-job.job-id-${job.id}`)) {
			elem.classList.remove('d-none');
		}
		for (const elem of $$(`.restart-job.job-id-${job.id}`)) {
			elem.classList.add('d-none');
		}
	}
	else if (job.state == "started")
	{
		for (const elem of $$(`.job-progress.job-id-${job.id} .progress-bar`)) {
			elem.classList.add('bg-green');
			elem.classList.remove('bg-azure', 'bg-orange', 'bg-red', 'bg-secondary', 'progress-bar-striped');
		}
		for (const elem of $$(`.job-status-indicator.job-id-${job.id}`)) {
			elem.classList.add('status-green', 'status-indicator-animated');
			elem.classList.remove('status-azure', 'status-secondary', 'status-orange', 'status-red');
		}
		for (const elem of $$(`.job-status-dot.job-id-${job.id}`)) {
			elem.classList.add('status-green', 'status-dot-animated');
			elem.classList.remove('status-azure', 'status-secondary', 'status-orange', 'status-red');
		}
		for (const elem of $$(`.job-status.job-id-${job.id}`)) {
			elem.innerText = "Running";
			elem.classList.add("text-green");
			elem.classList.remove('text-azure', 'text-orange', 'text-red', 'text-secondary');
		}
		for (const elem of $$(`.job-time-basis.job-id-${job.id}`)) {
			elem.innerText = "Started";
		}
		for (const elem of $$(`.job-time.job-id-${job.id}`)) {
			setDynamicTimestamp(elem, job.start);
		}
		for (const elem of $$(`.job-duration:not(.dynamic-duration).job-id-${job.id}`)) {
			setDynamicTimestamp(elem, job.start, true);
		}
		
		// buttons
		for (const elem of $$(`.pause-job.job-id-${job.id}`)) {
			elem.classList.remove('d-none');
		}
		for (const elem of $$(`.unpause-job.job-id-${job.id}`)) {
			elem.classList.add('d-none');
		}
		for (const elem of $$(`.cancel-job.job-id-${job.id}`)) {
			elem.classList.remove('d-none');
		}
		for (const elem of $$(`.start-job.job-id-${job.id}`)) {
			elem.classList.add('d-none');
		}
		for (const elem of $$(`.restart-job.job-id-${job.id}`)) {
			elem.classList.add('d-none');
		}
	}
	else if (job.state == "succeeded")
	{
		for (const elem of $$(`.job-progress.job-id-${job.id} .progress-bar`)) {
			elem.classList.add('bg-green');
			elem.classList.remove('bg-azure', 'bg-orange', 'bg-red', 'bg-secondary', 'progress-bar-indeterminate', 'progress-bar-striped');
		}
		for (const elem of $$(`.job-status-indicator.job-id-${job.id}`)) {
			elem.classList.add('status-green');
			elem.classList.remove('status-azure', 'status-secondary', 'status-orange', 'status-indicator-animated');
		}
		for (const elem of $$(`.job-status-dot.job-id-${job.id}`)) {
			elem.classList.add('status-green');
			elem.classList.remove('status-azure', 'status-secondary', 'status-orange', 'status-dot-animated');
		}
		for (const elem of $$(`.job-status.job-id-${job.id}`)) {
			elem.innerText = "Completed";
			elem.classList.add("text-green");
			elem.classList.remove('text-azure', 'text-orange', 'text-red', 'text-secondary');
		}
		for (const elem of $$(`.job-time-basis.job-id-${job.id}`)) {
			elem.innerText = "Finished";
		}
		for (const elem of $$(`.job-time.job-id-${job.id}`)) {
			setDynamicTimestamp(elem, job.ended);
		}
		for (const elem of $$(`.job-duration.job-id-${job.id}`)) {
			const start = typeof job.start === 'number' ? DateTime.fromSeconds(job.start) : DateTime.fromISO(job.start);
			const ended = typeof job.ended === 'number' ? DateTime.fromSeconds(job.ended) : DateTime.fromISO(job.ended);
			elem.innerText = betterToHuman(ended.diff(start));
			elem.classList.remove('dynamic-duration');
		}

		// buttons
		for (const elem of $$(`.pause-job.job-id-${job.id}`)) {
			elem.classList.add('d-none');
		}
		for (const elem of $$(`.unpause-job.job-id-${job.id}`)) {
			elem.classList.add('d-none');
		}
		for (const elem of $$(`.cancel-job.job-id-${job.id}`)) {
			elem.classList.add('d-none');
		}
		for (const elem of $$(`.start-job.job-id-${job.id}`)) {
			elem.classList.add('d-none');
		}
		for (const elem of $$(`.restart-job.job-id-${job.id}`)) {
			elem.classList.add('d-none');
		}
	}
	else if (job.state == "paused")
	{
		for (const elem of $$(`.job-progress.job-id-${job.id} .progress-bar`)) {
			elem.classList.add('bg-azure', 'progress-bar-striped', 'progress-bar-animated');
			elem.classList.remove('bg-green', 'bg-secondary', 'bg-orange', 'bg-red', 'progress-bar-indeterminate');
		}
		for (const elem of $$(`.job-status-indicator.job-id-${job.id}`)) {
			elem.classList.add('status-azure', 'status-indicator-animated');
			elem.classList.remove('status-green', 'status-orange', 'status-red');
		}
		for (const elem of $$(`.job-status-dot.job-id-${job.id}`)) {
			elem.classList.add('status-azure', 'status-dot-animated');
			elem.classList.remove('status-green', 'status-orange', 'status-red');
		}
		for (const elem of $$(`.job-status.job-id-${job.id}`)) {
			elem.innerText = "Paused";
			elem.classList.add('text-azure');
			elem.classList.remove('text-green', 'text-orange', 'text-red', 'text-secondary');
		}
		for (const elem of $$(`.job-time-basis.job-id-${job.id}`)) {
			elem.innerText = "Paused";
		}
		for (const elem of $$(`.job-time.job-id-${job.id}`)) {
			setDynamicTimestamp(elem, job.updated);
		}

		// buttons
		for (const elem of $$(`.pause-job.job-id-${job.id}`)) {
			elem.classList.add('d-none');
		}
		for (const elem of $$(`.unpause-job.job-id-${job.id}`)) {
			elem.classList.remove('d-none');
		}
		for (const elem of $$(`.cancel-job.job-id-${job.id}`)) {
			elem.classList.remove('d-none');
		}
		for (const elem of $$(`.start-job.job-id-${job.id}`)) {
			elem.classList.add('d-none');
		}
		for (const elem of $$(`.restart-job.job-id-${job.id}`)) {
			elem.classList.add('d-none');
		}
	}
	else if (job.state == "aborted" || job.state == "interrupted")
	{
		for (const elem of $$(`.job-progress.job-id-${job.id} .progress-bar`)) {
			elem.classList.add('bg-orange', 'progress-bar-striped');
			elem.classList.remove('bg-green', 'bg-azure', 'bg-secondary', 'bg-red', 'progress-bar-indeterminate', 'progress-bar-animated');
		}
		for (const elem of $$(`.job-status-indicator.job-id-${job.id}`)) {
			elem.classList.add('status-orange');
			elem.classList.remove('status-green', 'status-azure', 'status-indicator-animated');
		}
		for (const elem of $$(`.job-status-dot.job-id-${job.id}`)) {
			elem.classList.add('status-orange');
			elem.classList.remove('status-green', 'status-azure', 'status-dot-animated');
		}
		for (const elem of $$(`.job-status.job-id-${job.id}`)) {
			elem.innerText = job.state == "aborted" ? "Aborted" : "Interrupted";
			elem.classList.add("text-orange");
			elem.classList.remove('text-green', 'text-azure', 'text-secondary');
		}
		for (const elem of $$(`.job-time-basis.job-id-${job.id}`)) {
			elem.innerText = "Ended";
		}
		for (const elem of $$(`.job-time.job-id-${job.id}`)) {
			setDynamicTimestamp(elem, job.ended);
		}
		for (const elem of $$(`.job-duration.job-id-${job.id}`)) {
			const start = typeof job.start === 'number' ? DateTime.fromSeconds(job.start) : DateTime.fromISO(job.start);
			const ended = typeof job.ended === 'number' ? DateTime.fromSeconds(job.ended) : DateTime.fromISO(job.ended);
			elem.innerText = betterToHuman(ended.diff(start));
			elem.classList.remove('dynamic-duration');
		}

		// buttons
		for (const elem of $$(`.pause-job.job-id-${job.id}`)) {
			elem.classList.add('d-none');
		}
		for (const elem of $$(`.unpause-job.job-id-${job.id}`)) {
			elem.classList.add('d-none');
		}
		for (const elem of $$(`.cancel-job.job-id-${job.id}`)) {
			elem.classList.add('d-none');
		}
		for (const elem of $$(`.start-job.job-id-${job.id}`)) {
			// technically, aborted jobs can be resumed; but a button that says "Start" might be confusing, and "Resume" is what the unpause button says...
			elem.classList.add('d-none');
		}
		for (const elem of $$(`.restart-job.job-id-${job.id}`)) {
			// TODO: interrupted jobs can be resumed, but right now our handler for this link's click event tells the server to start the job over
			elem.classList.remove('d-none');
		}
	}
	else if (job.state == "failed")
	{
		for (const elem of $$(`.job-progress.job-id-${job.id} .progress-bar`)) {
			elem.classList.add('bg-red', 'progress-bar-striped');
			elem.classList.remove('bg-green', 'bg-azure', 'bg-orange', 'bg-secondary', 'progress-bar-indeterminate', 'progress-bar-animated');
		}
		for (const elem of $$(`.job-status-indicator.job-id-${job.id}`)) {
			elem.classList.add('status-red');
			elem.classList.remove('status-green', 'status-azure', 'status-orange', 'status-indicator-animated');
		}
		for (const elem of $$(`.job-status-dot.job-id-${job.id}`)) {
			elem.classList.add('status-red');
			elem.classList.remove('status-green', 'status-azure', 'status-orange', 'status-dot-animated');
		}
		for (const elem of $$(`.job-status.job-id-${job.id}`)) {
			elem.innerText = "Failed";
			elem.classList.add('text-red');
			elem.classList.remove('text-green', 'text-azure', 'text-secondary');
		}
		for (const elem of $$(`.job-time-basis.job-id-${job.id}`)) {
			elem.innerText = "Ended";
		}
		for (const elem of $$(`.job-time.job-id-${job.id}`)) {
			setDynamicTimestamp(elem, job.ended);
		}
		for (const elem of $$(`.job-duration.job-id-${job.id}`)) {
			const start = typeof job.start === 'number' ? DateTime.fromSeconds(job.start) : DateTime.fromISO(job.start);
			const ended = typeof job.ended === 'number' ? DateTime.fromSeconds(job.ended) : DateTime.fromISO(job.ended);
			elem.innerText = betterToHuman(ended.diff(start));
			elem.classList.remove('dynamic-duration');
		}

		// buttons
		for (const elem of $$(`.pause-job.job-id-${job.id}`)) {
			elem.classList.add('d-none');
		}
		for (const elem of $$(`.unpause-job.job-id-${job.id}`)) {
			elem.classList.remove('d-none');
		}
		for (const elem of $$(`.cancel-job.job-id-${job.id}`)) {
			elem.classList.add('d-none');
		}
		for (const elem of $$(`.start-job.job-id-${job.id}`)) {
			elem.classList.add('d-none');
		}
		for (const elem of $$(`.restart-job.job-id-${job.id}`)) {
			elem.classList.remove('d-none');
		}
	}

	// progress bars (other than color, which is done above based on state)
	if (job.total == null) {
		// indeterminate maximum; but if job is successful, just max out the progress bar
		if (job.state == "succeeded") {
			for (const elem of $$(`.job-progress.job-id-${job.id} .progress-bar`)) {
				elem.style.width = "100%";
			}
		} else if (job.state) {
			for (const elem of $$(`.job-progress.job-id-${job.id} .progress-bar`)) {
				if (job.state == "queued" || job.state == "paused" || job.state == "aborted" || job.state == "failed") {
					elem.style.width = "100%"; // striped bar
				} else {
					elem.style.width = "0%";
				}
				if (job.state == "started") {
					elem.classList.add('progress-bar-indeterminate');
				}
			}
		}
		if (job.progress != null) {
			for (const elem of $$(`.job-progress-text.job-id-${job.id}`)) {
				elem.innerText = job.progress.toLocaleString();
			}
		}
		for (const elem of $$(`.job-progress-text-detailed.job-id-${job.id}`)) {
			if (job.progress != null) {
				const total = job.state == "succeeded" ? job.progress.toLocaleString() : "?";
				elem.innerText = `${job.progress.toLocaleString()} / ${total}`;
			}
		}
	}
	if (job.total > 0) {
		// known maximum; show progress
		const percent = (job.progress || 0)/job.total * 100;
		for (const elem of $$(`.job-progress.job-id-${job.id} .progress-bar`)) {
			elem.style.width = `${percent}%`;
			elem.classList.remove('progress-bar-indeterminate');
		}
		const percentDisplay = `${percent.toFixed(2).replace(".00", "")}%`;
		for (const elem of $$(`.job-progress-text.job-id-${job.id}`)) {
			elem.innerText = percentDisplay;
		}
		for (const elem of $$(`.job-progress-text-detailed.job-id-${job.id}`)) {
			const progress = job.progress || 0;
			elem.innerText = `${progress.toLocaleString()}/${job.total.toLocaleString()}`;
		}
	}

	// message
	for (const elem of $$(`.job-message.job-id-${job.id}`)) {
		elem.innerText = job.message || "";
	}
}


////////////////////////////////////
// Job controls
////////////////////////////////////

// TODO: These all have the same basic behavior. How can I refactor this and avoid the repetition?

on('click', '.cancel-job', async e => {
	const target = e.target.closest('.cancel-job');
	const jobID = Number(target.dataset.jobId);

	for (const elem of $$(`.cancel-job.job-id-${jobID}`)) {
		elem.classList.add('disabled');
		let textElem = $('.cancel-job-text', elem); // for links that have more than just the text in it
		if (!textElem) {
			textElem = elem;
		}
		textElem.dataset.startingText = textElem.innerText;
		textElem.innerText = "Canceling...";
	}

	await app.CancelJobs(tlz.openRepos[0].instance_id, [jobID]);

	for (const elem of $$(`.cancel-job.job-id-${jobID}`)) {
		elem.classList.remove('disabled');
		let textElem = $('.cancel-job-text', elem); // for links that have more than just the text in it
		if (!textElem) {
			textElem = elem;
		}
		textElem.innerText = textElem.dataset.startingText;
		delete textElem.dataset.startingText;
	}
});

on('click', '.pause-job', async e => {
	const target = e.target.closest('.pause-job');
	const jobID = Number(target.dataset.jobId);

	for (const elem of $$(`.pause-job.job-id-${jobID}`)) {
		elem.classList.add('disabled');
		let textElem = $('.pause-job-text', elem); // for links that have more than just the text in it
		if (!textElem) {
			textElem = elem;
		}
		textElem.dataset.startingText = textElem.innerText;
		textElem.innerText = "Pausing...";
	}

	await app.PauseJob(tlz.openRepos[0].instance_id, jobID);

	for (const elem of $$(`.pause-job.job-id-${jobID}`)) {
		elem.classList.remove('disabled');
		let textElem = $('.pause-job-text', elem); // for links that have more than just the text in it
		if (!textElem) {
			textElem = elem;
		}
		textElem.innerText = textElem.dataset.startingText;
		delete textElem.dataset.startingText;
	}
});


on('click', '.unpause-job', async e => {
	const target = e.target.closest('.unpause-job');
	const jobID = Number(target.dataset.jobId);

	for (const elem of $$(`.unpause-job.job-id-${jobID}`)) {
		elem.classList.add('disabled');
		let textElem = $('.unpause-job-text', elem); // for links that have more than just the text in it
		if (!textElem) {
			textElem = elem;
		}
		textElem.dataset.startingText = textElem.textContent;
		textElem.textContent = "Resuming...";
	}

	await app.UnpauseJob(tlz.openRepos[0].instance_id, jobID);

	for (const elem of $$(`.unpause-job.job-id-${jobID}`)) {
		elem.classList.remove('disabled');
		let textElem = $('.unpause-job-text', elem); // for links that have more than just the text in it
		if (!textElem) {
			textElem = elem;
		}
		// it is possible for this element to be rendered while waiting for UnpauseJob to return,
		// meaning that we never changed its text content, so we never set startingText, and
		// if we did this without checking we would essentially empty its text contents each
		// time it is pressed
		if (textElem.dataset.startingText) {
			textElem.textContent = textElem.dataset.startingText;
			delete textElem.dataset.startingText;
		}
	}
});

on('click', '.start-job', async e => {
	const target = e.target.closest('.start-job');
	const jobID = Number(target.dataset.jobId);

	for (const elem of $$(`.start-job.job-id-${jobID}`)) {
		elem.classList.add('disabled');
		let textElem = $('.start-job-text', elem); // for links that have more than just the text in it
		if (!textElem) {
			textElem = elem;
		}
		textElem.dataset.startingText = textElem.innerText;
		textElem.innerText = "Starting...";
	}

	await app.StartJob(tlz.openRepos[0].instance_id, jobID, false);

	for (const elem of $$(`.start-job.job-id-${jobID}`)) {
		elem.classList.remove('disabled');
		let textElem = $('.start-job-text', elem); // for links that have more than just the text in it
		if (!textElem) {
			textElem = elem;
		}
		textElem.innerText = textElem.dataset.startingText;
		delete textElem.dataset.startingText;
	}
});

on('click', '.restart-job', async e => {
	const target = e.target.closest('.restart-job');
	const jobID = Number(target.dataset.jobId);

	for (const elem of $$(`.restart-job.job-id-${jobID}`)) {
		elem.classList.add('disabled');
		let textElem = $('.restart-job-text', elem); // for links that have more than just the text in it
		if (!textElem) {
			textElem = elem;
		}
		textElem.dataset.startingText = textElem.innerText;
		textElem.innerText = "Restarting...";
	}

	await app.StartJob(tlz.openRepos[0].instance_id, jobID, true);

	for (const elem of $$(`.restart-job.job-id-${jobID}`)) {
		elem.classList.remove('disabled');
		let textElem = $('.restart-job-text', elem); // for links that have more than just the text in it
		if (!textElem) {
			textElem = elem;
		}
		textElem.innerText = textElem.dataset.startingText;
		delete textElem.dataset.startingText;
	}
});