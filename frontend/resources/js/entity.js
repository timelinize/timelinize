const entityTypes = {
	person: `
		<svg xmlns="http://www.w3.org/2000/svg" class="icon icon-tabler icon-tabler-user me-2" width="24" height="24" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" fill="none" stroke-linecap="round" stroke-linejoin="round">
			<path stroke="none" d="M0 0h24v24H0z" fill="none"></path>
			<circle cx="12" cy="7" r="4"></circle>
			<path d="M6 21v-2a4 4 0 0 1 4 -4h4a4 4 0 0 1 4 4v2"></path>
		</svg>
		Person
	`,
	creature: `
		<svg xmlns="http://www.w3.org/2000/svg" class="icon icon-tabler icon-tabler-paw me-2" width="24" height="24" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" fill="none" stroke-linecap="round" stroke-linejoin="round">
			<path stroke="none" d="M0 0h24v24H0z" fill="none"></path>
			<path d="M14.7 13.5c-1.1 -1.996 -1.441 -2.5 -2.7 -2.5c-1.259 0 -1.736 .755 -2.836 2.747c-.942 1.703 -2.846 1.845 -3.321 3.291c-.097 .265 -.145 .677 -.143 .962c0 1.176 .787 2 1.8 2c1.259 0 3.004 -1 4.5 -1s3.241 1 4.5 1c1.013 0 1.8 -.823 1.8 -2c0 -.285 -.049 -.697 -.146 -.962c-.475 -1.451 -2.512 -1.835 -3.454 -3.538z"></path>
			<path d="M20.188 8.082a1.039 1.039 0 0 0 -.406 -.082h-.015c-.735 .012 -1.56 .75 -1.993 1.866c-.519 1.335 -.28 2.7 .538 3.052c.129 .055 .267 .082 .406 .082c.739 0 1.575 -.742 2.011 -1.866c.516 -1.335 .273 -2.7 -.54 -3.052z"></path>
			<path d="M9.474 9c.055 0 .109 -.004 .163 -.011c.944 -.128 1.533 -1.346 1.32 -2.722c-.203 -1.297 -1.047 -2.267 -1.932 -2.267c-.055 0 -.109 .004 -.163 .011c-.944 .128 -1.533 1.346 -1.32 2.722c.204 1.293 1.048 2.267 1.933 2.267z"></path>
			<path d="M16.456 6.733c.214 -1.376 -.375 -2.594 -1.32 -2.722a1.164 1.164 0 0 0 -.162 -.011c-.885 0 -1.728 .97 -1.93 2.267c-.214 1.376 .375 2.594 1.32 2.722c.054 .007 .108 .011 .162 .011c.885 0 1.73 -.974 1.93 -2.267z"></path>
			<path d="M5.69 12.918c.816 -.352 1.054 -1.719 .536 -3.052c-.436 -1.124 -1.271 -1.866 -2.009 -1.866c-.14 0 -.277 .027 -.407 .082c-.816 .352 -1.054 1.719 -.536 3.052c.436 1.124 1.271 1.866 2.009 1.866c.14 0 .277 -.027 .407 -.082z"></path>
		</svg>
		Creature
	`
};

async function entityPageMain() {
	const {repoID, rowID} = parseURIPath();

	$('#merge-entity').dataset.entityIDMerge = rowID;

	const entities = await app.SearchEntities({
		repo: repoID,
		row_id: [rowID],
		limit: 1,
	});

	console.log("RESULT:", entities);

	const ent = entities[0];
	
	$('#entity-id').innerText = ent.id;
	$('#entity-type').innerHTML = entityTypes[ent.type];
	$('#picture').innerHTML = avatar(true, ent, 'avatar-xxl');
	$('#name').innerText = ent.name || "Unknown";

	for (const attr of ent.attributes) {
		if (attr.name == "birth_date") {
			$('#birth-date').innerText = DateTime.fromSeconds(Number(attr.value)).toLocaleString(DateTime.DATE_FULL);
		}
		if (attr.name == "birth_place") {
			$('#birth-date').innerText = attr.value;
		}
		if (attr.name == "gender") {
			$('#birth-date').innerText = attr.value;
		}
		if (attr.name == "website") {
			const container = document.createElement('div');
			container.innerText = attr.value;
			if ($('#websites').children.length == 0) {
				$('#websites').innerHTML = '';
			}
			$('#websites').append(container);
		}
	}

	// no need to block page load for this
	app.SearchItems({
		entity_id: [rowID],
		only_total: true,
		flat: true
	}).then(result => {
		$('#num-items').innerText = `${result.total.toLocaleString()} items`;
	});


	///////////////////////////////////////////////////////////////////////////////



	const attributeStats = await app.ChartStats("attributes_stacked_area", tlz.openRepos[0].instance_id, {entity_id: rowID});
	console.log("ATTRIBUTE STATS:", attributeStats);


	const colors = [];
	for (const colorClass of tlz.colorClasses) {
		if (!colorClass.endsWith("-lt")) {
			colors.push(tabler.getColor(colorClass.slice(3)));
		}
	}

	for (const series of attributeStats) {
		let i = 0;
		for (const attrName in tlz.attributeLabels) {
			if (attrName == series.attribute_name) {
				break;
			}
			i++;
		}
		series.color = colors[i % colors.length];
	}
	

	var options2 = {
		series: attributeStats,
		chart: {
			type: 'area',
			stacked: false,
			height: 500,
			// zoom: {
			// 	enabled: false
			// },
		},
		dataLabels: {
			enabled: false
		},
		markers: {
			size: 0,
		},
		stroke: {
			// curve: 'straight'
		},
		fill: {
			type: 'gradient',
			gradient: {
				shadeIntensity: 1,
				inverseColors: false,
				opacityFrom: 0.45,
				opacityTo: 0.05,
				stops: [20, 100, 100, 100]
			},
		},
		yaxis: {
			// logarithmic: true,
			labels: {
				style: {
					colors: '#8e8da4',
				},
				offsetX: 0,
				// formatter: function (val) {
				// 	return (val / 1000000).toFixed(2);
				// },
			},
			axisBorder: {
				show: false,
			},
			axisTicks: {
				show: false
			}
		},
		xaxis: {
			type: 'datetime',
			// tickAmount: 8,
			// min: new Date("01/01/2014").getTime(),
			// max: new Date("01/20/2014").getTime(),
			labels: {
				// rotate: -15,
				// rotateAlways: true,
				// formatter: function (val, timestamp) {
				// 	return moment(new Date(timestamp)).format("DD MMM YYYY")
				// }
			}
		},
		title: {
			text: 'Items by Attribute Over Time',
			align: 'left',
			// offsetX: 14
		},
		tooltip: {
			// shared: true
		},
		legend: {
			position: 'top',
			// horizontalAlign: 'right',
			// offsetX: -10,
			// customLegendItems: [
			// 	"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", 
			// ]
		}
	};

	var chart2 = new ApexCharts(document.querySelector("#chart2"), options2);
	chart2.render();



	/////////////////////////////////////////////////////////////////////////////


	// const seriesMap = {};

	// for (const attr of ent.attributes) {
	// 	// if (attr.name == "_entity") continue;
	// 	// TODO: could group these to allow them to run concurrently
	// 	const first = await app.SearchItems({
	// 		start_timestamp: new Date("0000-01-01").toISOString(), // require result to have a timestamp
	// 		attribute_id: [attr.id],
	// 		flat: true,
	// 		limit: 1,
	// 		sort: "ASC"
	// 	});
	// 	const last = await app.SearchItems({
	// 		start_timestamp: new Date("0000-01-01").toISOString(), // require result to have a timestamp
	// 		attribute_id: [attr.id],
	// 		flat: true,
	// 		limit: 1,
	// 		sort: "DESC"
	// 	});

	// 	if (!first.items?.length || !last.items?.length) {
	// 		continue;
	// 	}

	// 	if (!seriesMap[attr.name]) {
	// 		seriesMap[attr.name] = {
	// 			name: attr.name,
	// 			data: []
	// 		};
	// 	}

	// 	seriesMap[attr.name].data.push({
	// 		x: attr.value,
	// 		y: [
	// 			new Date(first.items[0].timestamp).getTime(),
	// 			new Date(last.items[0].timestamp).getTime()
	// 		]
	// 	});
	// }

	// console.log("SERIES MAP:", seriesMap);
	// const series = [];
	// for (const attrName in seriesMap) {
	// 	series.push(seriesMap[attrName]);
	// }

	// console.log("SERIES:", series);

	// var options = {
	// 	series: series,
	// 	chart: {
	// 		height: ent.attributes.length*15,
	// 		type: 'rangeBar',
	// 		toolbar: {
	// 			show: false,
	// 		},
	// 		zoom: {
	// 			enabled: false
	// 		},
	// 	},
	// 	plotOptions: {
	// 		bar: {
	// 			borderRadius: 5,
	// 			horizontal: true,
	// 			barHeight: "75%",
	// 			rangeBarGroupRows: true
	// 		}
	// 	},
	// 	dataLabels: {
	// 		enabled: true,
	// 		formatter: function (val) {
	// 			return betterToHuman(DateTime.fromJSDate(val[0]).diff(DateTime.fromJSDate(val[1])))
	// 		}
	// 	},
	// 	fill: {
	// 		type: 'gradient',
	// 		gradient: {
	// 			shade: 'light',
	// 			type: 'vertical',
	// 			shadeIntensity: 0.25,
	// 			gradientToColors: undefined,
	// 			inverseColors: true,
	// 			opacityFrom: 1,
	// 			opacityTo: 1,
	// 			stops: [50, 0, 100, 100]
	// 		}
	// 	},
	// 	xaxis: {
	// 		type: 'datetime'
	// 	},
	// 	grid: {
	// 		row: {
	// 			colors: ['#fafafa', '#fff'],
	// 			opacity: 1
	// 		}
	// 	},
	// 	legend: {
	// 		position: 'top'
	// 	}
	// };

	// var chart = new ApexCharts($("#chart"), options);
	// chart.render();








	/////////////////////////////////////////////////////////



}
