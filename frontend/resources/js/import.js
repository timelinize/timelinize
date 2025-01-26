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
			$('.dsgroup-icon', dsGroupElem).style.backgroundImage = `url('/resources/images/data-sources/${ds.icon}')`;
			$('.dsgroup-name', dsGroupElem).innerText = ds.title;

			// each file listing in a DS group is a uniquely-ID'ed collapsible region
			const collapseID = `import-dsgroup-collapse-${tlz.collapseCounter}`;
			$('.collapse', dsGroupElem).id = collapseID;
			$('.accordion-button', dsGroupElem).dataset.bsTarget = "#"+collapseID;
			tlz.collapseCounter++;

			// render the DS group and its options modal
			$('#file-imports-container').append(dsGroupElem);
			renderDataSourceOptionsModal(dsGroupElem, ds);
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

async function renderDataSourceOptionsModal(dsgroupElem, ds) {
	// start with creating (or getting?) the modal and setting it up with the DS info
	let dsOptModal = $(`#modal-import-dsopt-${ds.name}`);
	if (!dsOptModal) {
		dsOptModal = cloneTemplate('#tpl-modal-import-dsopt');
		dsOptModal.id = `modal-import-dsopt-${ds.name}`;
	}
	$('.avatar', dsOptModal).style.backgroundImage = `url('/resources/images/data-sources/${ds.icon}')`;
	$('.modal-title', dsOptModal).append(document.createTextNode(ds.title));

	const dsOptElem = cloneTemplate(`#tpl-dsopt-${ds.name}`);

	if (!dsOptElem) {
		$('.import-dsgroup-opt-button', dsgroupElem).remove();
		return;
	}

	if (ds.name == "smsbackuprestore") {
		const owner = await getOwner(tlz.openRepos[0]);
		$('.smsbackuprestore-owner-phone', dsOptElem).value = entityAttribute(owner, "phone_number");
	}
	
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
				overwrite_modifications: $('#overwrite-modifications').checked,
				item_unique_constraints: {
					// TODO: it occurred to me that an item need not be from the same
					// data source to be the exact same thing; like if I copy a photo
					// from my iPhone and import it with Media data source, but later
					// from the iPhone data source, it should be considered the same, right?
					// "data_source_name": true,
					"classification_name": true,
					// "original_location": true,
					// "intermediate_location": true,
					"filename": true, // TODO: <-- this one might only apply to some data sources, like photo libraries...
					"timestamp": true,
					"timespan": true,
					"timeframe": true,
					"data": true,
					"location": true
				},
				// item_field_updates: {
				// 	"attribute_id": 2,
				// 	"classification_id": 2,
				// 	"original_location": 2,
				// 	"intermediate_location": 2,
				// 	"timestamp": 2,
				// 	"timespan": 2,
				// 	"timeframe": 2,
				// 	"time_offset": 2,
				// 	"time_uncertainty": 2,
				// 	"data": 2,
				// 	"metadata": 2,
				// 	"location": 2
				// }
				interactive: $('#interactive').checked ? {} : null
			},
			estimate_total: $('#estimate-total').checked
		}
	};

	for (const dsgroup of $$('.file-import-plan-dsgroup')) {
		importParams.job.plan.files.push({
			data_source_name: dsgroup.ds.name,
			data_source_options: dataSourceOptions(dsgroup.ds),
			filenames: dsgroup.filenames
		});
	}

	console.log("IMPORT PARAMS:", importParams)

	const result = await app.Import(importParams);
	console.log("JOB STARTED:", result);

	notify({
		type: "success",
		title: "Import queued",
		duration: 2000
	});

	if (importParams.job.processing_options.interactive) {
		// take user to page where they can begin their interactive import
		navigateSPA(`/input?repo_id=${repoID}&job_id=${result.job_id}`);
	} else {
		// otherwise, redirect to job status, I guess
		navigateSPA(`/jobs/${repoID}/${result.job_id}`);
	}

});

function dataSourceOptions(ds) {
	dsoptContainer = $(`#modal-import-dsopt-${ds.name}`);
	console.log(dsoptContainer);
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
	if (ds.name == "smsbackuprestore") {
		const ownerPhoneInput = $('.smsbackuprestore-owner-phone', dsoptContainer);
		console.log("INPUT:", ownerPhoneInput)
		if (!ownerPhoneInput.value) {
			// TODO: generalize field validation (see also the focusout event above)
			ownerPhoneInput.classList.add('is-invalid');
			ownerPhoneInput.classList.remove('is-valid');
			return;
		}
		dsOpt = {
			owner_phone_number: ownerPhoneInput.value
		};
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

	return dsOpt;
}


// // TODO: generalize input validation
// on('focusout', '.smsbackuprestore-owner-phone', async event => {
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

