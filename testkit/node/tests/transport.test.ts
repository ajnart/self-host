import { afterEach, expect, test } from "bun:test";
import {
	chmodSync,
	existsSync,
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

function fixture(
	timeoutMs = 1000,
	witnessMode = "valid",
	cleanupFails = false,
) {
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
		.replace(
			'const witnessMode = "valid";',
			`const witnessMode = ${JSON.stringify(witnessMode)};`,
		)
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
		[
			manifest,
			"private-witness-body",
			"private-test-api-key",
			"private-test-password",
		].some((value) => output.includes(value)),
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
		() => pair.createSonarrFilterWitness(),
		() => pair.readSonarrFilterWitness(),
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

const witnessImages = { sonarr: `example/sonarr@sha256:${"a".repeat(64)}` };

for (const order of ["consumer-first", "target-first"] as const) {
	test(`Sonarr witness ${order} delegates once and preserves immutable receipts`, async () => {
		const { selfHost, manifest } = fixture();
		const pair = await selfHost.spinupPair("homarr", "sonarr", {
			order,
			images: witnessImages,
			sonarrFilterWitness: true,
		});
		const baseline = await pair.createSonarrFilterWitness();
		expect(baseline).toEqual({
			kind: "sonarr-custom-filter-v1",
			objectId: 17,
			contentSha256: "a".repeat(64),
		});
		expect(Object.isFrozen(baseline)).toBe(true);
		expect(JSON.stringify(baseline)).not.toContain("label");
		for (let cycle = 0; cycle < 2; cycle++) {
			await pair.remove("sonarr");
			await pair.install("sonarr");
			expect(await pair.readSonarrFilterWitness()).toEqual(baseline);
		}
		const methods = events(manifest).map((value) => value.method);
		expect(methods.slice(0, 2)).toEqual(["capabilities", "spinupPair"]);
		expect(
			methods.filter((method) => method === "createSonarrFilterWitness"),
		).toHaveLength(1);
		expect(
			methods.filter((method) => method === "readSonarrFilterWitness"),
		).toHaveLength(2);
		await pair.close();
	});
}

test("Sonarr witness opt-in preserves legacy defaults and rejects scope/image overrides before startup", async () => {
	const { selfHost, manifest } = fixture();
	for (const [consumer, target, images] of [
		["sonarr", "homarr", witnessImages],
		["outside", "sonarr", witnessImages],
		["homarr", "sonarr", {}],
		["homarr", "sonarr", { sonarr: "example/sonarr:latest" }],
	] as const) {
		await expect(
			selfHost.spinupPair(consumer, target, {
				order: "consumer-first",
				images,
				sonarrFilterWitness: true,
			}),
		).rejects.toThrow("Invalid testkit protocol.");
	}
	expect(existsSync(manifest)).toBe(false);
	const legacy = await selfHost.spinupPair("homarr", "sonarr", {
		order: "consumer-first",
	});
	await expect(legacy.createSonarrFilterWitness()).rejects.toThrow(
		"Fixture is not available.",
	);
	await expect(legacy.readSonarrFilterWitness()).rejects.toThrow(
		"Fixture is not available.",
	);
	expect(events(manifest).map((value) => value.method)).toEqual(["spinupPair"]);
});

for (const mode of [
	"old-helper",
	"wrong-capability-envelope",
	"missing-capability",
	"false-capability",
	"string-capability",
	"wrong-capability-revision",
	"null-capabilities",
	"slow-capabilities",
]) {
	test(`Sonarr witness ${mode} rejects before native startup`, async () => {
		const { selfHost, manifest } = fixture(
			mode === "slow-capabilities" ? 25 : 1000,
			mode,
		);
		let caught: unknown;
		try {
			await selfHost.spinupPair("homarr", "sonarr", {
				order: "consumer-first",
				images: witnessImages,
				sonarrFilterWitness: true,
			});
		} catch (error) {
			caught = error;
		}
		expect(caught).toBeInstanceOf(TestkitError);
		expect(inspect(caught)).not.toContain("private-witness-body");
		expect(
			events(manifest).some((event) => event.method === "spinupPair"),
		).toBe(false);
	});
}

test("Sonarr witness requires the matching helper startup echo", async () => {
	const { selfHost, manifest } = fixture(1000, "missing-echo");
	let caught: unknown;
	try {
		await selfHost.spinupPair("homarr", "sonarr", {
			order: "consumer-first",
			images: witnessImages,
			sonarrFilterWitness: true,
		});
	} catch (error) {
		caught = error;
	}
	protocolFailure(caught, manifest, false);
	ownedCleanup(manifest);
});

for (const mode of [
	"bad-kind",
	"bad-id-zero",
	"bad-id-fraction",
	"bad-id-range",
	"bad-hash",
	"extra-receipt-field",
	"missing-receipt-field",
	"null-receipt",
]) {
	for (const method of [
		"createSonarrFilterWitness",
		"readSonarrFilterWitness",
	] as const) {
		test(`Sonarr ${mode} ${method} invalid receipt cleans and disables owner`, async () => {
			let selected = mode;
			if (method === "readSonarrFilterWitness") selected = `read-${mode}`;
			const { selfHost, manifest } = fixture(1000, selected);
			const pair = await selfHost.spinupPair("homarr", "sonarr", {
				order: "target-first",
				images: witnessImages,
				sonarrFilterWitness: true,
			});
			if (method === "readSonarrFilterWitness")
				await pair.createSonarrFilterWitness();
			let caught: unknown;
			try {
				await pair[method]();
			} catch (error) {
				caught = error;
			}
			protocolFailure(caught, manifest, false);
			ownedCleanup(manifest);
			await disabledPair(pair, manifest);
			await pair.close();
			expect(
				events(manifest).filter((event) => event.method === "cleanup"),
			).toHaveLength(1);
		});
	}
}

for (const method of [
	"createSonarrFilterWitness",
	"readSonarrFilterWitness",
] as const) {
	test(`Sonarr ${method} integrity failure preserves secondary cleanup failure`, async () => {
		let mode = "bad-hash";
		if (method === "readSonarrFilterWitness") mode = "read-bad-hash";
		const { selfHost, manifest } = fixture(1000, mode, true);
		const pair = await selfHost.spinupPair("homarr", "sonarr", {
			order: "consumer-first",
			images: witnessImages,
			sonarrFilterWitness: true,
		});
		if (method === "readSonarrFilterWitness")
			await pair.createSonarrFilterWitness();
		let caught: unknown;
		try {
			await pair[method]();
		} catch (error) {
			caught = error;
		}
		protocolFailure(caught, manifest, true);
		const owner = ownedCleanup(manifest);
		await disabledPair(pair, manifest);
		await expect(pair.close()).rejects.toThrow("Fixture cleanup failed");
		unlinkSync(`${manifest}.${owner}.failed`);
		await pair.close();
		await disabledPair(pair, manifest);
		expect(
			events(manifest).filter((event) => event.method === "cleanup"),
		).toHaveLength(3);
	});
}

test("Sonarr invalid startup echo preserves protocol and cleanup failure", async () => {
	const { selfHost, manifest } = fixture(1000, "missing-echo", true);
	let caught: unknown;
	try {
		await selfHost.spinupPair("homarr", "sonarr", {
			order: "consumer-first",
			images: witnessImages,
			sonarrFilterWitness: true,
		});
	} catch (error) {
		caught = error;
	}
	protocolFailure(caught, manifest, true);
	ownedCleanup(manifest);
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

test("Sonarr witness read-before-create and second create cannot seed twice", async () => {
	const { selfHost, manifest } = fixture();
	const pair = await selfHost.spinupPair("homarr", "sonarr", {
		order: "consumer-first",
		images: witnessImages,
		sonarrFilterWitness: true,
	});
	await expect(pair.readSonarrFilterWitness()).rejects.toThrow(
		"Fixture is not available.",
	);
	await pair.createSonarrFilterWitness();
	await expect(pair.createSonarrFilterWitness()).rejects.toThrow(
		"Fixture is not available.",
	);
	expect(
		events(manifest).filter(
			(event) => event.method === "createSonarrFilterWitness",
		),
	).toHaveLength(2);
});

test("Sonarr witness operation failure retains the primary and separately reports cleanup", async () => {
	const { binary, manifest } = fixture(1000, "operation-cleanup");
	let notified = false;
	let caught: unknown;
	try {
		await withPair(
			"homarr",
			"sonarr",
			(pair) => pair.createSonarrFilterWitness(),
			{
				binary,
				manifest,
				timeoutMs: 1000,
				order: "consumer-first",
				images: witnessImages,
				sonarrFilterWitness: true,
				onCleanupError(error) {
					notified = error.code === "CLEANUP";
				},
			},
		);
	} catch (error) {
		caught = error;
	}
	expect(caught).toBeInstanceOf(TestkitError);
	expect(caught instanceof TestkitError && caught.code).toBe("OPERATION");
	expect(caught instanceof TestkitError && caught.cleanupFailed).toBe(true);
	expect(notified).toBe(true);
	expect(inspect(caught)).not.toContain("private-witness-body");
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
