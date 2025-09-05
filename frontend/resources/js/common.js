// Commmon JS code for the whole application



// Toggle all checkboxes in dropdown list
on('click', '.dropdown-menu .select-all, .dropdown-menu .select-none', (e) => {
	const menu = e.target.closest('.dropdown-menu');
	for (const checkbox of $$('input[type=checkbox]', menu)) {
		checkbox.checked = e.target.classList.contains('select-all');
	}
	menu.dispatchEvent(new Event('change', { bubbles: true }));
});


// For some reason, the "Clear" and "Apply" Air Datepicker buttons
// will submit a parent form, when clicked... but I do not know why
// the other buttons don't, nor do I know why it only happened on
// the Items and Gallery pages. Form tags can be semantically useful
// so I don't want to never use them, but we also can't allow the
// browser to submit the form when we're using the datepicker.
on('submit', 'form', e => {
	if (e.submitter.classList.contains('air-datepicker-button')) {
		e.preventDefault();
	}
});


function classInfo(name) {
	const classes = load('item_classes');
	for (const clName in classes) {
		if (clName == name) {
			return classes[clName];
		}
	}
	return {
		name: "",
		labels: ["Unknown"],
		description: "The nature of this item is unknown."
	};
}

// getOwner returns the owner (person ID 1) of the given repo.
async function getOwner(repo) {
	if (!repo) {
		repo = tlz.openRepos[0];
	}
	let owner = load('owner');
	if (!owner) {
		owner = await app.GetEntity(repo.instance_id, 1);
		store('owner', owner);
	}
	return owner;
}

// entityAttribute returns the value of the given attribute for the given person.
function entityAttribute(entity, attribute) {
	if (!entity.attributes) {
		return "";
	}
	for (var i = 0; i < entity.attributes.length; i++) {
		if (entity.attributes[i].name == attribute) {
			return entity.attributes[i].value;
		}
	}
	return "";
}

















// // TODO: not used? (can be handy for turning a string into a number, like for assigning a data source or person's name a color, if not using their ID...)
// // Thanks to https://stackoverflow.com/a/7616484/1048862
String.prototype.hashCode = function() {
	var hash = 0,
		i, chr;
	if (this.length === 0) return hash;
	for (i = 0; i < this.length; i++) {
		chr = this.charCodeAt(i);
		hash = ((hash << 5) - hash) + chr;
		hash |= 0; // Convert to 32bit integer
	}
	return hash;
}


Element.prototype.isEmpty = function() {
	return this.textContent.trim() === "";
}





















function currentPageNum() {
	return Number(new URLSearchParams(window.location.search).get('page') || 1);
}

function activateTooltips() {
	const tooltipList = [...$$('[data-bs-toggle="tooltip"]')].map(tooltipTriggerEl => new bootstrap.Tooltip(tooltipTriggerEl))
}

// updateFilterResults runs the page's render function again to replace .filter-results
// with the latest parameters in the query string.
function updateFilterResults() {
	// fade out current results
	$$('.filter-results:not(.d-none)').forEach(elem => elem.classList.add('opacity0'));

	// if the results take a while to load, show a loading indicator
	let slowLoadingHandle = setTimeout(function() {
		const span = document.createElement('span');
		span.classList.add('slow-loader', 'filter-loader');
		$('.filter-results:not(.d-none)')?.insertAdjacentElement('beforebegin', span);
	}, 1000);
	
	// once fadeout is complete, render the new results
	setTimeout(async function() {
		// update the results
		await tlz.currentPageController?.render();
		
		// after the rendering is complete, fade in results
		// (need brief timeout to allow time for paint, I guess; otherwise browser just flashes in the content)
		setTimeout(function() {
			$$('.filter-results:not(.d-none)').forEach(elem => elem.classList.remove('opacity0'));
			
			// activate custom/Bootstrap tooltips on the page
			activateTooltips();
		}, 25);
		
		// hide any loading indicator
		clearTimeout(slowLoadingHandle);
		$('.slow-loader.filter-loader')?.remove();
	}, 250);
}

// when filter inputs change, update query string and re-render page
on('change',
	`.filter-input:not(.nonfilter),
	.filter input:not(.nonfilter),
	.filter select:not(.nonfilter),
	.filter .dropdown-menu:not(.nonfilter)`, event => {

	// convenient way to notify other parts of the code that the filter has been changed and the results are about to be updated/reset
	$('.filter').dispatchEvent(new Event('change', { bubbles: true }));

	// update query string in the URL bar so the filter will read the updated params
	var qs = filterToQueryString().toString();
	let newurl = window.location.protocol + "//" + window.location.host + window.location.pathname;
	if (qs) {
		newurl += '?' + qs;
	}
	window.history.replaceState(null, '', newurl);

	updateFilterResults();
});





on('change', '.date-sort', e => {
	setDateInputPlaceholder(e.target.closest('.date-input-container'));
});





on('mouseover', '.explore-pages a', e => {
	$('#explore-page-preview').src = `/resources/images/${e.target.closest('a').dataset.preview}`;	
});


Object.defineProperty(HTMLMediaElement.prototype, 'playing', {
    get: function(){
        return !!(this.currentTime > 0 && !this.paused && !this.ended && this.readyState > 2);
    }
})
on('mouseover', '.minidisp-media video, .video-thumbnail', e => {
	if (!e.target.playing) {
		e.target.muted = true; // TODO: store previous muted value, then restore that on mouseout
		e.target.play();
	}
});
on('mouseout', '.minidisp-media video, .video-thumbnail', e => {
	e.target.pause();
});


// This is a hack to fix tabler.js, wherein switch icons have event listeners
// added on page load, which doesn't work for dynamically-added elements.
on('click', '[data-bs-toggle="switch-icon"]', e => {
	e.target.closest('[data-bs-toggle="switch-icon"]').classList.toggle('active');
});


// Returns an array of the attrbutes with the given name on the entity.
function getEntityAttribute(entity, attributeName) {
	const attrs = [];
	if (!entity.attributes) {
		return attrs;
	}
	for (const attr of entity.attributes) {
		if (attr.name == attributeName) {
			attrs.push(attr)
		}
	}
	return attrs;
}


// Dynamic timestamps which update as much as every second to always show a correct
// relative time on the screen. Pass in the element to put the relative text in
// and the timestamp string from a JSON object.
function setDynamicTimestamp(elem, isoOrUnixSecTime, forDuration) {
	elem._timestamp = typeof isoOrUnixSecTime === 'number'
		? DateTime.fromSeconds(isoOrUnixSecTime)
		: DateTime.fromISO(isoOrUnixSecTime);
	elem.innerText = elem._timestamp.toRelative();
	elem.classList.add(forDuration ? "dynamic-duration" : "dynamic-time");
}

// Luxon (as of v3.5.0) does not have a good toHuman() function for Duration objects.
// It naively prints all the units of the duration even if they are 0, and the default
// units used by diff() is only milliseconds, which is not human readable at all. In
// other words, Luxon's Duration.toHuman() is totally broken.
// See bug report at https://github.com/moment/luxon/issues/1134.
//
// This function wraps Luxon's toHuman() with more sensible behavior. It prints milliseconds
// only if the duration < 1s, and only prints non-zero units. It also prints whole numbers,
// not fractions (unless the duration is <1 ms), and passes opts through to toHuman(), which
// are the same as those available with the standard Intl.NumberFormat constructor (see Luxon's
// toHuman() docs).
//
// Based on the workaround by seyeong on GitHub: https://github.com/moment/luxon/issues/1134#issuecomment-1637008762
function betterToHuman(luxonDuration, opts) {
	const duration = luxonDuration.shiftTo('days', 'hours', 'minutes', 'seconds', 'milliseconds').toObject();

	// remove 0-valued units
	const cleanedDuration = Object.fromEntries(
		Object.entries(duration).filter(([_k, v]) => v !== 0)
	);

	// if units larger than milliseconds exist, drop milliseconds
	if (Object.keys(cleanedDuration).length > 1) {
		delete cleanedDuration.milliseconds;
	}

	let digits = 0;
	if (cleanedDuration.milliseconds < 1.0) {
		digits = 3;
	}

	return Duration.fromObject(cleanedDuration).toHuman({ maximumFractionDigits: digits, ...opts });
}



//////////////////////////////////////////////////////
// Events handling (logs)
//////////////////////////////////////////////////////

function freezePage(modal) {
	if (modal) {
		console.log("Showing modal");
		tlz.loggerSocket.modal.show();
	}
	for (const [key, itvl] of Object.entries(tlz.intervals)) {
		if (itvl.interval) {
			console.info("Clearing interval:", key);
			clearInterval(itvl.interval);
		}
	}
}

function unfreezePage(modal) {
	if (modal) {
		tlz.loggerSocket.modal.hide();
		console.log("Modal hidden");
	}
	for (const [key, itvl] of Object.entries(tlz.intervals)) {
		if (itvl.interval) {
			console.info("Setting interval:", key);
			tlz.intervals[key].interval = itvl.set();
		}
	}
}

function connectLog() {
	// this sentinel value is used to avoid overlapping setTimeouts, and thus
	// extra calls to connectLog, by both onerror and onclose being invoked
	// (and since we are now trying to connect, we can clear the sentinel)
	tlz.loggerSocket.retrying = false;

	tlz.loggerSocket.socket = new WebSocket(`ws://${window.location.host}/api/logs`);

	tlz.loggerSocket.socket.onopen = function(event) {
		console.info("Established connection to logger socket", event, tlz.loggerSocket.socket);
		if (tlz.loggerSocket.modal) {
			unfreezePage(tlz.loggerSocket.modal);
			delete tlz.loggerSocket.modal;
		}
	};
	tlz.loggerSocket.socket.onmessage = function(event) {
		const l = JSON.parse(event.data);
		
		// for now, we don't care about HTTP access logs
		if (l.logger == "app.http") {
			return;
		}

		if (l.logger == "job.status" && l.job) {
			const job = l.job;

			// if this job has a parent that happens to be on the screen showing
			// previews of its children, make sure this job is rendered so it
			// can be updated
			if (job.parent_job_id != null) {
				const container = $(`#subsequent-jobs-container.job-id-${job.parent_job_id}`);
				if (container && !$(`.job-preview.job-id-${job.id}`, container)) {
					container.classList.remove('d-none');
					renderJobPreview($('#subsequent-jobs-list'), job);
				}
			}

			// add job preview to global nav dropdown
			for (listElem of $$('.recent-jobs-list')) {
				// don't duplicate job preview elements; normally, renderJobPreview()
				// does this for us, but we are wrapping the container for the sake of
				// display in the navbar dropdown, which is a list-group, so we have
				// to check for duplicates ourselves
				if ($(`.job-preview.job-id-${job.id}`, listElem)) {
					continue;
				}
				const listItemWrapperElem = document.createElement('div');
				listItemWrapperElem.classList.add('list-group-item');
				renderJobPreview(listItemWrapperElem, job);
				listElem.prepend(listItemWrapperElem);
			}

			// update UI elements that portray this job
			jobProgressUpdate(job);

			return;
		}

		// if the owner entity just had its picture set, update it in the UI
		if (l.logger == "job.action" && l.msg == "new owner picture") {
			updateRepoOwners(true);
		}

		if (l.logger == "job.action" && l.msg == "finished graph" && $(`.job-import-stream.job-id-${l.job.id}`)) {
			const job = l.job;

			// this page is for this import job, so display its table
			$('.job-import-stream-container').classList.remove('d-none');
			
			const tableElem = $(`.job-import-stream.job-id-${job.id}`);
			const rowElem = cloneTemplate('#tpl-job-import-stream-row');

			let location = l?.lat?.toFixed?.(4) || "";
			if (l.lon) {
				if (location != "") location += ", ";
				location += l.lon.toFixed(4);
			}

			let howStored = '<span class="badge bg-red me-1"></span> Interrupted';
			if (l.status == 'inserted') {
				howStored = '<span class="badge bg-green me-1"></span> New';
			} else if (l.status == 'skipped') {
				howStored = '<span class="badge bg-secondary me-1"></span> Skipped';
			} else if (l.status == 'updated') {
				howStored = '<span class="badge bg-blue me-1"></span> Updated';
			} else if (l.type == "entity") {
				howStored = '<span class="badge bg-purple me-1"></span> Processed';
			}

			let graphType = "";
			if (l.type == "item") {
				graphType = `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor"
						stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
						class="icon icon-tabler icons-tabler-outline icon-tabler-file">
						<path stroke="none" d="M0 0h24v24H0z" fill="none" />
						<path d="M14 3v4a1 1 0 0 0 1 1h4" />
						<path d="M17 21h-10a2 2 0 0 1 -2 -2v-14a2 2 0 0 1 2 -2h7l5 5v11a2 2 0 0 1 -2 2z" />
					</svg> Item`;
			} else if (l.type == "entity") {
				graphType = `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor"
						stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
						class="icon icon-tabler icons-tabler-outline icon-tabler-user">
						<path stroke="none" d="M0 0h24v24H0z" fill="none" />
						<path d="M8 7a4 4 0 1 0 8 0a4 4 0 0 0 -8 0" />
						<path d="M6 21v-2a4 4 0 0 1 4 -4h4a4 4 0 0 1 4 4v2" />
					</svg> Entity`;
			}

			$('.import-stream-row-id', rowElem).innerText = l.row_id || "";
			$('.import-stream-row-type', rowElem).innerHTML = graphType;
			$('.import-stream-row-status', rowElem).innerHTML = howStored;
			$('.import-stream-row-data-source', rowElem).innerHTML = `<img src="/ds-image/${l.data_source_name}">`;
			$('.import-stream-row-class', rowElem).innerText = l.classification !== undefined ? classInfo(l.classification).labels[0] : "";
			$('.import-stream-row-entity', rowElem).innerText = l.entity || "";
			$('.import-stream-row-content', rowElem).innerText = l.preview || maxlenStr(l.filename, 35) || maxlenStr(l.intermediate_path, 35) || maxlenStr(l.original_path, 35) || "";
			$('.import-stream-row-timestamp', rowElem).innerText = l.item_timestamp ? DateTime.fromSeconds(l.item_timestamp).toLocaleString(DateTime.DATETIME_SHORT_WITH_SECONDS) : "";
			$('.import-stream-row-location', rowElem).innerText = location;
			// $('.import-stream-row-content-type', rowElem).innerText = l.media_type || "";
			$('.import-stream-row-size', rowElem).innerText = humanizeBytes(l.size);
			// $('.import-stream-row-duration', rowElem).innerText = l.duration ? betterToHuman(Duration.fromMillis(l.duration*1000), { unitDisplay: 'short' }) : "-";
			
			$('tbody', tableElem).prepend(rowElem);

			const MAX_STREAM_TABLE_ROWS = 15;
			for (let i = MAX_STREAM_TABLE_ROWS; i < $$('tbody tr', tableElem).length; i++) {
				$$('tbody tr', tableElem)[i].remove();
			}
		} else if (l.logger == "job.action" && l.msg == "finished thumbnail" && $(`.job-thumbnail-stream.job-id-${l.job.id}`)) {
			const job = l.job;

			// this page is for this thumbnail job, so display its output
			$('.job-thumbnail-stream-container').classList.remove('d-none');

			const gridElem = $(`.job-thumbnail-stream.job-id-${job.id}`);
			const cellElem = cloneTemplate('#tpl-job-thumbnail-stream-cell');

			$('.datagrid-content', cellElem).append(itemContentElement({
				data_file: l.data_file,
				data_id: l.data_id,
				data_type: l.data_type,
				repo_id: job.repo_id,
				thumb_hash: l.thumb_hash,
			}, { thumbnail: true }))

			gridElem.prepend(cellElem);

			const MAX_STREAM_GRID_CELLS = 12;
			for (let i = MAX_STREAM_GRID_CELLS; i < $$('.datagrid-item', gridElem).length; i++) {
				$$('.datagrid-item', gridElem)[i].remove();
			}
		}
	};
	function lostConnection(event) {
		// don't repeat what has already been done for this connection failure
		if (tlz.loggerSocket.retrying) {
			return;
		}
		// log this event, then retry after a moment
		const logFn = event.type == "error" ? console.error : console.warn;
		logFn("Lost connection to logger socket; retrying:", event);
		// if a disconnect message isn't showing already, display it
		if (!tlz.loggerSocket.modal) {
			console.log("Making modal and freezing page")
			tlz.loggerSocket.modal = new bootstrap.Modal($('#modal-disconnected'));
			freezePage(tlz.loggerSocket.modal);
		}
		tlz.loggerSocket.retrying = true;
		setTimeout(connectLog, 500);
	}
	tlz.loggerSocket.socket.onclose = lostConnection;
	tlz.loggerSocket.socket.onerror = event => {
		const logFn = event.type == "error" ? console.error : console.warn;
		logFn("Logger socket error:", event);
	};
}
connectLog();














// This intersection observer is intended for map placeholder elements only.
tlz.mapIntersectionObs = new IntersectionObserver((entries, opts) => {
	entries.forEach(entry => {
		if (entry.isIntersecting) {
			tlz.mapsInViewport.add(entry.target);
		} else {
			tlz.mapsInViewport.delete(entry.target);
		}
	});
	if (tlz.mapsInViewport.size == 1) {
		moveMapInto(tlz.mapsInViewport.values().next().value);
	}
});





// These next blocks move the map to the map container nearest the mouse pointer

function getDistanceToRect(mouseX, mouseY, rect) {
	const dx = Math.max(rect.left - mouseX, mouseX - rect.right);
	const dy = Math.max(rect.top - mouseY, mouseY - rect.bottom);
	return Math.sqrt(dx**2 + dy**2);
}

function getNearestMapPlaceholderElement(mouseX, mouseY) {
	let nearestElement;
	let nearestDistance = Infinity;

	tlz.mapsInViewport.forEach(element => {
		const rect = element.getBoundingClientRect();
		const distance = getDistanceToRect(mouseX, mouseY, rect);

		if (distance < nearestDistance) {
			nearestDistance = distance;
			nearestElement = element;
		}
	});

	return nearestElement;
}

document.addEventListener('mousemove', event => {
	if (tlz.mapsInViewport.size > 1) {
		const nearestElement = getNearestMapPlaceholderElement(event.clientX, event.clientY);
		moveMapInto(nearestElement);
	}
});

function moveMapInto(mapContainerElem) {
	// no-op if there is nothing to move the map into, or if it's the same element
	if (!mapContainerElem || mapContainerElem == tlz.nearestMapElem) {
		return;
	}
	
	const prevMapElem = tlz.nearestMapElem;
	tlz.nearestMapElem = mapContainerElem;

	// when the map is rendered to the page, make sure it resizes properly, then render this map's data
	// See https://stackoverflow.com/a/66172042/1048862 (several answers exist, most are kind of hacky)
	// (the resizeCount is because... for some reason, it seems that the container on the map explore
	// page resizes twice before it settles on its initial size; it's a bit hacky but the other option
	// is to use a setTimeout but that's even worse)
	let resizeCount = 0;
	let observer = new ResizeObserver(function(arg) {
		resizeCount++
		tlz.map.resize();

		if (resizeCount == 1) {
			// clear map data
			tlz.map.tl_clear();

			const renderMapData = function() {
				if (mapContainerElem.getAttribute("tl-onload")) {
					eval(mapContainerElem.getAttribute("tl-onload"));
				} else if (typeof tlz.map.tl_containers.get(mapContainerElem) === 'function') {
					tlz.map.tl_containers.get(mapContainerElem)();
				}
			};

			// render new data
			if (tlz.map.isStyleLoaded()) {
				renderMapData();
			} else {
				tlz.map.once('style.load', async () => {
					// // Custom atmosphere styling
					// map.setFog({
					// 	'color': 'rgb(220, 159, 159)', // Pink fog / lower atmosphere
					// 	'high-color': 'rgb(36, 92, 223)', // Blue sky / upper atmosphere
					// 	'horizon-blend': 0.4 // Exaggerate atmosphere (default is .1)
					// });
					renderMapData();
				});
			}
		}

		if (resizeCount >= 10) {
			// hopefully no need to observe anymore
			observer.disconnect();
		}
	});
	observer.observe(mapContainerElem);

	const currentPlaceholder = tlz.map._container.previousElementSibling;

	if ($('.map-placeholder', mapContainerElem)) {
		$('.map-placeholder', mapContainerElem).classList.add('d-none');
	}
	
	mapContainerElem.append($('#map') || tlz.map._container);
	
	currentPlaceholder?.classList.remove('d-none');

	// inform document listeners that the map has moved containers
	document.dispatchEvent(new CustomEvent("mapMoved", {
		detail: {
			previousElement: prevMapElem,
			currentElement: mapContainerElem
		}
	}));
}