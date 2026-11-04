import type { APIRoute } from "astro";
import catalog from "../../data/catalog.json";

export const GET: APIRoute = () =>
	new Response(
		JSON.stringify({
			schemaVersion: 1,
			label: "Apps",
			message: String(catalog.counts.apps),
			color: "00d787",
			labelColor: "14121e",
		}) + "\n",
		{ headers: { "Content-Type": "application/json" } },
	);
