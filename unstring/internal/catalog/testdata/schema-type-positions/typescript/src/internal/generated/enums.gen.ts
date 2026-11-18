// A real closed set: a named union paired with a same-named literal-value table.
export type Stage = "draft" | "final";
export const Stage = Object.freeze({
  Draft: "draft",
  Final: "final",
} as const);
