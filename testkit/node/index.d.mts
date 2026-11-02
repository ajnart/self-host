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
    getApiKey(name?: string): Promise<string>;
    credentials(): Promise<Credentials>;
    info(): Promise<AppInfo>;
    close(): Promise<void>;
}
export interface SpinupOptions {
    image?: string;
}
export interface SelfHost {
    spinup(app: string, options?: SpinupOptions): Promise<App>;
    close(): Promise<void>;
}
export interface SelfHostOptions {
    binary?: string;
    timeoutMs?: number;
    manifest?: string;
}
export type TestkitErrorCode = "PROTOCOL" | "STATE" | "START" | "CREDENTIALS" | "INFO" | "CLEANUP" | "TIMEOUT" | "TRANSPORT" | "BINARY";
export type TestkitStartupReason = "APP" | "OPTIONS" | "DOCKER" | "CAPABILITIES" | "PORT" | "OWNERSHIP" | "LIMITS" | "LOCK" | "UNSUPPORTED" | "STORAGE" | "CONTAINER" | "SETUP";
export declare class TestkitError extends Error {
    readonly code: TestkitErrorCode;
    readonly reason: TestkitStartupReason | undefined;
    constructor(code: TestkitErrorCode, reason?: TestkitStartupReason);
}
export declare function createSelfHost(options?: SelfHostOptions): SelfHost;
