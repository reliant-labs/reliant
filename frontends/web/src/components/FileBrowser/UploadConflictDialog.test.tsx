import { fireEvent, render, screen } from "@testing-library/react";
import { UploadConflictDialog } from "./UploadConflictDialog";

vi.mock("../../hooks/useFocusManager", () => ({ focusChatInput: vi.fn() }));

function renderDialog(directory = "docs/reports") {
  const onChoose = vi.fn();
  render(<UploadConflictDialog conflict={{ name: "report.xlsx", directory }} onChoose={onChoose} />);
  return onChoose;
}

describe("UploadConflictDialog", () => {
  it("names the file and its folder, and focuses Cancel", () => {
    renderDialog();
    expect(screen.getByRole("dialog")).toHaveTextContent("“report.xlsx” already exists in docs/reports");
    expect(screen.getByRole("dialog")).toHaveTextContent("“report (1).xlsx”");
    expect(screen.getByRole("button", { name: "Cancel" })).toHaveFocus();
  });

  it("calls the workspace root the project root", () => {
    renderDialog("");
    expect(screen.getByRole("dialog")).toHaveTextContent("“report.xlsx” already exists in the project root");
  });

  it.each([
    ["Replace", "replace"],
    ["Keep both", "keep-both"],
    ["Cancel", "cancel"],
  ])("%s answers %s", (label, choice) => {
    const onChoose = renderDialog();
    fireEvent.click(screen.getByRole("button", { name: label }));
    expect(onChoose).toHaveBeenCalledWith(choice);
  });

  it("treats Escape as Cancel", () => {
    const onChoose = renderDialog();
    fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" });
    expect(onChoose).toHaveBeenCalledWith("cancel");
  });

  it("is closed while nothing clashes", () => {
    render(<UploadConflictDialog conflict={null} onChoose={vi.fn()} />);
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});
