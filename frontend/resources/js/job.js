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

	$('.job-config-code').innerText = JSON.stringify(JSON.parse(job.config), null, 4);

	// claim the job elements on this page for this job
	assignJobElements($('#page-content'), job);

	// fill out the majority of the job UI
	jobProgressUpdate(job);

	if (job.parent) {
		$('#parent-job-container').classList.remove('d-none');
		renderJobPreview($('#parent-job-list'), job.parent);
		jobProgressUpdate(job.parent);
	}

	if (job.children?.length > 0) {
		$('#subsequent-jobs-container').classList.remove('d-none');
		for (const child of job.children) {
			renderJobPreview($('#subsequent-jobs-list'), child);
			jobProgressUpdate(child);
		}
	}

	///////

	// how many points to show along the graph
	const xRange = 60;


	let chart = new ApexCharts($('#chart-active-job-throughput'), {
		chart: {
			type: 'line',
			fontFamily: 'inherit',
			height: 240,
			parentHeightOffset: 0,
			toolbar: {
				show: false,
			},
			zoom: {
                enabled: false
            },
			animations: {
				enabled: true,
				dynamicAnimation: {
					speed: 1000
				}
			},
		},
		fill: {
			opacity: 1,
		},
		stroke: {
			width: 3,
			lineCap: "round",
			curve: "smooth",
		},
		series: [{
			name: "Items" // TODO: should be customized based on job type
		}],
		// tooltip: {
		// 	theme: 'dark'
		// },
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
			range: xRange,
			// labels: {
			// 	padding: 0,
			// },
			// tooltip: {
			// 	enabled: false
			// },
			type: 'category',
		},
		yaxis: {
			labels: {
				padding: 4
			},
			max: 100,
		},
		colors: [tabler.getColor("blue")],
		legend: {
			show: false,
		},
	});

	chart.render();

	let data = [];

	let secSinceStart = 1;
	// setInterval(function() {
	// 	data.push({
	// 		x: secSinceStart,
	// 		y: Math.floor(Math.random()*(100-5)+5)
	// 	});
	// 	secSinceStart++;

	// 	chart.updateOptions({
	// 		series: [
	// 			{
	// 				data: data,
	// 			}
	// 		],
	// 		xaxis: {
	// 			range: Math.min(data.length-1, xRange)
	// 		}
	// 	})
	// }, 1000);

	setInterval(function() {
		// leave some extra points off the edge of the graph just to ensure
		// we don't accidentally chop them off while the chart is still animating
		// on the left side; ensure all pruning happens off-screen
		const offScreen = 5;
		if (data.length > xRange+offScreen) {
			data.splice(0, data.length-xRange-offScreen)
			chart.updateSeries([{data: data}], false);
		}
	}, 5000);
}