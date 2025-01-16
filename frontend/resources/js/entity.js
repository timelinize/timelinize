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

	const owner = await getOwner();

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
			continue;
		}
		if (attr.name == "birth_place") {
			$('#birth-place').innerText = attr.value;
			continue;
		}
		if (attr.name == "gender") {
			$('#gender').innerText = attr.value;
			continue;
		}
		if (attr.name == "website") {
			const container = document.createElement('div');
			container.innerText = attr.value;
			if ($('#websites').children.length == 0) {
				$('#websites').innerHTML = '';
			}
			$('#websites').append(container);
			continue;
		}

		if (attr.identity || attr.name == "_entity") {
			const valueEl = cloneTemplate('#tpl-attribute-label');
			valueEl.classList.add('attribute-value');
			$('.form-check-label', valueEl).innerText = attr.value;

			let groupEl = $(`.attribute-group.attribute-name-${attr.name}`);
			if (!groupEl) {
				const labelEl = cloneTemplate('#tpl-attribute-label');
				labelEl.classList.add('attribute-label');
				$('.form-check-label', labelEl).classList.add('strong');
				$('.form-check-label', labelEl).innerText = tlz.attributeLabels[attr.name] || attr.name;

				groupEl = document.createElement('div');
				groupEl.classList.add('attribute-group', `attribute-name-${attr.name}`);
				groupEl.append(labelEl);
				$('#attribute-groups').append(groupEl);
			}
			
			groupEl.append(valueEl);
		}
	}

	// show the "You" badge next to help identify self
	if (ent.id == owner.id) {
		const badge = document.createElement('span');
		badge.classList.add('badge', 'bg-purple-lt', 'ms-2');
		badge.innerText = "You";
		$('#name').append(badge);
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

	const attributeStats2 = await app.ChartStats("attributes_stacked_area", tlz.openRepos[0].instance_id, {entity_id: rowID});
	console.log("ATTRIBUTE STATS:", attributeStats2);
	for (const series of attributeStats2) {
		series.type = 'bar';
		// series.barWidth = '100%';
		// series.smooth = true;
		series.stack = 'total';
		// series.areaStyle = {};
		series.emphasis = {	focus: 'series' };
	}

	// const totalData = [];
	// for (const series of attributeStats2) {
	// 	let sum = 0;
	// 	for (let j = 0; j < series.data.length; ++j) {
	// 		sum += series.data[j][1];
	// 	}
	// 	totalData.push(sum);
	// }
	// console.log("TOTAL DATA:", totalData);


	$('#chart').chart = echarts.init($('#chart'));

	let option = {
		title: {
			text: 'Items by Attribute Over Time'
		},
		grid: {
			top: 80,
			left: 0,
			right: 0
		},
		tooltip: {
			trigger: 'axis',
			axisPointer: {
				type: 'shadow',
				label: {
					backgroundColor: '#6a7985'
				}
			}
		},
		toolbox: {
			feature: {
				saveAsImage: {},
				dataZoom: {
					yAxisIndex: 'none'
				},
				restore: {}
			}
		},
		legend: {
			type: 'scroll',
			top: 35
		},
		xAxis: [
			{
				type: 'time'
			}
		],
		yAxis: [
			{
				type: 'value'
			}
		],
		dataZoom: [
			{
				type: 'inside'
			},
			{
				type: 'slider'
			}
		],
		series: attributeStats2,
	};
	$('#chart').chart.setOption(option);
}


// we have to specify the checkbox element specifically, otherwise we end up with double events firing (one on the checkbox, one on the span)
on('click', '.attribute-label input[type=checkbox]', e => {
	const checked =  $('input[type=checkbox]', e.target.closest('label')).checked;
	const groupEl = e.target.closest('.attribute-group');
	const action = checked ? 'legendSelect' : 'legendUnSelect';
	const actionBatch = [];
	$$('.attribute-value input[type=checkbox]', groupEl).forEach(el => {
		el.checked = checked;
		actionBatch.push({
			name: el.nextElementSibling.innerText
		});
	});
	$('#chart').chart.dispatchAction({
		type: action,
		batch: actionBatch
	});
});

on('click', '.attribute-value input[type=checkbox]', e => {
	const checked =  $('input[type=checkbox]', e.target.closest('label')).checked;
	const value = $('.form-check-label', e.target.closest('label')).innerText;
	const groupEl = e.target.closest('.attribute-group');

	if (!$('.attribute-value input[type=checkbox]:checked')) {
		$('.attribute-label input[type=checkbox]', groupEl).checked = false;
	}

	const action = checked ? 'legendSelect' : 'legendUnSelect';

	$('#chart').chart.dispatchAction({
		type: action,
		name: value
	});
});