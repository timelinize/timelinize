// TODO: these two dropdowns are similar; refactor and consolidate code?
on('click', '.chart-scope .dropdown-item', (e) => {
	if (e.target.closest('#recent-items')) {
		$('.active', e.target.closest('.dropdown-menu')).classList.remove('active');
		e.target.classList.add('active');
		$('a', e.target.closest('.dropdown')).innerText = e.target.innerText;
		recentItemStats();
	}
});
on('click', '.chart-scope .dropdown-item', (e) => {
	if (e.target.closest('#days-documented')) {
		$('.active', e.target.closest('.dropdown-menu')).classList.remove('active');
		e.target.classList.add('active');
		$('a', e.target.closest('.dropdown')).innerText = e.target.innerText;
		recentDataSourceStats();
	}
});

async function renderDashboard() {
	await Promise.all([
		recentItemStats(),
		itemTypeStats(),
		dataSourceHourStats(),
		recentDataSourceStats()
	]);
}

async function recentItemStats() {
	const period = $('#recent-items .chart-scope .dropdown-item.active').dataset.period;

	const itemStats = await app.ChartStats("periodical", tlz.openRepos[0].instance_id, {period});

	if (!itemStats) {
		// TODO: show empty dashboard -- tell user to import some data
		return;
	}

	// The typical way of computing percent change is to subtract the first and last data points,
	// then divide by the first data point which is the "point of reference" then multiply by 100
	// to get the percent change. This is technically correct but ignores all the middle data points
	// and is very sensitive to outliers at the first and last positions. Basically nothing in between
	// matters if only the first and last are used to compute a trend. So here, I am averaging the
	// first and second halves of the data set. I then subtract first half average from second half
	// average, and divide that difference by the average over the whole series. This tells us the
	// approximate trend of the latter half relative to the first half of data, and considers all
	// data points. I think this makes much more sense in my trials.

	let data = [], groups = [], total = 0, firstHalf = 0, secondHalf = 0;
	itemStats.map((v) => {
		data.push(v.count);
		groups.push(v.period);
		
		total += v.count;

		// because of how we sort the groups in the DB query (oldest timestamp first), I've swapped
		// the secondHalf and firstHalf vars here from the original implementation that showed the
		// data from the most recent N days
		// TODO: I switched it back :thinking: not sure when/why that changed, but the numbers look right now
		if (data.length < itemStats.length / 2) {
			firstHalf += v.count;
		} else {
			secondHalf += v.count;
		}
	});

	const firstHalfAvg = firstHalf / (data.length/2),
		secondHalfAvg = secondHalf / (data.length/2);

	// As explained above, this is how the second half compares to the first half.
	const recentItemsTrend = (secondHalfAvg - firstHalfAvg) / (total / data.length) * 100;

	// clean up any prior info
	$('svg', '#recent-items-trend-container')?.remove();
	$('#recent-items-trend-container').classList.remove('text-red', 'text-green');


	$('#recent-items-count').innerText = total.toLocaleString();
	$('#recent-items-trend').innerText = Math.floor(Math.abs(recentItemsTrend)) + "%";
	$('#recent-items-trend-container').classList.add(recentItemsTrend < 0 ? 'text-red' : 'text-green');
	$('#recent-items-trend-container').innerHTML += recentItemsTrend > 0 ?
	`<svg xmlns="http://www.w3.org/2000/svg" class="icon ms-1" width="24" height="24" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" fill="none" stroke-linecap="round" stroke-linejoin="round">
			<path stroke="none" d="M0 0h24v24H0z" fill="none" />
			<polyline points="3 17 9 11 13 15 21 7" />
			<polyline points="14 7 21 7 21 14" />
		</svg>` :
		`<svg xmlns="http://www.w3.org/2000/svg" class="icon icon-tabler icon-tabler-trending-down" width="24" height="24" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" fill="none" stroke-linecap="round" stroke-linejoin="round">
		<path stroke="none" d="M0 0h24v24H0z" fill="none"></path>
		<polyline points="3 7 9 13 13 9 21 17"></polyline>
		<polyline points="21 10 21 17 14 17"></polyline>
		</svg>`;

	
	const elem = $('#chart-recent-items-count');
	let chartOptions = {};
	
	if (!elem.chart) {
		elem.chart = echarts.init(elem, null, {
			renderer: 'svg'
		});

		chartOptions = {
			xAxis: {
				show: false,
				type: 'category',
				boundaryGap: false,
			},
			tooltip: {
				trigger: 'axis',
			},
			yAxis: {
				show: false
			},
			grid: {
				left: 0,
				top: 0,
				right: 0,
				bottom: 0
			},
			animationDuration: 2000,
			series: [
				{
					name: "Items",
					data: data,
					type: 'line',
					smooth: true,
					lineStyle: {
						width: 3
					},
					symbolSize: 8,
					showSymbol: false,
					emphasis: {
						focus: 'series'
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
	} else {
		chartOptions = {
			series: [
				{
					data: data
				}
			],
			xAxis: {
				data: null
			}
		};
	}
	
	elem.chart.setOption(chartOptions);
}

async function itemTypeStats() {
	const stats = await app.ChartStats("classifications", tlz.openRepos[0].instance_id);

	if (!stats) {
		// TODO: show empty dashboard -- tell user to import some data
		return;
	}

	let total = 0;
	stats.map(point => total += point.value);

	// clean up any prior info
	$('#chart-item-types').replaceChildren();

	$('#item-class-count').innerText = total.toLocaleString();

	const elem = $('#chart-item-types');
	let chartOptions = {};
	
	if (!elem.chart) {
		elem.chart = echarts.init(elem, null, {
			renderer: 'svg'
		});

		chartOptions = {
			tooltip: {
				trigger: 'item'
			},
			grid: {
				top: 0,
				bottom: 0,
				left: 0,
				right: 0
			},
			series: [
				{
					name: 'Item type',
					type: 'pie',
					radius: ['50%', '80%'],
					center: ['70%', '50%'],
					avoidLabelOverlap: false,
					itemStyle: {
						borderRadius: 10,
						borderColor: '#fff',
						borderWidth: 2
					},
					label: {
						show: false,
						position: 'center'
					},
					emphasis: {
						label: {
							show: true,
							// fontSize: ,
							fontWeight: 'bold'
						}
					},
					labelLine: {
						show: false
					},
					data: stats
				}
			]
		};
	} else {
		chartOptions = {
			series: stats
		};
	}

	elem.chart.setOption(chartOptions);
}


async function dataSourceHourStats() {
	const series = await app.ChartStats("datasources", tlz.openRepos[0].instance_id);

	if (!series) {
		// TODO: show empty dashboard -- tell user to import some data
		return;
	}

	// find max across all data so we can scale the bubble size (yeah this is a mouthful)
	const dataMax = Math.max(...series.map(ds => Math.max(...ds.data.map(point => point[2]))));
	
	// add some chart formatting to the returned data, and scale the bubble size based on the 3rd dimension of the data point
	const minSymbolSize = 2, maxSymbolSize = 50;
	for (let i = 0; i < series.length; i++) {
		series[i] = {
			...series[i], // includes name and data
			type: 'scatter',
			symbolSize: point => ((point[2]+minSymbolSize)/dataMax * (maxSymbolSize-minSymbolSize)) + minSymbolSize,
			emphasis: {
				focus: 'series'
			}
		};
	}

	// turns a decimal hour (of day; i.e. between 0 and 24) into a human-readable time
	function hourDecimalToHumanTime(decimalHour) {
		// fast path for whole numbers
		if (decimalHour < 0 || decimalHour >= 24) return "";
		if (decimalHour == 0) return "12am";
		if (decimalHour == 12) return "noon";
		if (decimalHour % 1 == 0) {
			if (decimalHour > 12) return `${decimalHour%12}pm`;
			return `${decimalHour}am`;
		}

		const hours = Math.floor(decimalHour);
		const minutes = Math.round((decimalHour - hours) * 60);
	
		const date = new Date();
		date.setHours(hours, minutes, 0, 0);
	
		return date.toLocaleString(undefined, { hour: 'numeric', minute: '2-digit' });
	}

	const elem = $('#chart-data-sources');
	let chartOptions = {};
	
	if (!elem.chart) {
		elem.chart = echarts.init(elem, null, {
			// renderer: 'svg'
		});

		chartOptions = {
			legend: {
				bottom: 0
			},
			xAxis: {
				type: 'time',
				splitLine: {
					show: false
				}
			},
			yAxis: {
				splitLine: {
					show: false
				},
				splitNumber: 8,
				axisLabel: {
					formatter: hourDecimalToHumanTime
				},
				inverse: true,
				axisPointer: {
					label: {
						formatter: params =>  hourDecimalToHumanTime(params.value)
					}
				},
			},
			tooltip: {
				axisPointer: {
					type: 'cross',
					label: {
						backgroundColor: '#283b56'
					}
				},
				formatter: params => {
					return `
						<b>${params.data[0]}, ${hourDecimalToHumanTime(params.data[1])}&ndash;${hourDecimalToHumanTime((params.data[1]+1)%24)}</b>
						<br>
						<span style="color: ${params.color}; font-weight: bold">${params.seriesName}</span>:
						${params.data[2]}`;
				},
			},
			grid: {
				top: 15,
				right: 10,
				left: 10,
				bottom: 30,
				containLabel: true,
			},
			dataZoom: [
				{
					type: 'inside',
					xAxisIndex: [0],
				},
				{
					type: 'inside',
					yAxisIndex: [0],
				}
			],
			animation: false,
			animationDuration: 2000,
			series: series
		};
	} else {
		chartOptions = {
			series: series
		};
	}

	elem.chart.setOption(chartOptions);
}


async function recentDataSourceStats() {
	$('#chart-days-documented').replaceChildren();
	$('#chart-days-documented-legend').replaceChildren();

	const days = Number($('#days-documented .chart-scope .dropdown-item.active').dataset.days);

	const stats = await app.ChartStats("recent_data_sources", tlz.openRepos[0].instance_id, {days});

	let totalItems = 0;
	const dataSourceItemCounts = {};
	const daysSeen = {};

	if (stats)
	{
		for (const stat of stats) {
			daysSeen[stat.date] = true;
			dataSourceItemCounts[stat.data_source_name] = 
				(dataSourceItemCounts[stat.data_source_name] || 0) + stat.count;
			totalItems += stat.count;
		}
	}

	$('#days-documented-count').innerText = `${Object.keys(daysSeen).length.toLocaleString()} / ${days.toLocaleString()}`;

	const daysSeenPct = Object.keys(daysSeen).length / days * 100;
	const daysNotSeenPct = (days - Object.keys(daysSeen).length) / days * 100;

	let i = 0;
	for (const [dsName, count] of Object.entries(dataSourceItemCounts)) {
		const color = tlz.colorClasses[i%tlz.colorClasses.length];

		const div = document.createElement('div');
		div.classList.add('progress-bar', "bg-"+color);
		div.style.width = `${count/totalItems * daysSeenPct}%`;

		const legend = cloneTemplate('#tpl-days-documented-legend');
		$('.legend', legend).classList.add("bg-"+color);
		$('.ds-name', legend).innerText = dsName;
		$('.count', legend).innerText = count.toLocaleString();

		$('#chart-days-documented').append(div);
		$('#chart-days-documented-legend').append(legend);
		i++;
	}

	$('#days-documented-percent').innerText = `${daysSeenPct.toFixed(1).toLocaleString()}%`;
	if (daysSeenPct > 75) {
		$('#days-documented-percent').classList.add('text-green');
		$('#days-documented-percent').classList.remove('text-red', 'text-orange');
	} else if (daysSeenPct > 50) {
		$('#days-documented-percent').classList.add('text-orange');
		$('#days-documented-percent').classList.remove('text-red', 'text-green');
	} else {
		$('#days-documented-percent').classList.add('text-red');
		$('#days-documented-percent').classList.remove('text-green', 'text-orange');
	}
}