let jobThroughputInterval; // declared out here so the controller can clear it when leaving the page

const jobThroughputXRange = 60;   // range of the X-axis of the job throughput chart
const jobThroughputOffScreen = 2; // how many points to show off the left side of the chart, so it can be pruned without janking the display

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

	$('.job-config-code').innerText = maxlenStr(JSON.stringify(JSON.parse(job.config), null, 4), 1024*10);

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


	// set up throughput chart

	const jobStats = tlz.jobStats[job.id];
	let series = [];
	if (jobStats) {
		series = jobStats.chartSeries;
	}

	const chart = new ApexCharts($('#chart-active-job-throughput'), {
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
		series: series,
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
			// set the starting range at the full range if there's no data, or if there is data,
			// the length of the data up to the full range
			range:  series?.[0]?.data ? Math.min(series[0].data.length-1, jobThroughputXRange) : jobThroughputXRange,
			type: 'category',
		},
		yaxis: {
			labels: {
				padding: 4
			},
		},
		colors: [tabler.getColor("blue")],
		legend: {
			show: false,
		},
	});
	$('#chart-active-job-throughput').apexchart = chart; // input options can be accessed with the .opts property

	chart.render();

	// every so often, lob off the left side of the plot, to avoid a memory leak;
	// time it so that it occurs just as any repaint/update animation is ending,
	// since this requires updating the chart with animation disabled and it can
	// sometimes result in slight amount of jank if it's not perfectly synced.
	// I've found putting it at the tail end of animation is smoother. it doesn't
	// have to happen every second, just enough to keep the array under control.
	jobThroughputInterval = setInterval(function() {
		const chartSeries = tlz.jobStats[job.id]?.chartSeries;
		if (!chartSeries) {
			return;
		}
		const chartData = chartSeries[0].data;
		// leave some extra points off the edge of the graph just to ensure
		// we don't accidentally chop them off while the chart is still animating
		// on the left side; ensure all pruning happens off-screen
		if (chartData.length > jobThroughputXRange+jobThroughputOffScreen) {
			chartData.splice(0, chartData.length - jobThroughputXRange - jobThroughputOffScreen);
			chart.updateSeries(chartSeries, false, true);
		}
	}, 9990);
}
