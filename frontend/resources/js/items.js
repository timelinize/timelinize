function limit() {
	return Number($('#filter-limit').value);
}

function itemsPageFilterParams() {
	const datePicker = $('.date-input').datepicker;

	// TODO: get paging organized
	const lim = limit();
	
	let params = {
		start_timestamp: datePicker.selectedDates[0],
		end_timestamp: datePicker.selectedDates[1],
		offset: lim * (currentPageNum()-1),
		limit: lim + 1, // add one to know if there's a next page
		sort: $('.date-sort').value,
		// flat: $('#flatten').checked, // TODO: maybe this could be a toggle
		related: 1
	};
	commonFilterSearchParams(params);

	const similarTo = queryParam("similar_to");
	if (similarTo) {
		params.similar_to = [Number(similarTo)];
	}

	
	
	// TODO: to include only items that have coordinates, set these:
	// params.min_latitude = -90;
	// params.max_latitude = 90;
	// params.min_longitude = -180;
	// params.max_longitude = 180;
	
	// let offset = Number($('#filter-offset').value);
	// if (offset) {
	// 	let offsetType = $('input[name=filter-offset-type]:checked').value;
	// 	if (offsetType == 'offset') {
	// 		params.offset = offset;
	// 	} else if (offsetType == 'page') {
	// 		if (offset < 1) {
	// 			offset = 1;
	// 		}
	// 		params.offset = params.limit * (offset-1);
	// 	}
	// }

	// add 1 to the limit so we can know whether there is a next page
	params.limit++;

	console.log("FILTER PARAMETERS:", params);

	return params;
}


async function itemsMain() {
	const repo = tlz.openRepos[0];
	const params = itemsPageFilterParams();
	const results = await app.SearchItems(params);

	console.log("RESULTS:", params, results);


	// TODO: this is the same as on gallery page
	// configure pagination links: enable next if we overflowed the search results limit,
	// and enable prev if we are not on page 1; otherwise disable prev/next link(s)
	for (const elem of $$('.pagination .page-next')) {
		if (results.items?.length > limit()) {
			elem.classList.remove('disabled');
			let newQS = new URLSearchParams(window.location.search);
			newQS.set('page', currentPageNum() + 1);
			elem.href = '?'+newQS.toString();
		} else {
			elem.classList.add('disabled');
			elem.href = '';
		}
	}
	for (const elem of $$('.pagination .page-prev')) {
		if (currentPageNum() > 1) {
			elem.classList.remove('disabled');
			let newQS = new URLSearchParams(window.location.search);
			if (currentPageNum() == 2) {
				newQS.delete('page');
			} else {
				newQS.set('page', currentPageNum() - 1);
			}
			elem.href = '?'+newQS.toString();
		} else {
			elem.classList.add('disabled');
			elem.href = '';
		}
	}







	$$('.filter-results').forEach(elem => elem.replaceChildren());

	const offset = params.offset || 0;
	const pageSize = params.limit-1; // remember we set it to 1 more to determine next page
	// $('.page-offset-start').innerText = offset + 1;
	// $('.page-offset-end').innerText = offset + pageSize;
	// $('.page-total-count').innerText = results.total;


	const maxPageLinksAtBeginning = 3;

	// remove the extra item from the list that we only used to know whether there's another page
	if (results.items?.length > pageSize) {
		results.items.pop();
	}

	const classes = load('item_classes');
	function classInfo(name) {
		return classes[name] || {name: "-", description: "Unknown item type", labels: "Unknown"};
	}

	// render all items
	for (let i = 0; i < results.items?.length; i++) {
		const item = results.items[i];
		const ts = DateTime.fromISO(item.timestamp);
		const itemLink = `/items/${repo.instance_id}/${item.id}`;

		const tpl = cloneTemplate('#tpl-item');

		$('.ds-icon', tpl).style.backgroundImage = `url('/ds-image/${item.data_source_name}')`;
		$('.ds-icon', tpl).title = `Imported from ${item.data_source_title}`;

		$('.item-id', tpl).innerText = item.id;
		$('a.item-id', tpl).href = itemLink;

		if (item.entity) {
			$('.entity-picture', tpl).innerHTML = avatar(true, item.entity, "avatar-sm avatar-rounded");
			const entityDisplay = entityDisplayNameAndAttr(item.entity);
			$('.entity-name', tpl).textContent = entityDisplay.name;
			$('.entity-attribute', tpl).textContent = entityDisplay.attribute;
			if (!entityDisplay.attribute) {
				$('.entity-attribute', tpl).remove();
			}
			$('a.owner-entity', tpl).href = `/entities/${repo.instance_id}/${item.entity.id}`;
		}

		if (item.embedding_id) {
			const a = document.createElement('a');
			a.classList.add("btn", "btn-outline", "secondary");
			a.innerText = "Similar items";
			a.href = `?similar_to=${item.id}`;
			$('.similar-to', tpl).append(a);
		}

		$('a.item-timestamp', tpl).href = itemLink;
		const tsDisplay = itemTimestampDisplay(item);
		if (tsDisplay.dateTime) {
			$('.item-timestamp', tpl).textContent = tsDisplay.dateTime;
		} else {
			$('.item-timestamp', tpl).parentNode.remove();
		}

		// classification
		const cl = classInfo(item.classification);
		$('.class-label', tpl).textContent = cl.labels[0];
		$('.class-icon', tpl).innerHTML = tlz.itemClassIconAndLabel(item).icon;

		const itemContentEl = itemContentElement(item, { thumbnail: true });
		if (itemContentEl.dataset.contentType == "audio")
		{
			// show title & artist if available
			if (item.metadata['Title']) {
				const titleContainer = document.createElement("div");
				titleContainer.classList.add('small', 'mb-2');
				const title = document.createElement("b");
				title.innerText = item.metadata['Title'];
				titleContainer.append(title);
				if (item.metadata['Artist']) {
					const artist = document.createElement("div");
					artist.innerText = item.metadata['Artist'];
					artist.classList.add('text-secondary');
					titleContainer.append(artist);
				}
				$('.item-content', tpl).append(titleContainer);

				itemContentEl.classList.add("mt-2");
			}
		}
		else if (itemContentEl.dataset.contentType == "text")
		{
			// don't let text go on too long in the listing
			itemContentEl.textContent = maxlenStr(itemContentEl.textContent, 512);
		} 
		else if (itemContentEl.dataset.contentType == "location")
		{
			// make maps less tall than the default
			itemContentEl.classList.remove('ratio-16x9');
			itemContentEl.classList.add('ratio-21x9');
		}
		$('.item-content', tpl).append(itemContentEl);

		if (!$('.item-content', tpl).childNodes.length) {
			$('.item-content', tpl).remove();
		}

		renderItemSentTo(tpl, item, repo);

		// TODO: see similar code in item.js -- make this standard somehow? (refactor)
		if (item.related) {
			for (let rel of item.related) {
				if (rel.label == 'attachment' && rel.to_item) {
					const attachTpl = cloneTemplate('#tpl-related-item');

					const attachmentElem = itemContentElement(rel.to_item, { avatar: true, thumbnail: true });
					attachTpl.appendChild(attachmentElem);

					$('.related-items', tpl).append(attachTpl);
					$('.item-card-footer', tpl).classList.remove('d-none');
				}
			}
		}

		if (!item.entity && $('.other-entities', tpl).isEmpty()) {
			$('.item-entities', tpl).remove();
		}

		$('.btn-primary', tpl).href = itemLink;

		$('.space-y').appendChild(tpl);
	}
}

function renderItemSentTo(tpl, item, repo) {
	if (!item.related) return;

	// find out how many recipients
	let totalSentToCount = 0;
	for (let rel of item.related) {
		if ((rel.label == 'sent' || rel.label == 'cc') && rel.to_entity) {
			totalSentToCount++;
		}
	}
	if (!totalSentToCount) return;

	$('.item-entity-rel', tpl).innerHTML = `
		<svg xmlns="http://www.w3.org/2000/svg"
			class="item-entity-relation icon icon-tabler icon-tabler-arrow-narrow-right"
			width="24" height="24" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"
			fill="none" stroke-linecap="round" stroke-linejoin="round">
			<path stroke="none" d="M0 0h24v24H0z" fill="none"></path>
			<line x1="5" y1="12" x2="19" y2="12"></line>
			<line x1="15" y1="16" x2="19" y2="12"></line>
			<line x1="15" y1="8" x2="19" y2="12"></line>
		</svg>`;

	let appendedCount = 0;
	let tooltipNames = [];
	for (let rel of item.related) {
		if ((rel.label != 'sent' && rel.label != 'cc') || !rel.to_entity)
			continue;

		if (totalSentToCount == 1) {
			$('.other-entities', tpl).appendChild(itemEntityTemplate(repo, rel.to_entity));
			break;
		} else {
			if (appendedCount < 3) {
				let entityTpl = itemEntityTemplate(repo, rel.to_entity);
				$('.other-entities', tpl).appendChild(entityTpl);
				appendedCount++;
			} else {
				tooltipNames.push(rel.to_entity.name || rel.to_entity.attribute.value);
			}
		}
	}

	if (tooltipNames.length) {
		const moreEl = document.createElement('a');
		moreEl.classList.add('entity', 'clickable', 'd-flex', 'align-items-center');
		moreEl.href = `/items/${repo.instance_id}/${item.id}`;
		moreEl.title = tooltipNames.slice(0, 25).join(', ');
		moreEl.textContent = `and ${tooltipNames.length} more...`;
		$('.other-entities', tpl).appendChild(moreEl);
	}
}


function itemEntityTemplate(repo, entity) {
	let entityTpl = cloneTemplate('#tpl-item-entity');
	const entityDisplay = entityDisplayNameAndAttr(entity);
	$('.entity-name', entityTpl).textContent = entityDisplay.name;
	$('.entity-attribute', entityTpl).textContent = entityDisplay.attribute;
	$('.entity-picture', entityTpl).innerHTML = avatar(true, entity, "avatar-sm avatar-rounded");
	entityTpl.href = `/entities/${repo.instance_id}/${entity.id}`;
	return entityTpl;
}
