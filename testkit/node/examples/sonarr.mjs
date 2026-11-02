import { createSelfHost } from "@self-host/testkit";

const selfHost = createSelfHost();

try {
	const sonarr = await selfHost.spinup("sonarr");
	const response = await fetch(`${sonarr.url}/api/v3/system/status`, {
		headers: { "X-Api-Key": await sonarr.getApiKey() },
		signal: AbortSignal.timeout(10_000),
	});
	if (!response.ok)
		throw new Error("Sonarr rejected the authenticated request.");
	const status = await response.json();
	if (status.appName !== "Sonarr" || typeof status.version !== "string") {
		throw new Error("Sonarr returned an unexpected identity.");
	}
	console.log(`Connected to Sonarr ${status.version}.`);
} finally {
	await selfHost.close();
}
