export declare const startupReasons: {
    readonly APP: "Choose one supported Catalog App.";
    readonly CAPABILITIES: "Docker must support memory, CPU quota and PID limits.";
    readonly CONTAINER: "The selected App container is absent.";
    readonly DOCKER: "Check the local Unix Docker socket, daemon and Compose plugin.";
    readonly LIMITS: "Container resource caps or selected image could not be verified.";
    readonly LOCK: "The private startup lock is unavailable.";
    readonly OPTIONS: "Check the selected image and lifetime options.";
    readonly OWNERSHIP: "Fixture ownership or isolation could not be verified.";
    readonly PORT: "A required local port could not be reserved.";
    readonly SETUP: "App Setup failed or requires a Manual step.";
    readonly STORAGE: "Private fixture storage or the recovery manifest is unavailable.";
    readonly UNSUPPORTED: "This App's Compose configuration or operations are unsupported.";
};
export type TestkitStartupReason = keyof typeof startupReasons;
export declare const startupPhases: readonly ["api-key", "auth", "prepare", "pull", "ready", "render", "root-folder", "setup", "start"];
export type TestkitStartupPhase = (typeof startupPhases)[number];
