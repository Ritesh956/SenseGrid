import { backoffDelayMs } from "./backoff";

describe("backoffDelayMs", () => {
  it("starts at the base delay and doubles each attempt", () => {
    expect(backoffDelayMs(0)).toBe(1000);
    expect(backoffDelayMs(1)).toBe(2000);
    expect(backoffDelayMs(2)).toBe(4000);
    expect(backoffDelayMs(3)).toBe(8000);
  });

  it("caps at the max delay", () => {
    expect(backoffDelayMs(10)).toBe(30000);
    expect(backoffDelayMs(100)).toBe(30000);
  });
});
