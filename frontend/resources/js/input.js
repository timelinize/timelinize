async function inputPageMain() {
	const qs = new URLSearchParams(window.location.search);

	const graph = await app.NextGraph(qs.get('repo_id'), qs.get('job_id'));

	console.log("GRAPH:", graph);
}