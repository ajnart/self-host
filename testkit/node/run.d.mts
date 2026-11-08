import { type SelfHostOptions } from "./index.mjs";
export interface RunApp {
    app: string;
    image?: string;
    /** Map fixture fields to environment variables already used by your tests. */
    env?: Partial<Record<"url" | "apiKey" | "username" | "password" | "email", string>>;
}
export interface RunConfig {
    apps: Record<string, string | RunApp>;
    /** Limits simultaneous startups, not the total number of running Apps. */
    concurrency?: number;
    /** Whole startup + test command budget. Cleanup has a separate reserve. */
    timeoutMs?: number;
}
export interface RunReport {
    schemaVersion: 1;
    startedAt: string;
    durationMs: number;
    apps: Array<{
        name: string;
        app: string;
        status: "pending" | "ready" | "failed" | "skipped";
        durationMs: number;
        code?: string;
        reason?: string;
        image?: string;
        imageId?: string;
        repoDigests?: string[];
    }>;
    tests: {
        status: "not-run" | "passed" | "failed";
        exitCode: number | null;
    };
    cleanup: "passed" | "failed";
    interrupted: boolean;
    exitCode: number;
}
export declare class RunConfigError extends Error {
    constructor();
}
export declare function parseRunConfig(input: unknown): Required<RunConfig>;
/** Starts fixtures together, runs an existing command, and always attempts owned cleanup. */
export declare function runTests(config: RunConfig, testCommand: readonly string[], options?: SelfHostOptions & {
    cwd?: string;
    signal?: AbortSignal;
}): Promise<RunReport>;
export declare function markdownReport(report: RunReport): string;
export declare function writeRunReport(report: RunReport, directory: string): void;
