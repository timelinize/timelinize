// Commmon JS code for the whole application
































// Toggle all checkboxes in dropdown list
on('click', '.dropdown-menu .select-all, .dropdown-menu .select-none', (e) => {
	const menu = e.target.closest('.dropdown-menu');
	for (const checkbox of $$('input[type=checkbox]', menu)) {
		checkbox.checked = e.target.classList.contains('select-all');
	}
	menu.dispatchEvent(new Event('change', { bubbles: true }));
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
	for (var i = 0; i < entity.attributes.length; i++) {
		if (entity.attributes[i].name == attribute) {
			return entity.attributes[i].value;
		}
	}
	return "";
}

async function updateActiveJobs() {
	if (!$('#active-jobs')) return;

	const jobs = await app.ActiveJobs();
	
	$('#active-jobs').replaceChildren();
	
	for (let i = 0; i < jobs.length; i++) {
		const job = jobs[i];
		let elem = cloneTemplate('#tpl-active-job');
		elem.id = `active-job-${job.id}`;
		if (job.type == "import") {
			$('.data-source-title', elem).innerText = tlz.dataSources[job.import_parameters.data_source_name].title;
			$('.active-job-input', elem).innerText = job.import_parameters.filenames[0]; // TODO: support multiple I guess
		}
		$('.cancel-active-job', elem).dataset.jobid = job.id;
		$('.data-source-icon', elem).style.backgroundImage = `url("/resources/images/data-sources/${tlz.dataSources[job.import_parameters.data_source_name].icon}")`;

		elem.dataset.started = job.started;
		$('.import-duration', elem).innerText = DateTime.fromISO(job.started).diffNow().toHuman();

		$('#active-jobs').append(elem);
	}
	
	$('#notifications-link .badge')?.remove();
	if (jobs.length > 0) {
		$('#notifications-link').innerHTML += `<span class="badge bg-red badge-blink"></span>`;
	}
}

async function cancelJob(jobID) {
	await app.CancelJob(jobID);
	updateActiveJobs();
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


// updateFilterResults runs the page's render function again to replace .filter-results
// with the latest parameters in the query string.
function updateFilterResults() {
	// fade out current results
	$$('.filter-results:not(.d-none)').forEach(elem => elem.classList.add('opacity0'));

	// if the results take a while to load, show a loading indicator
	let slowLoadingHandle = setTimeout(function() {
		const span = document.createElement('span');
		span.classList.add('slow-loader');
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
		}, 25);
		
		// hide any loading indicator
		clearTimeout(slowLoadingHandle);
		$('.slow-loader')?.remove();
	}, 250);
}

// when filter inputs change, update query string and re-render page
// TODO: update to do server-side rendering...
on('change',
	`.filter-input:not(.nonfilter),
	.filter input:not(.nonfilter),
	.filter select:not(.nonfilter),
	.filter .dropdown-menu:not(.nonfilter)`, event => {

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





on('click', '.cancel-active-job', e => {
	const jobid = e.target.dataset.jobid;
	cancelJob(jobid);
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


// Dynamic timestamps which update as much as every second to always show a correct
// relative time on the screen. Pass in the element to put the relative text in
// and the timestamp string from a JSON object.
function setDynamicTimestamp(elem, isoOrUnixSecTime) {
	elem._timestamp = typeof isoOrUnixSecTime === 'number'
		? DateTime.fromSeconds(isoOrUnixSecTime)
		: DateTime.fromISO(isoOrUnixSecTime);
	elem.innerText = elem._timestamp.toRelative();
	elem.classList.add("dynamic-time");
}

// Update the dynamic timestamps every second to keep them accurate
setInterval(function() {
	for (elem of $$('.dynamic-time')) {
		elem.innerText = elem._timestamp.toRelative();
	}
}, 1000);









//////////////////////////////////////////////////////
// Events handling (logs)
//////////////////////////////////////////////////////

// runtime.EventsOn("log", (entryJSON) => {
// 	const l = JSON.parse(entryJSON);

// 	// for now, we don't care about HTTP access logs
// 	if (l.logger == "app.http") {
// 		return;
// 	}

// 	if (l.logger == "processor.progress") {
// 		const jobElem = $(`#active-job-${l.job_id}`);
// 		if (!jobElem) return;
// 		$('.import-item-count', jobElem).innerText = `${l.total_items.toLocaleString()} items`;
// 		$('.import-duration', jobElem).innerText = $('.import-duration').innerText = DateTime.now().diff(DateTime.fromISO(jobElem.dataset.started)).toFormat("h 'h' m 'min' s 'sec'");
// 		return;
// 	}

// 	if (l.logger == "job_manager" && l.msg == 'end') {
// 		updateActiveJobs();
// 	}
	
// 	console.log("LOG:", l);
// });




function connectLog() {
	logSocket = new WebSocket(`ws://${window.location.host}/api/logs`);
	logSocket.onmessage = function(event) {
		const l = JSON.parse(event.data);
		console.log("LOG:", l);

		// for now, we don't care about HTTP access logs
		if (l.logger == "app.http") {
			return;
		}

		if (l.logger == "job.status") {
			jobProgressUpdate(l);
			return;
		}
	
		if (l.logger == "job_manager" && l.msg == 'end') {
			updateActiveJobs();
		}
	};
	logSocket.onclose = function(event) {
		console.error("Lost connection to logger socket:", event);
		// TODO: put UI into frozen state
		// connect(false);
	}
}

connectLog();


function jobProgressUpdate(job) {
	if (job.name == "import") {
		for (elem of $$(`.job-name.job-id-${job.id}`)) {
			elem.innerText = "Import job";
		}
	} else if (job.name == "thumbnails") {
		for (elem of $$(`.job-name.job-id-${job.id}`)) {
			elem.innerText = "Generate thumbnails";
		}
	} else if (job.name == "embeddings") {
		for (elem of $$(`.job-name.job-id-${job.id}`)) {
			elem.innerText = "Generate embeddings";
		}
	}

	if (job.state == "started")
	{
		for (elem of $$(`.job-status-indicator.job-id-${job.id}`)) {
			elem.classList.add('status-green', 'status-indicator-animated');
		}
		for (elem of $$(`.job-status.job-id-${job.id}`)) {
			elem.innerText = "Running";
			elem.classList.add("text-green");
		}
		for (elem of $$(`.job-time-basis.job-id-${job.id}`)) {
			elem.innerText = "Started";
		}
		for (elem of $$(`.job-time.job-id-${job.id}`)) {
			setDynamicTimestamp(elem, job.start);
		}

		// buttons
		$('.pause-job')?.classList?.remove("d-none");
		$('.cancel-job')?.classList?.remove("d-none");
	}
	else if (job.state == "succeeded")
	{
		for (elem of $$(`.job-status-indicator.job-id-${job.id}`)) {
			elem.classList.add('status-green');
			elem.classList.remove('status-indicator-animated');
		}
		for (elem of $$(`.job-status.job-id-${job.id}`)) {
			elem.innerText = "Completed"
			elem.classList.add("text-green");
		}
		for (elem of $$(`.job-time-basis.job-id-${job.id}`)) {
			elem.innerText = "Finished";
		}
		for (elem of $$(`.job-time.job-id-${job.id}`)) {
			setDynamicTimestamp(elem, job.ended);
		}

		// buttons
		$('.pause-job')?.classList?.add("d-none");
		$('.cancel-job')?.classList?.add("d-none");
	}
	else if (job.state == "queued")
	{
		for (elem of $$(`.job-status-indicator.job-id-${job.id}`)) {
			elem.classList.add('status-secondary', 'status-indicator-animated');
		}
		for (elem of $$(`.job-status.job-id-${job.id}`)) {
			elem.innerText = "Queued"
			elem.classList.add("text-secondary");
		}
		for (elem of $$(`.job-time-basis.job-id-${job.id}`)) {
			if (job.start) {
				elem.innerText = "Starting";
			} else {
				elem.innerText = "Created";
			}
		}
		for (elem of $$(`.job-time.job-id-${job.id}`)) {
			if (job.start) {
				setDynamicTimestamp(elem, job.start);
			} else {
				setDynamicTimestamp(elem, job.created);
			}
		}

		// buttons
		$('.pause-job')?.classList?.add("d-none");
		$('.cancel-job')?.classList?.remove("d-none");
	}
	else if (job.state == "paused")
	{
		for (elem of $$(`.job-status-indicator.job-id-${job.id}`)) {
			elem.classList.add('status-yellow', 'status-indicator-animated');
		}
		for (elem of $$(`.job-status.job-id-${job.id}`)) {
			elem.innerText = "Paused"
			elem.classList.add("text-yellow");
		}
		for (elem of $$(`.job-time-basis.job-id-${job.id}`)) {
			elem.innerText = "Paused";
		}
		for (elem of $$(`.job-time.job-id-${job.id}`)) {
			setDynamicTimestamp(elem, job.updated);
		}

		// buttons
		$('.pause-job')?.classList?.add("d-none");
		$('.cancel-job')?.classList?.remove("d-none");
	}
	else if (job.state == "aborted")
	{
		for (elem of $$(`.job-status-indicator.job-id-${job.id}`)) {
			elem.classList.add('status-orange');
			elem.classList.remove('status-indicator-animated');
		}
		for (elem of $$(`.job-status.job-id-${job.id}`)) {
			elem.innerText = "Aborted"
			elem.classList.add("text-orange");
		}
		for (elem of $$(`.job-time-basis.job-id-${job.id}`)) {
			elem.innerText = "Ended";
		}
		for (elem of $$(`.job-time.job-id-${job.id}`)) {
			setDynamicTimestamp(elem, job.ended);
		}

		// buttons
		$('.pause-job')?.classList?.add("d-none");
		$('.cancel-job')?.classList?.add("d-none");
	}
	else if (job.state == "failed")
	{
		for (elem of $$(`.job-status-indicator.job-id-${job.id}`)) {
			elem.classList.add('status-red');
			elem.classList.remove('status-indicator-animated');
		}
		for (elem of $$(`.job-status.job-id-${job.id}`)) {
			elem.innerText = "Failed"
			elem.classList.add("text-red");
		}
		for (elem of $$(`.job-time-basis.job-id-${job.id}`)) {
			elem.innerText = "Ended";
		}
		for (elem of $$(`.job-time.job-id-${job.id}`)) {
			setDynamicTimestamp(elem, job.ended);
		}

		// buttons
		$('.pause-job')?.classList?.add("d-none");
		$('.cancel-job')?.classList?.add("d-none");
	}

	// progress bars
	if (job.total == null) {
		// indeterminate maximum; but if job is successful, just max out the progress bar
		if (job.state == "succeeded") {
			for (elem of $$(`.job-progress.job-id-${job.id} .progress-bar`)) {
				elem.style.width = "100%";
			}
		} else if (job.state) {
			for (elem of $$(`.job-progress.job-id-${job.id} .progress-bar`)) {
				elem.style.width = "0%";
				if (job.state == "queued") {
					elem.classList.remove('bg-green');
					elem.classList.add('bg-secondary');
				} else if (job.state == "started") {
					elem.classList.remove('bg-secondary');
					elem.classList.add('bg-green');
					elem.classList.add('progress-bar-indeterminate');
				}
			}
		}
		if (job.progress != null) {
			for (elem of $$(`.job-progress-text.job-id-${job.id}`)) {
				elem.innerText = job.progress;
			}
		}
	} else {
		// known maximum; show progress
		let percent = 100;
		if (job.total > 0) {
			percent = (job.progress || 0)/job.total * 100;
		}
		for (elem of $$(`.job-progress.job-id-${job.id} .progress-bar`)) {
			elem.style.width = `${percent}%`;
			elem.classList.remove('progress-bar-indeterminate');
		}
		const percentDisplay = `${percent.toFixed(2).replace(".00", "")}%`;
		for (elem of $$(`.job-progress-text.job-id-${job.id}`)) {
			elem.innerText = percentDisplay;
		}
	}

	// message
	for (elem of $$(`.job-message.job-id-${job.id}`)) {
		elem.innerText = job.message || "";
	}
}






















// This intersection observer is intended for map placeholder elements only.
const mapIntersectionObs = new IntersectionObserver((entries, opts) => {
	entries.forEach(entry => {
		if (entry.isIntersecting) {

			// when the map is rendered to the page, make sure it resizes properly, then render this map's data
			// See https://stackoverflow.com/a/66172042/1048862 (several answers exist, most are kind of hacky)
			var observer = new ResizeObserver(function(arg) {
				tlz.map.resize();

				// clear map data
				tlz.map.tl_clear();

				const renderMapData = function() {
					if (entry.target.getAttribute("tl-onload")) {
						eval(entry.target.getAttribute("tl-onload"));
					} else if (typeof tlz.map.tl_containers.get(entry.target) === 'function') {
						tlz.map.tl_containers.get(entry.target)();
					}
				};

				// render new data
				if (tlz.map.tl_isLoaded) {
					renderMapData();
				} else {
					tlz.map.on('load', async () => {
						// // Custom atmosphere styling
						// map.setFog({
						// 	'color': 'rgb(220, 159, 159)', // Pink fog / lower atmosphere
						// 	'high-color': 'rgb(36, 92, 223)', // Blue sky / upper atmosphere
						// 	'horizon-blend': 0.4 // Exaggerate atmosphere (default is .1)
						// });
						renderMapData();
					});
				}

				// we're done, so no need to observe anymore
				observer.disconnect();
			});
			observer.observe(entry.target);

			const currentPlaceholder = tlz.map._container.previousElementSibling;

			if ($('.map-placeholder', entry.target)) {
				$('.map-placeholder', entry.target).classList.add('d-none');
			}
			entry.target.append($('#map') || tlz.map._container);

			currentPlaceholder?.classList.remove('d-none');

		} else {
			// TODO: anything?
		}
	});
}, {
	root: null, // default is viewport
	rootMargin: '-40% 0% -40% 0%', // center of viewport
	threshold: 0 // percentage of element that intersects; triggers callback; must be zero for rootMargin
});

