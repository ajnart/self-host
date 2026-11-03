import { withPair } from "@ajnart/self-host-testkit";

const order = process.argv[2] ?? "consumer-first";
if (order !== "consumer-first" && order !== "target-first") {
	throw new Error("Choose consumer-first or target-first.");
}

await withPair(
	"homarr",
	"sonarr",
	async (pair) => {
		if ((await pair.inspect()).outcome !== "complete") {
			throw new Error(
				"Production Setup/Link has a failed or Manual prerequisite.",
			);
		}
		const response = await fetch(`${pair.target.url}/api/v3/system/status`, {
			headers: { "X-Api-Key": await pair.target.getApiKey() },
			signal: AbortSignal.timeout(10_000),
		});
		if (!response.ok || (await response.json()).appName !== "Sonarr") {
			throw new Error("Sonarr identity did not authenticate.");
		}
		const integrationId = await pair.consumer.getApiKey("linked_sonarr");
		const apiKey = await pair.consumer.getApiKey();
		const snapshot = async () => {
			const input = encodeURIComponent(
				JSON.stringify({ json: { integrationId } }),
			);
			const result = await fetch(
				`${pair.consumer.url}/api/trpc/widget.stats.snapshot?input=${input}`,
				{
					headers: { ApiKey: apiKey },
					signal: AbortSignal.timeout(10_000),
				},
			);
			if (!result.ok) throw new Error("Homarr Stats could not be read.");
			const envelope = await result.json();
			const sample = envelope.result?.data?.json;
			if (
				envelope.error ||
				!sample ||
				sample.error !== false ||
				sample.stale !== false ||
				!Number.isFinite(sample.updatedAt) ||
				sample.updatedAt <= 0 ||
				!["shows", "downloaded", "missing", "queued"].every(
					(key) =>
						Number.isInteger(sample.values?.[key]) && sample.values[key] >= 0,
				)
			) {
				throw new Error("Homarr did not return meaningful Sonarr Stats.");
			}
			return sample.updatedAt;
		};
		const before = await snapshot();
		if (
			(await pair.setup("homarr")).outcome !== "complete" ||
			(await snapshot()) <= before
		) {
			throw new Error("Production Link verification did not advance Stats.");
		}
		console.log("Production Homarr/Sonarr Link and advancing Stats verified.");
	},
	{ order },
);
