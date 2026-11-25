/**
 * ImagePreviewModal with `edit`: the wiring between the dialog and the markup
 * editor. The editor's own behaviour lives in imageMarkup.editor.test.tsx;
 * this file pins what the dialog adds: the window-capture key handler (Escape
 * ladder, undo/redo), the discard prompt, Save/Revert and their failure paths.
 * The real global dispatcher is mounted for the key tests, as in
 * ImagePreviewModal.escape.test.tsx.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { ImagePreviewModal, type ImageEditOptions } from "../ImagePreviewModal";
import type { CommentPin, MarkupDoc } from "../imageMarkup/model";
import { defaultShortcuts } from "../../../store/shortcutsData.generated";
import { parseBinding } from "../../../lib/keyboard/chord";
import { ShortcutRegistry, type ResolvedShortcut } from "../../../lib/keyboard/registry";
import { createDispatcher } from "../../../lib/keyboard/dispatcher";
import { toast } from "../../../lib/toast-manager";

vi.mock("../../../hooks/useFocusManager", () => ({ focusChatInput: vi.fn() }));
vi.mock("../../../lib/toast-manager", () => ({
  toast: { error: vi.fn(), info: vi.fn(), success: vi.fn(), warning: vi.fn() },
}));
const flatten = vi.hoisted(() => ({ fn: vi.fn() }));
vi.mock("../imageMarkup/renderCanvas", async (importActual) => ({
  ...(await importActual<typeof import("../imageMarkup/renderCanvas")>()),
  flattenImage: flatten.fn,
}));

const teardown: Array<() => void> = [];

function mountGlobalDispatcher(): { fired: string[] } {
  const resolved: ResolvedShortcut[] = Object.values(defaultShortcuts).map((shortcut) => ({
    id: shortcut.id,
    handler: shortcut.handler,
    binding: parseBinding(shortcut.defaultBinding, true),
    context: shortcut.context,
    allowInInput: shortcut.allowInInput,
    passthrough: shortcut.passthrough,
  }));
  const fired: string[] = [];
  const dispatcher = createDispatcher({
    registry: new ShortcutRegistry(resolved),
    getHandler: (name) => () => fired.push(name),
  });
  const onKeyDown = (event: KeyboardEvent) => dispatcher.handleKeyDown(event);
  document.addEventListener("keydown", onKeyDown, true);
  teardown.push(() => document.removeEventListener("keydown", onKeyDown, true));
  return { fired };
}

/** A keypress from whatever has focus, so it travels the real capture path. */
function press(key: string, init: KeyboardEventInit = {}): KeyboardEvent {
  const event = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init });
  act(() => {
    (document.activeElement ?? document.body).dispatchEvent(event);
  });
  return event;
}

beforeEach(() => {
  URL.createObjectURL = vi.fn(() => "blob:editor-source");
  URL.revokeObjectURL = vi.fn();
});

afterEach(() => {
  cleanup();
  while (teardown.length) teardown.pop()!();
  vi.clearAllMocks();
  flatten.fn.mockReset();
});

const SOURCE = new Blob(["png"], { type: "image/png" });

function makeEdit(overrides: Partial<ImageEditOptions> = {}): ImageEditOptions & {
  onSave: ReturnType<typeof vi.fn>;
} {
  return { source: SOURCE, onSave: vi.fn(async () => {}), ...overrides } as ImageEditOptions & {
    onSave: ReturnType<typeof vi.fn>;
  };
}

function mount(edit?: ImageEditOptions) {
  const onClose = vi.fn();
  const view = render(
    <ImagePreviewModal isOpen onClose={onClose} imageUrl="blob:chip" filename="shot.png" edit={edit} />,
  );
  if (edit) {
    const img = screen.getByAltText("shot.png") as HTMLImageElement;
    Object.defineProperty(img, "naturalWidth", { configurable: true, value: 800 });
    Object.defineProperty(img, "naturalHeight", { configurable: true, value: 600 });
    fireEvent.load(img);
  }
  return { onClose, ...view };
}

const PIN: CommentPin = { kind: "pin", id: "pin-1", n: 1, x: 40, y: 50, text: "keep me" };
const DOC_WITH_PIN: MarkupDoc = { width: 800, height: 600, shapes: [PIN] };

const clickImage = (x = 100, y = 120) =>
  fireEvent.click(screen.getByTestId("comment-layer"), { clientX: x, clientY: y });
const pinInput = () => screen.getByPlaceholderText("Add a comment…") as HTMLInputElement;
const enterCommentMode = () => fireEvent.click(screen.getByRole("button", { name: "Comment" }));
const dataMode = () => screen.getByTestId("markup-editor").getAttribute("data-mode");

/** Comment mode with one pin carrying `text`, its input committed. */
function addPin(text: string) {
  clickImage();
  fireEvent.change(pinInput(), { target: { value: text } });
  fireEvent.keyDown(pinInput(), { key: "Enter" });
}

describe("without edit", () => {
  it("renders no editor toolbar and closes on a backdrop click", () => {
    const { onClose } = mount();
    expect(screen.queryByRole("toolbar")).toBeNull();
    expect(screen.queryByTestId("markup-editor")).toBeNull();
    expect(screen.queryByText("Revert to original")).toBeNull();
    fireEvent.click(screen.getByAltText("shot.png").parentElement!);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("does not close when the image itself is clicked", () => {
    const { onClose } = mount();
    fireEvent.click(screen.getByAltText("shot.png"));
    expect(onClose).not.toHaveBeenCalled();
  });
});

describe("with edit", () => {
  it("shows the entry toolbar once the image has loaded, over the source blob", () => {
    const edit = makeEdit();
    render(<ImagePreviewModal isOpen onClose={vi.fn()} imageUrl="blob:chip" filename="shot.png" edit={edit} />);
    expect(URL.createObjectURL).toHaveBeenCalledWith(SOURCE);
    const img = screen.getByAltText("shot.png") as HTMLImageElement;
    // The editor shows the original, not the flattened copy the chip shows.
    expect(img.getAttribute("src")).toBe("blob:editor-source");
    expect(screen.queryByRole("toolbar")).toBeNull();

    Object.defineProperty(img, "naturalWidth", { configurable: true, value: 800 });
    Object.defineProperty(img, "naturalHeight", { configurable: true, value: 600 });
    fireEvent.load(img);
    expect(screen.getByRole("toolbar", { name: "Edit image" })).toBeInTheDocument();
    for (const name of ["Markup", "Comment", "Crop"]) {
      expect(screen.getByRole("button", { name })).toBeInTheDocument();
    }
  });

  it("revokes the source URL when it unmounts", () => {
    const view = mount(makeEdit());
    view.unmount();
    expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:editor-source");
  });

  it("does not close on a click in the image area (the editor owns clicks)", () => {
    const { onClose } = mount(makeEdit());
    fireEvent.click(screen.getByTestId("markup-image-box"));
    expect(onClose).not.toHaveBeenCalled();
  });
});

describe("Escape ladder", () => {
  it("cancels the pin input, then leaves comment mode, then closes a clean doc", () => {
    const dispatcher = mountGlobalDispatcher();
    const { onClose } = mount(makeEdit());
    enterCommentMode();
    expect(dataMode()).toBe("comment");
    clickImage();
    expect(pinInput()).toBeInTheDocument();

    const first = press("Escape");
    expect(first.defaultPrevented).toBe(true);
    expect(screen.queryByPlaceholderText("Add a comment…")).toBeNull();
    // The untouched pin is taken back, so the doc is clean and still in comment mode.
    expect(screen.queryByRole("button", { name: /^Comment 1/ })).toBeNull();
    expect(dataMode()).toBe("comment");
    expect(onClose).not.toHaveBeenCalled();

    press("Escape");
    expect(dataMode()).toBe("view");
    expect(onClose).not.toHaveBeenCalled();

    press("Escape");
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(screen.queryByText("Discard your changes?")).toBeNull();

    // The dialog claimed every Escape: neither dismissModal nor stopStreaming ran.
    expect(dispatcher.fired).toEqual([]);
  });

  it("asks before closing a dirty doc; Escape dismisses the prompt, Discard closes", () => {
    const dispatcher = mountGlobalDispatcher();
    const { onClose } = mount(makeEdit());
    enterCommentMode();
    addPin("blurry");
    expect(screen.getByRole("button", { name: "Comment 1: blurry" })).toBeInTheDocument();

    press("Escape"); // comment mode -> view
    expect(dataMode()).toBe("view");
    press("Escape"); // dirty: ask
    expect(screen.getByText("Discard your changes?")).toBeInTheDocument();
    expect(onClose).not.toHaveBeenCalled();

    press("Escape"); // dismisses the prompt only
    expect(screen.queryByText("Discard your changes?")).toBeNull();
    expect(onClose).not.toHaveBeenCalled();

    press("Escape");
    expect(screen.getByText("Discard your changes?")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Discard" }));
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(dispatcher.fired).toEqual([]);
  });

  it("keeps the markup when the prompt's Keep editing is chosen", () => {
    const { onClose } = mount(makeEdit());
    enterCommentMode();
    addPin("blurry");
    fireEvent.click(screen.getByRole("button", { name: "Close (ESC)" }));
    expect(screen.getByText("Discard your changes?")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Keep editing" }));
    expect(screen.queryByText("Discard your changes?")).toBeNull();
    expect(screen.getByRole("button", { name: "Comment 1: blurry" })).toBeInTheDocument();
    expect(onClose).not.toHaveBeenCalled();
  });

  it("keeps a typed pin on the first Escape (only an empty fresh pin is taken back)", () => {
    mount(makeEdit());
    enterCommentMode();
    clickImage();
    fireEvent.change(pinInput(), { target: { value: "typed" } });
    press("Escape");
    expect(screen.queryByPlaceholderText("Add a comment…")).toBeNull();
    expect(screen.getByRole("button", { name: "Comment 1: typed" })).toBeInTheDocument();
    expect(dataMode()).toBe("comment");
  });

  it("steps aside while focus is in a portalled popover outside the dialog", () => {
    const { onClose } = mount(makeEdit());
    const outside = document.createElement("button");
    document.body.appendChild(outside);
    teardown.push(() => outside.remove());
    outside.focus();
    expect(document.activeElement).toBe(outside);

    const event = press("Escape");
    expect(event.defaultPrevented).toBe(false);
    expect(onClose).not.toHaveBeenCalled();
  });
});

describe("undo / redo", () => {
  it("Cmd+Z removes a pin and Shift+Cmd+Z brings it back", () => {
    const dispatcher = mountGlobalDispatcher();
    mount(makeEdit());
    enterCommentMode();
    addPin("redo me");
    const pin = () => screen.queryByRole("button", { name: "Comment 1: redo me" });
    expect(pin()).toBeInTheDocument();
    expect(document.activeElement).not.toBeInstanceOf(HTMLInputElement);

    const undo = press("z", { metaKey: true });
    expect(undo.defaultPrevented).toBe(true);
    expect(pin()).toBeNull();

    press("z", { metaKey: true, shiftKey: true });
    expect(pin()).toBeInTheDocument();

    press("z", { ctrlKey: true });
    expect(pin()).toBeNull();
    press("y", { ctrlKey: true });
    expect(pin()).toBeInTheDocument();
    expect(dispatcher.fired).toEqual([]);
  });

  it("leaves Cmd+Z to the text field while an input has focus", () => {
    mount(makeEdit());
    enterCommentMode();
    clickImage();
    fireEvent.change(pinInput(), { target: { value: "typing" } });
    expect(document.activeElement).toBe(pinInput());

    for (const init of [{ metaKey: true }, { metaKey: true, shiftKey: true }, { ctrlKey: true }]) {
      const event = press("z", init);
      expect(event.defaultPrevented).toBe(false);
    }
    // Nothing was undone: the pin and its text are intact.
    expect(pinInput().value).toBe("typing");
    expect(screen.getByRole("button", { name: "Comment 1: typing" })).toBeInTheDocument();
  });

  it("does not claim Cmd+Z without edit", () => {
    mount();
    const event = press("z", { metaKey: true });
    expect(event.defaultPrevented).toBe(false);
  });
});

describe("Save", () => {
  it("flattens the doc, hands onSave the file, doc, source and pins, then closes", async () => {
    const file = new File(["flat"], "shot-marked.png", { type: "image/png" });
    flatten.fn.mockResolvedValue(file);
    const edit = makeEdit();
    const { onClose } = mount(edit);
    enterCommentMode();
    addPin("first");
    clickImage(300, 200);
    fireEvent.change(pinInput(), { target: { value: "second" } });
    fireEvent.keyDown(pinInput(), { key: "Enter" });

    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(edit.onSave).toHaveBeenCalledTimes(1));

    const [doc, filename] = [flatten.fn.mock.calls[0][1] as MarkupDoc, flatten.fn.mock.calls[0][2]];
    expect(flatten.fn.mock.calls[0][0]).toBe(SOURCE);
    expect(filename).toBe("shot.png");
    expect(doc).toMatchObject({ width: 800, height: 600 });

    const result = edit.onSave.mock.calls[0][0];
    expect(result.file).toBe(file);
    expect(result.source).toBe(SOURCE);
    expect(result.doc).toEqual(doc);
    expect(result.comments.map((p: CommentPin) => [p.n, p.text])).toEqual([
      [1, "first"],
      [2, "second"],
    ]);
    expect(result.comments[0]).toMatchObject({ kind: "pin", x: 100, y: 120 });
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
  });

  it("keeps the modal open and reports the failure when onSave rejects", async () => {
    flatten.fn.mockResolvedValue(new File(["flat"], "shot.png"));
    const edit = makeEdit({ onSave: vi.fn().mockRejectedValue(new Error("upload down")) });
    const { onClose } = mount(edit);
    enterCommentMode();
    addPin("keep");
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(toast.error).toHaveBeenCalledTimes(1));
    expect(vi.mocked(toast.error).mock.calls[0][0]).toContain("upload down");
    expect(onClose).not.toHaveBeenCalled();
    // The doc is intact and Save is available again for a retry.
    expect(screen.getByRole("button", { name: "Comment 1: keep" })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: "Save" })).toBeEnabled());
    expect(screen.queryByText("Discard your changes?")).toBeNull();
  });

  it("keeps the modal open when flattening fails", async () => {
    flatten.fn.mockRejectedValue(new Error("canvas too big"));
    const edit = makeEdit();
    const { onClose } = mount(edit);
    enterCommentMode();
    addPin("keep");
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(toast.error).toHaveBeenCalledTimes(1));
    expect(edit.onSave).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
  });

  it("reverts to the original when all earlier markup was removed", async () => {
    const onRevert = vi.fn(async () => {});
    const edit = makeEdit({ initialDoc: DOC_WITH_PIN, onRevert });
    render(<ImagePreviewModal isOpen onClose={vi.fn()} imageUrl="blob:chip" filename="shot.png" edit={edit} />);
    // initialDoc makes the editor ready without an image load.
    enterCommentMode();
    fireEvent.click(screen.getByRole("button", { name: "Comment 1: keep me" }));
    fireEvent.click(screen.getByRole("button", { name: "Delete comment 1" }));
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onRevert).toHaveBeenCalledTimes(1));
    expect(edit.onSave).not.toHaveBeenCalled();
    expect(flatten.fn).not.toHaveBeenCalled();
  });

  it("just closes when nothing is left and there is no original to restore", async () => {
    const edit = makeEdit({ initialDoc: DOC_WITH_PIN });
    const onClose = vi.fn();
    render(<ImagePreviewModal isOpen onClose={onClose} imageUrl="blob:chip" filename="shot.png" edit={edit} />);
    enterCommentMode();
    fireEvent.click(screen.getByRole("button", { name: "Comment 1: keep me" }));
    fireEvent.click(screen.getByRole("button", { name: "Delete comment 1" }));
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
    expect(edit.onSave).not.toHaveBeenCalled();
    expect(flatten.fn).not.toHaveBeenCalled();
  });
});

describe("Revert to original", () => {
  it("is absent without onRevert", () => {
    mount(makeEdit());
    expect(screen.queryByRole("button", { name: "Revert to original" })).toBeNull();
  });

  it("calls onRevert and closes", async () => {
    const onRevert = vi.fn(async () => {});
    const { onClose } = mount(makeEdit({ onRevert }));
    fireEvent.click(screen.getByRole("button", { name: "Revert to original" }));
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
    expect(onRevert).toHaveBeenCalledTimes(1);
  });

  it("stays open and reports when onRevert rejects", async () => {
    const onRevert = vi.fn().mockRejectedValue(new Error("offline"));
    const { onClose } = mount(makeEdit({ onRevert }));
    fireEvent.click(screen.getByRole("button", { name: "Revert to original" }));
    await waitFor(() => expect(toast.error).toHaveBeenCalledTimes(1));
    expect(onClose).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.getByRole("button", { name: "Revert to original" })).toBeEnabled());
  });
});
