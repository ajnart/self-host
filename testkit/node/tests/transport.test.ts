import { afterEach, expect, test } from "bun:test";
import {
	chmodSync,
	mkdtempSync,
	readFileSync,
	rmSync,
	writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { inspect } from "node:util";
import type { SelfHost } from "../index";
import { createSelfHost } from "../index.mjs";

const directories: string[] = [];
const handles: SelfHost[] = [];

function fixture(timeoutMs = 1000) {
	const directory = mkdtempSync(join(tmpdir(), "testkit-transport-"));
	directories.push(directory);
	chmodSync(directory, 0o700);
	const binary = join(directory, "helper");
	const manifest = join(directory, "manifest.jsonl");
	const source = readFileSync(
		new URL("./fake-helper.mjs", import.meta.url),
		"utf8",
	);
	writeFileSync(binary, `#!${process.execPath}\n${source}`, { mode: 0o700 });
	const selfHost = createSelfHost({ binary, manifest, timeoutMs });
	handles.push(selfHost);
	return { selfHost, manifest };
}

function events(
	manifest: string,
): { method: string; owner: string; pid: number }[] {
	return readFileSync(manifest, "utf8")
		.trim()
		.split("\n")
		.map((line) => JSON.parse(line));
}

afterEach(async () => {
	await Promise.allSettled(handles.splice(0).map((handle) => handle.close()));
	for (const directory of directories.splice(0))
		rmSync(directory, { recursive: true, force: true });
});

test("fragmented private transport exposes the explicit App contract", async () => {
	const { selfHost, manifest } = fixture();
	const app = await selfHost.spinup("sonarr", { image: "peer:latest" });
	expect(app.url).toBe("http://127.0.0.1:12345");
	expect(await app.getApiKey()).toBe("private-test-api-key");
	expect((await app.info()).id).toBe("sonarr");
	const credentials = await app.credentials();
	expect(credentials.password).toBe("private-test-password");
	expect(JSON.stringify(credentials)).not.toContain("private-test-password");
	expect(inspect(credentials)).not.toContain("private-test-api-key");
	expect(JSON.stringify(selfHost)).toBe('"[SelfHost]"');
	expect(inspect(app)).toBe("[App]");
	await app.close();
	await app.close();
	await selfHost.close();
	await selfHost.close();
	expect(
		events(manifest).filter((event) => event.method === "close"),
	).toHaveLength(1);
	expect(
		events(manifest).filter((event) => event.method === "cleanup"),
	).toHaveLength(1);
	await expect(app.getApiKey()).rejects.toThrow("Fixture is not available.");
});

test("multiple Apps use separate owned helpers and close all", async () => {
	const { selfHost, manifest } = fixture();
	const [first, second] = await Promise.all([
		selfHost.spinup("sonarr"),
		selfHost.spinup("radarr"),
	]);
	expect((await first.info()).id).toBe("sonarr");
	expect((await second.info()).id).toBe("radarr");
	await selfHost.close();
	const starts = events(manifest).filter((event) => event.method === "spinup");
	expect(new Set(starts.map((event) => event.owner)).size).toBe(2);
	expect(new Set(starts.map((event) => event.pid)).size).toBe(2);
	expect(
		events(manifest).filter((event) => event.method === "cleanup"),
	).toHaveLength(2);
});

for (const app of [
	"malformed",
	"wrong-version",
	"oversized",
	"exit",
	"secret-error",
]) {
	test(`${app} startup fails safely and always invokes scoped cleanup`, async () => {
		const { selfHost, manifest } = fixture();
		let caught: unknown;
		try {
			await selfHost.spinup(app);
		} catch (error) {
			caught = error;
		}
		expect(caught).toBeInstanceOf(Error);
		expect(inspect(caught)).not.toContain("private-test-raw-secret");
		await selfHost.close();
		expect(
			events(manifest).filter((event) => event.method === "cleanup"),
		).toHaveLength(1);
	});
}

test("startup deadlines terminate the helper and recover its owned fixtures", async () => {
	const { selfHost, manifest } = fixture(20);
	await expect(selfHost.spinup("slow")).rejects.toThrow(
		"Fixture operation timed out.",
	);
	await selfHost.close();
	expect(
		events(manifest).filter((event) => event.method === "cleanup"),
	).toHaveLength(1);
});

test("cleanup failures remain sanitized and visible", async () => {
	const { selfHost } = fixture();
	await selfHost.spinup("cleanup-failure");
	await expect(selfHost.close()).rejects.toThrow(
		"Fixture cleanup failed; run manifest cleanup.",
	);
});

test("missing helper and invalid options do not expose executable paths", async () => {
	const { manifest } = fixture();
	const selfHost = createSelfHost({
		binary: "/private/nonexistent-testkit-helper",
		manifest,
	});
	handles.push(selfHost);
	await expect(selfHost.spinup("sonarr")).rejects.toThrow(
		"Fixture cleanup failed; run manifest cleanup.",
	);
	expect(() => createSelfHost({ timeoutMs: 900001 })).toThrow(
		"Invalid testkit protocol.",
	);
});
