import { spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { mkdtempSync, rmdirSync, unlinkSync } from "node:fs";
import { tmpdir } from "node:os";
import { isAbsolute, join } from "node:path";
import { inspect } from "node:util";

const MAX_MESSAGE = 65536;
const CLEANUP_TIMEOUT = 50000;
const messages = new Map([
	["PROTOCOL", "Invalid testkit protocol."],
	["STATE", "Fixture is not available."],
	["START", "Fixture startup failed."],
	["CREDENTIALS", "Fixture credentials are not available."],
	["INFO", "Fixture information is not available."],
	["CLEANUP", "Fixture cleanup failed; run manifest cleanup."],
	["TIMEOUT", "Fixture operation timed out."],
	["TRANSPORT", "Testkit helper transport failed."],
]);

function fault(code) {
	const error = new Error(messages.get(code) || messages.get("TRANSPORT"));
	Object.defineProperty(error, "code", { value: code, enumerable: true });
	return error;
}

function record(value) {
	return value !== null && typeof value === "object" && !Array.isArray(value);
}

function strings(value, fields) {
	return (
		record(value) && fields.every((field) => typeof value[field] === "string")
	);
}

function privateCredentials(value) {
	if (!strings(value, ["username", "password", "email", "apiKey"])) {
		throw fault("PROTOCOL");
	}
	const credentials = Object.create(null);
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

function runCleanup(binary, manifest, owner) {
	return new Promise((resolve, reject) => {
		const child = spawn(
			binary,
			["--cleanup-manifest", manifest, "--owner", owner],
			{ stdio: "ignore" },
		);
		const timer = setTimeout(() => {
			child.kill("SIGKILL");
			reject(fault("CLEANUP"));
		}, CLEANUP_TIMEOUT);
		child.once("error", () => {
			clearTimeout(timer);
			reject(fault("CLEANUP"));
		});
		child.once("exit", (code) => {
			clearTimeout(timer);
			if (code === 0) resolve(undefined);
			else reject(fault("CLEANUP"));
		});
	});
}

class Bridge {
	#child;
	#binary;
	#manifest;
	#owner = randomBytes(16).toString("hex");
	#pending = new Map();
	#sequence = 0;
	#buffer = Buffer.alloc(0);
	#failed = false;
	#closing;
	#exited;

	constructor(binary, manifest) {
		this.#binary = binary;
		this.#manifest = manifest;
		this.#child = spawn(
			binary,
			["--manifest", manifest, "--owner", this.#owner],
			{ stdio: ["pipe", "pipe", "ignore"] },
		);
		this.#exited = new Promise((resolve) => {
			this.#child.once("exit", () => {
				this.#fail("TRANSPORT");
				resolve(undefined);
			});
			this.#child.once("error", () => {
				this.#fail("TRANSPORT");
				resolve(undefined);
			});
		});
		this.#child.stdin.on("error", () => this.#fail("TRANSPORT"));
		this.#child.stdout.on("data", (chunk) => this.#receive(chunk));
	}

	#fail(code) {
		this.#failed = true;
		this.#buffer = Buffer.alloc(0);
		for (const pending of this.#pending.values()) {
			clearTimeout(pending.timer);
			pending.reject(fault(code));
		}
		this.#pending.clear();
		this.#child.kill("SIGTERM");
	}

	#receive(chunk) {
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
				const response = JSON.parse(line);
				if (
					!record(response) ||
					response.version !== 1 ||
					!Number.isSafeInteger(response.id) ||
					!this.#pending.has(response.id) ||
					Object.hasOwn(response, "error") === Object.hasOwn(response, "result")
				) {
					throw fault("PROTOCOL");
				}
				const pending = this.#pending.get(response.id);
				this.#pending.delete(response.id);
				clearTimeout(pending.timer);
				if (Object.hasOwn(response, "error")) {
					if (!record(response.error) || !messages.has(response.error.code)) {
						pending.reject(fault("PROTOCOL"));
						this.#fail("PROTOCOL");
						return;
					}
					pending.reject(fault(response.error.code));
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

	request(method, params = {}, timeout = 30000) {
		if (this.#failed) return Promise.reject(fault("TRANSPORT"));
		const id = ++this.#sequence;
		return new Promise((resolve, reject) => {
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

	close() {
		if (!this.#closing) this.#closing = this.#close();
		return this.#closing;
	}

	async #close() {
		let failure;
		if (!this.#failed) {
			try {
				await this.request("close", {}, CLEANUP_TIMEOUT);
			} catch {
				failure = fault("CLEANUP");
			}
		}
		this.#child.stdin.end();
		const kill = setTimeout(() => this.#child.kill("SIGKILL"), CLEANUP_TIMEOUT);
		await this.#exited;
		clearTimeout(kill);
		try {
			await runCleanup(this.#binary, this.#manifest, this.#owner);
		} catch {
			failure = fault("CLEANUP");
		}
		if (failure) throw failure;
	}

	[inspect.custom]() {
		return "[TestkitBridge]";
	}
}

class AppHandle {
	#bridge;
	#url;
	#closed = false;

	constructor(bridge, url) {
		this.#bridge = bridge;
		this.#url = url;
	}

	get url() {
		return this.#url;
	}

	async getApiKey(name = "") {
		if (this.#closed || typeof name !== "string") throw fault("STATE");
		const value = await this.#bridge.request("getApiKey", { name });
		if (!strings(value, ["apiKey"])) throw fault("PROTOCOL");
		return value.apiKey;
	}

	async credentials() {
		if (this.#closed) throw fault("STATE");
		return privateCredentials(await this.#bridge.request("credentials"));
	}

	async info() {
		if (this.#closed) throw fault("STATE");
		const value = await this.#bridge.request("info");
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

	async close() {
		this.#closed = true;
		await this.#bridge.close();
	}

	toJSON() {
		return "[App]";
	}

	[inspect.custom]() {
		return "[App]";
	}
}

class SelfHostHandle {
	#binary;
	#timeout;
	#manifest;
	#directory;
	#bridges = new Set();
	#starts = new Set();
	#closed = false;
	#closing;

	constructor(options) {
		this.#binary =
			options.binary ??
			process.env.SELF_HOST_TESTKIT_BINARY ??
			"self-host-testkit";
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
		this.#manifest = options.manifest ?? process.env.SELF_HOST_TESTKIT_MANIFEST;
		if (
			this.#manifest !== undefined &&
			(typeof this.#manifest !== "string" || !isAbsolute(this.#manifest))
		) {
			throw fault("PROTOCOL");
		}
		if (!this.#manifest) {
			try {
				this.#directory = mkdtempSync(join(tmpdir(), "self-host-testkit-"));
				this.#manifest = join(this.#directory, "manifest.jsonl");
			} catch {
				throw fault("START");
			}
		}
	}

	async spinup(app, options) {
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

	async #spinup(bridge, app, options) {
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
				throw fault("PROTOCOL");
			}
			if (this.#closed) throw fault("STATE");
			return new AppHandle(bridge, value.url);
		} catch (error) {
			await bridge.close();
			throw error;
		}
	}

	close() {
		if (!this.#closing) {
			this.#closed = true;
			this.#closing = this.#close();
		}
		return this.#closing;
	}

	async #close() {
		const results = await Promise.allSettled(
			[...this.#bridges].map((bridge) => bridge.close()),
		);
		await Promise.allSettled([...this.#starts]);
		if (results.some((result) => result.status === "rejected"))
			throw fault("CLEANUP");
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
				throw fault("CLEANUP");
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

export function createSelfHost(options = {}) {
	if (!record(options)) throw fault("PROTOCOL");
	return new SelfHostHandle(options);
}
