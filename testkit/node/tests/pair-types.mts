import type {
	Pair,
	PairInspection,
	PairOptions,
	SelfHost,
	SonarrFilterWitness,
	withPair,
} from "../index.mjs";

type Expect<Value extends true> = Value;

export type PairViewsCannotClose = Expect<
	"close" extends keyof Pair["consumer"] ? false : true
>;
export type CompleteReason = Expect<
	Extract<PairInspection, { outcome: "complete" }>["reason"] extends "verified"
		? true
		: false
>;
export type PairFactory = Expect<
	Awaited<ReturnType<SelfHost["spinupPair"]>> extends Pair ? true : false
>;
export type CallbackResult = Expect<
	Awaited<ReturnType<typeof withPair<number>>> extends number ? true : false
>;

export type SonarrWitnessCreateHasNoMutationInputs = Expect<
	Parameters<Pair["createSonarrFilterWitness"]> extends [] ? true : false
>;
export type SonarrWitnessReadHasNoResourceSelector = Expect<
	Parameters<Pair["readSonarrFilterWitness"]> extends [] ? true : false
>;
export type SonarrWitnessReceipt = Expect<
	Awaited<
		ReturnType<Pair["createSonarrFilterWitness"]>
	> extends SonarrFilterWitness
		? true
		: false
>;
export type SonarrWitnessKind = Expect<
	SonarrFilterWitness["kind"] extends "sonarr-custom-filter-v1" ? true : false
>;
export type SonarrWitnessOptIn = Expect<
	PairOptions["sonarrFilterWitness"] extends true | undefined ? true : false
>;
export type SonarrWitnessNotOnAppViews = Expect<
	"createSonarrFilterWitness" extends keyof Pair["target"] ? false : true
>;
