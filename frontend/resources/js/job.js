async function jobPageMain() {
	const {repoID, rowID} = parseURIPath()

	const jobs = await app.Jobs({
		repo_id: repoID,
		job_ids: [rowID]
	});

	if (jobs.length != 1) {
		// TODO: handle error better
		console.error("Bad/unknown job ID:", rowID, jobs);
		return;
	}

	const job = jobs[0];
	console.log("JOB:", job);

	// claim the job elements on this page for this job
	for (elem of $$(`
		#page-content .job-name,
		#page-content .job-progress,
		#page-content .job-progress-text,
		#page-content .job-message,
		#page-content .job-status-indicator,
		#page-content .job-status,
		#page-content .job-time-basis,
		#page-content .job-time`)) {
		elem.classList.add(`job-id-${job.id}`);
	}

	jobProgressUpdate(job);



	/** TODO: column chart example **/
	new ApexCharts(document.getElementById('chart-items-per-minute'), {
		chart: {
			type: "line",
			fontFamily: 'inherit',
			height: 240,
			parentHeightOffset: 0,
			toolbar: {
				show: false,
			},
			animations: {
				enabled: false
			},
		},
		fill: {
			opacity: 1,
		},
		stroke: {
			width: 2,
			lineCap: "round",
			curve: "smooth",
		},
		series: [{
			name: "Tasks completion",
			data: [155, 65, 465, 265, 225, 325, 80]
		}],
		tooltip: {
			theme: 'dark'
		},
		grid: {
			padding: {
				top: -20,
				right: 0,
				left: -4,
				bottom: -4
			},
			strokeDashArray: 4,
		},
		xaxis: {
			labels: {
				padding: 0,
			},
			tooltip: {
				enabled: false
			},
			type: 'datetime',
		},
		yaxis: {
			labels: {
				padding: 4
			},
		},
		labels: [
			'2020-06-21', '2020-06-22', '2020-06-23', '2020-06-24', '2020-06-25', '2020-06-26', '2020-06-27'
		],
		colors: [tabler.getColor("primary")],
		legend: {
			show: false,
		},
	}).render();
}

