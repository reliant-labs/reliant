/**
 * ProjectPickerModal accepts Windows absolute paths.
 *
 * The picker used to validate with `path.startsWith("/")`, so every Windows
 * user hit "Path must be an absolute path starting with /" and could not
 * create a project at all. The daemon may run on a different OS than the
 * browser, so the modal has to accept any platform's absolute form and pass it
 * through to the daemon unchanged.
 */
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const createProject = vi.fn(async (data: unknown) => data);

vi.mock("@/store/projectStore", () => ({
  useProjectStore: Object.assign(
    (selector: (state: unknown) => unknown) => selector({ createProject }),
    { getState: () => ({ projects: [], loadProjects: vi.fn() }) },
  ),
}));

vi.mock("@/lib/toast-manager", () => ({
  toast: { success: vi.fn(), error: vi.fn(), loading: vi.fn(), dismiss: vi.fn() },
}));

// The browser-mode directory browser is a separate modal; it never opens here.
vi.mock("../DirectoryPicker", () => ({
  DirectoryPicker: () => null,
}));

import { ProjectPickerModal } from "../ProjectPickerModal";

function renderModal() {
  return render(
    <ProjectPickerModal isOpen onClose={vi.fn()} onProjectCreated={vi.fn()} />,
  );
}

async function submitWithPath(path: string) {
  const user = userEvent.setup();
  renderModal();
  const input = screen.getByPlaceholderText(/full path/i);
  await user.clear(input);
  await user.type(input, path);
  await user.click(screen.getByRole("button", { name: /create project/i }));
}

describe("ProjectPickerModal path validation", () => {
  beforeEach(() => {
    createProject.mockClear();
  });

  it("accepts a Windows drive path and submits it unchanged", async () => {
    await submitWithPath("C:\\Users\\sean\\projects\\app");

    expect(screen.queryByText(/enter a full path/i)).not.toBeInTheDocument();
    expect(createProject).toHaveBeenCalledWith(
      expect.objectContaining({ path: "C:\\Users\\sean\\projects\\app" }),
    );
  });

  it("derives the project name from the last Windows segment", async () => {
    await submitWithPath("C:\\Users\\sean\\projects\\app");

    expect(createProject).toHaveBeenCalledWith(
      expect.objectContaining({ name: "app" }),
    );
  });

  it("accepts a UNC share path", async () => {
    await submitWithPath("\\\\build-server\\share\\app");

    expect(createProject).toHaveBeenCalledWith(
      expect.objectContaining({ path: "\\\\build-server\\share\\app" }),
    );
  });

  it("still accepts a POSIX path", async () => {
    await submitWithPath("/Users/sean/projects/app");

    expect(createProject).toHaveBeenCalledWith(
      expect.objectContaining({ path: "/Users/sean/projects/app", name: "app" }),
    );
  });

  it("still rejects a relative path", async () => {
    await submitWithPath("projects/app");

    expect(createProject).not.toHaveBeenCalled();
    expect(screen.getByText(/enter a full path/i)).toBeInTheDocument();
  });

  // jsdom's navigator.platform is neither Mac nor Windows, so useDetectedOS
  // returns "unknown" and the example falls back to the POSIX form. The
  // platform-specific branch is covered in the pathUtils tests.
  it("names an example path the user could actually type", async () => {
    await submitWithPath("projects/app");

    expect(
      screen.getByText(/for example \/Users\/you\/projects\/my-app/),
    ).toBeInTheDocument();
  });
});
