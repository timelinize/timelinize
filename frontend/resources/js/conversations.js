// holds the conversation items that are rendered on the screen (in the same order)
var messages = [];

// avoids a logical race condition so that only one new message chunk is loaded at
// a time (like while scrolling quickly)
var _rendering = false;

// if true, the next scroll event on the content column will not fire event handlers
var _preventScrollEvent = true;

// if true, the conversation view is at the very end, or very beginning, respectively,
// of the entire conversation (i.e. no more messages to load in that direction)
var _veryEnd = true, _veryBeginning = false;

async function conversationsPageMain() {
	$('.tl-date-picker').append(newDatePicker({
		passThru: {
			range: true
		},
		rangeToggle: true,
		time: false,
		timeToggle: true,
		proximity: true,
		sort: false,
		vertical: true
	}));

	const entitySelect = newEntitySelect('.entity-input', 20);
	
	entitySelect.on('change', async () => {
		if (entitySelect.getValue().length == 0) {
			$('#selected-entities-only').checked = false;
			$('#selected-entities-only').disabled = true;
		} else {
			$('#selected-entities-only').disabled = false;
		}
	});
	
	$('#selected-entities-only').checked = queryParam("only_entity") == "true";

	$('.content-column').addEventListener('scroll', async (event) => {
		// only load more into the conversation view when a conversation is being viewed
		if ($('#convo-container').classList.contains('d-none')) {
			return false;
		}

		// this behavior can be undesirable at times, like when the conversation
		// is first loaded and scrolled to the end; only react to user scrolling
		if (_preventScrollEvent) {
			_preventScrollEvent = false;
			return false;
		}

		if (event.target.scrollTop == 0 && !_veryBeginning) {
			// fun little hack: if scrollTop is 0 when new elements are *prepended*, the
			// scrollTop stays 0 (which is jarring for the user who is scrolling up,
			// since they've now skipped all the newly-loaded content which is now
			// below the top of the container), but if the scrollTop is not 0, the
			// elements are displayed above the current position, as expected
			//
			// ... LOLOLOL, this is unfortunate. Apparently *ONLY* webkit does not support
			// overflow-anchor (default is "auto") which is what keeps the scroll position
			// relative to content... sigh... so I've disabled the auto overflow-anchor
			// (for the benefit of viewing in other browsers) and am manually preserving
			// the scroll position with JS. Oh well.
			event.target.scrollTop = 1;
			await renderConversationChunk("older");
			return;
		}
		if (event.target.scrollHeight - event.target.scrollTop - event.target.clientHeight < 1 && !_veryEnd) {
			await renderConversationChunk("newer");
			return;
		}
	});
}





function sameEntities(qsEntities, entitySelect) {
	selectedEntities = entitySelect.getValue();
	if (qsEntities.length != selectedEntities.length) {
		return false;
	}
	for (let i = 0; i < qsEntities.length; i++) {
		if (qsEntities[i] != selectedEntities[i]) {
			return false;
		}
	}
	return true;
}

function entitiesInvolved() {
	let entityIDs = $('.entity-input').tomselect.getValue();
	if (!entityIDs?.length) return; // length 0 in our UI means "any" but in the backend it means "none"
	if (!Array.isArray(entityIDs)) entityIDs = [entityIDs];
	entityIDs = entityIDs.map(Number);
	return entityIDs;
}

function itemClasses() {
	let types = [];
	if ($('#type-message').checked) types.push($('#type-message').value);
	if ($('#type-email').checked) types.push($('#type-email').value);
	if ($('#type-social-media').checked) types.push($('#type-social-media').value);
	return types;
}

function messageSubstring() {
	return $('#message-substring').value ? [$('#message-substring').value] : null;
}

async function renderConversationsPage() {
	// load the initial entities involved so they can be displayed in the selector, only if they aren't already
	const qsEntities = queryParam("entity");
	if (qsEntities) {
		const initialEntities = qsEntities.split(',').map(Number);
		const entitySelect = $('.entity-input').tomselect;
		// only query the DB if the entity select needs updating
		if (!sameEntities(initialEntities, entitySelect)) {
			const entities = await app.SearchEntities({
				repo: tlz.openRepos[0].instance_id,
				row_id: initialEntities
			});
			entities.map((v) => entitySelect.addOption(v));
			entities.map((v) => entitySelect.addItem(v.id));
			$('#selected-entities-only').disabled = false;
		}
	}

	const singleConversation = $('#selected-entities-only').checked;

	if (singleConversation) {
		await renderSingleConversation();
	} else {
		await renderConversations();
	}
}




async function renderConversations() {
	$('#convos-container').classList.remove('d-none');
	$('#convo-container').classList.add('d-none');
	$('#showing-info').classList.remove('d-none');
	$('.page-title').classList.remove('mb-4');

	const datePicker = $('.date-input').datepicker;

	const params = {
		data_text: messageSubstring(),
		limit: 30,
	};
	commonFilterSearchParams(params);

	const start = DateTime.now();
	const results = await app.LoadRecentConversations(params);
	const end = DateTime.now();
	const duration = end.diff(start, 'seconds');

	console.log("RECENT CONVERSATIONS:", results);

	// delete prior results
	$('#convos-container').replaceChildren();

	$('#convos-count').innerText = results.length;
	$('#convos-duration').innerText = duration.toHuman();

	// TODO: display something nice if there's no results

	for (const convo of results) {
		// put repo owner first; they're most likely to be in all the conversations,
		// and the last entity in the list has the full avatar shown in the stack,
		// and it's more useful to show the other participants more prominently
		convo.entities.sort((a, b) => a.id == 1 ? -1 : 1);

		const elem = cloneTemplate('#tpl-convo');

		// compile the list of names to display, some of which will be put into tooltips,
		// but have limits on how much we show
		let renderNames = [];
		let tooltipNames = [];
		const maxRenderNames = 3;
		const maxTooltipNames = 10;
		for (const entity of convo.entities) {
			const display = entity.name || entity.attributes[0].value;
			const displayTooltip = entity.name ? entity.attributes[0].value : "";

			if (renderNames.length < maxRenderNames) {
				renderNames.push(`<a href="/entities/${tlz.openRepos[0].instance_id}/${entity.id}" title="${displayTooltip}">${display}</a>`);
			} else {
				if (tooltipNames.length < maxTooltipNames) {
					tooltipNames.push(display);
				} else if (tooltipNames.length == maxTooltipNames) {
					tooltipNames.push(`+${convo.entities.length-maxRenderNames-maxTooltipNames} more`);
					break;
				}
			}
		}
		if (convo.entities.length > maxRenderNames) {
			renderNames.push(`<span title="${tooltipNames.join(", ")}">+${convo.entities.length-maxRenderNames} more</span>`);
		}
		$('.card-title', elem).innerHTML = renderNames.join(", ");

		// TODO: what if message is media (or has it attached)
		// TODO: richer preview, all around...
		const preview = maxlenStr(convo.messages[0].data_text, 200);
		$('.convo-preview', elem).innerText = preview;
		$('.convo-datetime', elem).innerText = DateTime.fromISO(convo.messages[0].timestamp, { setZone: true }).toLocaleString(DateTime.DATETIME_MED);

		// render avatars
		const maxAvatars = 5;
		let more = "";
		let entitiesWithAvatars = convo.entities;
		if (convo.entities.length > maxAvatars) {
			more = `<span
					title="${convo.entities.length-maxAvatars} more"
					class="avatar avatar-sm avatar-rounded">
					+${convo.entities.length-maxAvatars}
				</span>`;
			entitiesWithAvatars = convo.entities.slice(0, maxAvatars);
		}
		const avatars = entitiesWithAvatars.map((e)=>`<a href="/entities/${tlz.openRepos[0].instance_id}/${e.id}" title="${e.name || e.attributes[0].value}">${avatar(true, e, "avatar-sm avatar-rounded")}</a>`).join("");
		$('.avatar-list', elem).innerHTML = avatars+more;

		// attach the conversation info to the card so we can fill out the filter when clicked
		elem.conversation = convo;
		
		$('#convos-container').append(elem);
	}
}


async function renderSingleConversation() {
	$('#convos-container').classList.add('d-none');
	$('#showing-info').classList.add('d-none');
	$('.page-title').classList.add('mb-4');
	$('#convo-container .chat-bubbles').replaceChildren();

	await renderConversationChunk();

	$('#convo-container').classList.remove('d-none');

	// scroll to bottom, but don't allow it to trigger loading more
	await forMediaToRender();
	_preventScrollEvent = true;
	$('.content-column').scrollTop = $('.content-column').scrollHeight;
}

async function forMediaToRender() {
	await Promise.all([...$$('.chat-item img.content, .chat-item video.content')].map(async (elem) => {
		while (!elem.offsetHeight) {
			await new Promise(requestAnimationFrame);
		}
	}));
}

async function renderConversationChunk(direction) {
	if (_rendering) return;

	_rendering = true;

	try {
		let since, until;
		if (messages.length > 0 && direction) {
			 if (direction == "newer") {
				since = messages[messages.length-1].timestamp;
			} else if (direction == "older") {
				until = messages[0].timestamp;
			}
		}

		const sort = direction == "newer" ? "ASC" : "DESC";
		const limit = 50;

		const params = {
			data_text: messageSubstring(),
			related: 1,
			sort: sort,
			limit: limit,
		};
		commonFilterSearchParams(params);
		// constrain conversation to be within selected date/time
		if (!params.start_timestamp || new Date(since) > new Date(params.start_timestamp)) {
			params.start_timestamp = since;
		}
		if (!params.end_timestamp || new Date(until) < new Date(params.end_timestamp)) {
			params.end_timestamp = until;
		}
		console.log("PARAMS:", params)
		const results = await app.LoadConversation(params);
		console.log("RESULTS:", results)
		
		
		// TODO: display some filler/information if there's no items
		if (!results.items) {
			if (direction == "newer") {
				_veryEnd = true;
			} else if (direction == "older") {
				_veryBeginning = true;
			}
			return;
		}

		// Because of how the database query works, we have to query newer messages
		// in ASC order since our newest timestamp and older messages in DESC order
		// starting with our oldest timestamp. We then iterate the messages and
		// append newest or prepend oldest. The logic works out so that we have to
		// reverse the order returned by the DB in order to display them in the
		// right sequence.
		// TODO: make message order configurable
		results.items.reverse();

		// (only used on WebKit/Safari, until their Technology Preview ships to production)
		// we are going to be adding content to the screen, but should preserve the
		// user's scroll position - ideally the browser should do this for us, and it
		// does for any scrollTop that isn't 0 since the overflow-anchor property
		// defaults to "auto" -- HOWEVER that is not supported in Webkit, go figure...
		// so what we do instead is grab a reference to the element at the relevent
		// end of the container, and after we finish adding and deleting elements, we
		// scroll to that element's new position ("offsetTop"); if at the end, we have
		// to add the difference between its height and its container's height, so that
		// the *bottom* of that element is at the bottom of the container, but it works!
		const scrollTargetElem = $(`#convo-container .chat-item${direction == "older" ? ":first-child" : ":last-child"}`);

		for (const item of results.items) {
			// disable lazy loading because Safari won't give media elements their
			// offsetHeight until they are lazy-loaded, which means that the
			// "await forMediaToRender" call will block forever while it waits
			// for loading that will never happen first... sigh...
			// TODO: It'd be nice to still lazy-load images in a way that works in poor old Safari
			const elem = renderMessageItem(item, {noLazyLoading: true});
			if (direction == "newer") {
				$('#convo-container .chat-bubbles').append(elem);
				messages.push(item);
			} else {
				$('#convo-container .chat-bubbles').prepend(elem);
				messages.unshift(item);
			}
		}

		// as we scroll indefinitely, clear out messages from memory and the DOM that
		// are no longer visible or nearby in the opposite direction we're scrolling
		const maxMessages = limit * 3;
		if (messages.length > maxMessages && direction) {
			const messageElems = $$('#convo-container .chat-item');
			if (direction == "older") {
				// clear out newest messages
				messages.splice(-limit);
				for (let i = messageElems.length-limit; i < messageElems.length; i++) {
					messageElems[i].remove();
				}
				_veryEnd = false;
			} else {
				// clear out oldest messages
				messages.splice(0, limit);
				for (let i = 0; i < limit; i++) {
					messageElems[i].remove();
				}
				_veryBeginning = false;
			}
		}

		// Safari does not support overflow-anchor: auto, which is not only supported by
		// all other modern browsers, it is the DEFAULT in all other browsers. So we have
		// this shim that we employ only on browsers that don't support it (Safari, cough).
		// As described above, don't lose the user's scroll/read position; we just
		// added and deleted a bunch of elements, so it's quite likely they're getting
		// tossed around: but we can effectively coerce "overflow-anchor: auto" by
		// restoring the scrollTop to the previous position, while accounting for the
		// fact that the actual number of that scrollTop is likely different now
		if (scrollTargetElem && !CSS.supports("overflow-anchor", "none")) {
			// I did find that we have to wait for the media elements to load so we can get
			// an accurate reading of their effect on our target element's offsetTop
			await forMediaToRender();

			// finally, restore scroll position by setting it to its new offsetTop: this should look
			// the same to the user, and if it happens fast enough, they won't even see any thrashing
			_preventScrollEvent = true;
			if (direction == "older") {
				$('.content-column').scrollTop = scrollTargetElem.offsetTop;
			} else {
				$('.content-column').scrollTop = scrollTargetElem.offsetTop + scrollTargetElem.offsetHeight - $('.content-column').offsetHeight;
			}
		}

		activateTooltips();

		// TODO: error handling
	} /*catch(err) {
		console.error("caught:", err);
	}*/ finally {
		_rendering = false;
	}
}

// update filters when convo card is clicked; the conversations page with the convo cards is basically a glorified filter control
on('click', '#convos-container .card-link', event => {
	const convoCard = event.target.closest('.convo-card');
	const ts = $('.entity-input').tomselect;
	ts.clear();
	ts.clearOptions();
	for (const entity of convoCard.conversation.entities) {
		console.log("ADDING ENTITY:", entity.id, entity.name);
		ts.addOption(entity);
		ts.addItem(entity.id, true); // true = don't fire event (the updated filter gets submitted later)
	}
	$('#selected-entities-only').disabled = false;
	$('#selected-entities-only').checked = true;
	trigger($('#selected-entities-only'), 'change');
});

// reset state when the filter changes, since we may or may not
// still be at the end or beginning of the conversation
on('change', '.filter', e => {
	_veryBeginning = false;
	_veryEnd = false;
});

// avoid resetting entire page state, all we need to do is go back to the list of
// convos, but keep the rest of the filter params
on('click', '#conversations-reset', event => {
	const ts = $('.entity-input').tomselect;
	ts.clear();
	ts.clearOptions();
	$('#selected-entities-only').checked = false;
	$('#selected-entities-only').disabled = true;
	trigger($('#selected-entities-only'), 'change');
});

// Handle the clear filters button
on('click', '#clear-filter', event => {
	event.preventDefault();
	
	// Clear all filter inputs
	const ts = $('.entity-input').tomselect;
	ts.clear();
	ts.clearOptions();
	
	$('#selected-entities-only').checked = false;
	$('#selected-entities-only').disabled = true;
	
	$('#message-substring').value = '';
	
	// Clear date picker
	if ($('.date-input').datepicker) {
		$('.date-input').datepicker.clear();
	}
	
	// Clear data source selector
	if ($('.tl-data-source').tomselect) {
		$('.tl-data-source').tomselect.clear();
	}
	
	// Reset item type checkboxes to checked
	$$('.tl-item-class-dropdown input[type="checkbox"]').forEach(cb => cb.checked = true);
	
	// Trigger the filter change to update results
	$('.filter').dispatchEvent(new Event('change', { bubbles: true }));
});
