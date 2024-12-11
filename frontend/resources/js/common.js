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





on('click', '.cancel-job', e => {
	app.CancelJobs({repo_id: tlz.openRepos[0].instance_id, job_ids: [Number(e.target.closest('.cancel-job').dataset.jobId)]});
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
function setDynamicTimestamp(elem, isoOrUnixSecTime, forDuration) {
	elem._timestamp = typeof isoOrUnixSecTime === 'number'
		? DateTime.fromSeconds(isoOrUnixSecTime)
		: DateTime.fromISO(isoOrUnixSecTime);
	elem.innerText = elem._timestamp.toRelative();
	elem.classList.add(forDuration ? "dynamic-duration" : "dynamic-time");
}

// Update the dynamic timestamps (and durations) every second to keep them accurate
setInterval(function() {
	for (elem of $$('.dynamic-time')) {
		elem.innerText = elem._timestamp.toRelative();
	}
	for (elem of $$('.dynamic-duration')) {
		// don't use diffNow() because it's implemented backwards (durations are always negative)!
		elem.innerText = betterToHuman(DateTime.now().diff(elem._timestamp));
	}
}, 1000);


// Luxon (as of v3.5.0) does not have a good toHuman() function for Duration objects.
// It naively prints all the units of the duration even if they are 0, and the default
// units used by diff() is only milliseconds, which is not human readable at all. In
// other words, Luxon's Duration.toHuman() is totally broken.
// See bug report at https://github.com/moment/luxon/issues/1134.
//
// This function wraps Luxon's toHuman() with more sensible behavior. It prints milliseconds
// only if the duration < 1s, and only prints non-zero units. It also prints whole numbers,
// not fractions, and passes opts through to toHuman(), which are the same as those available
// with the standard Intl.NumberFormat constructor (see Luxon's toHuman() docs).
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

	return Duration.fromObject(cleanedDuration).toHuman({ maximumFractionDigits: 0, opts });
}

// assignJobElements adds the .job-id-* class to the job-related
// elements within containerElem, for the given job (the 'id'
// property must be set on the job). Then it can be synced by
// log messages.
function assignJobElements(containerElem, job) {
	const jobIDClass = `job-id-${job.id}`;
	for (elem of $$(`
		.job-title,
		.job-icon,
		.job-name,
		.job-name-suffix,
		.job-link,
		.job-progress,
		.job-progress-text,
		.job-progress-text-detailed,
		.job-message,
		.job-status-indicator,
		.job-status-dot,
		.job-status,
		.job-time-basis,
		.job-time,
		.job-duration,
		.pause-job,
		.cancel-job,
		#subsequent-jobs-container,
		#parent-job-container`, containerElem)) {
		elem.classList.add(jobIDClass);
	}
	containerElem.classList.add(jobIDClass);
}





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



function renderJobPreview(containerElem, job) {
	if (!containerElem) {
		return;
	}
	const elem = cloneTemplate('#tpl-job-preview');
	assignJobElements(elem, job);
	containerElem.classList.add(`job-id-${job.id}`);
	containerElem.append(elem);
}

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
			// if this job has a parent that happens to be on the screen showing
			// previews of its children, make sure this job is rendered so it
			// can be updated
			if (l.parent_job_id != null) {
				const container = $(`#subsequent-jobs-container.job-id-${l.parent_job_id}`);
				if (container && !$(`.job-preview.job-id-${l.id}`, container)) {
					container.classList.remove('d-none');
					renderJobPreview($('#subsequent-jobs-list'), l);
				}
			}

			// update UI elements that portray this job
			jobProgressUpdate(l);

			// TODO: experimental
			updateThroughput(l);

			return;
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
	for (elem of $$(`.job-link.job-id-${job.id}`)) {
		elem.href = `/jobs/${job.repo_id}/${job.id}`;
	}

	if (job.name == "import")
	{
		for (elem of $$(`.job-title.job-id-${job.id}`)) {
			elem.innerText = "Import job";
		}
		for (elem of $$(`.job-name.job-id-${job.id}`)) {
			elem.innerText = "Import";
		}
		for (elem of $$(`.job-icon.job-id-${job.id}`)) {
			elem.innerHTML = `
				<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor"
					stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
					class="icon icon-tabler icons-tabler-outline icon-tabler-database-import">
					<path stroke="none" d="M0 0h24v24H0z" fill="none" />
					<path d="M4 6c0 1.657 3.582 3 8 3s8 -1.343 8 -3s-3.582 -3 -8 -3s-8 1.343 -8 3" />
					<path d="M4 6v6c0 1.657 3.582 3 8 3c.856 0 1.68 -.05 2.454 -.144m5.546 -2.856v-6" />
					<path d="M4 12v6c0 1.657 3.582 3 8 3c.171 0 .341 -.002 .51 -.006" />
					<path d="M19 22v-6" />
					<path d="M22 19l-3 -3l-3 3" />
				</svg>`;
		}
	}
	else if (job.name == "thumbnails")
	{
		for (elem of $$(`.job-title.job-id-${job.id}`)) {
			elem.innerText = "Generate thumbnails";
		}
		for (elem of $$(`.job-name.job-id-${job.id}`)) {
			elem.innerText = "Thumbnails";
		}
		for (elem of $$(`.job-icon.job-id-${job.id}`)) {
			elem.innerHTML = `
				<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor"
					stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
					class="icon icon-tabler icons-tabler-outline icon-tabler-photo-scan">
					<path stroke="none" d="M0 0h24v24H0z" fill="none" />
					<path d="M15 8h.01" />
					<path d="M6 13l2.644 -2.644a1.21 1.21 0 0 1 1.712 0l3.644 3.644" />
					<path d="M13 13l1.644 -1.644a1.21 1.21 0 0 1 1.712 0l1.644 1.644" />
					<path d="M4 8v-2a2 2 0 0 1 2 -2h2" />
					<path d="M4 16v2a2 2 0 0 0 2 2h2" />
					<path d="M16 4h2a2 2 0 0 1 2 2v2" />
					<path d="M16 20h2a2 2 0 0 0 2 -2v-2" />
				</svg>`;
		}
	}
	else if (job.name == "embeddings")
	{
		for (elem of $$(`.job-title.job-id-${job.id}`)) {
			elem.innerText = "Generate embeddings";
		}
		for (elem of $$(`.job-name.job-id-${job.id}`)) {
			elem.innerText = "Embeddings";
		}
		for (elem of $$(`.job-icon.job-id-${job.id}`)) {
			elem.innerHTML = `
				<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor"
					stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
					class="icon icon-tabler icons-tabler-outline icon-tabler-sparkles">
					<path stroke="none" d="M0 0h24v24H0z" fill="none" />
					<path
						d="M16 18a2 2 0 0 1 2 2a2 2 0 0 1 2 -2a2 2 0 0 1 -2 -2a2 2 0 0 1 -2 2zm0 -12a2 2 0 0 1 2 2a2 2 0 0 1 2 -2a2 2 0 0 1 -2 -2a2 2 0 0 1 -2 2zm-7 12a6 6 0 0 1 6 -6a6 6 0 0 1 -6 -6a6 6 0 0 1 -6 6a6 6 0 0 1 6 6z" />
				</svg>`;
		}
	}

	if (job.state == "started")
	{
		for (elem of $$(`.job-progress.job-id-${job.id} .progress-bar`)) {
			elem.classList.add('bg-green');
			elem.classList.remove('bg-yellow', 'bg-orange', 'bg-red', 'bg-secondary');
		}
		for (elem of $$(`.job-status-indicator.job-id-${job.id}`)) {
			elem.classList.add('status-green', 'status-indicator-animated');
		}
		for (elem of $$(`.job-status-dot.job-id-${job.id}`)) {
			elem.classList.add('status-green', 'status-dot-animated');
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
		for (elem of $$(`.job-duration:not(.dynamic-duration).job-id-${job.id}`)) {
			setDynamicTimestamp(elem, job.start, true);
		}
		
		// buttons
		for (elem of $$(`.pause-job.job-id-${job.id}`)) {
			elem.classList.remove('d-none');
			elem.dataset.jobId = job.id;
		}
		for (elem of $$(`.cancel-job.job-id-${job.id}`)) {
			elem.classList.remove('d-none');
			elem.dataset.jobId = job.id;
		}
	}
	else if (job.state == "succeeded")
	{
		for (elem of $$(`.job-progress.job-id-${job.id} .progress-bar`)) {
			elem.classList.add('bg-green');
			elem.classList.remove('bg-yellow', 'bg-orange', 'bg-red', 'bg-secondary', 'progress-bar-indeterminate');
		}
		for (elem of $$(`.job-status-indicator.job-id-${job.id}`)) {
			elem.classList.add('status-green');
			elem.classList.remove('status-indicator-animated');
		}
		for (elem of $$(`.job-status-dot.job-id-${job.id}`)) {
			elem.classList.add('status-green');
			elem.classList.remove('status-dot-animated');
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
		for (elem of $$(`.job-duration.job-id-${job.id}`)) {
			const start = typeof job.start === 'number' ? DateTime.fromSeconds(job.start) : DateTime.fromISO(job.start);
			const ended = typeof job.ended === 'number' ? DateTime.fromSeconds(job.ended) : DateTime.fromISO(job.ended);
			elem.innerText = betterToHuman(ended.diff(start));
			elem.classList.remove('dynamic-duration');
		}

		// buttons
		for (elem of $$(`.pause-job.job-id-${job.id}`)) {
			elem.classList.add('d-none');
			elem.dataset.jobId = job.id;
		}
		for (elem of $$(`.cancel-job.job-id-${job.id}`)) {
			elem.classList.add('d-none');
			elem.dataset.jobId = job.id;
		}
	}
	else if (job.state == "queued")
	{
		for (elem of $$(`.job-progress.job-id-${job.id} .progress-bar`)) {
			elem.classList.add('bg-secondary');
			elem.classList.remove('bg-green', 'bg-yellow', 'bg-orange', 'bg-red', 'progress-bar-indeterminate');
		}
		for (elem of $$(`.job-status-indicator.job-id-${job.id}`)) {
			elem.classList.add('status-secondary', 'status-indicator-animated');
		}
		for (elem of $$(`.job-status-dot.job-id-${job.id}`)) {
			elem.classList.add('status-secondary', 'status-dot-animated');
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
		for (elem of $$(`.pause-job.job-id-${job.id}`)) {
			elem.classList.add('d-none');
			elem.dataset.jobId = job.id;
		}
		for (elem of $$(`.cancel-job.job-id-${job.id}`)) {
			elem.classList.remove('d-none');
			elem.dataset.jobId = job.id;
		}
	}
	else if (job.state == "paused")
	{
		for (elem of $$(`.job-progress.job-id-${job.id} .progress-bar`)) {
			elem.classList.add('bg-yellow');
			elem.classList.remove('bg-green', 'bg-secondary', 'bg-orange', 'bg-red');
		}
		for (elem of $$(`.job-status-indicator.job-id-${job.id}`)) {
			elem.classList.add('status-yellow', 'status-indicator-animated');
			elem.classList.remove('status-green');
		}
		for (elem of $$(`.job-status-dot.job-id-${job.id}`)) {
			elem.classList.add('status-yellow', 'status-dot-animated');
			elem.classList.remove('status-green');
		}
		for (elem of $$(`.job-status.job-id-${job.id}`)) {
			elem.innerText = "Paused"
			elem.classList.add('text-yellow');
			elem.classList.remove('text-green', 'text-secondary');
		}
		for (elem of $$(`.job-time-basis.job-id-${job.id}`)) {
			elem.innerText = "Paused";
		}
		for (elem of $$(`.job-time.job-id-${job.id}`)) {
			setDynamicTimestamp(elem, job.updated);
		}

		// buttons
		for (elem of $$(`.pause-job.job-id-${job.id}`)) {
			elem.classList.add('d-none');
			elem.dataset.jobId = job.id;
		}
		for (elem of $$(`.cancel-job.job-id-${job.id}`)) {
			elem.classList.remove('d-none');
			elem.dataset.jobId = job.id;
		}
	}
	else if (job.state == "aborted")
	{
		for (elem of $$(`.job-progress.job-id-${job.id} .progress-bar`)) {
			elem.classList.add('bg-orange');
			elem.classList.remove('bg-green', 'bg-yellow', 'bg-secondary', 'bg-red', 'progress-bar-indeterminate');
		}
		for (elem of $$(`.job-status-indicator.job-id-${job.id}`)) {
			elem.classList.add('status-orange');
			elem.classList.remove('status-green', 'status-yellow', 'status-indicator-animated');
		}
		for (elem of $$(`.job-status-dot.job-id-${job.id}`)) {
			elem.classList.add('status-orange');
			elem.classList.remove('status-dot-animated');
		}
		for (elem of $$(`.job-status.job-id-${job.id}`)) {
			elem.innerText = "Aborted"
			elem.classList.add("text-orange");
			elem.classList.remove('text-green', 'text-yellow', 'text-secondary');
		}
		for (elem of $$(`.job-time-basis.job-id-${job.id}`)) {
			elem.innerText = "Ended";
		}
		for (elem of $$(`.job-time.job-id-${job.id}`)) {
			setDynamicTimestamp(elem, job.ended);
		}
		for (elem of $$(`.job-duration.job-id-${job.id}`)) {
			const start = typeof job.start === 'number' ? DateTime.fromSeconds(job.start) : DateTime.fromISO(job.start);
			const ended = typeof job.ended === 'number' ? DateTime.fromSeconds(job.ended) : DateTime.fromISO(job.ended);
			elem.innerText = betterToHuman(ended.diff(start));
			elem.classList.remove('dynamic-duration');
		}

		// buttons
		for (elem of $$(`.pause-job.job-id-${job.id}`)) {
			elem.classList.add('d-none');
			elem.dataset.jobId = job.id;
		}
		for (elem of $$(`.cancel-job.job-id-${job.id}`)) {
			elem.classList.add('d-none');
			elem.dataset.jobId = job.id;
		}
	}
	else if (job.state == "failed")
	{
		for (elem of $$(`.job-progress.job-id-${job.id} .progress-bar`)) {
			elem.classList.add('bg-red');
			elem.classList.remove('bg-green', 'bg-yellow', 'bg-orange', 'bg-secondary', 'progress-bar-indeterminate');
		}
		for (elem of $$(`.job-status-indicator.job-id-${job.id}`)) {
			elem.classList.add('status-red');
			elem.classList.remove('status-green', 'status-yellow', 'status-indicator-animated');
		}
		for (elem of $$(`.job-status-dot.job-id-${job.id}`)) {
			elem.classList.add('status-red');
			elem.classList.remove('status-green', 'status-dot-animated');
		}
		for (elem of $$(`.job-status.job-id-${job.id}`)) {
			elem.innerText = "Failed"
			elem.classList.add('text-red');
			elem.classList.remove('text-green', 'text-yellow', 'text-secondary');
		}
		for (elem of $$(`.job-time-basis.job-id-${job.id}`)) {
			elem.innerText = "Ended";
		}
		for (elem of $$(`.job-time.job-id-${job.id}`)) {
			setDynamicTimestamp(elem, job.ended);
		}
		for (elem of $$(`.job-duration.job-id-${job.id}`)) {
			const start = typeof job.start === 'number' ? DateTime.fromSeconds(job.start) : DateTime.fromISO(job.start);
			const ended = typeof job.ended === 'number' ? DateTime.fromSeconds(job.ended) : DateTime.fromISO(job.ended);
			elem.innerText = betterToHuman(ended.diff(start));
			elem.classList.remove('dynamic-duration');
		}

		// buttons
		for (elem of $$(`.pause-job.job-id-${job.id}`)) {
			elem.classList.add('d-none');
			elem.dataset.jobId = job.id;
		}
		for (elem of $$(`.cancel-job.job-id-${job.id}`)) {
			elem.classList.add('d-none');
			elem.dataset.jobId = job.id;
		}
	}

	// progress bars (other than color, which is done above based on state)
	if (job.total == null) {
		// indeterminate maximum; but if job is successful, just max out the progress bar
		if (job.state == "succeeded") {
			for (elem of $$(`.job-progress.job-id-${job.id} .progress-bar`)) {
				elem.style.width = "100%";
			}
		} else if (job.state) {
			for (elem of $$(`.job-progress.job-id-${job.id} .progress-bar`)) {
				elem.style.width = "0%";
				if (job.state == "started") {
					elem.classList.add('progress-bar-indeterminate');
				}
			}
		}
		if (job.progress != null) {
			for (elem of $$(`.job-progress-text.job-id-${job.id}`)) {
				elem.innerText = job.progress.toLocaleString();
			}
		}
		for (elem of $$(`.job-progress-text-detailed.job-id-${job.id}`)) {
			if (job.progress == null) {
				elem.innerText = "0";
			} else {
				const total = job.state == "succeeded" ? job.progress.toLocaleString() : "?";
				elem.innerText = `${job.progress.toLocaleString()}/${total}`;
			}
		}
	}
	if (job.total > 0) {
		// known maximum; show progress
		const percent = (job.progress || 0)/job.total * 100;
		for (elem of $$(`.job-progress.job-id-${job.id} .progress-bar`)) {
			elem.style.width = `${percent}%`;
			elem.classList.remove('progress-bar-indeterminate');
		}
		const percentDisplay = `${percent.toFixed(2).replace(".00", "")}%`;
		for (elem of $$(`.job-progress-text.job-id-${job.id}`)) {
			elem.innerText = percentDisplay;
		}
		for (elem of $$(`.job-progress-text-detailed.job-id-${job.id}`)) {
			const progress = job.progress || 0;
			elem.innerText = `${progress.toLocaleString()}/${job.total.toLocaleString()}`;
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

