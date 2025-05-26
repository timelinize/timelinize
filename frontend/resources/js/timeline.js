async function loadAllGroups(maxGroups = 50) {
	async function loadBatch(params) {
		const results = await app.SearchItems(params);
		console.log("BATCH PARAMS, RESULTS:", params, results);
		return results.items;
	}

	// TODO: reinstate slicing?
	function finished(groups) {
		// groups.sort((a, b) => a[0].timestamp > b[0].timestamp ? 1 : -1 );
		// // keep last groups (newest items) because we truncate oldest items
		// return groups.slice(-(Math.min(groups.length, maxGroups) - groups.length));
		return groups;
	}



	let items = [];
	const start = DateTime.now();

	do {
		const params = timelineFilterParams(items?.[items.length-1]);
		const batch = await loadBatch(params);
		if (!batch) break;
		items = items.concat(batch);
		const groups = timelineGroups(items);

		if (groups.length > maxGroups || batch?.length < params.limit) {
			return finished(groups);
		}
	} while (-start.diffNow().as('seconds') < 10);

	return finished(timelineGroups(items));
}


function timelineFilterParams(lastItem) {
	const params = {
		related: 1,
		relations: [
			// don't show motion pictures / live photos, since they are not
			// considered their own item in a gallery sense, and perhaps
			// more importantly, we don't want to have to generate a thumbnail
			// for them (literally no need for a thumbnail of those, just
			// wasted CPU time and storage space)
			{
				"not": true,
				"relation_label": "motion"
			}
		],
		// offset: limit * (currentPageNum()-1), // TODO: figure out how to paginate the timeline
		limit: 100
	};


	commonFilterSearchParams(params);

	if (lastItem) {
		params.start_timestamp = lastItem?.timestamp;
	}

	console.log("PARAMS:", params)

	return params;
}











// // median returns the median of array. *NOTE:* array must be sorted!
// // (TODO: If needed, implement "median of medians" algorithm to get median from unsorted array in O(n) time)
// function median(array) {
// 	if (array.length == 0) {
// 		return 0;
// 	}
// 	if (array.length == 1) {
// 		return array[0];
// 	}
// 	if (array.length % 2 == 0) {
// 		return array[Math.floor(array.length/2)];
// 	}
// 	const middex = Math.floor(array.length / 2);
// 	return (array[middex] + array[middex+1]) / 2;
// }

// function standardDeviation(array) {
// 	if (!array?.length) return 0;
// 	const n = array.length;
// 	const mean = array.reduce((a, b) => a + b) / n;
// 	return Math.sqrt(array.map(x => Math.pow(x - mean, 2)).reduce((a, b) => a + b) / n);
// }

// function renderGroup(group, prevGroup) {
// 	if (!group.length) return;

// 	const tpl = cloneTemplate('#tpl-tl-item-card');
	
// 	let base, word;
// 	if (prevGroup?.length) {
// 		base = DateTime.fromISO(prevGroup[0].timestamp);
// 		word = "earlier";
// 	}
// 	let relativeTime = DateTime.fromISO(group[0].timestamp).toRelative({
// 		base: base
// 	});
// 	if (relativeTime) {
// 		// TODO: this only works for English, sigh...
// 		if (relativeTime.startsWith("in ")) {
// 			relativeTime = relativeTime.replace("in ", "") + " later";
// 		}
// 		$('.list-timeline-time', tpl).innerText = relativeTime;
// 	}

// 	const display = itemMiniDisplay(tlz.openRepos[0], group);
	
// 	$('.list-timeline-icon', tpl).innerHTML = display.icon;
// 	$('.list-timeline-icon', tpl).classList.add(`bg-${display.iconColor}`);
// 	$('.list-timeline-content-container', tpl).append(display.element);
// 	$('#timeline').append(tpl);
// }
