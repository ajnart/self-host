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
import {
	type SelfHost,
	TestkitError,
	createSelfHost,
	withApp,
	withPair,
} from "../index.mjs";

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
	return { selfHost, manifest, binary };
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
		if (app === "secret-error") {
			expect(caught instanceof TestkitError && caught.reason).toBe("SETUP");
		}
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
		"Testkit helper not found.",
	);
	expect(() => createSelfHost({ timeoutMs: 900001 })).toThrow(
		"Invalid testkit protocol.",
	);
});

for (const order of ["consumer-first", "target-first"] as const) {
	test(`pair ${order} shares transport and owns lifecycle`, async () => {
		const { selfHost, manifest } = fixture();
		const pair = await selfHost.spinupPair("consumer", "peer", { order });
		expect(pair.initial.outcome).toBe("prerequisite");
		expect(pair.initial.apps.filter((app) => app.installed)[0]?.id).toBe(
			order === "consumer-first" ? "consumer" : "peer",
		);
		expect("close" in pair.consumer).toBe(false);
		expect((await pair.target.info()).id).toBe("peer");
		expect((await pair.inspect()).outcome).toBe("complete");
		expect(JSON.stringify(pair)).toBe('"[Pair]"');
		expect(inspect(await pair.consumer.credentials())).toBe("[Credentials]");
		await expect(pair.remove("outside")).rejects.toThrow(
			"Fixture is not available.",
		);
		expect((await pair.remove("peer")).outcome).toBe("prerequisite");
		await expect(pair.target.info()).rejects.toThrow(
			"Fixture is not available.",
		);
		expect((await pair.install("peer")).outcome).toBe("complete");
		expect((await pair.setup()).outcome).toBe("complete");
		await Promise.all([pair.close(), selfHost.close()]);
		expect(
			events(manifest).filter((event) => event.method === "spinupPair"),
		).toHaveLength(1);
		expect(
			events(manifest).filter((event) => event.method === "cleanup"),
		).toHaveLength(1);
		await expect(pair.inspect()).rejects.toThrow();
	});
}

test("pair malformed inspection and selected options fail explicitly", async () => {
	const { selfHost } = fixture();
	await expect(
		selfHost.spinupPair("peer", "peer", { order: "consumer-first" }),
	).rejects.toThrow("Invalid testkit protocol.");
	await expect(
		selfHost.spinupPair("consumer", "peer", {
			order: "consumer-first",
			images: { outside: "image:latest" },
		}),
	).rejects.toThrow("Invalid testkit protocol.");
	const pair = await selfHost.spinupPair("bad-inspection", "peer", {
		order: "consumer-first",
	});
	await expect(pair.inspect()).rejects.toThrow("Invalid testkit protocol.");
});

test("scoped pair preserves undefined failure and reports cleanup separately", async () => {
	const { binary, manifest } = fixture();
	let notified = false;
	let caught = false;
	try {
		await withPair(
			"cleanup-failure",
			"peer",
			() => {
				throw undefined;
			},
			{
				binary,
				manifest,
				timeoutMs: 1000,
				order: "consumer-first",
				onCleanupError(error) {
					notified = error.code === "CLEANUP";
					throw new Error("secondary callback failure");
				},
			},
		);
	} catch (error) {
		caught = true;
		expect(error).toBeUndefined();
	}
	expect(caught).toBe(true);
	expect(notified).toBe(true);
	await expect(
		withPair("cleanup-failure", "peer", () => 1, {
			binary,
			manifest,
			timeoutMs: 1000,
			order: "target-first",
		}),
	).rejects.toThrow("Fixture cleanup failed");
	expect(
		await withPair("consumer", "peer", () => 42, {
			binary,
			manifest,
			timeoutMs: 1000,
			order: "consumer-first",
		}),
	).toBe(42);
	const primary = new Error("primary");
	await expect(
		withApp(
			"cleanup-failure",
			() => {
				throw primary;
			},
			{ binary, manifest, timeoutMs: 1000 },
		),
	).rejects.toBe(primary);
});

test("pair action failures retain typed primary causes and cleanup status", async () => {
	for (const consumer of [
		"action-caps",
		"action-timeout",
		"action-cancel",
		"action-cleanup",
	]) {
		const { selfHost, manifest } = fixture();
		const pair = await selfHost.spinupPair(consumer, "peer", {
			order: "consumer-first",
		});
		await pair.remove("peer");
		let caught: unknown;
		try {
			await pair.install("peer");
		} catch (error) {
			caught = error;
		}
		expect(caught).toBeInstanceOf(TestkitError);
		if (!(caught instanceof TestkitError))
			throw new Error("Missing typed action error");
		let code = "OPERATION";
		if (consumer === "action-timeout") code = "TIMEOUT";
		if (consumer === "action-cancel") code = "CANCELED";
		expect(caught.code).toBe(code);
		if (code === "OPERATION") expect(caught.reason).toBe("LIMITS");
		expect(caught.cleanupFailed).toBe(consumer === "action-cleanup");
		expect(inspect(caught)).not.toContain("private-test-raw-secret");
		expect(
			events(manifest).filter((event) => event.method === "owned-cleanup"),
		).toHaveLength(1);
		await expect(pair.inspect()).rejects.toThrow("Fixture is not available.");
	}
});

test("shared scoped lifetime preserves single-App returns and both APIs' primitive failures", async () => {
	const { binary, manifest } = fixture();
	const options = { binary, manifest, timeoutMs: 1000 };
	expect(await withApp("sonarr", async () => 0, options)).toBe(0);
	expect(await withApp("sonarr", () => undefined, options)).toBeUndefined();
	await expect(withApp("cleanup-failure", () => 1, options)).rejects.toThrow(
		"Fixture cleanup failed",
	);
	for (const pair of [false, true]) {
		for (const primary of [undefined, 0]) {
			let caught = false;
			const callback = async () => {
				throw primary;
			};
			try {
				if (pair)
					await withPair("cleanup-failure", "peer", callback, {
						...options,
						order: "consumer-first",
						onCleanupError: async () => {
							throw new Error("secondary notification");
						},
					});
				else await withApp("cleanup-failure", callback, options);
			} catch (error) {
				caught = true;
				expect(error).toBe(primary);
			}
			expect(caught).toBe(true);
		}
	}
});
