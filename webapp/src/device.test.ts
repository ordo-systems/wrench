import { flushPromises, mount } from "@vue/test-utils";
import { describe, expect, it, vi } from "vitest";
import DeviceApproval from "./components/DeviceApproval.vue";
import { Client } from "./api";

const identity = { email: "dev@example.com", role: "user", credential: "session", status_fresh: true };

// fakeFetch answers /api/me per signedIn and records every call.
function fakeFetch(signedIn: boolean) {
  const calls: { url: string; method: string; body?: string }[] = [];
  const fn = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push({ url, method: init?.method ?? "GET", body: init?.body as string | undefined });
    if (url === "/api/me") {
      return signedIn
        ? new Response(JSON.stringify(identity), { status: 200 })
        : new Response(JSON.stringify({ error: { message: "unauthenticated" } }), { status: 401 });
    }
    if (url === "/api/device") return new Response(null, { status: 204 });
    return new Response("{}", { status: 404 });
  }) as typeof fetch;
  return { fn, calls };
}

describe("device approval", () => {
  it("sends a signed-out visitor to the sign-in that returns to this page", async () => {
    const { fn } = fakeFetch(false);
    const navigate = vi.fn();
    mount(DeviceApproval, { props: { client: new Client(fn), code: "ABCD-EFGH", navigate } });
    await flushPromises();
    expect(navigate).toHaveBeenCalledWith("/auth/gitlab/login?return=%2Fdevice%3Fcode%3DABCD-EFGH");
  });

  it("approves the code for a signed-in user", async () => {
    const { fn, calls } = fakeFetch(true);
    const wrapper = mount(DeviceApproval, {
      props: { client: new Client(fn), code: "ABCD-EFGH", navigate: vi.fn() },
    });
    await flushPromises();
    expect(wrapper.text()).toContain("ABCD-EFGH");
    expect(wrapper.text()).toContain("dev@example.com");
    await wrapper.find("button.approve").trigger("click");
    await flushPromises();
    const post = calls.find((c) => c.url === "/api/device");
    expect(post?.method).toBe("POST");
    expect(JSON.parse(post!.body!)).toEqual({ user_code: "ABCD-EFGH", decision: "approve" });
    expect(wrapper.text()).toContain("Device approved");
  });

  it("denies the code", async () => {
    const { fn, calls } = fakeFetch(true);
    const wrapper = mount(DeviceApproval, {
      props: { client: new Client(fn), code: "ABCD-EFGH", navigate: vi.fn() },
    });
    await flushPromises();
    await wrapper.find("button.deny").trigger("click");
    await flushPromises();
    expect(JSON.parse(calls.find((c) => c.url === "/api/device")!.body!).decision).toBe("deny");
    expect(wrapper.text()).toContain("Device login denied");
  });

  it("gives the Approve and Deny buttons icons", async () => {
    const { fn } = fakeFetch(true);
    const wrapper = mount(DeviceApproval, {
      props: { client: new Client(fn), code: "ABCD-EFGH", navigate: vi.fn() },
    });
    await flushPromises();
    const buttons = wrapper.findAll("button");
    expect(buttons.map((b) => b.text())).toEqual(["Approve", "Deny"]);
    for (const b of buttons) expect(b.find("svg").exists()).toBe(true);
  });
});
