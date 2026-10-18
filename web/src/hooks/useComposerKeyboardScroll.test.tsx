import { act, fireEvent, render } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { useComposerKeyboardScroll } from "./useComposerKeyboardScroll";
let viewport: EventTarget & {width:number; height:number; scale:number; offsetTop:number; offsetLeft:number};
beforeEach(() => {
  vi.useFakeTimers();
  viewport=Object.assign(new EventTarget(), {width:390,height:800,scale:1,offsetTop:0,offsetLeft:0});
  vi.stubGlobal("visualViewport",viewport);
  vi.stubGlobal("innerHeight",800);
});
afterEach(()=>{vi.useRealTimers();vi.unstubAllGlobals();});
function Harness({scroll,enabled=true}:{scroll:()=>void;enabled?:boolean}) {
  useComposerKeyboardScroll(enabled,scroll);
  return <><div data-mobile-composer><textarea aria-label="Draft" /></div><input aria-label="Settings" /></>;
}
function resize(height:number) {act(()=>{viewport.height=height;viewport.dispatchEvent(new Event("resize"));vi.advanceTimersByTime(20);});act(()=>vi.advanceTimersByTime(60));}
it("scrolls on keyboard opening and resizing, but not during ordinary history scrolling",()=>{
  const scroll=vi.fn();const {getByLabelText}=render(<Harness scroll={scroll}/>);
  getByLabelText("Draft").focus();resize(400);expect(scroll).toHaveBeenCalledTimes(1);
  resize(350);expect(scroll).toHaveBeenCalledTimes(2);
  fireEvent.scroll(document.body);act(()=>vi.advanceTimersByTime(80));expect(scroll).toHaveBeenCalledTimes(2);
  getByLabelText("Draft").blur();resize(800);expect(scroll).toHaveBeenCalledTimes(2);
});
it("does not jump for settings inputs or desktop",()=>{
  const scroll=vi.fn();const {getByLabelText,rerender}=render(<Harness scroll={scroll}/>);
  getByLabelText("Settings").focus();resize(400);expect(scroll).not.toHaveBeenCalled();
  rerender(<Harness scroll={scroll} enabled={false}/>);getByLabelText("Draft").focus();resize(350);expect(scroll).not.toHaveBeenCalled();
});
