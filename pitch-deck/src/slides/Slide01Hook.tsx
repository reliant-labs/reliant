import { slides } from "../slides";
import { SlideLayout } from "../components/SlideLayout";

/** Slide 01: Hook. Thin wrapper — selects its data, renders the layout. */
const data = slides.find((s) => s.id === "hook")!;
export default function Slide01Hook() {
  return <SlideLayout data={data} />;
}
