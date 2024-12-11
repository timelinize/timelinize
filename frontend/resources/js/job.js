let updateThroughput = function() { }; // TODO: temporary

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

	$('.job-config-code').innerText = maxlenStr(JSON.stringify(JSON.parse(job.config), null, 4), 1024*128);

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

	// how many points to show along the graph
	const xRange = 60;
	const seriesName = "Items"; // TODO: should be customized based on job type

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
			name: seriesName,
			data: []
		}],
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
	chart.render();

	let chartData = [];


	////////////////
	const MAX_WINDOW_SIZE = 3;
	let window = [];
	let latestProgress = 0, progressAtLastPaint = 0;
	
	updateThroughput = function(log) {
		if (log.progress > 0) {
			latestProgress = log.progress;			
		}
	};
	
	let secSinceStart = 1;
	setInterval(function() {
		const throughputSinceLastPaint = latestProgress - progressAtLastPaint;
		progressAtLastPaint = latestProgress;
		
		$('.throughput-rate').innerText = throughputSinceLastPaint;

		window.push(throughputSinceLastPaint);
		if (window.length > MAX_WINDOW_SIZE) {
			window = window.slice(window.length-MAX_WINDOW_SIZE);
		}

		chartData.push({
			x: secSinceStart,
			y: Math.floor(window.reduce((sum, val) => sum+val, 0) / window.length)
		});
		secSinceStart++;

		chart.updateOptions({
			series: [
				{
					name: "Items",
					data: chartData,
				}
			],
			xaxis: {
				// allow the axis to grow (squishing the line graph) until it reaches its max size; this prevents negative x-values etc
				range: Math.min(chartData.length-1, xRange)
			}
		})
	}, 1000);

	// every so often, lob off the left side of the plot, to avoid a memory leak;
	// time it so that it occurs just as any repaint/update animation is ending,
	// since this requires updating the chart with animation disabled and it can
	// sometimes result in slight amount of jank if it's not perfectly synced.
	// I've found putting it at the tail end of animation is smoother. it doesn't
	// have to happen every second, just enough to keep the array under control.
	setInterval(function() {
		// leave some extra points off the edge of the graph just to ensure
		// we don't accidentally chop them off while the chart is still animating
		// on the left side; ensure all pruning happens off-screen
		const offScreen = 2;
		if (chartData.length > xRange+offScreen) {
			chartData.splice(0, chartData.length-xRange-offScreen)
			chart.updateSeries([{name: "Items", data: chartData}], false);
		}
	}, 9990);
}
