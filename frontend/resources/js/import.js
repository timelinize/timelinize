// TODO: Presumably, these handlers will need to be generalized if we ever want to reuse our filepicker modal
on('show.bs.modal', '#modal-file-picker', async event => {
	const filePicker = await newFilePicker();
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

	for (const file of plan.files) {
		const tpl = cloneTemplate('#tpl-file-import-plan');
		tpl.file_info = file; // attach the results to the DOM element

		// each result's data source options is in a uniquely-ID'ed collapsible region
		const collapseID = `import-item-dsopt-collapse-${tlz.collapseCounter}`;
		$('.import-item-dsopt-collapse', tpl).id = collapseID;
		$('.collapse-button', tpl).dataset.bsTarget = "#"+collapseID;
		tlz.collapseCounter++;

		var icon;
		if (file.file_type == "file") {
			icon = `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor"
					stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
					class="icon icon-tabler icons-tabler-outline icon-tabler-file">
					<path stroke="none" d="M0 0h24v24H0z" fill="none" />
					<path d="M14 3v4a1 1 0 0 0 1 1h4" />
					<path d="M17 21h-10a2 2 0 0 1 -2 -2v-14a2 2 0 0 1 2 -2h7l5 5v11a2 2 0 0 1 -2 2z" />
				</svg>`;
		} else if (file.file_type == "dir") {
			icon = `<svg xmlns="http://www.w3.org/2000/svg" width="48" height="48" viewBox="0 0 24 24" fill="currentColor"
					class="icon icon-tabler icons-tabler-filled icon-tabler-folder">
					<path stroke="none" d="M0 0h24v24H0z" fill="none" />
					<path d="M9 3a1 1 0 0 1 .608 .206l.1 .087l2.706 2.707h6.586a3 3 0 0 1 2.995 2.824l.005 .176v8a3 3 0 0 1 -2.824 2.995l-.176 .005h-14a3 3 0 0 1 -2.995 -2.824l-.005 -.176v-11a3 3 0 0 1 2.824 -2.995l.176 -.005h4z" />
				</svg>`;
		} else if (file.file_type == "archive") {
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
		$('.avatar', tpl).innerHTML = icon;
		$('.file-import-path', tpl).innerText = file.filename;

		// put top data source match in button
		$('.import-item-top-ds', tpl).ds = file.recognize_results[0];
		$('.top-ds-icon', tpl).src = `/resources/images/data-sources/${file.recognize_results[0].icon}`;
		$('.top-ds-title', tpl).innerText = file.recognize_results[0].title;


		for (const ds of file.recognize_results) {
			const dsTpl = cloneTemplate('#tpl-ds-dropdown-item');
			$('.ds-icon', dsTpl).src = `/resources/images/data-sources/${ds.icon}`;
			$('.ds-title', dsTpl).innerText = ds.title;
			$('.ds-confidence', dsTpl).innerText = `${(ds.confidence*100).toFixed(0)}% match`;
			$('.dropdown-menu', tpl).append(dsTpl);
		}

		$('#file-imports-container').append(tpl);

		renderDataSourceOptionsToItemImport(tpl);
	}
});

async function renderDataSourceOptionsToItemImport(elem) {
	const ds = $('.import-item-top-ds', elem).ds;

	const dsOptElem = cloneTemplate(`#tpl-dsopt-${ds.name}`);

	if (!dsOptElem) {
		$('.import-item-dsopt', elem).replaceChildren();
		$('.import-item-dsopt', elem).innerText = "No options available for this data source.";
		return;
	}

	if (ds.name == "smsbackuprestore") {
		const owner = await getOwner(tlz.openRepos[0]);
		$('.smsbackuprestore-owner-phone', dsOptElem).value = entityAttribute(owner, "phone_number");
	}

	// add a button to copy settings to all other files with that data source
	const copyBtn = document.createElement('button');
	copyBtn.classList.add("btn");
	copyBtn.innerHTML = `
		<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor"
			stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
			class="icon icon-tabler icons-tabler-outline icon-tabler-copy">
			<path stroke="none" d="M0 0h24v24H0z" fill="none" />
			<path
				d="M7 7m0 2.667a2.667 2.667 0 0 1 2.667 -2.667h8.666a2.667 2.667 0 0 1 2.667 2.667v8.666a2.667 2.667 0 0 1 -2.667 2.667h-8.666a2.667 2.667 0 0 1 -2.667 -2.667z" />
			<path d="M4.012 16.737a2.005 2.005 0 0 1 -1.012 -1.737v-10c0 -1.1 .9 -2 2 -2h10c.75 0 1.158 .385 1.5 1" />
		</svg>
		Apply to all ${ds.title}`;
	dsOptElem.append(copyBtn);
	
	// render the data source options template
	$('.import-item-dsopt', elem).replaceChildren(dsOptElem);

	// these can't be set up until after they're displayed
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