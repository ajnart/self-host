import type { Pair, PairInspection, SelfHost, withPair } from "../index.mjs";

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
