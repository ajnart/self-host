import { spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { mkdtempSync, rmdirSync, unlinkSync } from "node:fs";
import { tmpdir } from "node:os";
import { isAbsolute, join } from "node:path";
import { inspect } from "node:util";
import { installedBinary } from "./binary.mjs";
import { startupReasons } from "./reasons.mjs";
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
    ["OPERATION", "Fixture pair action or inspection failed."],
    ["CANCELED", "Fixture operation was canceled."],
    [
        "BINARY",
        "Testkit helper not found. Run self-host-testkit-setup or set SELF_HOST_TESTKIT_BINARY.",
    ],
]);
export class TestkitError extends Error {
    code;
    reason;
    cleanupFailed;
    constructor(code, reason, recoveryPath, cleanupFailed = false) {
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
function fault(code, reason, recoveryPath, cleanupFailed = false) {
    return new TestkitError(code, reason, recoveryPath, cleanupFailed);
}
function record(value) {
    return value !== null && typeof value === "object" && !Array.isArray(value);
}
function strings(value, fields) {
    return (record(value) && fields.every((field) => typeof value[field] === "string"));
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
        const child = spawn(binary, ["--cleanup-manifest", manifest, "--owner", owner], { stdio: "ignore" });
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
            if (!failed && code === 0)
                resolve(undefined);
            else
                reject(fault("CLEANUP", undefined, manifest));
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
    #spawned = false;
    constructor(binary, manifest) {
        this.#binary = binary;
        this.#manifest = manifest;
        this.#child = spawn(binary, ["--manifest", manifest, "--owner", this.#owner], { stdio: ["pipe", "pipe", "ignore"] });
        this.#child.once("spawn", () => {
            this.#spawned = true;
        });
        this.#exited = new Promise((resolve) => {
            this.#child.once("exit", () => {
                this.#fail("TRANSPORT");
                resolve(undefined);
            });
            this.#child.once("error", (error) => {
                let code = "TRANSPORT";
                if (error.code === "ENOENT")
                    code = "BINARY";
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
    #fault(code, reason, cleanupFailed = false) {
        return fault(code, reason, this.#manifest, cleanupFailed);
    }
    #fail(code) {
        this.#failed = true;
        this.#buffer = Buffer.alloc(0);
        for (const pending of this.#pending.values()) {
            clearTimeout(pending.timer);
            pending.reject(this.#fault(code));
        }
        this.#pending.clear();
        this.#child.kill("SIGTERM");
    }
    #receive(chunk) {
        if (this.#failed)
            return;
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
                if (!record(response) ||
                    response.version !== 1 ||
                    typeof response.id !== "number" ||
                    !Number.isSafeInteger(response.id) ||
                    !this.#pending.has(response.id) ||
                    Object.hasOwn(response, "error") === Object.hasOwn(response, "result")) {
                    throw this.#fault("PROTOCOL");
                }
                const pending = this.#pending.get(response.id);
                if (!pending)
                    throw this.#fault("PROTOCOL");
                this.#pending.delete(response.id);
                clearTimeout(pending.timer);
                if (Object.hasOwn(response, "error")) {
                    if (!record(response.error) ||
                        typeof response.error.code !== "string" ||
                        !isErrorCode(response.error.code)) {
                        pending.reject(this.#fault("PROTOCOL"));
                        this.#fail("PROTOCOL");
                        return;
                    }
                    let reason;
                    if (response.error.reason !== undefined) {
                        if ((response.error.code !== "START" &&
                            response.error.code !== "OPERATION") ||
                            typeof response.error.reason !== "string" ||
                            !isStartupReason(response.error.reason)) {
                            pending.reject(this.#fault("PROTOCOL"));
                            this.#fail("PROTOCOL");
                            return;
                        }
                        reason = response.error.reason;
                    }
                    if (response.error.cleanupFailed !== undefined &&
                        typeof response.error.cleanupFailed !== "boolean") {
                        pending.reject(this.#fault("PROTOCOL"));
                        this.#fail("PROTOCOL");
                        return;
                    }
                    pending.reject(this.#fault(response.error.code, reason, response.error.cleanupFailed === true));
                }
                else {
                    pending.resolve(response.result);
                }
            }
            catch {
                this.#fail("PROTOCOL");
                return;
            }
            newline = this.#buffer.indexOf(10);
        }
    }
    request(method, params = {}, timeout = 35000) {
        if (this.#failed)
            return Promise.reject(this.#fault("TRANSPORT"));
        if (this.#closing)
            return Promise.reject(this.#fault("STATE"));
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
                if (error)
                    this.#fail("TRANSPORT");
            });
        });
    }
    async failAndClose(primary) {
        let code = "PROTOCOL";
        if (primary instanceof TestkitError)
            code = primary.code;
        this.#fail(code);
        try {
            await this.close();
        }
        catch {
            if (primary instanceof TestkitError)
                throw this.#fault(primary.code, primary.reason, true);
            throw primary;
        }
        throw primary;
    }
    close() {
        if (!this.#closing) {
            this.#closing = this.#close().catch((error) => {
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
            }
            catch {
                this.#fail("TRANSPORT");
            }
        }
        this.#child.stdin.end();
        const kill = setTimeout(() => this.#child.kill("SIGKILL"), CLEANUP_TIMEOUT);
        await this.#exited;
        clearTimeout(kill);
        if (!this.#spawned)
            return;
        await runCleanup(this.#binary, this.#manifest, this.#owner);
    }
    [inspect.custom]() {
        return "[TestkitBridge]";
    }
}
class AppHandle {
    #bridge;
    #url;
    #closed = false;
    #app;
    constructor(bridge, url, app) {
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
    #validate(read) {
        try {
            return read();
        }
        catch (error) {
            if (this.#app !== undefined)
                return this.#bridge.failAndClose(error);
            throw error;
        }
    }
    async getApiKey(name = "") {
        if (this.#closed || typeof name !== "string")
            throw fault("STATE");
        const value = await this.#bridge.request("getApiKey", {
            name,
            app: this.#app,
        });
        return this.#validate(() => {
            if (!strings(value, ["apiKey"]))
                throw fault("PROTOCOL");
            return value.apiKey;
        });
    }
    async credentials() {
        if (this.#closed)
            throw fault("STATE");
        const value = await this.#bridge.request("credentials", { app: this.#app });
        return this.#validate(() => privateCredentials(value));
    }
    async info() {
        if (this.#closed)
            throw fault("STATE");
        const value = await this.#bridge.request("info", { app: this.#app });
        return this.#validate(() => {
            if (!strings(value, ["id", "url", "image", "imageId"]) ||
                !Array.isArray(value.repoDigests) ||
                !value.repoDigests.every((digest) => typeof digest === "string")) {
                throw fault("PROTOCOL");
            }
            if (this.#app !== undefined &&
                (value.id !== this.#app ||
                    !loopback(value.url) ||
                    value.url !== this.#url))
                throw fault("PROTOCOL");
            return {
                id: value.id,
                url: value.url,
                image: value.image,
                imageId: value.imageId,
                repoDigests: [...value.repoDigests],
            };
        });
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
function appID(value) {
    return typeof value === "string" && /^[a-z0-9][a-z0-9-]{0,63}$/.test(value);
}
function loopback(value) {
    try {
        const address = new URL(value);
        return (address.protocol === "http:" &&
            address.hostname === "127.0.0.1" &&
            address.port !== "" &&
            address.username === "" &&
            address.password === "" &&
            address.search === "" &&
            address.hash === "");
    }
    catch {
        return false;
    }
}
function pairAppStatus(value, id, url) {
    if (!strings(value, ["id", "url", "state", "setup"]) ||
        value.id !== id ||
        !loopback(value.url) ||
        value.url !== url ||
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
        !Array.isArray(value.steps))
        throw fault("PROTOCOL");
    const seen = new Set();
    const steps = value.steps.map((step) => {
        if (!strings(step, ["id", "status"]) ||
            !appID(step.id) ||
            (step.status !== "ok" &&
                step.status !== "manual" &&
                step.status !== "fail" &&
                step.status !== "skip"))
            throw fault("PROTOCOL");
        if (seen.has(step.id))
            throw fault("PROTOCOL");
        seen.add(step.id);
        return { id: step.id, status: step.status };
    });
    return {
        id,
        url: value.url,
        installed: value.installed,
        state: value.state,
        setup: value.setup,
        steps,
    };
}
function pairInspection(value, ids, urls) {
    if (!strings(value, ["link", "outcome", "reason"]) ||
        !appID(value.link) ||
        !Array.isArray(value.apps) ||
        value.apps.length !== 2)
        throw fault("PROTOCOL");
    const rawApps = value.apps;
    const apps = ids.map((id, index) => pairAppStatus(rawApps[index], id, urls[index] ?? ""));
    const base = { link: value.link, apps };
    const failed = apps.some((app) => app.steps.some((step) => step.status === "fail"));
    if ((value.outcome === "failed") !== failed)
        throw fault("PROTOCOL");
    if (!failed &&
        (value.outcome === "manual") !== apps.some((app) => app.setup === "manual"))
        throw fault("PROTOCOL");
    if (value.outcome === "complete" && value.reason === "verified") {
        if (apps.some((app) => !app.installed ||
            app.setup !== "ok" ||
            app.steps.some((step) => step.status === "fail" || step.status === "manual")) ||
            !apps[0]?.steps.some((step) => step.id === base.link && step.status === "ok"))
            throw fault("PROTOCOL");
        return { ...base, outcome: "complete", reason: "verified" };
    }
    if ((value.outcome === "manual" || value.outcome === "failed") &&
        value.reason === "setup")
        return { ...base, outcome: value.outcome, reason: "setup" };
    if (value.outcome === "prerequisite" &&
        (value.reason === "absent" ||
            value.reason === "unready" ||
            value.reason === "not-run")) {
        const absent = apps.some((app) => !app.installed);
        const unready = apps.some((app) => app.setup !== "ok");
        if ((value.reason === "absent") !== absent ||
            (!absent && (value.reason === "unready") !== unready) ||
            (!absent &&
                !unready &&
                apps[0]?.steps.some((step) => step.id === base.link && step.status === "ok")))
            throw fault("PROTOCOL");
        return { ...base, outcome: "prerequisite", reason: value.reason };
    }
    throw fault("PROTOCOL");
}
function sonarrFilterWitness(value) {
    if (!record(value) ||
        Object.keys(value).length !== 3 ||
        value.kind !== "sonarr-custom-filter-v1" ||
        typeof value.objectId !== "number" ||
        !Number.isSafeInteger(value.objectId) ||
        value.objectId < 1 ||
        value.objectId > 2147483647 ||
        typeof value.contentSha256 !== "string" ||
        !/^[a-f0-9]{64}$/.test(value.contentSha256))
        throw fault("PROTOCOL");
    return Object.freeze({
        kind: "sonarr-custom-filter-v1",
        objectId: value.objectId,
        contentSha256: value.contentSha256,
    });
}
class PairAppHandle {
    #view;
    constructor(bridge, id, url) {
        this.#view = new AppHandle(bridge, url, id);
    }
    get url() {
        return this.#view.url;
    }
    get recoveryPath() {
        return this.#view.recoveryPath;
    }
    getApiKey(name) {
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
class PairHandle {
    #bridge;
    #ids;
    #initial;
    #timeout;
    #consumer;
    #target;
    #witnessEnabled;
    constructor(bridge, consumer, target, consumerURL, targetURL, initial, timeout, witnessEnabled) {
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
        return pairInspection(this.#initial, this.#ids, this.#initial.apps.map((app) => app.url));
    }
    get recoveryPath() {
        return this.#bridge.recoveryPath;
    }
    async #action(method, app) {
        if (app !== undefined && !this.#ids.includes(app))
            throw fault("STATE");
        const result = await this.#bridge.request(method, { app }, this.#timeout + 1000);
        try {
            if (!record(result) || result.link !== this.#initial.link)
                throw fault("PROTOCOL");
            return pairInspection(result, this.#ids, this.#initial.apps.map((app) => app.url));
        }
        catch (error) {
            return this.#bridge.failAndClose(error);
        }
    }
    inspect() {
        return this.#action("inspect");
    }
    setup(app) {
        return this.#action("setup", app);
    }
    remove(app) {
        if (!this.#ids.includes(app))
            return Promise.reject(fault("STATE"));
        return this.#action("remove", app);
    }
    install(app) {
        if (!this.#ids.includes(app))
            return Promise.reject(fault("STATE"));
        return this.#action("install", app);
    }
    async #sonarrWitness(method) {
        if (!this.#witnessEnabled)
            throw fault("STATE");
        const value = await this.#bridge.request(method);
        try {
            return sonarrFilterWitness(value);
        }
        catch (error) {
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
                installedBinary();
        this.#timeout = options.timeoutMs ?? 720000;
        if (typeof this.#binary !== "string" ||
            this.#binary.length === 0 ||
            !Number.isSafeInteger(this.#timeout) ||
            this.#timeout < 1 ||
            this.#timeout > 840000) {
            throw fault("PROTOCOL");
        }
        const manifest = options.manifest ?? process.env.SELF_HOST_TESTKIT_MANIFEST;
        if (manifest !== undefined &&
            (typeof manifest !== "string" || !isAbsolute(manifest))) {
            throw fault("PROTOCOL");
        }
        if (manifest) {
            this.#manifest = manifest;
        }
        else {
            try {
                this.#directory = mkdtempSync(join(tmpdir(), "self-host-testkit-"));
                this.#manifest = join(this.#directory, "manifest.jsonl");
            }
            catch {
                throw fault("START");
            }
        }
    }
    get recoveryPath() {
        return this.#manifest;
    }
    async spinup(app, options) {
        const selected = options ?? {};
        if (this.#closed)
            throw fault("STATE");
        if (!record(selected) ||
            typeof app !== "string" ||
            !/^[a-z0-9][a-z0-9-]{0,63}$/.test(app) ||
            (selected.image !== undefined && typeof selected.image !== "string")) {
            throw fault("PROTOCOL");
        }
        const bridge = new Bridge(this.#binary, this.#manifest);
        this.#bridges.add(bridge);
        const start = this.#spinup(bridge, app, selected);
        this.#starts.add(start);
        try {
            return await start;
        }
        finally {
            this.#starts.delete(start);
        }
    }
    async #spinup(bridge, app, options) {
        try {
            const value = await bridge.request("spinup", {
                app,
                image: options.image,
                timeoutMs: this.#timeout,
            }, this.#timeout + 1000);
            if (!strings(value, ["url"]) ||
                !/^http:\/\/127\.0\.0\.1:\d+\/?$/.test(value.url)) {
                throw fault("PROTOCOL", undefined, this.#manifest);
            }
            if (this.#closed)
                throw fault("STATE", undefined, this.#manifest);
            return new AppHandle(bridge, value.url);
        }
        catch (error) {
            try {
                await bridge.close();
            }
            catch {
                throw error;
            }
            throw error;
        }
    }
    async spinupPair(consumer, target, options) {
        if (this.#closed)
            throw fault("STATE");
        if (!appID(consumer) ||
            !appID(target) ||
            consumer === target ||
            !record(options) ||
            (options.order !== "consumer-first" && options.order !== "target-first"))
            throw fault("PROTOCOL");
        if (options.sonarrFilterWitness !== undefined &&
            (options.sonarrFilterWitness !== true ||
                consumer !== "homarr" ||
                target !== "sonarr" ||
                typeof options.images?.sonarr !== "string" ||
                !/^[a-zA-Z0-9][a-zA-Z0-9._/:-]*@sha256:[a-f0-9]{64}$/.test(options.images.sonarr) ||
                options.images.sonarr.length > 512))
            throw fault("PROTOCOL");
        if (options.images !== undefined &&
            (!record(options.images) ||
                Object.entries(options.images).some(([id, image]) => (id !== consumer && id !== target) || typeof image !== "string")))
            throw fault("PROTOCOL");
        const bridge = new Bridge(this.#binary, this.#manifest);
        this.#bridges.add(bridge);
        const start = this.#spinupPair(bridge, consumer, target, options);
        this.#starts.add(start);
        try {
            return await start;
        }
        finally {
            this.#starts.delete(start);
        }
    }
    async #spinupPair(bridge, consumer, target, options) {
        try {
            if (options.sonarrFilterWitness) {
                const capabilities = await bridge.request("capabilities", {}, Math.min(35000, this.#timeout));
                if (!record(capabilities) ||
                    Object.keys(capabilities).length !== 1 ||
                    capabilities.sonarrFilterWitness !== 1)
                    throw fault("PROTOCOL");
            }
            const value = await bridge.request("spinupPair", {
                consumer,
                target,
                order: options.order,
                images: options.images,
                timeoutMs: this.#timeout,
                sonarrFilterWitness: options.sonarrFilterWitness,
            }, this.#timeout + 1000);
            if (!record(value) ||
                !strings(value.consumer, ["id", "url"]) ||
                !strings(value.target, ["id", "url"]) ||
                value.consumer.id !== consumer ||
                value.target.id !== target ||
                !loopback(value.consumer.url) ||
                !loopback(value.target.url) ||
                value.consumer.url === value.target.url)
                throw fault("PROTOCOL", undefined, this.#manifest);
            const initial = pairInspection(value.initial, [consumer, target], [value.consumer.url, value.target.url]);
            if (options.sonarrFilterWitness && value.sonarrFilterWitness !== 1)
                throw fault("PROTOCOL");
            if (this.#closed)
                throw fault("STATE", undefined, this.#manifest);
            return new PairHandle(bridge, consumer, target, value.consumer.url, value.target.url, initial, this.#timeout, options.sonarrFilterWitness === true);
        }
        catch (error) {
            return bridge.failAndClose(error);
        }
    }
    close() {
        if (!this.#closing) {
            this.#closed = true;
            this.#closing = this.#close().catch((error) => {
                this.#closing = undefined;
                throw error;
            });
        }
        return this.#closing;
    }
    async #close() {
        const results = await Promise.allSettled([...this.#bridges].map((bridge) => bridge.close()));
        await Promise.allSettled([...this.#starts]);
        if (results.some((result) => result.status === "rejected"))
            throw fault("CLEANUP", undefined, this.#manifest);
        if (this.#directory) {
            try {
                try {
                    unlinkSync(this.#manifest);
                }
                catch (error) {
                    if (!(error instanceof Error) ||
                        !("code" in error) ||
                        error.code !== "ENOENT")
                        throw error;
                }
                rmdirSync(this.#directory);
            }
            catch {
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
function isErrorCode(value) {
    return [...messages.keys()].some((code) => code === value);
}
function isStartupReason(value) {
    return Object.hasOwn(startupReasons, value);
}
export function createSelfHost(options = {}) {
    if (!record(options))
        throw fault("PROTOCOL");
    return new SelfHostHandle(options);
}
async function scopedLifetime(work, close, onCleanupError) {
    let result;
    try {
        result = { ok: true, value: await work() };
    }
    catch (error) {
        result = { ok: false, error };
    }
    try {
        await close();
    }
    catch (error) {
        if (result.ok)
            throw error;
        if (onCleanupError && error instanceof TestkitError)
            await Promise.allSettled([
                Promise.resolve().then(() => onCleanupError(error)),
            ]);
    }
    if (!result.ok)
        throw result.error;
    return result.value;
}
export async function withApp(app, callback, options = {}) {
    const selfHost = createSelfHost(options);
    return scopedLifetime(async () => callback(await selfHost.spinup(app, options)), () => selfHost.close());
}
export async function withPair(consumer, target, callback, options) {
    const selfHost = createSelfHost(options);
    return scopedLifetime(async () => callback(await selfHost.spinupPair(consumer, target, options)), () => selfHost.close(), options.onCleanupError);
}
