import { type TestkitStartupReason } from "./reasons.mjs";
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
    close(): Promise<void>;
}
export interface SelfHostOptions {
    binary?: string;
    timeoutMs?: number;
    manifest?: string;
}
export type TestkitErrorCode = "PROTOCOL" | "STATE" | "START" | "CREDENTIALS" | "INFO" | "CLEANUP" | "TIMEOUT" | "TRANSPORT" | "BINARY";
export declare class TestkitError extends Error {
    readonly code: TestkitErrorCode;
    readonly reason: TestkitStartupReason | undefined;
    readonly recoveryPath?: string;
    constructor(code: TestkitErrorCode, reason?: TestkitStartupReason, recoveryPath?: string);
}
export declare function createSelfHost(options?: SelfHostOptions): SelfHost;
export declare function withApp<T>(app: string, callback: (app: App) => T | Promise<T>, options?: SelfHostOptions & SpinupOptions): Promise<T>;
