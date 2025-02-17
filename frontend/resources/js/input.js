async function inputPageMain() {
	const qs = new URLSearchParams(window.location.search);
	const repoID = qs.get('repo_id');
	const jobID = Number(qs.get('job_id'));

	const graph = await app.NextGraph(repoID, jobID);
	console.log("FIRST GRAPH:", graph);

	await app.SubmitGraph(repoID, jobID, graph, false);

	const graph2 = await app.NextGraph(repoID, jobID);
	console.log("NEXT GRAPH:", graph2);
}