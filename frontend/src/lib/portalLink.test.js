import { describe, it, expect } from "vitest";
import { portalLoginUrl } from "./portalLink";

describe("portalLoginUrl", () => {
  const origin = window.location.origin;

  it("is the plain link for a workspace on the shared database", () => {
    expect(portalLoginUrl("a@b.co", { id: "t1" })).toBe(`${origin}/portal/login?email=a%40b.co`);
    expect(portalLoginUrl("a@b.co", null)).toBe(`${origin}/portal/login?email=a%40b.co`);
    expect(portalLoginUrl("", undefined)).toBe(`${origin}/portal/login?email=`);
  });

  it("adds the workspace for a dedicated-database workspace", () => {
    expect(portalLoginUrl("a@b.co", { id: "t1", database_name: "recurso_acme" })).toBe(
      `${origin}/portal/login?email=a%40b.co&w=t1`
    );
  });
});
