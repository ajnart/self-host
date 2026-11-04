import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { test } from "node:test";
import { type App, withApp } from "@ajnart/self-host-testkit";

function docker(...args: string[]) {
	try {
		return execFileSync("docker", args, {
			encoding: "utf8",
			stdio: ["ignore", "pipe", "ignore"],
			timeout: 10_000,
		}).trim();
	} catch {
		throw new Error("Docker fixture inspection failed.");
	}
}

function fixtureProject(app: App) {
	const container = docker(
		"ps",
		"--quiet",
		"--filter",
		"label=com.docker.compose.service=sonarr",
	);
	assert.match(container, /^[a-f0-9]{12,64}$/);
	assert.equal(docker("port", container, "8989/tcp"), new URL(app.url).host);
	const project = docker(
		"inspect",
		"--format",
		'{{index .Config.Labels "com.docker.compose.project"}}',
		container,
	);
	assert.match(project, /^testkit-[a-f0-9]{24}$/);
	return project;
}

function assertCleaned(project: string) {
	assert.match(project, /^testkit-[a-f0-9]{24}$/);
	const filter = `label=com.docker.compose.project=${project}`;
	assert.equal(docker("ps", "--all", "--quiet", "--filter", filter), "");
	assert.equal(docker("network", "ls", "--quiet", "--filter", filter), "");
}

test(
	"published Sonarr fixture authenticates and cleans up",
	{ timeout: 900_000 },
	async () => {
		let project = "";
		await withApp("sonarr", async (sonarr) => {
			project = fixtureProject(sonarr);
			const response = await fetch(`${sonarr.url}/api/v3/system/status`, {
				headers: { "X-Api-Key": await sonarr.getApiKey() },
				signal: AbortSignal.timeout(10_000),
			});
			assert.equal(response.ok, true);
			const status: unknown = await response.json();
			assert.ok(status !== null && typeof status === "object");
			assert.ok("appName" in status);
			assert.equal(status.appName, "Sonarr");
			assert.ok("version" in status);
			assert.equal(typeof status.version, "string");
			const rejected = await fetch(`${sonarr.url}/api/v3/system/status`, {
				headers: { "X-Api-Key": "invalid-smoke-test-key" },
				signal: AbortSignal.timeout(10_000),
			});
			assert.equal(rejected.status, 401);
		});
		assertCleaned(project);
	},
);

test(
	"callback failure preserves its error and cleans owned resources",
	{ timeout: 900_000 },
	async () => {
		let project = "";
		let cleanupFailed = false;
		const expected = new Error("Expected smoke callback failure.");
		await assert.rejects(
			withApp(
				"sonarr",
				(sonarr) => {
					project = fixtureProject(sonarr);
					throw expected;
				},
				{
					onCleanupError() {
						cleanupFailed = true;
					},
				},
			),
			(error: unknown) => error === expected,
		);
		assert.equal(cleanupFailed, false);
		assertCleaned(project);
	},
);
