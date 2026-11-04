import { afterEach, expect, test } from "bun:test";
import {
	chmodSync,
	mkdtempSync,
	readFileSync,
	rmSync,
	unlinkSync,
	writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { inspect } from "node:util";
import {
	type Pair,
	type SelfHost,
	TestkitError,
	createSelfHost,
	withApp,
	withPair,
} from "../index.mjs";

const directories: string[] = [];
const handles: SelfHost[] = [];

function fixture(timeoutMs = 1000, mode = "valid", cleanupFails = false) {
	const directory = mkdtempSync(join(tmpdir(), "testkit-transport-"));
	directories.push(directory);
	chmodSync(directory, 0o700);
	const binary = join(directory, "helper");
	const manifest = join(directory, "manifest.jsonl");
	const source = readFileSync(
		new URL("./fake-helper.mjs", import.meta.url),
		"utf8",
	);
	const selected = source
		.replace('const mode = "valid";', `const mode = ${JSON.stringify(mode)};`)
		.replace(
			"const cleanupFails = false;",
			`const cleanupFails = ${cleanupFails};`,
		);
	writeFileSync(binary, `#!${process.execPath}\n${selected}`, { mode: 0o700 });
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

function protocolFailure(
	error: unknown,
	manifest: string,
	cleanupFailed: boolean,
) {
	expect(error instanceof TestkitError).toBe(true);
	if (!(error instanceof TestkitError))
		throw new Error("Missing typed protocol failure");
	expect(error.code).toBe("PROTOCOL");
	expect(error.reason).toBeUndefined();
	expect(error.cause).toBeUndefined();
	expect(error.cleanupFailed).toBe(cleanupFailed);
	const output = inspect(error) + JSON.stringify(error);
	expect(
		[manifest, "private-test-api-key", "private-test-password"].some((value) =>
			output.includes(value),
		),
	).toBe(false);
}

function ownedCleanup(manifest: string) {
	const log = events(manifest);
	const start = log.find((event) => event.method === "spinupPair");
	const cleanup = log.filter((event) => event.method === "cleanup");
	expect(start).toBeDefined();
	expect(cleanup).toHaveLength(1);
	expect(cleanup[0]?.owner).toBe(start?.owner);
	expect(cleanup[0]?.pid).not.toBe(start?.pid);
	expect(log.at(-1)).toEqual({ ...cleanup[0], method: "cleanup-complete" });
	if (!start) throw new Error("Missing Pair owner");
	return start.owner;
}

async function disabledPair(pair: Pair, manifest: string) {
	const before = events(manifest).length;
	const actions: (() => Promise<unknown>)[] = [
		() => pair.inspect(),
		() => pair.setup("homarr"),
		() => pair.remove("sonarr"),
		() => pair.install("sonarr"),
		() => pair.target.info(),
		() => pair.target.getApiKey(),
		() => pair.target.credentials(),
		() => pair.consumer.info(),
		() => pair.consumer.getApiKey(),
		() => pair.consumer.credentials(),
	];
	for (const action of actions) {
		let caught: unknown;
		try {
			await action();
		} catch (error) {
			caught = error;
		}
		expect(caught instanceof TestkitError).toBe(true);
	}
	expect(events(manifest).length).toBe(before);
}

afterEach(async () => {
	await Promise.allSettled(handles.splice(0).map((handle) => handle.close()));
	for (const directory of directories.splice(0))
		rmSync(directory, { recursive: true, force: true });
});

test("pair startup timeout preserves its code when cleanup also fails", async () => {
	const { selfHost, manifest } = fixture(1000, "valid", true);
	let caught: unknown;
	try {
		await selfHost.spinupPair("slow", "peer", { order: "consumer-first" });
	} catch (error) {
		caught = error;
	}
	expect(caught).toBeInstanceOf(TestkitError);
	if (!(caught instanceof TestkitError))
		throw new Error("Missing timeout failure");
	expect(caught.code).toBe("TIMEOUT");
	expect(caught.cleanupFailed).toBe(true);
	expect(caught.reason).toBeUndefined();
	ownedCleanup(manifest);
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
	const { selfHost, manifest } = fixture();
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
	let caught: unknown;
	try {
		await pair.inspect();
	} catch (error) {
		caught = error;
	}
	protocolFailure(caught, manifest, false);
	ownedCleanup(manifest);
	await disabledPair(pair, manifest);
});

for (const method of ["inspect", "setup", "remove", "install"] as const) {
	for (const cleanupFailed of [false, true]) {
		test(`pair malformed ${method} result awaits cleanup, failure=${cleanupFailed}`, async () => {
			const { selfHost, manifest } = fixture(
				1000,
				`bad-${method}`,
				cleanupFailed,
			);
			const pair = await selfHost.spinupPair("homarr", "sonarr", {
				order: "consumer-first",
			});
			let caught: unknown;
			try {
				if (method === "inspect") await pair.inspect();
				else await pair[method]("sonarr");
			} catch (error) {
				caught = error;
			}
			protocolFailure(caught, manifest, cleanupFailed);
			const owner = ownedCleanup(manifest);
			await disabledPair(pair, manifest);
			if (cleanupFailed) {
				await expect(pair.close()).rejects.toThrow("Fixture cleanup failed");
				await disabledPair(pair, manifest);
				unlinkSync(`${manifest}.${owner}.failed`);
			}
			await pair.close();
			await disabledPair(pair, manifest);
			let attempts = 1;
			if (cleanupFailed) attempts = 3;
			expect(
				events(manifest).filter((event) => event.method === "cleanup"),
			).toHaveLength(attempts);
		});
	}
}

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

for (const method of ["getApiKey", "credentials", "info"] as const) {
	test(`pair ${method} preserves credential failure kinds and cleanup status`, async () => {
		for (const mode of ["missing", "storage", "timeout", "cancel", "cleanup"]) {
			const { selfHost, manifest } = fixture(1000, `credential-${mode}`);
			const pair = await selfHost.spinupPair("homarr", "sonarr", {
				order: "consumer-first",
			});
			let caught: unknown;
			try {
				await pair.target[method]();
			} catch (error) {
				caught = error;
			}
			expect(caught).toBeInstanceOf(TestkitError);
			if (!(caught instanceof TestkitError))
				throw new Error("Missing typed credential error");
			let code = "OPERATION";
			if (mode === "missing") code = "CREDENTIALS";
			if (mode === "missing" && method === "info") code = "INFO";
			if (mode === "timeout") code = "TIMEOUT";
			if (mode === "cancel") code = "CANCELED";
			expect(caught.code).toBe(code);
			expect(caught.cleanupFailed).toBe(mode === "cleanup");
			if (code === "OPERATION") expect(caught.reason).toBe("STORAGE");
			else expect(caught.reason).toBeUndefined();
			expect(inspect(caught)).not.toContain("private-test-password");
			const log = events(manifest);
			const owned = log.filter((event) => event.method === "owned-cleanup");
			if (mode === "missing") {
				expect(owned).toHaveLength(0);
				expect((await pair.inspect()).outcome).toBe("complete");
			} else {
				expect(owned).toHaveLength(1);
				await expect(pair.inspect()).rejects.toThrow(
					"Fixture is not available.",
				);
			}
			expect(log.filter((event) => event.method === "cleanup")).toHaveLength(0);
			if (mode === "cleanup") {
				await expect(pair.close()).rejects.toThrow("Fixture cleanup failed");
				const owner = log.find((event) => event.method === "spinupPair")?.owner;
				unlinkSync(`${manifest}.${owner}.failed`);
			}
			await pair.close();
		}
	});
}

for (const method of ["getApiKey", "credentials", "info"] as const) {
	for (const shape of ["null", "shape"]) {
		for (const selected of ["consumer", "target"] as const) {
			for (const cleanupFailed of [false, true]) {
				test(`pair selected ${selected} ${method} ${shape} result poisons owner, cleanup failure=${cleanupFailed}`, async () => {
					const { selfHost, manifest } = fixture(
						1000,
						`bad-view-${method}-${shape}`,
						cleanupFailed,
					);
					const pair = await selfHost.spinupPair("homarr", "sonarr", {
						order: "consumer-first",
					});
					let caught: unknown;
					try {
						await pair[selected][method]();
					} catch (error) {
						caught = error;
					}
					protocolFailure(caught, manifest, cleanupFailed);
					const owner = ownedCleanup(manifest);
					await disabledPair(pair, manifest);
					if (cleanupFailed) {
						await expect(pair.close()).rejects.toThrow(
							"Fixture cleanup failed",
						);
						await disabledPair(pair, manifest);
						unlinkSync(`${manifest}.${owner}.failed`);
					}
					await pair.close();
					await disabledPair(pair, manifest);
					let attempts = 1;
					if (cleanupFailed) attempts = 3;
					expect(
						events(manifest).filter((event) => event.method === "cleanup"),
					).toHaveLength(attempts);
				});
			}
		}
		test(`single App malformed ${method} ${shape} result keeps legacy owner live`, async () => {
			const { selfHost, manifest } = fixture(
				1000,
				`bad-view-${method}-${shape}`,
			);
			const app = await selfHost.spinup("sonarr");
			let caught: unknown;
			try {
				await app[method]();
			} catch (error) {
				caught = error;
			}
			protocolFailure(caught, manifest, false);
			if (method === "info") expect(await app.getApiKey()).toBeDefined();
			else expect((await app.info()).id).toBe("sonarr");
			expect(
				events(manifest).filter((event) => event.method === "cleanup"),
			).toHaveLength(0);
			await app.close();
		});
	}
}

for (const mode of [
	"info-id",
	"info-sibling-id",
	"info-remote",
	"info-https",
	"info-invalid",
	"info-port",
	"info-sibling-url",
	"info-query",
	"info-path",
]) {
	for (const selected of ["consumer", "target"] as const) {
		for (const cleanupFailed of [false, true]) {
			test(`pair selected Info scope ${mode} ${selected}, cleanup failure=${cleanupFailed}`, async () => {
				const { selfHost, manifest } = fixture(1000, mode, cleanupFailed);
				const pair = await selfHost.spinupPair("homarr", "sonarr", {
					order: "consumer-first",
				});
				let caught: unknown;
				try {
					await pair[selected].info();
				} catch (error) {
					caught = error;
				}
				protocolFailure(caught, manifest, cleanupFailed);
				const owner = ownedCleanup(manifest);
				await disabledPair(pair, manifest);
				if (cleanupFailed) {
					await expect(pair.close()).rejects.toThrow("Fixture cleanup failed");
					unlinkSync(`${manifest}.${owner}.failed`);
				}
				await pair.close();
				await disabledPair(pair, manifest);
			});
		}
	}
}

test("pair selected Info preserves cached fixture paths and metadata", async () => {
	const { selfHost } = fixture(1000, "valid-paths");
	const pair = await selfHost.spinupPair("homarr", "sonarr", {
		order: "consumer-first",
	});
	for (const selected of [pair.consumer, pair.target]) {
		const info = await selected.info();
		expect(info.url).toBe(selected.url);
		expect(info.url).toEndWith("/app/");
		expect(info.image).toBe("peer:latest");
		expect(info.imageId).toBe("sha256:test");
		expect(info.repoDigests).toEqual(["peer@sha256:test"]);
	}
	expect((await pair.inspect()).outcome).toBe("complete");
});

for (const mode of ["info-id", "info-remote"]) {
	test(`single App Info scope ${mode} retains legacy type-only validation`, async () => {
		const { selfHost, manifest } = fixture(1000, mode);
		const app = await selfHost.spinup("sonarr");
		const info = await app.info();
		if (mode === "info-id") expect(info.id).toBe("outside");
		else expect(info.url).toBe("http://example.invalid:12345");
		expect(await app.getApiKey()).toBeDefined();
		expect(
			events(manifest).filter((event) => event.method === "cleanup"),
		).toHaveLength(0);
	});
}

for (const method of ["inspect", "setup", "remove", "install"] as const) {
	test(`pair inspection scope ${method} rejects loopback publication drift`, async () => {
		const { selfHost, manifest } = fixture(1000, `bad-${method}-address`);
		const pair = await selfHost.spinupPair("homarr", "sonarr", {
			order: "consumer-first",
		});
		let caught: unknown;
		try {
			if (method === "inspect") await pair.inspect();
			else await pair[method]("sonarr");
		} catch (error) {
			caught = error;
		}
		protocolFailure(caught, manifest, false);
		ownedCleanup(manifest);
		await disabledPair(pair, manifest);
	});
}

test("pair startup inspection scope matches the selected address echo", async () => {
	const { selfHost, manifest } = fixture(1000, "bad-initial-address");
	let caught: unknown;
	try {
		await selfHost.spinupPair("homarr", "sonarr", {
			order: "consumer-first",
		});
	} catch (error) {
		caught = error;
	}
	protocolFailure(caught, manifest, false);
	ownedCleanup(manifest);
});

test("pair direct failed close permanently disables all reads and actions", async () => {
	const { selfHost, manifest } = fixture(1000, "valid", true);
	const pair = await selfHost.spinupPair("homarr", "sonarr", {
		order: "consumer-first",
	});
	await expect(pair.close()).rejects.toThrow("Fixture cleanup failed");
	const owner = ownedCleanup(manifest);
	await disabledPair(pair, manifest);
	await expect(pair.close()).rejects.toThrow("Fixture cleanup failed");
	await disabledPair(pair, manifest);
	unlinkSync(`${manifest}.${owner}.failed`);
	await pair.close();
	await disabledPair(pair, manifest);
	expect(
		events(manifest).filter((event) => event.method === "cleanup"),
	).toHaveLength(3);
});

for (const method of ["inspect", "setup", "remove", "install"] as const) {
	for (const replacement of ["unchanged", "forged", "manual"]) {
		test(`pair declared Link ${method} rejects replacement with ${replacement} steps`, async () => {
			const cleanupFailed = method === "inspect" && replacement !== "unchanged";
			const { selfHost, manifest } = fixture(
				1000,
				`replace-${method}-${replacement}`,
				cleanupFailed,
			);
			const pair = await selfHost.spinupPair("homarr", "sonarr", {
				order: "consumer-first",
			});
			let caught: unknown;
			try {
				if (method === "inspect") await pair.inspect();
				else await pair[method]("sonarr");
			} catch (error) {
				caught = error;
			}
			protocolFailure(caught, manifest, cleanupFailed);
			const owner = ownedCleanup(manifest);
			await disabledPair(pair, manifest);
			if (cleanupFailed) {
				await expect(pair.close()).rejects.toThrow("Fixture cleanup failed");
				await disabledPair(pair, manifest);
				unlinkSync(`${manifest}.${owner}.failed`);
			}
			await pair.close();
			await disabledPair(pair, manifest);
		});
	}
}

for (const shape of ["missing", "null", "number", "empty", "syntax", "long"]) {
	test(`pair declared Link rejects malformed ${shape} startup identity`, async () => {
		const { selfHost, manifest } = fixture(1000, `bad-link-${shape}`);
		let caught: unknown;
		try {
			await selfHost.spinupPair("homarr", "sonarr", {
				order: "consumer-first",
			});
		} catch (error) {
			caught = error;
		}
		protocolFailure(caught, manifest, false);
		ownedCleanup(manifest);
	});
}

test("pair immutable scope ignores a shadowed exposed view Address", async () => {
	const { selfHost, manifest } = fixture(1000, "bad-inspect-address");
	const pair = await selfHost.spinupPair("homarr", "sonarr", {
		order: "consumer-first",
	});
	const admitted = pair.initial;
	Object.defineProperty(pair.target, "url", {
		value: "http://127.0.0.1:33333",
	});
	let caught: unknown;
	try {
		await pair.inspect();
	} catch (error) {
		caught = error;
	}
	protocolFailure(caught, manifest, false);
	ownedCleanup(manifest);
	await disabledPair(pair, manifest);
	expect(pair.initial).toEqual(admitted);
});

for (const [status, outcome] of [
	["ok", "complete"],
	["skip", "prerequisite"],
	["missing", "prerequisite"],
	["manual", "manual"],
	["fail", "failed"],
] as const) {
	test(`pair declared Link preserves ${status} observations and original admission`, async () => {
		const { selfHost, manifest } = fixture(1000, `valid-link-${status}`);
		const pair = await selfHost.spinupPair("homarr", "sonarr", {
			order: "consumer-first",
		});
		const initial = pair.initial;
		expect(initial.link).toBe("approved-connection");
		Object.defineProperty(initial, "link", { value: "check" });
		const consumer = initial.apps[0];
		if (!consumer) throw new Error("Missing consumer snapshot");
		Object.defineProperty(consumer, "id", { value: "outside" });
		Object.defineProperty(consumer, "url", {
			value: "http://127.0.0.1:33333",
		});
		expect(pair.initial.link).toBe("approved-connection");
		expect(pair.initial.apps[0]?.id).toBe("homarr");
		expect(pair.initial.apps[0]?.url).toBe(pair.consumer.url);
		for (const observed of [await pair.inspect(), await pair.setup()]) {
			expect(observed.link).toBe("approved-connection");
			expect(observed.outcome).toBe(outcome);
		}
		const absent = await pair.remove("sonarr");
		expect(absent.link).toBe("approved-connection");
		expect(absent.outcome).toBe("prerequisite");
		expect(absent.reason).toBe("absent");
		const restored = await pair.install("sonarr");
		expect(restored.link).toBe("approved-connection");
		expect(restored.outcome).toBe(outcome);
		expect((await pair.consumer.info()).id).toBe("homarr");
		expect((await pair.target.info()).id).toBe("sonarr");
		expect(
			events(manifest).filter((event) => event.method === "cleanup"),
		).toHaveLength(0);
	});
}

for (const kind of [
	"fail-complete",
	"fail-manual",
	"fail-prerequisite",
	"fail-peer-manual",
	"fail-peer-prerequisite",
	"duplicate-link-skip-ok",
	"duplicate-link-ok-skip",
	"duplicate-consumer",
	"duplicate-peer",
	"duplicate-manual",
	"duplicate-failed",
	"duplicate-prerequisite",
	"failed-without-fail",
	"manual-without-manual",
	"prerequisite-manual",
	"prerequisite-verified",
	"prerequisite-absent-present",
	"prerequisite-missing-notrun",
	"prerequisite-ready-unready",
	"prerequisite-pending-notrun",
	"unverified-skip",
]) {
	for (const stage of ["startup", "live"]) {
		test(`pair observational integrity ${stage} rejects ${kind}`, async () => {
			const cleanupFailed =
				kind === "fail-manual" || kind === "duplicate-link-ok-skip";
			const { selfHost, manifest } = fixture(
				1000,
				`integrity-${stage}-${kind}`,
				cleanupFailed,
			);
			let pair: Pair | undefined;
			let caught: unknown;
			try {
				pair = await selfHost.spinupPair("homarr", "sonarr", {
					order: "consumer-first",
				});
				if (stage === "live") await pair.inspect();
			} catch (error) {
				caught = error;
			}
			protocolFailure(caught, manifest, cleanupFailed);
			const owner = ownedCleanup(manifest);
			if (stage === "startup") expect(pair).toBeUndefined();
			if (pair) await disabledPair(pair, manifest);
			if (cleanupFailed) {
				await expect(selfHost.close()).rejects.toThrow(
					"Fixture cleanup failed",
				);
				if (pair) await disabledPair(pair, manifest);
				unlinkSync(`${manifest}.${owner}.failed`);
			}
			await selfHost.close();
			if (pair) await disabledPair(pair, manifest);
		});
	}
}

for (const method of ["setup", "remove", "install"] as const) {
	test(`pair observational integrity rejects contradictory ${method} results`, async () => {
		const { selfHost, manifest } = fixture(1000, "integrity-live-fail-manual");
		const pair = await selfHost.spinupPair("homarr", "sonarr", {
			order: "consumer-first",
		});
		let caught: unknown;
		try {
			await pair[method]("sonarr");
		} catch (error) {
			caught = error;
		}
		protocolFailure(caught, manifest, false);
		ownedCleanup(manifest);
		await disabledPair(pair, manifest);
	});
}

for (const [kind, outcome] of [
	["fail-failed", "failed"],
	["fail-peer-failed", "failed"],
	["failed-with-manual", "failed"],
	["manual-missing", "manual"],
	["unique-per-app", "complete"],
	["verified-skip", "complete"],
] as const) {
	for (const stage of ["startup", "live"]) {
		test(`pair observational integrity accepts ${stage} ${kind}`, async () => {
			const { selfHost, manifest } = fixture(
				1000,
				`integrity-${stage}-${kind}`,
			);
			const pair = await selfHost.spinupPair("homarr", "sonarr", {
				order: "consumer-first",
			});
			let observed = pair.initial;
			if (stage === "live") observed = await pair.inspect();
			expect(observed.outcome).toBe(outcome);
			expect((await pair.consumer.info()).id).toBe("homarr");
			expect((await pair.target.info()).id).toBe("sonarr");
			expect(
				events(manifest).filter((event) => event.method === "cleanup"),
			).toHaveLength(0);
		});
	}
}

test("shared scoped lifetime preserves single-App returns and both APIs' primitive failures", async () => {
	const { binary, manifest } = fixture();
	const options = { binary, manifest, timeoutMs: 1000 };
	expect(await withApp("sonarr", async () => 0, options)).toBe(0);
	expect(await withApp("sonarr", () => undefined, options)).toBeUndefined();
	await expect(withApp("cleanup-failure", () => 1, options)).rejects.toThrow(
		"Fixture cleanup failed",
	);
	for (const pair of [false, true]) {
		for (const primary of [undefined, 0, new Error("primary")]) {
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
