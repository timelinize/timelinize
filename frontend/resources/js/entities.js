async function entitiesPageMain() {
	filterAndRenderEntitiesList();
}

async function filterAndRenderEntitiesList() {
	const searchParams = {
		repo: tlz.openRepos[0].instance_id,
		order_by: 'id',
		or_fields: true,
		limit: 100
	}
	
	const filter = $('.filter-entities').value.trim();
	if (filter) {
		searchParams.name = [filter];
		searchParams.birth_place = [filter];
		searchParams.attributes = [{ value: filter }];
	}

	const entities = await app.SearchEntities(searchParams);
	console.log("GOT ENTITIES:", entities);
	
	$('#results-container').replaceChildren();

	if (!entities) {
		return; // there SHOULD always be one, but just in case...
	}

	const owner = await getOwner();

	for (const ent of entities) {
		const tpl = cloneTemplate('#tpl-entity');
		$('.entity-id', tpl).innerText = ent.id;//String(ent.id).padStart(4, '0');
		$('.entity-picture', tpl).innerHTML = avatar(true, ent, "avatar me-2").replace(/span/g, "a");
		$('.entity-name', tpl).innerText = ent.name; // TODO: and if no name...?
		if (ent.type && ent.type != "person") {
			$('.entity-type', tpl).innerText = ent.type;
		}
		$('.entity-stored', tpl).innerText = DateTime.fromISO(ent.stored).toLocaleString(DateTime.DATE_MED);
		$('.select-entity', tpl).dataset.entityID = ent.id;

		const entHref = `/entities/${tlz.openRepos[0].instance_id}/${ent.id}`;
		$('.entity-name', tpl).href = entHref;
		$('.entity-picture a', tpl).href = entHref;
		$('.entity-view', tpl).href = entHref;

		// find some attributes to display; display up to 2 (we may change this
		// depending on viability)... generally email and phone are the most useful,
		// so default to those, but if one or both of those aren't available,
		// show the first 1 or 2 identity attributes as they are more useful than
		// generic attributes... if there aren't any identity attributes, just show
		// the first generic attributes, doesn't really matter at that point
		const attrs = {};
		for (const attr of ent.attributes) {
			if (!attrs[attr.name]) {
				attrs[attr.name] = [];
			}
			attrs[attr.name].push(attr);
		}
		let attr1, attr2;
		attr1 = attrs["email_address"]?.slice(0, 5) || attrs["phone_number"]?.slice(0, 5);
		if (!attr1) {
			for (const name in attrs) {
				if (attrs[name][0].identity) {
					attr1 = attrs[name].slice(0, 5);
					break;
				}
			}
		}
		if (attr1) {
			for (const name in attrs) {
				if (attrs[name][0].identity && name != attr1[0].name) {
					attr2 = attrs[name].slice(0, 5);
					break;
				}
			}
		}
		if (attr1 && !attr2) {
			for (const name in attrs) {
				if (name != attr1[0].name) {
					attr2 = attrs[name].slice(0, 5);
					break;
				}
			}
		}
		function renderAttributes(attrs, container) {
			for (const attr of attrs) {
				let tagName = attr.name == "email_address" || attr.name == "phone_number" || attr.name == "url" ? 'a' : 'span';
				const elem = document.createElement(tagName);
				if (attr.name == "email_address") {
					elem.href = "mailto:" + attr.value;
				} else if (attr.name == "phone_number") {
					elem.href = "tel:" + attr.value;
				} else if (attr.name == "url") {
					elem.href = attr.value;
				}
				elem.innerText = attr.value;
				container.append(elem);
				container.append(document.createElement("br"));
			}
			const elem = document.createElement('span');
			elem.classList.add('text-secondary');
			elem.innerText = tlz.attributeLabels[attrs[0].name] || attrs[0].name;
			container.append(elem);
		}
		if (attr1) {
			renderAttributes(attr1, $('.entity-attr1', tpl));
		}
		if (attr2) {
			renderAttributes(attr2, $('.entity-attr2', tpl));
		}

		// show the "You" badge next to help identify self
		if (ent.id == owner.id) {
			const badge = document.createElement('span');
			badge.classList.add('badge', 'bg-purple-lt', 'ms-2');
			badge.innerText = "You";
			$('.entity-name', tpl).append(badge);
		}

		// if the user clicks the Merge action, this will pre-populate the modal
		$('.entity-list-merge-link', tpl).dataset.entityIDMerge = ent.id;

		$('#results-container').append(tpl);
	}
}

on('keyup', '.filter-entities', async e => {
	// skip Shift, Ctrl, etc. (but allow Backspace)
	if ((e.keyCode < 32 && e.keyCode != 8) || e.ctrlKey || e.metaKey) {
		return;
	}

	// TODO: debounce

	filterAndRenderEntitiesList();
});

on('change', '.select-entity', e => {
	if ($$('.select-entity:checked').length > 1) {
		$('#merge-entities').classList.remove('disabled');
	} else {
		$('#merge-entities').classList.add('disabled');
	}
});

on('click', '#confirm-merge-entities', async e => {
	const button = $('#confirm-merge-entities');

	const done = function() {
		button.classList.remove('disabled');
		$('spinner', button)?.remove();
		bootstrap.Modal.getInstance('#modal-merge-multiple-entities').hide();
	};
	
	// get all the IDs of entities to be merged
	const ids = Array.from($$('.select-entity:checked'), check => Number(check.dataset.entityID));
	if (ids.length < 2) {
		done();
		return;
	}
	
	// show loading indicator on button and disable clicks until done
	const spinner = document.createElement('spinner');
	spinner.classList.add('spinner-border', 'spinner-border-sm', 'me-2');
	button.prepend(spinner);
	button.classList.add('disabled');

	// choose an entity to use as the keeper, I'm going to go with the lowest ID (oldest)
	ids.sort();
	keepID = ids.shift();

	// perform merge
	await app.MergeEntities(tlz.openRepos[0].instance_id, keepID, ids);

	notify({
		type: "success",
		title: `${ids.length+1} entit${ids.length+1 == 1 ? "y" : "ies"} merged`,
		duration: 2000
	});
	
	
	// close modal and update list
	done();
	filterAndRenderEntitiesList();

	// also update owner picture at the top if its entity was in the merge
	if (keepID == 1 || ids[0] == 1) {
		updateRepoOwners(true);
	}
});

on('hidden.bs.modal', '#confirm-merge-entities', async e => {
	// reset UI state
	$('#confirm-merge-entities').classList.remove('disabled');
	$('#confirm-merge-entities .spinner-border').remove();
});