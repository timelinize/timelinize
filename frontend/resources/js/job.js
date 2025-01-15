let jobThroughputInterval; // declared out here so the controller can clear it when leaving the page

const jobThroughputXRange = 60;   // range of the X-axis of the job throughput chart
const jobThroughputOffScreen = 1; // how many points to show off the left side of the chart, so it can be pruned without janking the display

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
	let seriesData = [];
	if (jobStats) {
		seriesData = jobStats.chartSeries;
	}

	const elem = $('#chart-active-job-throughput');
	let chartOptions = {};

	if (!elem.chart) {
		elem.chart = echarts.init(elem/*, null, {
			renderer: 'svg' // TODO: SVG is vector, so it scales when page is zoomed; see if it's performant enough
		}*/);

		chartOptions = {
			grid: {
				top: 10,
				bottom: 20,
				left: 50,
				right: 0,
			},
			tooltip: {
				trigger: 'axis',
				axisPointer: {
					type: 'cross'
				}
			},
			xAxis: {
				splitLine: {
					show: false
				}
			},
			yAxis: {
				type: 'value',
				splitLine: {
					show: true,
					lineStyle: {
						type: 'dashed',
						width: 2,
					}
				}
			},
			series: [
				{
					name: 'Job Throughput',
					type: 'line',
					smooth: true,
					showSymbol: false,
					data: seriesData,
					animationDurationUpdate: 1000,
					animationEasingUpdate: "linear",
					lineStyle: {
						width: 3,
						color: tabler.getColor('blue'),
					},
					areaStyle: {
						opacity: 0.8,
						color: new echarts.graphic.LinearGradient(0, 0, 0, 1, [
							{
								offset: 0,
								color: tabler.getColor("primary")
							},
							{
								offset: 1,
								color: tabler.hexToRgba(tabler.getColor("primary"), .1)
							}
						])
					}
				}
			]
		};
	}

	elem.chart.setOption(chartOptions);
}
