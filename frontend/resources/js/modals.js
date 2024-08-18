/////////////////////////
// IMPORT
/////////////////////////

on('show.bs.modal', '#modal-import', async event => {
	const filePicker = await newFilePicker();
	filePicker.classList.add('fw-normal');
	
	const container = $('.file-picker-container', event.target);
	container.innerHTML = '';
	container.append(filePicker);
});

// validate form on change and enable/disable import button
on('selection', '#modal-import input, #modal-import select, #modal-import .file-picker', (e) => {
	const hasFiles = $('#modal-import .file-picker').selected().length > 0; 
	const hasDataSource = $('#modal-import input[name="data-source"]:checked')?.value != "";
	if (hasFiles && hasDataSource) {
		// TODO: validate required data source options too
		$('#start-import').classList.remove('disabled');
	} else {
		disableImportButton();
	}
});

function resetImportModal() {
	hideRecognizeResults();
	clearRecognizeResults();
	hideRecognizePlaceholders();
	hideDataSourceOptions();
	disableImportButton();
	advanceImportStep('choose-files');
}

// reset form if user explicitly cancels
on('click', '#cancel-import', () => {
	resetImportModal();
});

const filePickerDblClickMap = new Map();

// when an item in the file picker is clicked
on('selection', '#modal-import .file-picker', event => {
	// remove any existing 'recognize' results and DS options, and show placeholders
	clearRecognizeResults();
	hideDataSourceOptions();
	hideRecognizePlaceholders();

	const filePicker = event.target;
	const filenames = filePicker.selected();

	if (filenames.length == 1) {
		const item = $('.file-picker-item.selected', filePicker);

		// Add a delay when clicking a folder, because the user might double-click it;
		// if this is the first click in a double-click, don't run an expensive Recognize
		// call too eagerly. If it's a file, double-click doesn't do anything, so just
		// run Recognize immediately in that case.
		if (item.classList.contains("file-picker-item-dir")) {
			filePickerDblClickMap.set(item, true);
			
			// show placeholders while we wait for second click
			showRecognizePlaceholders();

			setTimeout(function() {
				if (!filePickerDblClickMap.has(item)) {
					return;
				}
				filePickerDblClickMap.delete(item);
				recognize(filenames);
			}, 500); // ideally this duration would be the same as the system's double-click timeout, but there's no way to get that
			return;
		}
	}

	if (filenames.length > 0) {
		recognize(filenames);
	}
});

// when navigating, skip trying to recognize it, and remove any Recognize results and hide placeholders
// on('dblclick', '#modal-import .file-picker-table .file-picker-item-dir', event => {
on('navigate', '#modal-import .file-picker', event => {
	// skip trying to recognize it
	filePickerDblClickMap.delete(event.detail.selectedItem);

	resetImportModal();
});

// recognize file after selection
async function recognize(filepaths) {
	console.log("FILEPATHS:", filepaths)

	showRecognizePlaceholders();

	// TODO: temporary really bad error handling just to get a sense of what's going on
	try {
		let results = await app.Recognize(filepaths);
		
		console.log("RECOGNIZED:", results);
		
		advanceImportStep('data-source');
		hideRecognizePlaceholders();

		for (let i = 0; i < results.length; i++) {
			const ds = results[i];
			if (ds.confidence < .5)	continue;
			const dsElem = cloneTemplate('#tpl-import-file-data-source');
			$('input[type=radio]', dsElem).value = ds.name;
			if (!$('.recognize-results input[name="data-source"]:checked')) {
				$('input[type=radio]', dsElem).checked = true;
			}
			$('.avatar', dsElem).style.backgroundImage = `url(/resources/images/data-sources/${ds.icon})`;
			$('.font-weight-bold', dsElem).innerText = ds.title;
			$('.text-secondary', dsElem).innerText = ds.description;
			$('.form-selectgroup-boxes').appendChild(dsElem);
		}

		showRecognizeResults();

		updateDataSourceOptions();
	} catch(err) {
		alert("Error: "+JSON.stringify(err));
	}
}

function showRecognizeResults() {
	$('.after-file-selection').classList.remove('d-none');
}

function hideRecognizeResults() {
	$('.after-file-selection').classList.add('d-none');
}

function clearRecognizeResults() {
	$('.recognize-results').replaceChildren();
}

function hideDataSourceOptions() {
	$('.dsopt-container').classList.add('d-none');
}

function disableImportButton() {
	$('#start-import').classList.add('disabled');
}

function showRecognizePlaceholders() {
	$('.recognize-placeholders').classList.remove('d-none');
}

function hideRecognizePlaceholders() {
	$('.recognize-placeholders').classList.add('d-none');
}

function advanceImportStep(stepName) {
	$('#modal-import .step-item.active .step-title').classList.remove('h2', 'lh-1');
	$('#modal-import .step-item.active').classList.remove('active');
	$(`#import-step-${stepName}`).classList.add('active');
	$(`#import-step-${stepName} .step-title`).classList.add('h2', 'lh-1');
}

async function updateDataSourceOptions() {
	advanceImportStep('customize');

	const ds = dataSource();
	const dsOptElem = cloneTemplate(`#tpl-dsopt-${ds}`);

	if (!dsOptElem) {
		$('.dsopt-container').replaceChildren();
		$('.dsopt-container').classList.add('d-none');
		$('.dsopt-nothing').classList.remove('d-none');
		return;
	}

	
	if (ds == "smsbackuprestore") {
		const owner = await getOwner(tlz.openRepos[0]);
		$('#smsbackuprestore-owner-phone', dsOptElem).value = entityAttribute(owner, "phone_number");
	}
	
	// render the data source options template
	$('.dsopt-container').replaceChildren(dsOptElem);
	$('.dsopt-container').classList.remove('d-none');
	$('.dsopt-nothing').classList.add('d-none');

	// these can't be set up until after they're displayed
	if (ds == "google_location") {
		const entitySelect = newEntitySelect('#google_location-owner', 1);
		const owner = await getOwner(tlz.openRepos[0]);
		entitySelect.addOption(owner);
		entitySelect.addItem(owner.id);

		noUiSlider.create($('#google_location-simplification', dsOptElem), {
			start: 2,
			connect: [true, false],
			step: 0.1,
			range: {
				min: 0,
				max: 10
			}
		});
	}
	if (ds == "gpx") {
		const entitySelect = newEntitySelect('#gpx-owner', 1);
		const owner = await getOwner(tlz.openRepos[0]);
		entitySelect.addOption(owner);
		entitySelect.addItem(owner.id);

		noUiSlider.create($('#gpx-simplification', dsOptElem), {
			start: 2,
			connect: [true, false],
			step: 0.1,
			range: {
				min: 0,
				max: 10
			}
		});
	}
	if (ds == "geojson") {
		const entitySelect = newEntitySelect('#geojson-owner', 1);
		const owner = await getOwner(tlz.openRepos[0]);
		entitySelect.addOption(owner);
		entitySelect.addItem(owner.id);
	}
	if (ds == "email") {
		new TomSelect("#email-skip-labels",{
			persist: false,
			create: true,
			createOnBlur: true
		});
	}
	if (ds == "media") {
		const entitySelect = newEntitySelect('#media-owner', 1);
		const owner = await getOwner(tlz.openRepos[0]);
		entitySelect.addOption(owner);
		entitySelect.addItem(owner.id);
	}
	if (ds == "icloud") {
		const entitySelect = newEntitySelect('#icloud-owner', 1);
		const owner = await getOwner(tlz.openRepos[0]);
		entitySelect.addOption(owner);
		entitySelect.addItem(owner.id);
	}
}

// validate year inputs
on('change keyup', '#media-start-year, #media-end-year', e => {
	if (e.target.value && !/^\d{4}$/.test(e.target.value)) {
		e.target.classList.add('is-invalid');
		$('#start-import').classList.add('disabled');
	} else if (parseInt(e.target.value) > 0) {
		e.target.classList.remove('is-invalid');
		e.target.classList.add('is-valid');
	} else {
		e.target.classList.remove('is-invalid', 'is-valid');
	}

	if (!$('#modal-import .is-invalid')) {
		$('#start-import').classList.remove('disabled');
	}
});

// TODO: figure this out. I should be able to paste in a path or type a path and have the file picker go to work
on('change paste', '#modal-import .file-picker-path', async e => {
	const doNav = async function() {
		const info = await app.FileStat(e.target.value);
		$('#modal-import .file-picker').navigate(info.full_name);
	};
	if (e.type == 'paste') {
		setTimeout(doNav, 0);
	} else {
		doNav();
	}
});

// upon selecting a data source, render data source options
on('click', '.recognize-results .form-selectgroup-input', async e => {
	updateDataSourceOptions();
});

// TODO: generalize field validation
on('focusout', '#smsbackuprestore-owner-phone', async event => {
	if (event.target.value) {
		event.target.classList.remove('is-invalid');
		event.target.classList.add('is-valid');
	} else {
		event.target.classList.add('is-invalid');
		event.target.classList.remove('is-valid');
	}
});

// begin import!
on('click', '#start-import', async e => {
	const ds = dataSource();

	let dsOpt;
	if (ds == "smsbackuprestore") {
		const ownerPhoneInput = $('#smsbackuprestore-owner-phone');
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
	if (ds == "google_location") {
		dsOpt = {};
		const owner = $('#google_location-owner').tomselect.getValue();
		if (owner.length) {
			dsOpt.owner_entity_id = Number(owner[0]);
		}
		const simplification = $('#google_location-simplification').noUiSlider.get();
		if (simplification) {
			dsOpt.simplification = Number(simplification);
		}
	}
	if (ds == "gpx") {
		dsOpt = {};
		const owner = $('#gpx-owner').tomselect.getValue();
		if (owner.length) {
			dsOpt.owner_entity_id = Number(owner[0]);
		}
		const simplification = $('#gpx-simplification').noUiSlider.get();
		if (simplification) {
			dsOpt.simplification = Number(simplification);
		}
	}
	if (ds == "geojson") {
		dsOpt = {};
		const owner = $('#geojson-owner').tomselect.getValue();
		if (owner.length) {
			dsOpt.owner_entity_id = Number(owner[0]);
		}
		dsOpt.lenient = $('#geojson-lenient').checked;
	}
	if (ds == "media") {
		dsOpt = {
			use_filepath_time: $('#media-use-file-path-time').checked,
			use_file_mod_time: $('#media-use-file-mod-time').checked,
			folder_is_album: $('#media-folder-is-album').checked,
			date_range: {}
		};
		const owner = $('#media-owner').tomselect.getValue();
		if (owner.length) {
			dsOpt.owner_entity_id = Number(owner[0]);
		}
		if ($('#media-start-year').value) {
			dsOpt.date_range.since = DateTime.utc(Number($('#media-start-year').value)).toISO();
		}
		if ($('#media-end-year').value) {
			dsOpt.date_range.until = DateTime.utc(Number($('#media-end-year').value)).endOf('year').toISO();
		}
	}
	if (ds == "email") {
		const skipLabels = $('#email-skip-labels').tomselect.getValue().split(",");
		dsOpt = {
			gmail_skip_labels: skipLabels
		};
	}
	if (ds == "icloud") {
		dsOpt = {
			recently_deleted: $('#icloud-recently-deleted').checked
		};
		const owner = $('#icloud-owner').tomselect.getValue();
		if (owner.length) {
			dsOpt.owner_entity_id = Number(owner[0]);
		}
	}

	console.log("DS OPT:", dsOpt);
	const filenames = $('#modal-import .file-picker').selected();
	console.log("FILENAMES:", filenames);
	
	const job = await app.Import({
		repo: tlz.openRepos[0].instance_id,
		filenames: filenames,
		data_source_name: ds,
		data_source_options: dsOpt,
		processing_options: {
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
		}
	});
	console.log("JOB STARTED:", job);

	notify({
		type: "success",
		title: "Import started",
		message: filenames.join(", "),
		duration: 2000
	});

	updateActiveJobs();

	const importModal = bootstrap.Modal.getInstance('#modal-import');
	importModal.hide();

	resetImportModal();
});


function dataSource() {
	return $('input[name="data-source"]:checked').value;
}











////////////////////////
// MERGE ENTITY
////////////////////////

// when "merge entity" dialog is shown, set up the form
on('show.bs.modal', '#modal-merge-entity', async e => {
	const entitySelectMerge = newEntitySelect('#modal-merge-entity .entity-merge', 1, true);
	const entitySelectKeep = newEntitySelect('#modal-merge-entity .entity-keep', 1, true);
	const entityIDMerge =  e?.relatedTarget?.dataset?.entityIDMerge;
	const entityIDKeep = e?.relatedTarget?.dataset?.entityIDKeep;
	if (entityIDMerge) {
		const entities = await app.SearchEntities({
			repo: tlz.openRepos[0].instance_id,
			row_id: [Number(entityIDMerge)]
		});
		entitySelectMerge.addOption(entities[0]);
		entitySelectMerge.addItem(entities[0].id);
	}
	if (entityIDKeep) {
		const entities = await app.SearchEntities({
			repo: tlz.openRepos[0].instance_id,
			row_id: [Number(entityIDKeep)]
		});
		entitySelectKeep.addOption(entities[0]);
		entitySelectKeep.addItem(entities[0].id);
	}
});

on('change', '#modal-merge-entity select', e => {
	const keepID = $('#modal-merge-entity .entity-keep').tomselect.getValue();
	const mergeID = $('#modal-merge-entity .entity-merge').tomselect.getValue();
	if (keepID && mergeID) {
		$('#do-entity-merge').classList.remove('disabled');
	} else {
		$('#do-entity-merge').classList.add('disabled');
	}
});

on('click', '#do-entity-merge', async e => {
	const keepID = $('#modal-merge-entity .entity-keep').tomselect.getValue();
	const mergeID = $('#modal-merge-entity .entity-merge').tomselect.getValue();
	if (!keepID || !mergeID) return;
	await app.MergeEntities(tlz.openRepos[0].instance_id, Number(keepID), [Number(mergeID)]);
});
