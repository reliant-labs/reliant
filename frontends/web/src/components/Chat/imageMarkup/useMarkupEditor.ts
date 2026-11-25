import { useCallback, useMemo, useReducer, useState, type Dispatch } from 'react';
import {
  canRedo,
  canUndo,
  createHistory,
  DEFAULT_MARKUP_COLOR,
  emptyDoc,
  isDirty,
  markupReducer,
  type MarkupAction,
  type MarkupDoc,
  type MarkupHistory,
} from './model';

export type EditorMode = 'view' | 'markup' | 'comment' | 'crop';
export type MarkupTool = 'select' | 'pen' | 'text' | 'shape' | 'eraser';
export type ShapeKind = 'rect' | 'ellipse' | 'arrow';

type EditorAction = MarkupAction | { type: 'init'; doc: MarkupDoc };

function editorReducer(h: MarkupHistory, action: EditorAction): MarkupHistory {
  return action.type === 'init' ? createHistory(action.doc) : markupReducer(h, action);
}

/**
 * The editor's state: the undoable document plus the toolbar choices (tool,
 * remembered shape, colour). With no initial doc the document is empty and
 * zero-sized until `init` is called with the image's natural size.
 */
export function useMarkupEditor(initialDoc?: MarkupDoc) {
  const [history, rawDispatch] = useReducer(editorReducer, initialDoc, (doc) => createHistory(doc ?? emptyDoc(0, 0)));
  const dispatch = rawDispatch as Dispatch<MarkupAction>;
  const [tool, setTool] = useState<MarkupTool>('pen');
  const [shapeKind, setShapeKind] = useState<ShapeKind>('rect');
  const [color, setColor] = useState<string>(DEFAULT_MARKUP_COLOR);

  const init = useCallback((doc: MarkupDoc) => rawDispatch({ type: 'init', doc }), []);
  const undo = useCallback(() => rawDispatch({ type: 'undo' }), []);
  const redo = useCallback(() => rawDispatch({ type: 'redo' }), []);

  const doc = history.present;
  return useMemo(
    () => ({
      history,
      doc,
      ready: doc.width > 0 && doc.height > 0,
      dirty: isDirty(history),
      canUndo: canUndo(history),
      canRedo: canRedo(history),
      dispatch,
      init,
      undo,
      redo,
      tool,
      setTool,
      shapeKind,
      setShapeKind,
      color,
      setColor,
    }),
    [history, doc, dispatch, init, undo, redo, tool, shapeKind, color],
  );
}
