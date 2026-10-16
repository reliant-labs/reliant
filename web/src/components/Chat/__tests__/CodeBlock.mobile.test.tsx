import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import { CodeBlock } from "../CodeBlock";
vi.mock("sonner",()=>({toast:{success:vi.fn(),error:vi.fn()}}));
afterEach(()=>vi.unstubAllGlobals());
it("exposes copy and a viewport-safe expanded code sheet on mobile", async()=>{
 const writeText=vi.fn().mockResolvedValue(undefined);vi.stubGlobal("navigator",{clipboard:{writeText}});
 render(<SurfaceProvider surface="mobile"><CodeBlock language="typescript">const hello = 1;</CodeBlock></SurfaceProvider>);
 expect(screen.getByRole("button",{name:"Copy code"})).toHaveClass("min-h-[44px]");
 await act(async()=>fireEvent.click(screen.getByRole("button",{name:"Copy code"})));
 expect(writeText).toHaveBeenCalledWith("const hello = 1;");
 fireEvent.click(screen.getByRole("button",{name:"Expand code"}));
 expect(screen.getByRole("dialog")).toHaveTextContent("const hello = 1;");
 fireEvent.click(screen.getByRole("button",{name:"Close code"}));expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
});
