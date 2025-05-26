// TODO: Presumably, these handlers will need to be generalized if we ever want to reuse our filepicker modal
on('show.bs.modal', '#modal-file-picker', async event => {
	const filePicker = await newFilePicker("import");
	filePicker.classList.add('fw-normal');
	
	const container = $('.modal-body', event.target);
	container.innerHTML = '';
	container.append(filePicker);
});

on('selection', '#modal-file-picker .file-picker', event => {
	if (event.target.selected().length == 1) {
		$('#select-file-picker').classList.remove('disabled');
	} else {
		$('#select-file-picker').classList.add('disabled');
	}
});

on('change', '#recursive', event => {
	if (event.target.checked) {
		$('#traverse-archives').removeAttribute('disabled');
	} else {
		$('#traverse-archives').setAttribute('disabled', true);
		$('#traverse-archives').checked = false;
	}
});

// Show respective DS options modal; for some reason, we can't use data attributes
// like normal, because the button is a child (inside) of the accordion button,
// so clicking the dsopt button would also toggle the accordion, EVEN WHEN CALLING
// STOPPROPAGATION IN AN EVENT HANDLER ON THE DSOPT BUTTON. WHY!? Anyway...
// by disabling the data-bs-* voodoo on these buttons in the HTML, we can then open
// the modal manually.
on('click', '.import-dsgroup-opt-button', event => {
	const dsoptContainer = event.target.closest('.file-import-plan-dsgroup');
	bootstrap.Modal.getOrCreateInstance($(`#modal-import-dsopt-${dsoptContainer.ds.name}`)).show();
});
on('click', '.import-dsgroup-remove-button', event => {
	const dsgroup = event.target.closest('.file-import-plan-dsgroup');
	removeDsGroup(dsgroup);
});

// remove row and update UI elements
on('click', '.dsgroup-remove-row', event => {
	const row = event.target.closest('tr');
	const dsgroup = event.target.closest('.file-import-plan-dsgroup');

	const file = row.fileInfo;
	const fileIndex = dsgroup.filenames.indexOf(file.filename);
	if (fileIndex > -1) {
		dsgroup.filenames.splice(fileIndex, 1);
	}
	dsgroup.fileCounts[file.file_type]--;

	row.remove();
	
	// remove entire DS group (and its DS options modal, if any) if no files left
	if (dsgroup.filenames.length == 0) {
		removeDsGroup(dsgroup);
	} else {
		updateFileCountDisplays(dsgroup);
	}
});

function removeDsGroup(dsgroupElem) {
	dsgroupElem.remove();
	$(`#modal-import-dsopt-${dsgroupElem.ds.name}`)?.remove();

	updateExpandCollapseAll();

	// disable button to start import if no DS groups left
	if (!$('.file-import-plan-dsgroup')) {
		$('#start-import').classList.add('disabled');
	}
}

// TODO: when loader modal is dismissed, either by keyboard or a deliberate event, cancel the request

on('shown.bs.modal', '#modal-plan-loading', async event => {
	const selectedFiles = $('#modal-file-picker .file-picker').selected();
	if (selectedFiles.length != 1) {
		return;
	}

	const plan = await app.PlanImport({
		path: $('#modal-file-picker .file-picker').selected()[0],
		hidden: $('.file-picker-hidden-files').checked,
		recursive: $('#recursive').checked,
		traverse_archives: $('#traverse-archives').checked
	});

	const planLoadingModal = bootstrap.Modal.getInstance('#modal-plan-loading');
	planLoadingModal.hide();

	console.log("IMPORT PLAN:", plan);

	if (!plan || !plan.files) {
		// TODO: show modal saying that nothing was found
		return;
	}

	for (const file of plan.files) {
		// one drawback of our current UI is we only make available the first data source that matches
		// (but so far, it is rare for multiple to match, especially with near-equivalent confidence, I think)
		const recognition = file.data_sources[0];
		const ds = recognition.data_source;
		let dsGroupElem = $(`.file-import-plan-dsgroup.ds-${ds.name}`);


		// if the data source doesn't have a group element yet, create one
		if (!dsGroupElem) {
			dsGroupElem = cloneTemplate('#tpl-file-import-plan-dsgroup');
			dsGroupElem.classList.add(`ds-${ds.name}`);
			dsGroupElem.ds = ds;
			dsGroupElem.fileCounts = {'file': 0, 'dir': 0, 'archive': 0};
			dsGroupElem.filenames = [];
			$('.dsgroup-icon', dsGroupElem).style.backgroundImage = `url('/ds-image/${ds.name}')`;
			$('.dsgroup-name', dsGroupElem).innerText = ds.title;

			// each file listing in a DS group is a uniquely-ID'ed collapsible region
			const collapseID = `import-dsgroup-collapse-${tlz.collapseCounter}`;
			$('.collapse', dsGroupElem).id = collapseID;
			$('.accordion-button', dsGroupElem).dataset.bsTarget = "#"+collapseID;
			tlz.collapseCounter++;

			// render the DS group and, if it has options, its options modal (otherwise remove the Options button)
			$('#file-imports-container').append(dsGroupElem);
			if ($(`#tpl-dsopt-${ds.name}`)) {
				renderDataSourceOptionsModal(ds);
			} else {
				$('.import-dsgroup-opt-button', dsGroupElem).remove();
			}
		}

		// don't add duplicate filenames
		if (dsGroupElem.filenames.includes(file.filename)) {
			continue;
		}

		// add this file to the file list in the DS group

		const row = cloneTemplate('#tpl-file-import-dsgroup-row');

		row.fileInfo = file;
		$('.sort-type', row).dataset.type = file.file_type;
		$('.sort-filename', row).innerText = file.filename;
		$('.sort-confidence', row).dataset.confidence = recognition.confidence;
		$('.sort-confidence', row).innerText = `${(recognition.confidence*100).toFixed(0)}% match`;
		if (recognition.confidence >= 0.90) {
			$('.sort-confidence', row).classList.add("text-green");
		} else if (recognition.confidence >= 0.75) {
			$('.sort-confidence', row).classList.add("text-lime");
		} else if (recognition.confidence > 0.50) {
			$('.sort-confidence', row).classList.add("text-yellow");
		} else {
			$('.sort-confidence', row).classList.add("text-orange");
		}


		let label, icon;
		if (file.file_type == "file") {
			label = "File";
			icon = `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor"
					stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
					class="icon icon-tabler icons-tabler-outline icon-tabler-file">
					<path stroke="none" d="M0 0h24v24H0z" fill="none" />
					<path d="M14 3v4a1 1 0 0 0 1 1h4" />
					<path d="M17 21h-10a2 2 0 0 1 -2 -2v-14a2 2 0 0 1 2 -2h7l5 5v11a2 2 0 0 1 -2 2z" />
				</svg>`;
		} else if (file.file_type == "dir") {
			label = "Directory";
			icon = `<svg xmlns="http://www.w3.org/2000/svg" width="48" height="48" viewBox="0 0 24 24" fill="currentColor"
					class="icon icon-tabler icons-tabler-filled icon-tabler-folder">
					<path stroke="none" d="M0 0h24v24H0z" fill="none" />
					<path d="M9 3a1 1 0 0 1 .608 .206l.1 .087l2.706 2.707h6.586a3 3 0 0 1 2.995 2.824l.005 .176v8a3 3 0 0 1 -2.824 2.995l-.176 .005h-14a3 3 0 0 1 -2.995 -2.824l-.005 -.176v-11a3 3 0 0 1 2.824 -2.995l.176 -.005h4z" />
				</svg>`;
		} else if (file.file_type == "archive") {
			label = "Archive";
			icon = `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor"
						stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
						class="icon icon-tabler icons-tabler-outline icon-tabler-file-zip">
						<path stroke="none" d="M0 0h24v24H0z" fill="none" />
						<path d="M6 20.735a2 2 0 0 1 -1 -1.735v-14a2 2 0 0 1 2 -2h7l5 5v11a2 2 0 0 1 -2 2h-1" />
						<path d="M11 17a2 2 0 0 1 2 2v2a1 1 0 0 1 -1 1h-2a1 1 0 0 1 -1 -1v-2a2 2 0 0 1 2 -2z" />
						<path d="M11 5l-1 0" />
						<path d="M13 7l-1 0" />
						<path d="M11 9l-1 0" />
						<path d="M13 11l-1 0" />
						<path d="M11 13l-1 0" />
						<path d="M13 15l-1 0" />
					</svg>`;
		}
		$('.avatar', row).title = label;
		$('.avatar', row).innerHTML = icon;

		dsGroupElem.fileCounts[file.file_type]++;

		dsGroupElem.filenames.push(file.filename);
		$('.import-dsgroup-files tbody', dsGroupElem).append(row);
	}

	// once all the tables are rendered, make them sortable
	for (var elem of $$('.dsgroup-file-list')) {
		if (elem.listjs) {
			elem.listjs.reIndex();
		} else {
			elem.listjs = new List(elem, {
				listClass: 'table-tbody',
				sortClass: 'table-sort',
				valueNames: [
					'sort-filename',
					{ name: 'sort-type',       attr: 'data-type' },
					{ name: 'sort-confidence', attr: 'data-confidence' }
				]
			});
		}
	}

	// update UI elements
	for (const dsgroup of $$('.file-import-plan-dsgroup')) {
		updateFileCountDisplays(dsgroup);
	}
	updateExpandCollapseAll();
});

async function renderDataSourceOptionsModal(ds) {
	// start with creating (or getting?) the modal and setting it up with the DS info
	let dsOptModal = $(`#modal-import-dsopt-${ds.name}`);
	if (!dsOptModal) {
		dsOptModal = cloneTemplate('#tpl-modal-import-dsopt');
		dsOptModal.id = `modal-import-dsopt-${ds.name}`;
	}
	$('.avatar', dsOptModal).style.backgroundImage = `url('/ds-image/${ds.name}')`;
	$('.modal-title', dsOptModal).append(document.createTextNode(ds.title));

	const dsOptElem = cloneTemplate(`#tpl-dsopt-${ds.name}`);
	
	// render the data source options template, then the modal to the DOM
	$('.modal-body', dsOptModal).replaceChildren(dsOptElem);
	$('#page-content').append(dsOptModal);

	// these can't be set up until after they're displayed
	if (ds.name == "calendar") {
		const entitySelect = newEntitySelect($('.calendar-owner', dsOptElem), 1);
		const owner = await getOwner(tlz.openRepos[0]);
		entitySelect.addOption(owner);
		entitySelect.addItem(owner.id);
	}
	if (ds.name == "google_location") {
		const entitySelect = newEntitySelect($('.google_location-owner', dsOptElem), 1);
		const owner = await getOwner(tlz.openRepos[0]);
		entitySelect.addOption(owner);
		entitySelect.addItem(owner.id);

		noUiSlider.create($('.google_location-simplification', dsOptElem), {
			start: 2,
			connect: [true, false],
			step: 0.1,
			range: {
				min: 0,
				max: 10
			}
		});
	}
	if (ds.name == "gpx") {
		const entitySelect = newEntitySelect($('.gpx-owner', dsOptElem), 1);
		const owner = await getOwner(tlz.openRepos[0]);
		entitySelect.addOption(owner);
		entitySelect.addItem(owner.id);

		noUiSlider.create($('.gpx-simplification', dsOptElem), {
			start: 2,
			connect: [true, false],
			step: 0.1,
			range: {
				min: 0,
				max: 10
			}
		});
	}
	if (ds.name == "geojson") {
		const entitySelect = newEntitySelect($('.geojson-owner', dsOptElem), 1);
		const owner = await getOwner(tlz.openRepos[0]);
		entitySelect.addOption(owner);
		entitySelect.addItem(owner.id);
	}
	if (ds.name == "email") {
		new TomSelect($(".email-skip-labels", dsOptElem),{
			persist: false,
			create: true,
			createOnBlur: true
		});
	}
	if (ds.name == "icloud") {
		const entitySelect = newEntitySelect($('.icloud-owner', dsOptElem), 1);
		const owner = await getOwner(tlz.openRepos[0]);
		entitySelect.addOption(owner);
		entitySelect.addItem(owner.id);
	}
	if (ds.name == "media") {
		const entitySelect = newEntitySelect($('.media-owner', dsOptElem), 1);
		const owner = await getOwner(tlz.openRepos[0]);
		entitySelect.addOption(owner);
		entitySelect.addItem(owner.id);
	}
	if (ds.name == "apple_photos") {
		// This data source can sometimes detect its owner, so it's not required for us to assume!
		newEntitySelect($('.apple_photos-owner', dsOptElem), 1);
	}
}

function updateFileCountDisplays(dsgroupElem) {
	$('.dsgroup-file-count', dsgroupElem).innerText = dsgroupElem.fileCounts['file'];
	$('.dsgroup-dir-count', dsgroupElem).innerText = dsgroupElem.fileCounts['dir'];
	$('.dsgroup-archive-count', dsgroupElem).innerText = dsgroupElem.fileCounts['archive'];
}

function updateExpandCollapseAll() {
	// toggle expand/collapse all buttons
	if ($('.file-import-plan-dsgroup')) {
		$('#expand-all-dsgroup').classList.remove('disabled');
		$('#collapse-all-dsgroup').classList.remove('disabled');
		$('#start-import').classList.remove('disabled');
	} else {
		$('#expand-all-dsgroup').classList.add('disabled');
		$('#collapse-all-dsgroup').classList.add('disabled');
		$('#start-import').classList.add('disabled');
	}
}


on('click', '#expand-all-dsgroup', event => {
	for (const elem of $$('.file-import-plan-dsgroup .accordion-button.collapsed')) {
		elem.classList.remove('collapsed');
	}
	for (const elem of $$('.file-import-plan-dsgroup .collapse')) {
		elem.classList.add('show');
	}
});

on('click', '#collapse-all-dsgroup', event => {
	for (const elem of $$('.file-import-plan-dsgroup .accordion-button:not(.collapsed)')) {
		elem.classList.add('collapsed');
	}
	for (const elem of $$('.file-import-plan-dsgroup .collapse.show')) {
		elem.classList.remove('show');
	}
});

// begin import!
on('click', '#start-import', async event => {
	// TODO: validate input (data source options, etc) -- show modal of DS options that need fixing

	const repoID = tlz.openRepos[0].instance_id;

	const importParams = {
		repo: repoID,
		job: {
			plan: {
				files: []
			},
			processing_options: {
				integrity: $('#integrity-checks').checked,
				overwrite_local_changes: $('#overwrite-local-changes').checked,
				item_unique_constraints: page.itemUniqueConstraints,
				interactive: $('#interactive').checked ? {} : null
			},
			estimate_total: $('#estimate-total').checked
		}
	};

	// collect item update preferences
	if (page.itemUpdatePrefs?.length) {
		importParams.job.processing_options.item_update_preferences = page.itemUpdatePrefs;
	}

	// collect data source options - if there are any input validation errors, show an alert and redirect to that dsOpt modal after it
	for (const dsgroup of $$('.file-import-plan-dsgroup')) {
		try {
			importParams.job.plan.files.push({
				data_source_name: dsgroup.ds.name,
				data_source_options: await dataSourceOptions(dsgroup.ds),
				filenames: dsgroup.filenames
			});
		} catch (err) {
			// TODO: It might be good to show the data source name and icon in the modal?
			console.log("Data source options error:", err)
			$('#modal-error .modal-error-dismiss').dataset.bsTarget = `#modal-import-dsopt-${dsgroup.ds.name}`;
			$('#modal-error .modal-error-dismiss').dataset.bsToggle = 'modal';
			$('#modal-error .modal-error-title').innerText = err.title;
			$('#modal-error .modal-error-message').innerText = err.message;
			const errorModal = new bootstrap.Modal('#modal-error');
			errorModal.show();
			return;
		}
	}

	console.log("IMPORT PARAMS:", importParams)

	const result = await app.Import(importParams);
	console.log("JOB STARTED:", result)

	notify({
		type: "success",
		title: "Import queued",
		duration: 2000
	});

	if (importParams.job.processing_options.interactive) {
		// take user to page where they can begin their interactive import
		navigateSPA(`/input?repo_id=${repoID}&job_id=${result.job_id}`, true);
	} else {
		// otherwise, redirect to job status, I guess
		navigateSPA(`/jobs/${repoID}/${result.job_id}`, true);
	}
});

async function dataSourceOptions(ds) {
	dsoptContainer = $(`#modal-import-dsopt-${ds.name}`);
	if (!dsoptContainer) {
		return;
	}

	let dsOpt;

	if (ds.name == "calendar") {
		dsOpt = {};
		const owner = $('.calendar-owner', dsoptContainer).tomselect.getValue();
		if (owner.length) {
			dsOpt.owner_entity_id = Number(owner[0]);
		}
	}
	if (ds.name == "sms_backup_restore") {
		const ownerPhoneInput = $('.sms_backup_restore-owner-phone', dsoptContainer);
		if (ownerPhoneInput.value) {
			dsOpt = {
				owner_phone_number: ownerPhoneInput.value
			};
		} else {
			// this DS requires we input the phone number of the phone that created the data,
			// so if the input field was left empty, ensure the repo owner has a phone number
			const owner = await getOwner(tlz.openRepos[0]);
			if (getEntityAttribute(owner, 'phone_number').length == 0) {
				ownerPhoneInput.classList.add('is-invalid');
				ownerPhoneInput.classList.remove('is-valid');
				throw {
					elem: ownerPhoneInput,
					title: "Phone number required",
					message: "The data source doesn't provide a phone number by itself. When no phone number is entered here as part of the data source options, we use the phone number of the timeline owner, but no phone number is known for the timeline owner. Please enter a phone number."
				}
			}
			return;
		}
	}
	if (ds.name == "google_location") {
		dsOpt = {};
		const owner = $('.google_location-owner', dsoptContainer).tomselect.getValue();
		if (owner.length) {
			dsOpt.owner_entity_id = Number(owner[0]);
		}
		const simplification = $('.google_location-simplification', dsoptContainer).noUiSlider.get();
		if (simplification) {
			dsOpt.simplification = Number(simplification);
		}
	}
	if (ds.name == "gpx") {
		dsOpt = {};
		const owner = $('.gpx-owner', dsoptContainer).tomselect.getValue();
		if (owner.length) {
			dsOpt.owner_entity_id = Number(owner[0]);
		}
		const simplification = $('.gpx-simplification', dsoptContainer).noUiSlider.get();
		if (simplification) {
			dsOpt.simplification = Number(simplification);
		}
	}
	if (ds.name == "geojson") {
		dsOpt = {};
		const owner = $('.geojson-owner', dsoptContainer).tomselect.getValue();
		if (owner.length) {
			dsOpt.owner_entity_id = Number(owner[0]);
		}
		dsOpt.lenient = $('.geojson-lenient', dsoptContainer).checked;
	}
	if (ds.name == "media") {
		dsOpt = {
			use_filepath_time: $('.media-use-file-path-time', dsoptContainer).checked,
			use_file_mod_time: $('.media-use-file-mod-time', dsoptContainer).checked,
			folder_is_album: $('.media-folder-is-album', dsoptContainer).checked,
			date_range: {}
		};
		const owner = $('.media-owner', dsoptContainer).tomselect.getValue();
		if (owner.length) {
			dsOpt.owner_entity_id = Number(owner[0]);
		}
		if ($('.media-start-year', dsoptContainer).value) {
			dsOpt.date_range.since = DateTime.utc(Number($('.media-start-year', dsoptContainer).value)).toISO();
		}
		if ($('.media-end-year', dsoptContainer).value) {
			dsOpt.date_range.until = DateTime.utc(Number($('.media-end-year', dsoptContainer).value)).endOf('year').toISO();
		}
	}
	if (ds.name == "email") {
		const skipLabels = $('.email-skip-labels', dsoptContainer).tomselect.getValue().split(",");
		dsOpt = {
			gmail_skip_labels: skipLabels
		};
	}
	if (ds.name == "icloud") {
		dsOpt = {
			recently_deleted: $('.icloud-recently-deleted', dsoptContainer).checked
		};
		const owner = $('.icloud-owner', dsoptContainer).tomselect.getValue();
		if (owner.length) {
			dsOpt.owner_entity_id = Number(owner[0]);
		}
	}
	if (ds.name == "apple_photos") {
		dsOpt = {
			include_trashed: $('.apple_photos-trashed', dsoptContainer).checked
		};
		const owner = $('.apple_photos-owner', dsoptContainer).tomselect.getValue();
		if (owner.length) {
			dsOpt.owner_entity_id = Number(owner[0]);
		}
	}

	return dsOpt;
}



// ITEM UPDATE PREFERENCES

// load stored settings when modal is opened, or reset to default UI if no settings are saved
on('show.bs.modal', '#modal-advanced-settings', () => {
	if (page.itemUniqueConstraints) {
		$('#unique-data-source').checked = page.itemUniqueConstraints.data_source_name;
		$('#unique-original-location').checked = page.itemUniqueConstraints.original_location;
		$('#unique-filename').checked = page.itemUniqueConstraints.filename;
		$('#unique-timestamp').checked = page.itemUniqueConstraints.timestamp;
		$('#unique-latlon').checked = page.itemUniqueConstraints.latlon;
		$('#unique-altitude').checked = page.itemUniqueConstraints.altitude;
		$('#unique-classification').checked = page.itemUniqueConstraints.classification_name;
		$('#unique-data').checked = page.itemUniqueConstraints.data;
	}
	if (page.itemUpdatePrefs) {
		// reset previous UI
		$('#update-prefs-table tbody').replaceChildren();
		$('#no-rules').classList.remove('d-none');

		for (const pref of page.itemUpdatePrefs) {
			// add new rule row
			$('#add-update-prefs-row').click();
			const newRowEl = $('#update-prefs-table tbody tr:last-child');

			// fill out field and delete options
			$('.field-name', newRowEl).value = pref.field;
			$('.field-update-deletes', newRowEl).checked = pref.nulls;

			// fill out each priority
			if (!pref.priorities) {
				continue;
			}
			pref.priorities.forEach((priority, i) => {
				// add new priority if it's not the first one, since adding a rule row adds one priority
				if (i > 0) {
					$('.add-priority', newRowEl).click();
				}
				const priorityEl = $('.field-update-pref-priority:last-child', newRowEl);
				for (const key in priority) {
					if (key == "keep") {
						$('.priority-property', priorityEl).value = priority[key];
					} else if (key == "data_source") {
						$('.priority-property', priorityEl).value = key;
						trigger($('.priority-property', priorityEl), 'change', { tsVal: priority[key] });
					} else if (key == "media_type") {
						$('.priority-property', priorityEl).value = key;
						trigger($('.priority-property', priorityEl), 'change');
						$('.priority-media-type', priorityEl).value = priority[key];
					} else if (key == "size") {
						$('.priority-property', priorityEl).value = priority[key] == "bigger" ? "size_larger" : "size_smaller";
					} else if (key == "timestamp") {
						$('.priority-property', priorityEl).value = priority[key] == "earlier" ? "ts_earlier" : "ts_later";
					}
				}
			});
		}
	}
});

// save settings when button is clicked (only saved for duration of page load; that's probably best tbh)
on('click', '#save-settings', () => {
	saveAdvancedSettings();
});

function saveAdvancedSettings() {
	page.itemUniqueConstraints = {};
	// The API actually uses the presence of any key as "yes" to being a unique
	// constraint; the value is whether to strictly enforce NULLs. Our UI doesn't
	// yet offer toggling strict nulls for unique constraints, so for now we only
	// treat the checkboxes as cues to add them to the unique constraints at all,
	// and we assume strict nulls (i.e. if incoming is NULL, db row must also have NULL)
	if ($('#unique-data-source').checked) {
		page.itemUniqueConstraints["data_source_name"] = true;
	}
	if ($('#unique-original-location').checked) {
		page.itemUniqueConstraints["original_location"] = true;
	}
	if ($('#unique-filename').checked) {
		page.itemUniqueConstraints["filename"] = true;
	}
	if ($('#unique-timestamp').checked) {
		page.itemUniqueConstraints["timestamp"] = true;
	}
	if ($('#unique-latlon').checked) {
		page.itemUniqueConstraints["latlon"] = true;
	}
	if ($('#unique-altitude').checked && !$('#unique-altitude').getAttribute('disabled')) {
		page.itemUniqueConstraints["altitude"] = true;
	}
	if ($('#unique-classification').checked) {
		page.itemUniqueConstraints["classification_name"] = true;
	}
	if ($('#unique-data').checked) {
		page.itemUniqueConstraints["data"] = true;
	}
	saveItemUpdatePreferences();
}

function saveItemUpdatePreferences() {
	page.itemUpdatePrefs = [];
	$$('#update-prefs-table tbody tr').forEach(rowEl => {
		const fieldName = $('.field-name', rowEl).value;
		if (!fieldName) {
			return;
		}
		const priorities = [];
		$$('.field-update-pref-priority', rowEl).forEach(priorityEl => {
			const prop = $('.priority-property', priorityEl).value;
			if (prop == "incoming") {
				priorities.push({ "keep": "incoming" });
			} else if (prop == "existing") {
				priorities.push({ "keep": "existing" });
			} else if (prop == "data_source") {
				const dataSourceEl = $('select.priority-data-source', priorityEl);
				if (dataSourceEl) {
					priorities.push({ "data_source": dataSourceEl.value });
				}
			} else if (prop == "media_type") {
				const mediaTypeEl = $('.priority-media-type', priorityEl);
				if (mediaTypeEl) {
					priorities.push({ "media_type": mediaTypeEl.value });
				}
			} else if (prop == "size_larger") {
				priorities.push({ "size": "bigger" });
			} else if (prop == "size_smaller") {
				priorities.push({ "size": "smaller" });
			} else if (prop == "ts_earlier") {
				priorities.push({ "timestamp": "ealier" });
			} else if (prop == "ts_later") {
				priorities.push({ "timestamp": "later" });
			}
		});
		if (!priorities.length) {
			return;
		}
		const rule = {
			field: $('.field-name', rowEl).value,
			priorities: priorities,
			nulls: $('.field-update-deletes', rowEl).checked
		};
		page.itemUpdatePrefs.push(rule);
	});
}

// add rule/preference row
on('click', '#add-update-prefs-row', e => {
	$('#no-rules').classList.add('d-none');

	// no technical limitation, just an arbitrary one to keep the UI sane
	if ($$('#update-prefs-table tbody tr').length >= 15) {
		return;
	}

	// make new row, and get last row so we can copy values from it
	const rowEl = cloneTemplate('#tpl-item-update-prefs-row');
	const lastRowEl = $('#update-prefs-table tbody tr:last-child');

	// make new priority for the new row
	const priorityEl = cloneTemplate('#tpl-field-update-pref-priority');
	$('.add-priority', priorityEl).classList.remove('d-none');
	$('.field-update-priorities', rowEl).append(priorityEl);
	
	// add the row element early so we can trigger events within it as we copy things into it
	$('#update-prefs-table tbody').append(rowEl);

	// copy the priorities from the last rule row into the new one
	const lastPriorities = $('.field-update-priorities', lastRowEl);
	if (lastRowEl) {
		$$('.field-update-pref-priority', lastPriorities).forEach((lastPriorityEl, i) => {
			// either this is the first priority element, which we already made above for the new row,
			// or we need to make another new priority element if the last row has more than one
			let newPriorityEl = $(`.field-update-pref-priority:nth-child(${i+1})`, rowEl);
			if (!newPriorityEl) {
				$('.add-priority', rowEl).click();
				newPriorityEl = $(`.field-update-pref-priority:nth-child(${i+1})`, rowEl);
			}
			// copy the property, and trigger the change event so it can set up a possible value input
			// (we attach the data source value if it's a data source priority because the change event
			// will create a data source tomselect, which is an async operation, and we need to set it
			// to the same value after that completes, which only the event handler can do)
			$('.priority-property', newPriorityEl).value = $(`.priority-property`, lastPriorityEl).value;
			trigger($('.priority-property', newPriorityEl), 'change', { tsVal: $(`select.priority-data-source`, lastPriorityEl)?.value });
			// media source priority needs its value copied over too, but that's much simpler than the async data source selector
			if ($('.priority-media-type', newPriorityEl)) {
				$('.priority-media-type', newPriorityEl).value = $(`.priority-media-type`, lastPriorityEl).value;
			}
		});
	}
});

// delete rule row
on('click', '#update-prefs-table .delete-rule', e => {
	e.target.closest('tr').remove();

	if ($$('#update-prefs-table tbody tr').length == 0) {
		$('#no-rules').classList.remove('d-none');
	}
});

// when a priority property is changed in a rule, set up a value input if necessary
on('change', '#update-prefs-table .priority-property', async e => {
	const containerEl = e.target.closest('.field-update-pref-priority');
	$('.priority-value-input-container', containerEl).innerHTML = '';

	if (e.target.value == 'data_source') {
		$('.priority-value-input-container', containerEl).innerHTML = '<select class="priority-data-source form-select mt-1" placeholder="Data source" autocomplete="off"></select>';
		const dsSel = await newDataSourceSelect($('.priority-data-source', containerEl), {
			maxItems: 1
		});
		if (e.detail?.tsVal) {
			dsSel.setValue(e.detail.tsVal);
		}
	} else if (e.target.value == "media_type") {
		$('.priority-value-input-container', containerEl).innerHTML = '<input class="priority-media-type form-control mt-1" placeholder="Media type">';
	}
});

// delete priority from rule row
on('click', '#update-prefs-table .delete-priority', e => {
	const tr = e.target.closest('tr');

	e.target.closest('.field-update-pref-priority').remove();

	// make sure "+" (add priority) is only shown on the last one,
	// and that "-" (delete priority) is only shown if there is more than one
	const priorities = $$('.field-update-pref-priority', tr);
	if (priorities.length == 1) {
		priorities.forEach(el => {
			$('.delete-priority', el).classList.add('d-none');
		});
	}
	$$('.field-update-pref-priority:not(:last-child)', tr).forEach(el => {
		$('.add-priority', el).classList.add('d-none');
	});
	$('.field-update-pref-priority:last-child .add-priority', tr).classList.remove('d-none');
});

// add priority to rule row
on('click', '#update-prefs-table .add-priority', e => {
	const priorityEl = cloneTemplate('#tpl-field-update-pref-priority');

	const containerEl = e.target.closest('.field-update-priorities');
	containerEl.append(priorityEl);
	
	// only allow up to a few priorities -- no technical reason per-se,
	// but I think more than a few gets a bit ridiculous
	const tr = e.target.closest('tr');
	if ($$('.field-update-pref-priority', tr).length < 3) {
		$('.add-priority', priorityEl).classList.remove('d-none');
	}

	// make sure that "-" (delete priority) is shown if there is more than one,
	// and hide "+" (add priority) on all but the new one we just appended
	$$('.field-update-pref-priority', tr).forEach(el => {
		$('.delete-priority', el).classList.remove('d-none');
	});

	$$('.field-update-pref-priority:not(:last-child)', tr).forEach(el => {
		$('.add-priority', el).classList.add('d-none');
	});
});

// altitude is only usable as a unique constraint if lat/lon also is
on('change', '#unique-latlon', e => {
	if (e.target.checked) {
		$('#unique-altitude').removeAttribute('disabled', true);
	} else {
		$('#unique-altitude').setAttribute('disabled', true);
	}
});



// // TODO: generalize input validation
// on('focusout', '.sms_backup_restore-owner-phone', async event => {
// 	if (event.target.value) {
// 		event.target.classList.remove('is-invalid');
// 		event.target.classList.add('is-valid');
// 	} else {
// 		event.target.classList.add('is-invalid');
// 		event.target.classList.remove('is-valid');
// 	}
// });


// // TODO: figure this out. I should be able to paste in a path or type a path and have the file picker go to work
// on('change paste', '#modal-import .file-picker-path', async e => {
// 	const doNav = async function() {
// 		const info = await app.FileStat(e.target.value);
// 		$('#modal-import .file-picker').navigate(info.full_name);
// 	};
// 	if (e.type == 'paste') {
// 		setTimeout(doNav, 0);
// 	} else {
// 		doNav();
// 	}
// });

// TODO:
// // validate year inputs
// on('change keyup', '#media-start-year, #media-end-year', e => {
// 	if (e.target.value && !/^\d{4}$/.test(e.target.value)) {
// 		e.target.classList.add('is-invalid');
// 		$('#start-import').classList.add('disabled');
// 	} else if (parseInt(e.target.value) > 0) {
// 		e.target.classList.remove('is-invalid');
// 		e.target.classList.add('is-valid');
// 	} else {
// 		e.target.classList.remove('is-invalid', 'is-valid');
// 	}

// 	if (!$('#modal-import .is-invalid')) {
// 		$('#start-import').classList.remove('disabled');
// 	}
// });

