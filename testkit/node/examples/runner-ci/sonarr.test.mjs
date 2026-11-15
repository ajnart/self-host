import assert from "node:assert/strict";
import { writeFileSync } from "node:fs";
import { test } from "node:test";
import { docker } from "./docker.mjs";

// Ordinary application tests: no SDK import, withApp, or fixture lifecycle hooks.
test(
	"injected Sonarr authenticates, rejects a bad key and has an empty library",
	{
		timeout: 60_000,
	},
	async () => {
		const url = process.env.SONARR_URL;
		const apiKey = process.env.SONARR_API_KEY;
		assert.ok(
			url && apiKey,
			"Runner did not supply the configured environment.",
		);
		const address = new URL(url);
		assert.equal(address.protocol, "http:");
		assert.equal(address.hostname, "127.0.0.1");
		const container = docker(
			"ps",
			"--quiet",
			"--filter",
			"label=com.docker.compose.service=sonarr",
		);
		assert.match(container, /^[a-f0-9]{12,64}$/);
		assert.equal(docker("port", container, "8989/tcp"), address.host);
		const info = JSON.parse(
			docker(
				"inspect",
				"--format",
				'{"project":{{json (index .Config.Labels "com.docker.compose.project")}},"imageId":{{json .Image}},"memory":{{json .HostConfig.Memory}},"nanoCPUs":{{json .HostConfig.NanoCpus}},"pids":{{json .HostConfig.PidsLimit}}}',
				container,
			),
		);
		assert.match(info.project, /^testkit-[a-f0-9]{24}$/);
		assert.match(info.imageId, /^sha256:[a-f0-9]{64}$/);
		const owned = docker(
			"ps",
			"--all",
			"--quiet",
			"--filter",
			`label=com.docker.compose.project=${info.project}`,
		);
		assert.equal(
			owned,
			container,
			"The acceptance scope is exactly one container.",
		);
		assert.ok(info.memory > 0 && info.memory <= 2 ** 30);
		assert.ok(info.nanoCPUs > 0 && info.nanoCPUs <= 1e9);
		assert.ok(info.pids > 0 && info.pids <= 512);
		// This non-secret witness lets the next step prove cleanup of this exact Project.
		writeFileSync("witness.json", JSON.stringify(info), { mode: 0o600 });
		async function request(path, key = apiKey) {
			return fetch(new URL(path, url), {
				headers: { "X-Api-Key": key },
				signal: AbortSignal.timeout(10_000),
			});
		}
		const response = await request("/api/v3/system/status");
		assert.equal(response.status, 200);
		const status = await response.json();
		assert.equal(status.appName, "Sonarr");
		assert.equal(typeof status.version, "string");
		assert.ok(status.version.length > 0);
		assert.equal(
			(await request("/api/v3/system/status", "invalid-runner-test-key"))
				.status,
			401,
		);
		const series = await request("/api/v3/series");
		assert.equal(series.status, 200);
		assert.deepEqual(await series.json(), []);
	},
);
