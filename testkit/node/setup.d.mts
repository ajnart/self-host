#!/usr/bin/env node
export declare class SetupError extends Error {
}
export declare function setup(options?: {
    version?: string;
    directory?: string;
}): Promise<string>;
