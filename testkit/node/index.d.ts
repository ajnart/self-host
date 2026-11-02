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

export interface SelfHost {
	spinup(app: string, options?: { image?: string }): Promise<App>;
	close(): Promise<void>;
}

export interface SelfHostOptions {
	binary?: string;
	timeoutMs?: number;
	manifest?: string;
}

export declare function createSelfHost(options?: SelfHostOptions): SelfHost;
