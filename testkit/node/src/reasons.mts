export const startupReasons = {
	APP: "Choose one supported Catalog App.",
	CAPABILITIES: "Docker must support memory, CPU quota and PID limits.",
	CONTAINER: "The selected App container is absent.",
	DOCKER: "Check the local Unix Docker socket, daemon and Compose plugin.",
	LIMITS: "Container resource caps or selected image could not be verified.",
	LOCK: "The private startup lock is unavailable.",
	OPTIONS: "Check the selected image and lifetime options.",
	OWNERSHIP: "Fixture ownership or isolation could not be verified.",
	PORT: "A required local port could not be reserved.",
	SETUP: "App Setup failed or requires a Manual step.",
	STORAGE: "Private fixture storage or the recovery manifest is unavailable.",
	UNSUPPORTED:
		"This App's Compose configuration or operations are unsupported.",
} as const;

export type TestkitStartupReason = keyof typeof startupReasons;

export const startupPhases = [
	"api-key",
	"auth",
	"prepare",
	"pull",
	"ready",
	"render",
	"root-folder",
	"setup",
	"start",
] as const;

export type TestkitStartupPhase = (typeof startupPhases)[number];
