import { closed } from "./closed";

export const Status = Object.freeze({
  Ready: closed("ready", "ready"),
  Failed: closed("failed", "failed"),
});

export const isFailed = (s: string) => s === "failed";
