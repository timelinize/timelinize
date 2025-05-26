let emptyRepo;
let proposedPath;

function advanceToPersonForm() {
	replace('#phase-open-repo', '#phase-repo-person');
	$('.progress-bar').style.width = '75%';
	$('#continue').innerHTML = `
	<svg xmlns="http://www.w3.org/2000/svg" class="icon icon-tabler icon-tabler-device-floppy" width="24" height="24" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" fill="none" stroke-linecap="round" stroke-linejoin="round">
		<path stroke="none" d="M0 0h24v24H0z" fill="none"></path>
		<path d="M6 4h10l4 4v10a2 2 0 0 1 -2 2h-12a2 2 0 0 1 -2 -2v-12a2 2 0 0 1 2 -2"></path>
		<circle cx="12" cy="14" r="2"></circle>
		<polyline points="14 4 14 8 8 8 8 4"></polyline>
	</svg>
	Save and continue`;

	new TomSelect("[name=email]",{
		persist: false,
		create: true,
		createOnBlur: true
	});
	new TomSelect("[name=phone]",{
		persist: false,
		create: true,
		createOnBlur: true
	});
}

// TODO: Presumably, these handlers will need to be generalized if we ever want to reuse our filepicker modal
on('show.bs.modal', '#modal-timeline-folder', async event => {
	const filePicker = await newFilePicker("repo", {
		only_dirs: true
	});
	filePicker.classList.add('fw-normal');
	
	const container = $('.modal-body', event.target);
	container.innerHTML = '';
	container.append(filePicker);
});

on('selection', '#modal-timeline-folder .file-picker', event => {
	if (event.target.selected().length == 1) {
		$('#select-timeline-folder').innerText = "Use Selected Folder";
	} else {
		$('#select-timeline-folder').innerText = "Select This Folder";
	}
});

on('click', '#select-timeline-folder', event => {
	let selectedDirs = $('#modal-timeline-folder .file-picker').selected();
	if (!selectedDirs.length) {
		const currentPath = $('#modal-timeline-folder .file-picker-path').value;
		if (currentPath) {
			selectedDirs = [currentPath];
		}
	}
	if (selectedDirs.length != 1) {
		return;
	}
	$('#repo-path').value = selectedDirs[0];
});

on('click', '#continue', async () => {
	if (isVisible('#phase-open-repo')) {
		try {
			const repo = await openRepository($('#repo-path').value, false);
			
			// notify({
			// 	type: "success",
			// 	title: "Timeline opened",
			// 	duration: 2000
			// });

			// if repo is empty (no persons), advance to fill out person info
			if (await app.RepositoryIsEmpty(repo.instance_id)) {
				emptyRepo = repo;
				advanceToPersonForm();
				return;
			}

			// otherwise, if there is at least one person, return to app
			await navigateSPA('/', true);

			notify({
				type: "success",
				title: "Timeline opened",
				duration: 2000
			});

		} catch (e) {
			console.error("EXCEPTION:", e);
			const assessment = e.error.data;

			if (!assessment.has_timeline && assessment.timeline_can_be_created) {
				proposedPath = assessment.timeline_path;
				$('#new-repo-path').innerText = assessment.timeline_path;
				new bootstrap.Modal('#modal-create-confirm').show();
			}
		}
	} else {
		attributes = [];

		$('[name=email]').tomselect.getValue().split(",").forEach(email => {
			attributes.push({
				name: "email_address",
				value: email,
				identifying: true
			});
		});
		$('[name=phone]').tomselect.getValue().split(",").forEach(phone => {
			attributes.push({
				name: "phone_number",
				value: phone,
				identifying: true
			});
		});
		if ($('[name=dob-year').value && $('[name=dob-month]').value && $('[name=dob-day]').value) {
			attributes.push({
				name: "birth_date",
				value: new Date($('[name=dob-year').value, $('[name=dob-month]').value, $('[name=dob-day]').value)
			});
		}
		if ($('[name=birth_place').value) {
			attributes.push({
				name: "birth_place",
				value: $('[name=birth_place]').value
			});
		}

		await app.AddEntity(emptyRepo.instance_id, {
			type: 'person',
			name: $('[name=name]').value,
			attributes: attributes
		});

		await updateRepoOwners();

		// continue to app
		await navigateSPA('/', true);

		notify({
			type: "success",
			title: "Profile saved",
			duration: 2000
		});
	}
});


on('click', '#create-repo', async () => {
	// try {
		const repo = await openRepository(proposedPath, true);
		notify({
			type: "success",
			title: "New timeline created!",
			duration: 2000
		});
		emptyRepo = repo;
		advanceToPersonForm();
	// } catch (error) {
	// 	// TODO: better error handling
	// 	console.error("ERROR:", error);
	// }
});
