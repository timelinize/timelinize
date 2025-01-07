on('click', '.chart-scope .dropdown-item', (e) => {
	if (e.target.closest('#recent-items')) {
		$('.active', e.target.closest('.dropdown-menu')).classList.remove('active');
		e.target.classList.add('active');
		$('a', e.target.closest('.dropdown')).innerText = e.target.innerText;
		recentItemStats();
	}
});

async function renderDashboard() {
	await Promise.all([
		recentItemStats(),
		itemTypeStats(),
		dataSourceHourStats()
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
	$('#chart-recent-items-count').replaceChildren();


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

	new ApexCharts($('#chart-recent-items-count'), {
		chart: {
			type: 'area',
			fontFamily: 'inherit',
			height: 80.0,
			sparkline: {
				enabled: true
			},
			animations: {
				enabled: true
			},
		},
		dataLabels: {
			enabled: false,
		},
		// fill: {
		// 	opacity: .16,
		// 	type: 'solid'
		// },
		stroke: {
			width: 2
		},
		series: [{
			name: "Items",
			data: data,
		}],
		tooltip: {
			theme: 'dark'
		},
		grid: {
			strokeDashArray: 4,
		},
		xaxis: {
			labels: {
				padding: 0,
			},
			tooltip: {
				enabled: false
			},
			axisBorder: {
				show: false,
			},
			type: 'category',
		},
		yaxis: {
			labels: {
				padding: 4
			},
		},
		labels: groups,
		colors: [tabler.getColor("primary")],
		legend: {
			show: false,
		},
	}).render();	
}

async function itemTypeStats() {
	const stats = await app.ChartStats("classifications", tlz.openRepos[0].instance_id);

	if (!stats) {
		// TODO: show empty dashboard -- tell user to import some data
		return;
	}

	let total = 0;
	stats.map(point => total += point.y);

	// clean up any prior info
	$('#chart-item-types').replaceChildren();

	// TODO: Figure out what the number will be
	$('#item-class-count').innerText = total.toLocaleString();


	//////

	// const series = [], labels = [];
	// stats.map(point => {
	// 	labels.push(point.x);
	// 	series.push(point.y);
	// });
	// // console.log("SERIES:", series);

	// // can't use sparkline because we want the legend
	// new ApexCharts($('#chart-item-types'), {
	// 	chart: {
	// 		type: 'polarArea',
	// 		fontFamily: 'inherit',

	// 		// these values are to make up for the fact that barHeight doesn't seem to work
	// 		height: 110,
	// 		// height: 150.0, // 40 more than it should be
	// 		// offsetY: -35, // 50 less than it should be; the gap of 10 becomes a welcomed margin/padding
	// 		// parentHeightOffset: -40, // 40 less than it should be to keep the card equally sized
			
	// 		animations: {
	// 			enabled: true
	// 		},
	// 		toolbar: {
	// 			show: false
	// 		}
	// 	},
	// 	plotOptions: {
	// 		dataLabels: {
	// 			position: 'top'
	// 		}
	// 	},
	// 	dataLabels: {
	// 		enabled: false,
	// 	},
	// 	theme: {
	// 		palette: 'palette1' // up to palette10
	// 	},
	// 	stroke: {
	// 		colors: ['#fff']
	// 	  },
	// 	  fill: {
	// 		opacity: 0.8
	// 	  },
	// 	series: series,
	// 	labels: labels,
	// 	// tooltip: {
	// 	// 	theme: 'dark'
	// 	// },
	// 	// grid: {
	// 	// 	strokeDashArray: 4
	// 	// },
	// 	xaxis: {
	// 		type: 'category',
	// 		// show: false,
	// 		// labels: {
	// 		// 	show: false
	// 		// },
	// 		// axisBorder: {
	// 		// 	show: false
	// 		// },
	// 		// axisTicks: {
	// 		// 	show: false
	// 		// }
	// 		// tooltip: {
	// 		// 	enabled: false
	// 		// },
	// 	},
	// 	// yaxis: {
	// 	// 	show: false
	// 	// },
	// 	// grid: {
	// 	// 	show: false
	// 	// },
	// 	// legend: {
	// 	// 	// show: true
	// 	// 	position: 'left'
	// 	// },
	// }).render();

	/////

	// can't use sparkline because we want the legend
	new ApexCharts($('#chart-item-types'), {
		chart: {
			type: 'bar',
			fontFamily: 'inherit',

			// these values are to make up for the fact that barHeight doesn't seem to work
			height: 150.0, // 40 more than it should be
			offsetY: -35, // 50 less than it should be; the gap of 10 becomes a welcomed margin/padding
			parentHeightOffset: -40, // 40 less than it should be to keep the card equally sized
			
			animations: {
				enabled: true
			},
			toolbar: {
				show: false
			}
		},
		plotOptions: {
			bar: {
				columnWidth: '50%',
				barHeight: '200%', // TODO: this doesn't work :()
				distributed: true // necessary for palette colors
			},
			dataLabels: {
				position: 'top'
			}
		},
		dataLabels: {
			enabled: false,
		},
		theme: {
			palette: 'palette1' // up to palette10
		},
		series: [{
			name: "Item types",
			data: stats
		}],
		// tooltip: {
		// 	theme: 'dark'
		// },
		// grid: {
		// 	strokeDashArray: 4
		// },
		xaxis: {
			type: 'category',
			labels: {
				show: false
			},
			axisBorder: {
				show: false
			},
			axisTicks: {
				show: false
			}
			// tooltip: {
			// 	enabled: false
			// },
		},
		yaxis: {
			show: false
		},
		grid: {
			show: false
		},
		legend: {
			// show: true
			position: 'left'
		},
	}).render();
}


async function dataSourceHourStats() {
	const stats = await app.ChartStats("datasources", tlz.openRepos[0].instance_id);

	if (!stats) {
		// TODO: show empty dashboard -- tell user to import some data
		return;
	}

	console.log("STATS:", stats);

	// clean up any prior info
	$('#chart-data-sources').replaceChildren();


	// // can't use sparkline because we want the legend
	// new ApexCharts($('#chart-data-sources'), {
	// 	chart: {
	// 		type: 'bubble',
	// 		fontFamily: 'inherit',
	// 		height: 270.0,
	// 		offsetY: -50,
	// 		parentHeightOffset: -70,
	// 		animations: {
	// 			enabled: true
	// 		},
	// 		toolbar: {
	// 			show: false
	// 		},
	// 		zoom: {
	// 			enabled: false
	// 		}
	// 	},
	// 	plotOptions: {
	// 		bubble: {
	// 			// zScaling: false,
	// 			minBubbleRadius: 1,
	// 			// maxBubbleRadius: 10
	// 		},
	// 		dataLabels: {
	// 			enabled: false
	// 		}
	// 	},
	// 	dataLabels: {
	// 		enabled: false,
	// 	},
	// 	theme: {
	// 		palette: 'palette1' // up to palette10
	// 	},
	// 	series: stats,
	// 	// tooltip: {
	// 	// 	theme: 'dark'
	// 	// },
	// 	// grid: {
	// 	// 	strokeDashArray: 4
	// 	// },
	// 	xaxis: {
	// 		type: 'datetime',
	// 		// show: false,
	// 		labels: {
	// 			show: false
	// 		},
	// 		axisBorder: {
	// 			show: false
	// 		},
	// 		axisTicks: {
	// 			show: false
	// 		}
	// 		// tooltip: {
	// 		// 	enabled: false
	// 		// },
	// 	},
	// 	yaxis: {
	// 		show: false,
	// 		reversed: true
	// 	},
	// 	grid: {
	// 		show: false
	// 	},
	// 	legend: {
	// 		show: false,
	// 		// position: 'bottom'
	// 	}
	// }).render();

	//////////
	// With axes:

	// can't use sparkline because we want the legend
	new ApexCharts($('#chart-data-sources'), {
		chart: {
			type: 'bubble',
			fontFamily: 'inherit',

			// these values are to make up for the fact that barHeight doesn't seem to work
			height: 500.0, // 40 more than it should be
			offsetY: -70, // 50 less than it should be; the gap of 10 becomes a welcomed margin/padding
			parentHeightOffset: -70, // 40 less than it should be to keep the card equally sized
			
			animations: {
				enabled: true
			},
			toolbar: {
				show: false
			},
			zoom: {
				enabled: false
			}
		},
		plotOptions: {
			bubble: {
				// zScaling: false,
				minBubbleRadius: 1,
				// maxBubbleRadius: 10
			},
			dataLabels: {
				enabled: false
			}
		},
		dataLabels: {
			enabled: false,
		},
		theme: {
			palette: 'palette1' // up to palette10
		},
		series: stats,
		// tooltip: {
		// 	theme: 'dark'
		// },
		// grid: {
		// 	strokeDashArray: 4
		// },
		xaxis: {
			type: 'datetime',
			// show: false,
			// labels: {
			// 	show: false
			// },
			// axisBorder: {
			// 	show: false
			// },
			// axisTicks: {
			// 	show: false
			// }
			// tooltip: {
			// 	enabled: false
			// },
		},
		yaxis: {
			// show: false,
			// floating: true,
			min: 0,
			max: 24,
			stepSize: 3,
			reversed: true,
			labels: {
				// offsetX: 25,
				formatter: v => {
					if (v == 0) return "12am";
					if (v < 0 || v >= 24) return "";
					if (v == 12) return "noon";
					if (v > 12) return `${v%12}pm`;
					return `${v}am`;
				}
			}
		},
		grid: {
			show: false
		},
		legend: {
			show: false,
			// position: 'bottom'
		}
	}).render();
}