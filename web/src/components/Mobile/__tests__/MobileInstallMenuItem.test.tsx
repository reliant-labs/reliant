import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MobileInstallMenuItem } from "../MobileInstallMenuItem";
afterEach(() => vi.unstubAllGlobals());
describe("Home Screen menu", () => {
  it("shows Apple installation instructions on request", () => {
    vi.stubGlobal("navigator", {userAgent:"iPhone", platform:"iPhone", maxTouchPoints:1});
    render(<MobileInstallMenuItem />);
    expect(screen.queryByRole("region")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", {name:"Add to Home Screen"}));
    expect(screen.getByText(/Keep Open as Web App/)).toBeInTheDocument();
  });
  it("hides itself in a Home Screen app", () => {
    vi.stubGlobal("navigator", {standalone:true,userAgent:"iPhone"});
    render(<MobileInstallMenuItem />);
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });
  it("uses a captured browser prompt once and shows help after dismissal", async () => {
    render(<MobileInstallMenuItem />);
    const prompt=vi.fn().mockResolvedValue({outcome:"dismissed"});
    const event=new Event("beforeinstallprompt", {cancelable:true});
    Object.assign(event,{prompt});
    act(()=>window.dispatchEvent(event));
    await act(async()=>fireEvent.click(screen.getByRole("button", {name:"Add to Home Screen"})));
    expect(event.defaultPrevented).toBe(true);
    expect(prompt).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("region")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", {name:"Add to Home Screen"}));
    expect(prompt).toHaveBeenCalledTimes(1);
    act(()=>window.dispatchEvent(new Event("appinstalled")));
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });
});
