var pageItems;

const galleryLimit = 100;

async function galleryPageMain() {
	// set up the preview modal to work on our page
	PreviewModal.prev = async (currentItem) => {
		const params = galleryFilterParams("prev", currentItem);
		// params.limit = 1;
		// params.sort = null; // let backend use smart sort
		// if ($('#filter-sort').value == "ASC") {
		// 	params.end_timestamp = currentItem.timestamp;
		// } else {
		// 	params.start_timestamp = currentItem.timestamp;
		// }
		const results = await app.SearchItems(params);
		console.log("PREV RESULTS:", results, currentItem);
		return results.items?.[0];
	};
	PreviewModal.next = async (currentItem) => {
		const params = galleryFilterParams("next", currentItem);
		// params.limit = 1;
		// params.sort = null; // let backend use smart sort
		// if ($('#filter-sort').value == "ASC") {
		// 	params.start_timestamp = currentItem.timestamp;
		// } else {
		// 	params.end_timestamp = currentItem.timestamp;
		// }
		const results = await app.SearchItems(params);
		console.log("NEXT RESULTS:", results, currentItem);
		return results.items?.[0];
	};

	// perform search
	const results = await app.SearchItems(galleryFilterParams());

	// configure pagination links: enable next if we overflowed the search results limit,
	// and enable prev if we are not on page 1; otherwise disable prev/next link(s)
	for (const elem of $$('.pagination .page-next')) {
		if (results.items?.length > galleryLimit) {
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

	if (!results.items) {
		return;
	}
	
	results.items.splice(galleryLimit); // strip the extra item that is only used for pagination purposes

	// reset items
	pageItems = {};
	$('.filter-results').replaceChildren();

	// render each item
	for (let item of results.items) {
		const elem = cloneTemplate('#tpl-media');
		elem.id = `item-${item.id}`;

		let mediaElem = itemContentElement(item, { thumbnail: true });
		mediaElem.classList.add('w-100', 'h-100', 'object-cover', 'card-img-top');
		mediaElem.classList.remove('rounded');

		/*
			TODO: I kind of like this dreamy glow effect around each video (maybe image too?) -- maybe just on hover though?
			<div style="position: relative; display: contents">
				<video>...</video>
				<div style="position: absolute;
					top: 0; left: 0; bottom: 0; right: 0;
					box-shadow: inset 0px 0px 15px 20px white;
					pointer-events: none;"></div>
			</div>
		*/

		elem.prepend(mediaElem);
		elem.dataset.rowid = item.id;
		$('.media-owner-avatar', elem).innerHTML = avatar(true, item.entity, "me-3");
		$('.media-owner-name', elem).innerText = entityDisplayNameAndAttr(item.entity).name;
		$('.media-timestamp', elem).innerText = DateTime.fromISO(item.timestamp).toLocaleString(DateTime.DATETIME_MED);
		
		if (item.score) {
			$('.media-similarity-score', elem).innerHTML = `<b>${(item.score * 100).toFixed(3)}%</b> match`;
		}

		$('.filter-results').append(elem);
		pageItems[item.id] = item;
	}
};


// when preview modal is brought up, render item content
on('click', '.filter-results [data-bs-toggle=modal]', async e => {
	const link = e.target.closest('[data-rowid]');
	const itemID = link.dataset.rowid;
	await PreviewModal.renderItem(pageItems[itemID]);
});












function galleryFilterParams(peekPrevOrNext, peekFromItem) {
	const params = {
		related: 1,
		relations: [
			// don't show motion pictures / live photos, since they are not
			// considered their own item in a gallery sense, and perhaps
			// more importantly, we don't want to have to generate a thumbnail
			// for them (literally no need for a thumbnail of those, just
			// wasted CPU time and storage space)
			{
				"not": true,
				"relation_label": "motion"
			}
		],
		flat: $('#include-attachments').checked,
		offset: galleryLimit * (currentPageNum()-1),
		limit: galleryLimit+1, // add one to know if there's a next page
		sort: $('.date-sort').value,
	};
	commonFilterSearchParams(params);

	// media types
	params.data_type = [];
	if ($('#format-images').checked) {
		params.data_type.push("image/*");
	}
	if ($('#format-videos').checked) {
		params.data_type.push("video/*");
	}

	// if peeking the previous or next page, adjust parameters accordingly
	if (peekPrevOrNext == "prev") {
		if (params.sort == "ASC") {
			params.end_timestamp = peekFromItem.timestamp;
		} else {
			params.start_timestamp = peekFromItem.timestamp;
		}
		params.sort = null; // let backend use smart sort
		params.limit = 1;
	} else if (peekPrevOrNext == "next") {
		if (params.sort == "ASC") {
			params.start_timestamp = peekFromItem.timestamp;
		} else {
			params.end_timestamp = peekFromItem.timestamp;
		}
		params.sort = null; // let backend use smart sort
		params.limit = 1;
	}

	console.log("PARAMS:", params)

	return params;
}
