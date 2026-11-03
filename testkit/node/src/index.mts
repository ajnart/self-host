import { type ChildProcessByStdio, spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { mkdtempSync, rmdirSync, unlinkSync } from "node:fs";
import { tmpdir } from "node:os";
import { isAbsolute, join } from "node:path";
import type { Readable, Writable } from "node:stream";
import { inspect } from "node:util";
import { installedBinary } from "./binary.mjs";
import { type TestkitStartupReason, startupReasons } from "./reasons.mjs";

export type { TestkitStartupReason } from "./reasons.mjs";

export interface Credentials {
	readonly username: string;
	readonly password: string;
	readonly email: string;
	readonly apiKey: string;
}

export interface AppInfo {
	readonly id: string;
	readonly url: string;
	readonly image: string;
	readonly imageId: string;
	readonly repoDigests: string[];
}

export interface App {
	readonly url: string;
	readonly recoveryPath?: string;
	getApiKey(name?: string): Promise<string>;
	credentials(): Promise<Credentials>;
	info(): Promise<AppInfo>;
	close(): Promise<void>;
}

export interface SpinupOptions {
	image?: string;
}

export interface SelfHost {
	readonly recoveryPath?: string;
	spinup(app: string, options?: SpinupOptions): Promise<App>;
	spinupPair(
		consumer: string,
		target: string,
		options: PairOptions,
	): Promise<Pair>;
	close(): Promise<void>;
}

export interface PairOptions {
	order: "consumer-first" | "target-first";
	images?: Readonly<Record<string, string>>;
	sonarrFilterWitness?: true;
}

export interface SonarrFilterWitness {
	readonly kind: "sonarr-custom-filter-v1";
	readonly objectId: number;
	readonly contentSha256: string;
}

export interface PairAppStatus {
	readonly id: string;
	readonly url: string;
	readonly installed: boolean;
	readonly state: string;
	readonly setup: "ok" | "manual" | "incomplete" | "pending";
	readonly steps: ReadonlyArray<{
		readonly id: string;
		readonly status: "ok" | "manual" | "fail" | "skip";
	}>;
}

export type PairInspection = {
	readonly link: string;
	readonly apps: readonly PairAppStatus[];
} & (
	| { readonly outcome: "complete"; readonly reason: "verified" }
	| { readonly outcome: "manual" | "failed"; readonly reason: "setup" }
	| {
			readonly outcome: "prerequisite";
			readonly reason: "absent" | "unready" | "not-run";
	  }
);

export type PairApp = Omit<App, "close">;

export interface Pair {
	readonly consumer: PairApp;
	readonly target: PairApp;
	readonly initial: PairInspection;
	readonly recoveryPath?: string;
	inspect(): Promise<PairInspection>;
	setup(app?: string): Promise<PairInspection>;
	remove(app: string): Promise<PairInspection>;
	install(app: string): Promise<PairInspection>;
	createSonarrFilterWitness(): Promise<SonarrFilterWitness>;
	readSonarrFilterWitness(): Promise<SonarrFilterWitness>;
	close(): Promise<void>;
}

export interface SelfHostOptions {
	binary?: string;
	timeoutMs?: number;
	manifest?: string;
}

export type TestkitErrorCode =
	| "PROTOCOL"
	| "STATE"
	| "START"
	| "CREDENTIALS"
	| "INFO"
	| "CLEANUP"
	| "TIMEOUT"
	| "TRANSPORT"
	| "OPERATION"
	| "CANCELED"
	| "BINARY";

const MAX_MESSAGE = 65536;
const CLEANUP_TIMEOUT = 50000;
const messages = new Map<TestkitErrorCode, string>([
	["PROTOCOL", "Invalid testkit protocol."],
	["STATE", "Fixture is not available."],
	["START", "Fixture startup failed."],
	["CREDENTIALS", "Fixture credentials are not available."],
	["INFO", "Fixture information is not available."],
	["CLEANUP", "Fixture cleanup failed; run manifest cleanup."],
	["TIMEOUT", "Fixture operation timed out."],
	["TRANSPORT", "Testkit helper transport failed."],
	["OPERATION", "Fixture pair action or inspection failed."],
	["CANCELED", "Fixture operation was canceled."],
	[
		"BINARY",
		"Testkit helper not found. Run self-host-testkit-setup or set SELF_HOST_TESTKIT_BINARY.",
	],
]);

export class TestkitError extends Error {
	readonly code: TestkitErrorCode;
	readonly reason: TestkitStartupReason | undefined;
	readonly cleanupFailed: boolean;
	declare readonly recoveryPath?: string;

	constructor(
		code: TestkitErrorCode,
		reason?: TestkitStartupReason,
		recoveryPath?: string,
		cleanupFailed = false,
	) {
		let message = messages.get(code);
		if ((code === "START" || code === "OPERATION") && reason)
			message = `${message} ${startupReasons[reason]}`;
		super(message);
		this.name = "TestkitError";
		this.code = code;
		this.reason = reason;
		this.cleanupFailed = cleanupFailed;
		Object.defineProperty(this, "recoveryPath", { value: recoveryPath });
	}
}

function fault(
	code: TestkitErrorCode,
	reason?: TestkitStartupReason,
	recoveryPath?: string,
	cleanupFailed = false,
): TestkitError {
	return new TestkitError(code, reason, recoveryPath, cleanupFailed);
}

function record(value: unknown): value is Record<string, unknown> {
	return value !== null && typeof value === "object" && !Array.isArray(value);
}

function strings<K extends string>(
	value: unknown,
	fields: readonly K[],
): value is Record<K, string> & Record<string, unknown> {
	return (
		record(value) && fields.every((field) => typeof value[field] === "string")
	);
}

function privateCredentials(value: unknown): Credentials {
	if (!strings(value, ["username", "password", "email", "apiKey"])) {
		throw fault("PROTOCOL");
	}
	const credentials: Credentials = Object.create(null);
	for (const field of ["username", "password", "email", "apiKey"]) {
		Object.defineProperty(credentials, field, { get: () => value[field] });
	}
	Object.defineProperty(credentials, "toJSON", {
		value: () => "[Credentials]",
	});
	Object.defineProperty(credentials, inspect.custom, {
		value: () => "[Credentials]",
	});
	return Object.freeze(credentials);
}

function runCleanup(
	binary: string,
	manifest: string,
	owner: string,
): Promise<void> {
	return new Promise<void>((resolve, reject) => {
		const child = spawn(
			binary,
			["--cleanup-manifest", manifest, "--owner", owner],
			{ stdio: "ignore" },
		);
		let failed = false;
		const timer = setTimeout(() => {
			failed = true;
			child.kill("SIGKILL");
		}, CLEANUP_TIMEOUT);
		child.once("error", () => {
			failed = true;
		});
		child.once("close", (code) => {
			clearTimeout(timer);
			if (!failed && code === 0) resolve(undefined);
			else reject(fault("CLEANUP", undefined, manifest));
		});
	});
}

class Bridge {
	#child: ChildProcessByStdio<Writable, Readable, null>;
	#binary: string;
	#manifest: string;
	#owner = randomBytes(16).toString("hex");
	#pending = new Map<
		number,
		{
			resolve: (value: unknown) => void;
			reject: (error: Error) => void;
			timer: ReturnType<typeof setTimeout>;
		}
	>();
	#sequence = 0;
	#buffer = Buffer.alloc(0);
	#failed = false;
	#closing?: Promise<void>;
	#exited: Promise<void>;
	#spawned = false;

	constructor(binary: string, manifest: string) {
		this.#binary = binary;
		this.#manifest = manifest;
		this.#child = spawn(
			binary,
			["--manifest", manifest, "--owner", this.#owner],
			{ stdio: ["pipe", "pipe", "ignore"] },
		);
		this.#child.once("spawn", () => {
			this.#spawned = true;
		});
		this.#exited = new Promise<void>((resolve) => {
			this.#child.once("exit", () => {
				this.#fail("TRANSPORT");
				resolve(undefined);
			});
			this.#child.once("error", (error: NodeJS.ErrnoException) => {
				let code: TestkitErrorCode = "TRANSPORT";
				if (error.code === "ENOENT") code = "BINARY";
				this.#fail(code);
				resolve(undefined);
			});
		});
		this.#child.stdin.on("error", () => this.#fail("TRANSPORT"));
		this.#child.stdout.on("data", (chunk) => this.#receive(chunk));
	}

	get recoveryPath() {
		return this.#manifest;
	}

	#fault(
		code: TestkitErrorCode,
		reason?: TestkitStartupReason,
		cleanupFailed = false,
	) {
		return fault(code, reason, this.#manifest, cleanupFailed);
	}

	#fail(code: TestkitErrorCode) {
		this.#failed = true;
		this.#buffer = Buffer.alloc(0);
		for (const pending of this.#pending.values()) {
			clearTimeout(pending.timer);
			pending.reject(this.#fault(code));
		}
		this.#pending.clear();
		this.#child.kill("SIGTERM");
	}

	#receive(chunk: Buffer) {
		if (this.#failed) return;
		if (this.#buffer.length + chunk.length > MAX_MESSAGE) {
			this.#fail("PROTOCOL");
			return;
		}
		this.#buffer = Buffer.concat([this.#buffer, chunk]);
		let newline = this.#buffer.indexOf(10);
		while (newline !== -1) {
			const line = this.#buffer.subarray(0, newline).toString("utf8");
			this.#buffer = this.#buffer.subarray(newline + 1);
			try {
				const response: unknown = JSON.parse(line);
				if (
					!record(response) ||
					response.version !== 1 ||
					typeof response.id !== "number" ||
					!Number.isSafeInteger(response.id) ||
					!this.#pending.has(response.id) ||
					Object.hasOwn(response, "error") === Object.hasOwn(response, "result")
				) {
					throw this.#fault("PROTOCOL");
				}
				const pending = this.#pending.get(response.id);
				if (!pending) throw this.#fault("PROTOCOL");
				this.#pending.delete(response.id);
				clearTimeout(pending.timer);
				if (Object.hasOwn(response, "error")) {
					if (
						!record(response.error) ||
						typeof response.error.code !== "string" ||
						!isErrorCode(response.error.code)
					) {
						pending.reject(this.#fault("PROTOCOL"));
						this.#fail("PROTOCOL");
						return;
					}
					let reason: TestkitStartupReason | undefined;
					if (response.error.reason !== undefined) {
						if (
							(response.error.code !== "START" &&
								response.error.code !== "OPERATION") ||
							typeof response.error.reason !== "string" ||
							!isStartupReason(response.error.reason)
						) {
							pending.reject(this.#fault("PROTOCOL"));
							this.#fail("PROTOCOL");
							return;
						}
						reason = response.error.reason;
					}
					if (
						response.error.cleanupFailed !== undefined &&
						typeof response.error.cleanupFailed !== "boolean"
					) {
						pending.reject(this.#fault("PROTOCOL"));
						this.#fail("PROTOCOL");
						return;
					}
					pending.reject(
						this.#fault(
							response.error.code,
							reason,
							response.error.cleanupFailed === true,
						),
					);
				} else {
					pending.resolve(response.result);
				}
			} catch {
				this.#fail("PROTOCOL");
				return;
			}
			newline = this.#buffer.indexOf(10);
		}
	}

	request(
		method: string,
		params: Record<string, unknown> = {},
		timeout = 35000,
	): Promise<unknown> {
		if (this.#failed) return Promise.reject(this.#fault("TRANSPORT"));
		if (this.#closing) return Promise.reject(this.#fault("STATE"));
		const id = ++this.#sequence;
		return new Promise<unknown>((resolve, reject) => {
			const timer = setTimeout(() => this.#fail("TIMEOUT"), timeout);
			this.#pending.set(id, { resolve, reject, timer });
			const message = JSON.stringify({ version: 1, id, method, params });
			if (Buffer.byteLength(message) >= MAX_MESSAGE) {
				this.#fail("PROTOCOL");
				return;
			}
			this.#child.stdin.write(`${message}\n`, (error) => {
				if (error) this.#fail("TRANSPORT");
			});
		});
	}

	async failAndClose(primary: unknown): Promise<never> {
		let code: TestkitErrorCode = "PROTOCOL";
		if (primary instanceof TestkitError) code = primary.code;
		this.#fail(code);
		try {
			await this.close();
		} catch {
			if (primary instanceof TestkitError)
				throw this.#fault(primary.code, primary.reason, true);
			throw primary;
		}
		throw primary;
	}

	close() {
		if (!this.#closing) {
			this.#closing = this.#close().catch((error: unknown) => {
				this.#closing = undefined;
				throw error;
			});
		}
		return this.#closing;
	}

	async #close() {
		if (!this.#failed) {
			try {
				await this.request("close", {}, CLEANUP_TIMEOUT);
			} catch {
				this.#fail("TRANSPORT");
			}
		}
		this.#child.stdin.end();
		const kill = setTimeout(() => this.#child.kill("SIGKILL"), CLEANUP_TIMEOUT);
		await this.#exited;
		clearTimeout(kill);
		if (!this.#spawned) return;
		await runCleanup(this.#binary, this.#manifest, this.#owner);
	}

	[inspect.custom]() {
		return "[TestkitBridge]";
	}
}

class AppHandle {
	#bridge: Bridge;
	#url: string;
	#closed = false;
	#app?: string;

	constructor(bridge: Bridge, url: string, app?: string) {
		this.#bridge = bridge;
		this.#url = url;
		this.#app = app;
	}

	get url() {
		return this.#url;
	}

	get recoveryPath() {
		return this.#bridge.recoveryPath;
	}

	async getApiKey(name = "") {
		if (this.#closed || typeof name !== "string") throw fault("STATE");
		const value = await this.#bridge.request("getApiKey", {
			name,
			app: this.#app,
		});
		if (!strings(value, ["apiKey"])) throw fault("PROTOCOL");
		return value.apiKey;
	}

	async credentials() {
		if (this.#closed) throw fault("STATE");
		return privateCredentials(
			await this.#bridge.request("credentials", { app: this.#app }),
		);
	}

	async info() {
		if (this.#closed) throw fault("STATE");
		const value = await this.#bridge.request("info", { app: this.#app });
		if (
			!strings(value, ["id", "url", "image", "imageId"]) ||
			!Array.isArray(value.repoDigests) ||
			!value.repoDigests.every((digest) => typeof digest === "string")
		) {
			throw fault("PROTOCOL");
		}
		return {
			id: value.id,
			url: value.url,
			image: value.image,
			imageId: value.imageId,
			repoDigests: [...value.repoDigests],
		};
	}

	close() {
		this.#closed = true;
		return this.#bridge.close();
	}

	toJSON() {
		return "[App]";
	}

	[inspect.custom]() {
		return "[App]";
	}
}

function appID(value: unknown): value is string {
	return typeof value === "string" && /^[a-z0-9][a-z0-9-]{0,63}$/.test(value);
}

function loopback(value: string): boolean {
	try {
		const address = new URL(value);
		return (
			address.protocol === "http:" &&
			address.hostname === "127.0.0.1" &&
			address.port !== "" &&
			address.username === "" &&
			address.password === "" &&
			address.search === "" &&
			address.hash === ""
		);
	} catch {
		return false;
	}
}

function pairAppStatus(value: unknown, id: string): PairAppStatus {
	if (
		!strings(value, ["id", "url", "state", "setup"]) ||
		value.id !== id ||
		!loopback(value.url) ||
		typeof value.installed !== "boolean" ||
		![
			"running",
			"exited",
			"created",
			"restarting",
			"paused",
			"missing",
			"unknown",
			"dead",
			"removing",
		].includes(value.state) ||
		(value.setup !== "ok" &&
			value.setup !== "manual" &&
			value.setup !== "incomplete" &&
			value.setup !== "pending") ||
		!Array.isArray(value.steps)
	)
		throw fault("PROTOCOL");
	const steps = value.steps.map(
		(step: unknown): PairAppStatus["steps"][number] => {
			if (
				!strings(step, ["id", "status"]) ||
				!appID(step.id) ||
				(step.status !== "ok" &&
					step.status !== "manual" &&
					step.status !== "fail" &&
					step.status !== "skip")
			)
				throw fault("PROTOCOL");
			return { id: step.id, status: step.status };
		},
	);
	return {
		id,
		url: value.url,
		installed: value.installed,
		state: value.state,
		setup: value.setup,
		steps,
	};
}

function pairInspection(
	value: unknown,
	ids: readonly string[],
): PairInspection {
	if (
		!strings(value, ["link", "outcome", "reason"]) ||
		!appID(value.link) ||
		!Array.isArray(value.apps) ||
		value.apps.length !== 2
	)
		throw fault("PROTOCOL");
	const rawApps = value.apps;
	const apps = ids.map((id, index) => pairAppStatus(rawApps[index], id));
	const base = { link: value.link, apps };
	if (value.outcome === "complete" && value.reason === "verified") {
		if (
			apps.some(
				(app) =>
					!app.installed ||
					app.setup !== "ok" ||
					app.steps.some(
						(step) => step.status === "fail" || step.status === "manual",
					),
			) ||
			!apps[0]?.steps.some(
				(step) => step.id === base.link && step.status === "ok",
			)
		)
			throw fault("PROTOCOL");
		return { ...base, outcome: "complete", reason: "verified" };
	}
	if (
		(value.outcome === "manual" || value.outcome === "failed") &&
		value.reason === "setup"
	)
		return { ...base, outcome: value.outcome, reason: "setup" };
	if (
		value.outcome === "prerequisite" &&
		(value.reason === "absent" ||
			value.reason === "unready" ||
			value.reason === "not-run")
	)
		return { ...base, outcome: "prerequisite", reason: value.reason };
	throw fault("PROTOCOL");
}

function sonarrFilterWitness(value: unknown): SonarrFilterWitness {
	if (
		!record(value) ||
		Object.keys(value).length !== 3 ||
		value.kind !== "sonarr-custom-filter-v1" ||
		typeof value.objectId !== "number" ||
		!Number.isSafeInteger(value.objectId) ||
		value.objectId < 1 ||
		value.objectId > 2147483647 ||
		typeof value.contentSha256 !== "string" ||
		!/^[a-f0-9]{64}$/.test(value.contentSha256)
	)
		throw fault("PROTOCOL");
	return Object.freeze({
		kind: "sonarr-custom-filter-v1",
		objectId: value.objectId,
		contentSha256: value.contentSha256,
	});
}

class PairAppHandle implements PairApp {
	#view: AppHandle;
	constructor(bridge: Bridge, id: string, url: string) {
		this.#view = new AppHandle(bridge, url, id);
	}
	get url() {
		return this.#view.url;
	}
	get recoveryPath() {
		return this.#view.recoveryPath;
	}
	getApiKey(name?: string) {
		return this.#view.getApiKey(name);
	}
	credentials() {
		return this.#view.credentials();
	}
	info() {
		return this.#view.info();
	}
	toJSON() {
		return "[PairApp]";
	}
	[inspect.custom]() {
		return "[PairApp]";
	}
}

class PairHandle implements Pair {
	#bridge: Bridge;
	#ids: readonly string[];
	#initial: PairInspection;
	#timeout: number;
	#consumer: PairApp;
	#target: PairApp;
	#witnessEnabled: boolean;
	constructor(
		bridge: Bridge,
		consumer: string,
		target: string,
		consumerURL: string,
		targetURL: string,
		initial: PairInspection,
		timeout: number,
		witnessEnabled: boolean,
	) {
		this.#bridge = bridge;
		this.#ids = [consumer, target];
		this.#initial = initial;
		this.#timeout = timeout;
		this.#witnessEnabled = witnessEnabled;
		this.#consumer = new PairAppHandle(bridge, consumer, consumerURL);
		this.#target = new PairAppHandle(bridge, target, targetURL);
	}
	get consumer() {
		return this.#consumer;
	}
	get target() {
		return this.#target;
	}
	get initial() {
		return pairInspection(this.#initial, this.#ids);
	}
	get recoveryPath() {
		return this.#bridge.recoveryPath;
	}
	async #action(method: string, app?: string): Promise<PairInspection> {
		if (app !== undefined && !this.#ids.includes(app)) throw fault("STATE");
		const result = await this.#bridge.request(
			method,
			{ app },
			this.#timeout + 1000,
		);
		try {
			return pairInspection(result, this.#ids);
		} catch (error) {
			return this.#bridge.failAndClose(error);
		}
	}
	inspect() {
		return this.#action("inspect");
	}
	setup(app?: string) {
		return this.#action("setup", app);
	}
	remove(app: string) {
		if (!this.#ids.includes(app)) return Promise.reject(fault("STATE"));
		return this.#action("remove", app);
	}
	install(app: string) {
		if (!this.#ids.includes(app)) return Promise.reject(fault("STATE"));
		return this.#action("install", app);
	}
	async #sonarrWitness(
		method: "createSonarrFilterWitness" | "readSonarrFilterWitness",
	) {
		if (!this.#witnessEnabled) throw fault("STATE");
		const value = await this.#bridge.request(method);
		try {
			return sonarrFilterWitness(value);
		} catch (error) {
			return this.#bridge.failAndClose(error);
		}
	}
	createSonarrFilterWitness() {
		return this.#sonarrWitness("createSonarrFilterWitness");
	}
	readSonarrFilterWitness() {
		return this.#sonarrWitness("readSonarrFilterWitness");
	}
	close() {
		return this.#bridge.close();
	}
	toJSON() {
		return "[Pair]";
	}
	[inspect.custom]() {
		return "[Pair]";
	}
}

class SelfHostHandle {
	#binary: string;
	#timeout: number;
	#manifest: string;
	#directory?: string;
	#bridges = new Set<Bridge>();
	#starts = new Set<Promise<App | Pair>>();
	#closed = false;
	#closing?: Promise<void>;

	constructor(options: SelfHostOptions) {
		this.#binary =
			options.binary ??
			process.env.SELF_HOST_TESTKIT_BINARY ??
			installedBinary();
		this.#timeout = options.timeoutMs ?? 720000;
		if (
			typeof this.#binary !== "string" ||
			this.#binary.length === 0 ||
			!Number.isSafeInteger(this.#timeout) ||
			this.#timeout < 1 ||
			this.#timeout > 840000
		) {
			throw fault("PROTOCOL");
		}
		const manifest = options.manifest ?? process.env.SELF_HOST_TESTKIT_MANIFEST;
		if (
			manifest !== undefined &&
			(typeof manifest !== "string" || !isAbsolute(manifest))
		) {
			throw fault("PROTOCOL");
		}
		if (manifest) {
			this.#manifest = manifest;
		} else {
			try {
				this.#directory = mkdtempSync(join(tmpdir(), "self-host-testkit-"));
				this.#manifest = join(this.#directory, "manifest.jsonl");
			} catch {
				throw fault("START");
			}
		}
	}

	get recoveryPath() {
		return this.#manifest;
	}

	async spinup(app: string, options?: SpinupOptions): Promise<App> {
		const selected = options ?? {};
		if (this.#closed) throw fault("STATE");
		if (
			!record(selected) ||
			typeof app !== "string" ||
			!/^[a-z0-9][a-z0-9-]{0,63}$/.test(app) ||
			(selected.image !== undefined && typeof selected.image !== "string")
		) {
			throw fault("PROTOCOL");
		}
		const bridge = new Bridge(this.#binary, this.#manifest);
		this.#bridges.add(bridge);
		const start = this.#spinup(bridge, app, selected);
		this.#starts.add(start);
		try {
			return await start;
		} finally {
			this.#starts.delete(start);
		}
	}

	async #spinup(
		bridge: Bridge,
		app: string,
		options: SpinupOptions,
	): Promise<App> {
		try {
			const value = await bridge.request(
				"spinup",
				{
					app,
					image: options.image,
					timeoutMs: this.#timeout,
				},
				this.#timeout + 1000,
			);
			if (
				!strings(value, ["url"]) ||
				!/^http:\/\/127\.0\.0\.1:\d+\/?$/.test(value.url)
			) {
				throw fault("PROTOCOL", undefined, this.#manifest);
			}
			if (this.#closed) throw fault("STATE", undefined, this.#manifest);
			return new AppHandle(bridge, value.url);
		} catch (error) {
			try {
				await bridge.close();
			} catch {
				throw error;
			}
			throw error;
		}
	}

	async spinupPair(
		consumer: string,
		target: string,
		options: PairOptions,
	): Promise<Pair> {
		if (this.#closed) throw fault("STATE");
		if (
			!appID(consumer) ||
			!appID(target) ||
			consumer === target ||
			!record(options) ||
			(options.order !== "consumer-first" && options.order !== "target-first")
		)
			throw fault("PROTOCOL");
		if (
			options.sonarrFilterWitness !== undefined &&
			(options.sonarrFilterWitness !== true ||
				consumer !== "homarr" ||
				target !== "sonarr" ||
				typeof options.images?.sonarr !== "string" ||
				!/^[a-zA-Z0-9][a-zA-Z0-9._/:-]*@sha256:[a-f0-9]{64}$/.test(
					options.images.sonarr,
				) ||
				options.images.sonarr.length > 512)
		)
			throw fault("PROTOCOL");
		if (
			options.images !== undefined &&
			(!record(options.images) ||
				Object.entries(options.images).some(
					([id, image]) =>
						(id !== consumer && id !== target) || typeof image !== "string",
				))
		)
			throw fault("PROTOCOL");
		const bridge = new Bridge(this.#binary, this.#manifest);
		this.#bridges.add(bridge);
		const start = this.#spinupPair(bridge, consumer, target, options);
		this.#starts.add(start);
		try {
			return await start;
		} finally {
			this.#starts.delete(start);
		}
	}

	async #spinupPair(
		bridge: Bridge,
		consumer: string,
		target: string,
		options: PairOptions,
	): Promise<Pair> {
		try {
			if (options.sonarrFilterWitness) {
				const capabilities = await bridge.request(
					"capabilities",
					{},
					Math.min(35000, this.#timeout),
				);
				if (
					!record(capabilities) ||
					Object.keys(capabilities).length !== 1 ||
					capabilities.sonarrFilterWitness !== 1
				)
					throw fault("PROTOCOL");
			}
			const value = await bridge.request(
				"spinupPair",
				{
					consumer,
					target,
					order: options.order,
					images: options.images,
					timeoutMs: this.#timeout,
					sonarrFilterWitness: options.sonarrFilterWitness,
				},
				this.#timeout + 1000,
			);
			if (
				!record(value) ||
				!strings(value.consumer, ["id", "url"]) ||
				!strings(value.target, ["id", "url"]) ||
				value.consumer.id !== consumer ||
				value.target.id !== target ||
				!loopback(value.consumer.url) ||
				!loopback(value.target.url) ||
				value.consumer.url === value.target.url
			)
				throw fault("PROTOCOL", undefined, this.#manifest);
			const initial = pairInspection(value.initial, [consumer, target]);
			if (options.sonarrFilterWitness && value.sonarrFilterWitness !== 1)
				throw fault("PROTOCOL");
			if (this.#closed) throw fault("STATE", undefined, this.#manifest);
			return new PairHandle(
				bridge,
				consumer,
				target,
				value.consumer.url,
				value.target.url,
				initial,
				this.#timeout,
				options.sonarrFilterWitness === true,
			);
		} catch (error) {
			return bridge.failAndClose(error);
		}
	}

	close() {
		if (!this.#closing) {
			this.#closed = true;
			this.#closing = this.#close().catch((error: unknown) => {
				this.#closing = undefined;
				throw error;
			});
		}
		return this.#closing;
	}

	async #close() {
		const results = await Promise.allSettled(
			[...this.#bridges].map((bridge) => bridge.close()),
		);
		await Promise.allSettled([...this.#starts]);
		if (results.some((result) => result.status === "rejected"))
			throw fault("CLEANUP", undefined, this.#manifest);
		if (this.#directory) {
			try {
				try {
					unlinkSync(this.#manifest);
				} catch (error) {
					if (
						!(error instanceof Error) ||
						!("code" in error) ||
						error.code !== "ENOENT"
					)
						throw error;
				}
				rmdirSync(this.#directory);
			} catch {
				throw fault("CLEANUP", undefined, this.#manifest);
			}
		}
	}

	toJSON() {
		return "[SelfHost]";
	}

	[inspect.custom]() {
		return "[SelfHost]";
	}
}

function isErrorCode(value: string): value is TestkitErrorCode {
	return [...messages.keys()].some((code) => code === value);
}

function isStartupReason(value: string): value is TestkitStartupReason {
	return Object.hasOwn(startupReasons, value);
}

export function createSelfHost(options: SelfHostOptions = {}): SelfHost {
	if (!record(options)) throw fault("PROTOCOL");
	return new SelfHostHandle(options);
}

async function scopedLifetime<T>(
	work: () => T | Promise<T>,
	close: () => Promise<void>,
	onCleanupError?: (error: TestkitError) => void | Promise<void>,
): Promise<T> {
	let result: { ok: true; value: T } | { ok: false; error: unknown };
	try {
		result = { ok: true, value: await work() };
	} catch (error) {
		result = { ok: false, error };
	}
	try {
		await close();
	} catch (error) {
		if (result.ok) throw error;
		if (onCleanupError && error instanceof TestkitError)
			await Promise.allSettled([
				Promise.resolve().then(() => onCleanupError(error)),
			]);
	}
	if (!result.ok) throw result.error;
	return result.value;
}

export async function withApp<T>(
	app: string,
	callback: (app: App) => T | Promise<T>,
	options: SelfHostOptions & SpinupOptions = {},
): Promise<T> {
	const selfHost = createSelfHost(options);
	return scopedLifetime<T>(
		async () => callback(await selfHost.spinup(app, options)),
		() => selfHost.close(),
	);
}

export async function withPair<T>(
	consumer: string,
	target: string,
	callback: (pair: Pair) => T | Promise<T>,
	options: SelfHostOptions &
		PairOptions & {
			onCleanupError?: (error: TestkitError) => void | Promise<void>;
		},
): Promise<T> {
	const selfHost = createSelfHost(options);
	return scopedLifetime<T>(
		async () => callback(await selfHost.spinupPair(consumer, target, options)),
		() => selfHost.close(),
		options.onCleanupError,
	);
}
