// A vendored upstream bundle: its literals are the upstream's vocabulary.
export type Mode = "alpha" | "beta";
export const isUpstreamReady = (s: string) => s === "ready";
