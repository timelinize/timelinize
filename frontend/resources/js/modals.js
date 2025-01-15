////////////////////////
// MERGE ENTITY
////////////////////////

// when "merge entity" dialog is shown, set up the form
on('show.bs.modal', '#modal-merge-entity', async e => {
	const entitySelectMerge = newEntitySelect($('#modal-merge-entity .entity-merge'), 1, true);
	const entitySelectKeep = newEntitySelect($('#modal-merge-entity .entity-keep'), 1, true);
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
	notify({
		type: "success",
		title: `Entities merged`,
		duration: 2000
	});
});
