import { type TestkitStartupPhase, type TestkitStartupReason } from "./reasons.mjs";
export type { TestkitStartupReason, TestkitStartupPhase } from "./reasons.mjs";
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
    spinupPair(consumer: string, target: string, options: PairOptions): Promise<Pair>;
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
declare const pairStates: readonly ["running", "exited", "created", "restarting", "paused", "missing", "unknown", "dead", "removing", "degraded"];
export type PairAppState = (typeof pairStates)[number];
export interface PairAppStatus {
    readonly id: string;
    readonly url: string;
    readonly installed: boolean;
    readonly state: PairAppState;
    readonly setup: "ok" | "manual" | "incomplete" | "pending";
    readonly steps: ReadonlyArray<{
        readonly id: string;
        readonly status: "ok" | "manual" | "fail" | "skip";
        readonly verified: boolean;
    }>;
}
export type PairInspection = {
    readonly link: string;
    readonly apps: readonly PairAppStatus[];
} & ({
    readonly outcome: "complete";
    readonly reason: "verified";
} | {
    readonly outcome: "manual" | "failed";
    readonly reason: "setup";
} | {
    readonly outcome: "prerequisite";
    readonly reason: "absent" | "unready" | "not-run";
});
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
export type TestkitErrorCode = "PROTOCOL" | "STATE" | "START" | "CREDENTIALS" | "INFO" | "CLEANUP" | "TIMEOUT" | "TRANSPORT" | "OPERATION" | "CANCELED" | "BINARY";
export declare class TestkitError extends Error {
    readonly code: TestkitErrorCode;
    readonly reason: TestkitStartupReason | undefined;
    readonly phase: TestkitStartupPhase | undefined;
    readonly cleanupFailed: boolean;
    readonly recoveryPath?: string;
    constructor(code: TestkitErrorCode, reason?: TestkitStartupReason, recoveryPath?: string, cleanupFailed?: boolean, phase?: TestkitStartupPhase);
}
export declare function createSelfHost(options?: SelfHostOptions): SelfHost;
export interface ScopedOptions {
    onCleanupError?: (error: TestkitError) => void | Promise<void>;
}
export declare function withApp<T>(app: string, callback: (app: App) => T | Promise<T>, options?: SelfHostOptions & SpinupOptions & ScopedOptions): Promise<T>;
export declare function withPair<T>(consumer: string, target: string, callback: (pair: Pair) => T | Promise<T>, options: SelfHostOptions & PairOptions & ScopedOptions): Promise<T>;
