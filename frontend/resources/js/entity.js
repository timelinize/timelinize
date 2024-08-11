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
		row_id: [ rowID ],
		// count_items: true, // TODO: this is broken (counts are wrong)
		limit: 1,
	});

	console.log("RESULT:", entities);

	const ent = entities[0];
	
	$('#entity-id').innerText = ent.id;
	$('.entity-type').innerHTML = entityTypes[ent.type];
	$('.picture').innerHTML = avatar(true, ent, 'avatar-xxl avatar-rounded');
	$('.name').innerText = ent.name;

	for (const attr of ent.attributes) {
		if (attr.name == "_entity") continue;
		const tpl = cloneTemplate('#tpl-attribute');
		$('.attribute-name', tpl).innerText = attr.name;
		$('.attribute-value', tpl).innerText = attr.value;
		if (!attr.identity) {
			$('.ribbon', tpl).remove();
		}
		$('#attributes').append(tpl);
	}

	// const labels = [];
	// const data = [];
	// for (const attr of ent.attributes) {
	// 	labels.push(attr.value);
	// 	data.push(attr.item_count || 0);
	// }

	// console.log(labels, data);

	// new ApexCharts($('#chart-item-counts-by-attribute'), {
	// 	chart: {
	// 		type: "bar",
	// 		fontFamily: 'inherit',
	// 		height: 200,
	// 		parentHeightOffset: 0,
	// 		toolbar: {
	// 			show: false,
	// 		},
	// 		animations: {
	// 			enabled: false
	// 		},
	// 	},
	// 	plotOptions: {
	// 		bar: {
	// 			barHeight: '50%',
	// 			 horizontal: true,
	// 		}
	// 	},
	// 	dataLabels: {
	// 		enabled: false,
	// 	},
	// 	fill: {
	// 		opacity: 1,
	// 	},
	// 	series: [{
	// 		name: "Item count",
	// 		data: data
	// 	}],
	// 	tooltip: {
	// 		theme: 'dark'
	// 	},
	// 	grid: {
	// 		padding: {
	// 			top: -20,
	// 			right: 0,
	// 			left: -4,
	// 			bottom: -4
	// 		},
	// 		show: false
	// 		// strokeDashArray: 4,
	// 	},
	// 	xaxis: {
	// 		labels: {
	// 			padding: 0,
	// 		},
	// 		tooltip: {
	// 			enabled: false
	// 		},
	// 		axisBorder: {
	// 			show: false,
	// 		},
	// 		// type: 'datetime',
	// 	},
	// 	yaxis: {
	// 		labels: {
	// 			padding: 4
	// 		}
	// 	},
	// 	labels: labels,
	// 	colors: [tabler.getColor("primary")],
	// }).render();


}
